package export

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/documents"
	"phoebe/internal/ids"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
)

type env struct {
	pool   *pgxpool.Pool
	store  *storage.LocalFS
	signer *storage.Signer
	svc    *Service
	files  *Files
}

func newEnv(t *testing.T) *env {
	t.Helper()
	cfg := testutil.Config(t)
	e := &env{pool: testutil.DB(t), store: testutil.Storage(t)}
	e.signer = storage.NewSigner(cfg.SigningSecret, cfg.PublicBaseURL, cfg.SignedURLTTL)
	e.svc = NewService(e.pool, e.store, e.signer, testutil.DiscardLogger())
	e.files = NewFiles(e.pool, e.store, e.signer)
	return e
}

func (e *env) revision(t *testing.T, meetingID string, kind documents.Kind, raw string) string {
	t.Helper()
	content, err := json.Marshal(mustContent(t, kind, raw))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var docID, revID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO documents (meeting_id, kind, current_revision_number) VALUES ($1, $2, 1) RETURNING id`,
		meetingID, string(kind)).Scan(&docID); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `
		INSERT INTO document_revisions (document_id, revision_number, origin, schema_version, content, mutation_id, mutation_fingerprint, created_at)
		VALUES ($1, 1, 'user_edit', 1, $2, $3, $4, $5) RETURNING id`,
		docID, content, ids.New(), strings.Repeat("a", 64), createdAt).Scan(&revID); err != nil {
		t.Fatal(err)
	}
	return revID
}

func ptr(s string) *string { return &s }

func (e *env) stored(t *testing.T, exportID string) []byte {
	t.Helper()
	var key string
	if err := e.pool.QueryRow(context.Background(), `SELECT storage_key FROM exports WHERE id = $1`, exportID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	rc, _, err := e.store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return b
}

func (e *env) create(t *testing.T, uid, revID string, f Format) *Result {
	t.Helper()
	res, err := e.svc.Create(context.Background(), uid, CreateInput{ptr(revID), ptr(string(f))})
	if err != nil {
		t.Fatal(f, err)
	}
	return res
}

func TestServiceExportConsistencyAndDraft(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	rev := e.revision(t, mid, documents.KindMoM, momFixture)

	in := Input{Content: mustContent(t, documents.KindMoM, momFixture), Language: "id", IsDraft: true, CreatedAt: createdAt}
	got := map[Format]string{}
	for _, f := range []Format{MD, WhatsApp, DOCX, PDF} {
		res := e.create(t, u.ID, rev, f)
		x := res.Export
		if x.Kind != "mom" || x.Format != string(f) || x.DocumentRevisionID != rev || !x.IsDraft || x.ContentType != ContentType(f) ||
			x.Filename != "weekly-sync-rilis-q1-cafe-20261106-mom-draft."+Extension(f) {
			t.Fatalf("%s: %+v", f, x)
		}
		b := e.stored(t, x.ID)
		if want := render(t, in, f); string(b) != string(want) || x.SizeBytes != int64(len(b)) {
			t.Fatalf("%s: stored export differs from a direct render of the same revision", f)
		}
		if (f == MD || f == WhatsApp) != (res.Text != nil) || (res.Text != nil && *res.Text != string(b)) {
			t.Fatalf("%s: text %v", f, res.Text)
		}
		if !strings.HasPrefix(res.DownloadURL, "http://phoebe.test/v1/files/export/"+x.ID+"?exp=") ||
			res.DownloadExpiresAt.Time.Before(time.Now().Add(14*time.Minute)) {
			t.Fatalf("%s: url %s exp %v", f, res.DownloadURL, res.DownloadExpiresAt)
		}
		var sha string
		if err := e.pool.QueryRow(ctx, `SELECT sha256 FROM exports WHERE id = $1`, x.ID).Scan(&sha); err != nil || sha != testutil.SHA256Hex(b) {
			t.Fatal(sha, err)
		}
		switch f {
		case MD:
			got[f] = mdUnescape(string(b))
		case DOCX:
			got[f] = docxText(t, b)
		case PDF:
			got[f] = pdfToText(t, b)
		default:
			got[f] = string(b)
		}
		if !strings.Contains(got[f], "DRAFT — belum ditinjau") {
			t.Errorf("%s: unreviewed revision must carry the Draft label", f)
		}
	}
	for _, f := range []Format{MD, DOCX, PDF} {
		norm := strings.Join(strings.Fields(got[f]), " ")
		for _, s := range []string{
			"Budget marketing naik 10% menjadi Rp60 juta. [01:02, 1:02:05]", "Rilis hari Jumat. [05:40] (dari catatan pengguna)",
			"Siapkan laporan vendor", "Budi", "2026-11-13 (minggu depan)", "Kirim notulen", "Belum ditentukan", "akhir bulan",
			"Cek kontrak", "Sari", "Weekly Sync: Rilis Q1 / Café", "2026-11-06 09:00 WIB",
		} {
			if !strings.Contains(norm, s) {
				t.Errorf("%s missing %q", f, s)
			}
		}
	}

	first := e.create(t, u.ID, rev, PDF)
	again := e.create(t, u.ID, rev, PDF)
	if first.Export.ID == again.Export.ID || string(e.stored(t, first.Export.ID)) != string(e.stored(t, again.Export.ID)) {
		t.Fatal("each export is a new object with byte-identical content")
	}

	if _, err := e.pool.Exec(ctx, `UPDATE document_revisions SET reviewed_at = now() WHERE id = $1`, rev); err != nil {
		t.Fatal(err)
	}
	in.IsDraft = false
	for _, f := range []Format{MD, WhatsApp, DOCX, PDF} {
		res := e.create(t, u.ID, rev, f)
		b := e.stored(t, res.Export.ID)
		if res.Export.IsDraft || strings.Contains(res.Export.Filename, "draft") || string(b) != string(render(t, in, f)) {
			t.Fatalf("%s reviewed: %+v", f, res.Export)
		}
	}
	if s := pdfToText(t, e.stored(t, e.create(t, u.ID, rev, PDF).Export.ID)); strings.Contains(s, "DRAFT") {
		t.Fatal("reviewed revision must not carry the Draft label")
	}
}

func TestServiceNotesHeaderAndLanguage(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	if _, err := e.pool.Exec(ctx, `UPDATE meetings SET title = 'Rapat Klien', started_at = '2026-11-06T02:00:00Z' WHERE id = $1`, mid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET language = 'en' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	notes := e.revision(t, mid, documents.KindManualNotes, manualNotesFixture)
	res := e.create(t, u.ID, notes, MD)
	if res.Export.Filename != "rapat-klien-20261106-manual-notes-draft.md" || res.Export.Kind != "manual_notes" {
		t.Fatal(res.Export)
	}
	for _, s := range []string{"# Rapat Klien", "2026-11-06 09:00 WIB", "DRAFT — not reviewed", "Bahas budget Q1"} {
		if !strings.Contains(*res.Text, s) {
			t.Errorf("manual notes md missing %q:\n%s", s, *res.Text)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE meetings SET title = 'Renamed' WHERE id = $1`, mid); err != nil {
		t.Fatal(err)
	}
	if res = e.create(t, u.ID, notes, WhatsApp); !strings.HasPrefix(*res.Text, "*Renamed*") || !strings.HasSuffix(res.Export.Filename, ".txt") {
		t.Fatal("notes header reads the meeting row at export time:", *res.Text)
	}
}

func TestServiceValidationOwnershipAndGet(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := testutil.CreateUser(t, e.pool, 0)
	b := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, a.ID)
	rev := e.revision(t, mid, documents.KindAINotes, aiNotesFixture)

	for _, c := range []struct {
		in     CreateInput
		fields string
	}{
		{CreateInput{}, "document_revision_id:required,format:required"},
		{CreateInput{ptr("nope"), ptr("html")}, "document_revision_id:invalid,format:invalid"},
		{CreateInput{ptr(rev), ptr("PDF")}, "format:invalid"},
	} {
		_, err := e.svc.Create(ctx, a.ID, c.in)
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Code != apperr.ValidationFailed {
			t.Fatal(c, err)
		}
		var got []string
		for _, f := range ae.Details["fields"].([]apperr.FieldError) {
			got = append(got, f.Field+":"+f.Reason)
		}
		if strings.Join(got, ",") != c.fields {
			t.Fatal(got)
		}
	}
	if _, err := e.svc.Create(ctx, b.ID, CreateInput{ptr(rev), ptr("md")}); !apperr.Is(err, apperr.NotFound) {
		t.Fatal("other user's revision:", err)
	}
	if _, err := e.svc.Create(ctx, a.ID, CreateInput{ptr(ids.New()), ptr("md")}); !apperr.Is(err, apperr.NotFound) {
		t.Fatal(err)
	}

	res := e.create(t, a.ID, rev, DOCX)
	got, err := e.svc.Get(ctx, a.ID, res.Export.ID)
	if err != nil || got.Export != res.Export || got.Text != nil || !strings.Contains(got.DownloadURL, res.Export.ID) {
		t.Fatal(got, err)
	}
	if _, err := e.svc.Get(ctx, b.ID, res.Export.ID); !apperr.Is(err, apperr.NotFound) {
		t.Fatal(err)
	}

	if _, err := e.pool.Exec(ctx, `UPDATE meetings SET deleted_at = now() WHERE id = $1`, mid); err != nil {
		t.Fatal(err)
	}
	var before int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM exports`).Scan(&before)
	if _, err := e.svc.Create(ctx, a.ID, CreateInput{ptr(rev), ptr("md")}); !apperr.Is(err, apperr.NotFound) {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, a.ID, res.Export.ID); !apperr.Is(err, apperr.NotFound) {
		t.Fatal(err)
	}
	var after int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM exports`).Scan(&after)
	if after != before {
		t.Fatal("tombstoned meeting must not get an export row")
	}
}

func insertAudio(t *testing.T, e *env, uid, mid string, data []byte) string {
	t.Helper()
	ctx := context.Background()
	var sid string
	if err := e.pool.QueryRow(ctx, `
		INSERT INTO upload_sessions (meeting_id, user_id, status, declared_content_type, size_bytes, sha256, declared_duration_ms,
		                             received_bytes, storage_key, expires_at, completed_at)
		VALUES ($1, $2, 'completed', 'audio/wav', $3, $4, 2000, $3, 'x', now() + interval '1 day', now()) RETURNING id`,
		mid, uid, len(data), testutil.SHA256Hex(data)).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	key := "u/" + uid + "/m/" + mid + "/audio/" + sid + ".wav"
	if _, err := e.store.Put(ctx, key, strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	var aid string
	if err := e.pool.QueryRow(ctx, `
		INSERT INTO audio_assets (meeting_id, upload_session_id, content_type, size_bytes, sha256, duration_ms, storage_key)
		VALUES ($1, $2, 'audio/wav', $3, $4, 2000, $5) RETURNING id`,
		mid, sid, len(data), testutil.SHA256Hex(data), key).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	return aid
}

func query(t *testing.T, url string) (id, exp, sig string) {
	t.Helper()
	path, q, _ := strings.Cut(url, "?")
	id = path[strings.LastIndex(path, "/")+1:]
	for _, kv := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "exp":
			exp = v
		case "sig":
			sig = v
		}
	}
	return
}

func TestFilesSignedURLs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := testutil.CreateUser(t, e.pool, 0)
	other := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)

	if _, _, err := e.files.AudioURL(ctx, u.ID, mid); !apperr.Is(err, apperr.AudioNotReady) {
		t.Fatal(err)
	}
	wav := testutil.WAV(2000)
	aid := insertAudio(t, e, u.ID, mid, wav)
	url, exp, err := e.files.AudioURL(ctx, u.ID, mid)
	if err != nil || !strings.HasPrefix(url, "http://phoebe.test/v1/files/audio/"+aid+"?") || exp.Before(time.Now().Add(14*time.Minute)) {
		t.Fatal(url, exp, err)
	}
	if _, _, err := e.files.AudioURL(ctx, other.ID, mid); !apperr.Is(err, apperr.NotFound) {
		t.Fatal(err)
	}
	id, ex, sig := query(t, url)
	f, err := e.files.Open(ctx, "audio", id, ex, sig)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f.Body)
	f.Body.Close()
	if string(b) != string(wav) || f.ContentType != "audio/wav" || f.Filename != "" {
		t.Fatal(f.ContentType, len(b))
	}

	rev := e.revision(t, mid, documents.KindMoM, momFixture)
	res := e.create(t, u.ID, rev, PDF)
	eid, eex, esig := query(t, res.DownloadURL)
	if f, err = e.files.Open(ctx, "export", eid, eex, esig); err != nil || f.Filename != res.Export.Filename || f.ContentType != "application/pdf" {
		t.Fatal(f, err)
	}
	f.Body.Close()

	expired, _ := e.signer.Sign("export", eid, time.Now().Add(-time.Hour))
	_, xex, xsig := query(t, expired)
	flip := []byte(esig)
	flip[0] ^= 1
	for name, c := range map[string]struct {
		kind, id, exp, sig string
		code               apperr.Code
	}{
		"tampered sig":     {"export", eid, eex, string(flip), apperr.InvalidSignature},
		"tampered exp":     {"export", eid, xex, esig, apperr.InvalidSignature},
		"tampered id":      {"export", ids.New(), eex, esig, apperr.InvalidSignature},
		"kind swap":        {"audio", eid, eex, esig, apperr.InvalidSignature},
		"missing sig":      {"export", eid, eex, "", apperr.InvalidSignature},
		"bad exp":          {"export", eid, "soon", esig, apperr.InvalidSignature},
		"expired":          {"export", eid, xex, xsig, apperr.URLExpired},
		"unknown kind":     {"transcript", eid, eex, esig, apperr.NotFound},
		"expired+tampered": {"export", eid, xex, string(flip), apperr.InvalidSignature},
	} {
		if _, err := e.files.Open(ctx, c.kind, c.id, c.exp, c.sig); !apperr.Is(err, c.code) {
			t.Errorf("%s: %v", name, err)
		}
	}

	if _, err := e.pool.Exec(ctx, `UPDATE audio_assets SET deleted_at = now(), deleted_reason = 'user' WHERE id = $1`, aid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.files.AudioURL(ctx, u.ID, mid); !apperr.Is(err, apperr.AudioUnavailable) {
		t.Fatal(err)
	}
	if _, err := e.files.Open(ctx, "audio", id, ex, sig); !apperr.Is(err, apperr.NotFound) {
		t.Fatal("deleted audio must be 404 even with a valid URL:", err)
	}
	if f, err = e.files.Open(ctx, "export", eid, eex, esig); err != nil {
		t.Fatal("exports survive audio deletion:", err)
	}
	f.Body.Close()

	if _, err := e.pool.Exec(ctx, `UPDATE meetings SET deleted_at = now() WHERE id = $1`, mid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.files.Open(ctx, "export", eid, eex, esig); !apperr.Is(err, apperr.NotFound) {
		t.Fatal("tombstoned meeting export must be 404:", err)
	}
	if _, _, err := e.files.AudioURL(ctx, u.ID, mid); !apperr.Is(err, apperr.NotFound) {
		t.Fatal(err)
	}
}
