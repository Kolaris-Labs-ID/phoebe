package meetings

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
)

type env struct {
	pool  *pgxpool.Pool
	store *storage.LocalFS
	svc   *Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.DB(t)
	store := testutil.Storage(t)
	return &env{pool: pool, store: store, svc: New(pool, testutil.Config(t), store, testutil.DiscardLogger())}
}

var ctx = context.Background()

func ptr[T any](v T) *T { return &v }

func code(err error) apperr.Code { return apperr.CodeOf(err) }

func fields(err error) map[string]string {
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.ValidationFailed {
		return nil
	}
	out := map[string]string{}
	for _, f := range ae.Details["fields"].([]apperr.FieldError) {
		out[f.Field] = f.Reason
	}
	return out
}

func decodePatch(t *testing.T, body string) Patch {
	t.Helper()
	var p Patch
	if err := jsonx.Decode([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) create(t *testing.T, uid string, in CreateInput) *Meeting {
	t.Helper()
	if in.Timezone == nil {
		in.Timezone = ptr("Asia/Jakarta")
	}
	m, created, err := e.svc.Create(ctx, uid, in)
	if err != nil || !created {
		t.Fatalf("create: %v created=%v", err, created)
	}
	return m
}

func (e *env) label(t *testing.T, k LabelKind, uid, name string) string {
	t.Helper()
	l, _, err := e.svc.CreateLabel(ctx, k, uid, LabelInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	return l.ID
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// audio inserts a completed upload session + audio asset and stores the bytes.
func (e *env) audio(t *testing.T, uid, mid string, durationMS int64) (assetID, key string) {
	t.Helper()
	b := testutil.WAV(durationMS)
	sum := testutil.SHA256Hex(b)
	sid := ids.New()
	key = "u/" + uid + "/m/" + mid + "/audio/" + sid + ".wav"
	if _, err := e.store.Put(ctx, key, bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO upload_sessions (id, meeting_id, user_id, status, declared_content_type, size_bytes, sha256,
		declared_duration_ms, received_bytes, storage_key, expires_at, completed_at)
		VALUES ($1, $2, $3, 'completed', 'audio/wav', $4, $5, $6, $4, $7, now() + interval '1 day', now())`,
		sid, mid, uid, len(b), sum, durationMS, "u/"+uid+"/m/"+mid+"/uploads/"+sid+".part")
	assetID = ids.New()
	e.exec(t, `INSERT INTO audio_assets (id, meeting_id, upload_session_id, content_type, size_bytes, sha256, duration_ms, storage_key)
		VALUES ($1, $2, $3, 'audio/wav', $4, $5, $6, $7)`, assetID, mid, sid, len(b), sum, durationMS, key)
	return assetID, key
}

func (e *env) openUpload(t *testing.T, uid, mid string) string {
	t.Helper()
	sid := ids.New()
	e.exec(t, `INSERT INTO upload_sessions (id, meeting_id, user_id, declared_content_type, size_bytes, sha256,
		declared_duration_ms, storage_key, expires_at, writer_lease_id, writer_lease_until)
		VALUES ($1, $2, $3, 'audio/mp4', 1000, $4, 5000, $5, now() + interval '1 day', $6, now() + interval '5 minutes')`,
		sid, mid, uid, strings.Repeat("a", 64), "u/"+uid+"/m/"+mid+"/uploads/"+sid+".part", ids.New())
	return sid
}

func (e *env) job(t *testing.T, uid, mid, typ string) *jobs.Job {
	t.Helper()
	trigger, fp := jobs.TriggerUser, strings.Repeat("f", 64)
	j, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Type: typ, UserID: uid, MeetingID: mid, Trigger: trigger,
		IdempotencyKey: ids.New(), RequestFingerprint: fp, MaxAttempts: jobs.AIMaxAttempts, CorrelationID: ids.New()})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (e *env) finishJob(t *testing.T, jobID, status, category string) {
	t.Helper()
	var result any
	if status == jobs.StatusSucceeded {
		result = map[string]any{}
	}
	var cat *string
	if category != "" {
		cat = &category
	}
	e.exec(t, `UPDATE jobs SET status = $2, finished_at = now(), result = $3, last_error_category = $4 WHERE id = $1`,
		jobID, status, result, cat)
}

func (e *env) transcript(t *testing.T, uid, mid string, rev int, parent *string) string {
	t.Helper()
	id := ids.New()
	if rev == 1 {
		j := e.job(t, uid, mid, jobs.TypeTranscribe)
		e.finishJob(t, j.ID, jobs.StatusSucceeded, "")
		e.exec(t, `INSERT INTO transcript_revisions (id, meeting_id, revision_number, kind, language, provider, model, segments, job_id)
			VALUES ($1, $2, 1, 'original', 'id', 'fake', 'fake', '[]', $3)`, id, mid, j.ID)
		return id
	}
	e.exec(t, `INSERT INTO transcript_revisions (id, meeting_id, revision_number, kind, parent_revision_id, language, provider, model,
		segments, mutation_id, mutation_fingerprint)
		VALUES ($1, $2, $3, 'correction', $4, 'id', 'fake', 'fake', '[]', $5, $6)`, id, mid, rev, parent, ids.New(), strings.Repeat("e", 64))
	return id
}

type srcs struct{ transcript, manual, ai *string }

// docRev appends a user_edit revision with the given pinned sources and makes it current.
func (e *env) docRev(t *testing.T, mid, kind string, s srcs) string {
	t.Helper()
	var docID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO documents (meeting_id, kind) VALUES ($1, $2)
		ON CONFLICT (meeting_id, kind) DO UPDATE SET updated_at = now() RETURNING id`, mid, kind).Scan(&docID); err != nil {
		t.Fatal(err)
	}
	id := ids.New()
	e.exec(t, `INSERT INTO document_revisions (id, document_id, revision_number, origin, schema_version, content,
		source_transcript_revision_id, source_manual_notes_revision_id, source_ai_notes_revision_id, mutation_id, mutation_fingerprint)
		SELECT $1, $2, current_revision_number + 1, 'user_edit', 1, '{}', $3, $4, $5, $6, $7 FROM documents WHERE id = $2`,
		id, docID, s.transcript, s.manual, s.ai, ids.New(), strings.Repeat("d", 64))
	e.exec(t, `UPDATE documents SET current_revision_number = current_revision_number + 1 WHERE id = $1`, docID)
	return id
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// ageMeeting moves updated_at an hour back so a later bump is unambiguous.
func (e *env) ageMeeting(t *testing.T, mid string) {
	t.Helper()
	e.exec(t, `UPDATE meetings SET updated_at = now() - interval '1 hour' WHERE id = $1`, mid)
}

func (e *env) bumped(t *testing.T, mid string) bool {
	t.Helper()
	var at time.Time
	if err := e.pool.QueryRow(ctx, `SELECT updated_at FROM meetings WHERE id = $1`, mid).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return time.Since(at) < 10*time.Minute
}
