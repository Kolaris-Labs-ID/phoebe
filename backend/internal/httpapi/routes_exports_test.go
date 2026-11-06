package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/auth"
	"phoebe/internal/export"
	"phoebe/internal/ids"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
)

const manualNotesJSON = `{"schema_version": 1, "items": [{"id": "n1", "text": "Bahas budget Q1", "at_ms": 125000}]}`

func newExportsServer(t *testing.T) (*Server, *httptest.Server, *pgxpool.Pool, *storage.LocalFS, *storage.Signer) {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	log := testutil.DiscardLogger()
	store := testutil.Storage(t)
	signer := storage.NewSigner(cfg.SigningSecret, cfg.PublicBaseURL, cfg.SignedURLTTL)
	a := auth.NewService(pool, cfg, log, nil)
	s := New(Deps{Config: cfg, Log: log, DB: pool, Storage: store, Signer: signer, Sessions: a, Auth: a,
		Exports: export.NewService(pool, store, signer, log), Files: export.NewFiles(pool, store, signer)})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv, pool, store, signer
}

func insertRevision(t *testing.T, pool *pgxpool.Pool, meetingID string) string {
	t.Helper()
	ctx := context.Background()
	var docID, revID string
	if err := pool.QueryRow(ctx, `INSERT INTO documents (meeting_id, kind, current_revision_number) VALUES ($1, 'manual_notes', 1) RETURNING id`,
		meetingID).Scan(&docID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO document_revisions (document_id, revision_number, origin, schema_version, content, mutation_id, mutation_fingerprint)
		VALUES ($1, 1, 'user_edit', 1, $2, $3, $4) RETURNING id`,
		docID, manualNotesJSON, ids.New(), strings.Repeat("b", 64)).Scan(&revID); err != nil {
		t.Fatal(err)
	}
	return revID
}

func insertAudioAsset(t *testing.T, pool *pgxpool.Pool, store storage.Storage, uid, mid string, data []byte) string {
	t.Helper()
	ctx := context.Background()
	var sid, aid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO upload_sessions (meeting_id, user_id, status, declared_content_type, size_bytes, sha256, declared_duration_ms,
		                             received_bytes, storage_key, expires_at, completed_at)
		VALUES ($1, $2, 'completed', 'audio/wav', $3, $4, 2000, $3, 'x', now() + interval '1 day', now()) RETURNING id`,
		mid, uid, len(data), testutil.SHA256Hex(data)).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	key := "u/" + uid + "/m/" + mid + "/audio/" + sid + ".wav"
	if _, err := store.Put(ctx, key, strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO audio_assets (meeting_id, upload_session_id, content_type, size_bytes, sha256, duration_ms, storage_key)
		VALUES ($1, $2, 'audio/wav', $3, $4, 2000, $5) RETURNING id`,
		mid, sid, len(data), testutil.SHA256Hex(data), key).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	return aid
}

func fetch(t *testing.T, srv *httptest.Server, signed string, rangeHdr string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+strings.TrimPrefix(signed, "http://phoebe.test"), nil)
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func fileErr(b []byte) string {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return errCode(m)
}

func TestExportsAndFilesHTTP(t *testing.T) {
	_, srv, pool, store, signer := newExportsServer(t)
	u := testutil.CreateUser(t, pool, 0)
	mid := testutil.CreateMeeting(t, pool, u.ID)
	rev := insertRevision(t, pool, mid)

	st, body := call(t, srv, "POST", "/v1/exports", u.Token, `{"document_revision_id":"`+rev+`","format":"md"}`)
	x, _ := body["export"].(map[string]any)
	if st != 201 || x["kind"] != "manual_notes" || x["is_draft"] != true || x["content_type"] != "text/markdown; charset=utf-8" ||
		!strings.Contains(body["text"].(string), "DRAFT — belum ditinjau") || body["download_expires_at"] == nil {
		t.Fatal(st, body)
	}
	resp, b := fetch(t, srv, body["download_url"].(string), "")
	if resp.StatusCode != 200 || string(b) != body["text"].(string) || resp.Header.Get("Cache-Control") != "private, no-store" ||
		resp.Header.Get("Content-Type") != "text/markdown; charset=utf-8" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="`+x["filename"].(string)+`"` {
		t.Fatal(resp.StatusCode, resp.Header, string(b))
	}

	st, body = call(t, srv, "POST", "/v1/exports", u.Token, `{"document_revision_id":"`+rev+`","format":"pdf"}`)
	if st != 201 || body["text"] != nil {
		t.Fatal(st, body)
	}
	pdfID := body["export"].(map[string]any)["id"].(string)
	st, got := call(t, srv, "GET", "/v1/exports/"+pdfID, u.Token, "")
	if st != 200 || got["text"] != nil || got["export"].(map[string]any)["filename"] != body["export"].(map[string]any)["filename"] {
		t.Fatal(st, got)
	}
	resp, b = fetch(t, srv, got["download_url"].(string), "bytes=0-4")
	if resp.StatusCode != 206 || string(b) != "%PDF-" || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal(resp.StatusCode, string(b))
	}

	for _, c := range []struct{ body, field string }{
		{`{"format":"pdf"}`, "document_revision_id"},
		{`{"document_revision_id":"` + rev + `","format":"rtf"}`, "format"},
	} {
		if st, body = call(t, srv, "POST", "/v1/exports", u.Token, c.body); st != 422 || errCode(body) != "validation_failed" {
			t.Fatal(c, st, body)
		}
	}
	if st, body = call(t, srv, "POST", "/v1/exports", u.Token, `{"document_revision_id":"`+ids.New()+`","format":"pdf"}`); st != 404 {
		t.Fatal(st, body)
	}
	if st, _ = call(t, srv, "POST", "/v1/exports", "", `{}`); st != 401 {
		t.Fatal(st)
	}

	signed := got["download_url"].(string)
	tampered := strings.Replace(signed, "sig=", "sig=A", 1)
	if resp, b = fetch(t, srv, tampered, ""); resp.StatusCode != 403 || fileErr(b) != "invalid_signature" {
		t.Fatal(resp.StatusCode, string(b))
	}
	otherID := strings.Replace(signed, pdfID, ids.New(), 1)
	if resp, b = fetch(t, srv, otherID, ""); resp.StatusCode != 403 || fileErr(b) != "invalid_signature" {
		t.Fatal(resp.StatusCode, string(b))
	}
	expired, _ := signer.Sign("export", pdfID, time.Now().Add(-time.Hour))
	if resp, b = fetch(t, srv, expired, ""); resp.StatusCode != 410 || fileErr(b) != "url_expired" {
		t.Fatal(resp.StatusCode, string(b))
	}
	if resp, b = fetch(t, srv, "/v1/files/export/"+pdfID, ""); resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode, string(b))
	}

	if st, body = call(t, srv, "GET", "/v1/meetings/"+mid+"/audio/url", u.Token, ""); st != 409 || errCode(body) != "audio_not_ready" {
		t.Fatal(st, body)
	}
	wav := testutil.WAV(2000)
	aid := insertAudioAsset(t, pool, store, u.ID, mid, wav)
	st, body = call(t, srv, "GET", "/v1/meetings/"+mid+"/audio/url", u.Token, "")
	if st != 200 || !strings.Contains(body["url"].(string), "/v1/files/audio/"+aid+"?") || body["expires_at"] == nil {
		t.Fatal(st, body)
	}
	audioURL := body["url"].(string)
	resp, b = fetch(t, srv, audioURL, "bytes=100-199")
	if resp.StatusCode != 206 || string(b) != string(wav[100:200]) || resp.Header.Get("Content-Type") != "audio/wav" ||
		resp.Header.Get("Content-Disposition") != "" || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatal(resp.StatusCode, resp.Header)
	}
	if resp, b = fetch(t, srv, audioURL, ""); resp.StatusCode != 200 || len(b) != len(wav) {
		t.Fatal(resp.StatusCode, len(b))
	}

	if _, err := pool.Exec(context.Background(), `UPDATE audio_assets SET deleted_at = now(), deleted_reason = 'expired' WHERE id = $1`, aid); err != nil {
		t.Fatal(err)
	}
	if st, body = call(t, srv, "GET", "/v1/meetings/"+mid+"/audio/url", u.Token, ""); st != 410 || errCode(body) != "audio_unavailable" {
		t.Fatal(st, body)
	}
	if resp, b = fetch(t, srv, audioURL, ""); resp.StatusCode != 404 || fileErr(b) != "not_found" {
		t.Fatal(resp.StatusCode, string(b))
	}

	if _, err := pool.Exec(context.Background(), `UPDATE meetings SET deleted_at = now() WHERE id = $1`, mid); err != nil {
		t.Fatal(err)
	}
	if resp, b = fetch(t, srv, signed, ""); resp.StatusCode != 404 || fileErr(b) != "not_found" {
		t.Fatal(resp.StatusCode, string(b))
	}
}

func TestExportsFilesCrossAccount(t *testing.T) {
	s, srv, pool, store, _ := newExportsServer(t)
	a := testutil.CreateUser(t, pool, 0)
	b := testutil.CreateUser(t, pool, 0)
	mid := testutil.CreateMeeting(t, pool, a.ID)
	rev := insertRevision(t, pool, mid)
	insertAudioAsset(t, pool, store, a.ID, mid, testutil.WAV(2000))
	_, body := call(t, srv, "POST", "/v1/exports", a.Token, `{"document_revision_id":"`+rev+`","format":"docx"}`)
	eid := body["export"].(map[string]any)["id"].(string)

	count := func() (n int) {
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM exports`).Scan(&n)
		return
	}
	before := count()
	cases := map[string][]struct{ method, path, body string }{
		"POST /v1/exports":                {{"POST", "/v1/exports", `{"document_revision_id":"` + rev + `","format":"pdf"}`}},
		"GET /v1/exports/{id}":            {{"GET", "/v1/exports/" + eid, ""}},
		"GET /v1/meetings/{id}/audio/url": {{"GET", "/v1/meetings/" + mid + "/audio/url", ""}},
	}
	for _, rt := range append(s.routesExports(), s.routesFiles()...) {
		if _, ok := cases[rt.Pattern()]; !ok && !rt.Public {
			t.Errorf("no cross-account case for %s", rt.Pattern())
		}
	}
	for route, list := range cases {
		for _, c := range list {
			if st, body := call(t, srv, c.method, c.path, b.Token, c.body); st != 404 || errCode(body) != "not_found" {
				t.Errorf("%s: %d %v", route, st, body)
			}
		}
	}
	if count() != before {
		t.Fatal("user B created an export")
	}
}
