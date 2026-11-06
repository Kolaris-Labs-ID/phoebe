package transcripts

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai/fake"
	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
	"phoebe/internal/usage"
)

var ctx = context.Background()

type env struct {
	pool   *pgxpool.Pool
	store  *storage.LocalFS
	cfg    *config.Config
	stt    *fake.STT
	svc    *Service
	runner *jobs.Runner
}

func newEnv(t *testing.T, stt *fake.STT, mod func(*config.Config)) *env {
	t.Helper()
	pool := testutil.DB(t)
	store := testutil.Storage(t)
	cfg := testutil.Config(t)
	if mod != nil {
		mod(cfg)
	}
	if stt == nil {
		stt = &fake.STT{}
	}
	log := testutil.DiscardLogger()
	svc := New(pool, cfg, store, usage.New(pool, cfg, log), stt, log)
	r := jobs.NewRunner(pool, log, jobs.RunnerConfig{WorkerID: "test", Lease: time.Minute, RetryBase: time.Millisecond})
	r.Register(jobs.TypeTranscribe, svc.TranscribeSpec())
	return &env{pool: pool, store: store, cfg: cfg, stt: stt, svc: svc, runner: r}
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *env) str(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := e.pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// meeting creates a user (quota seconds) and a meeting in 'uploaded' with a verified WAV of durationMS.
func (e *env) meeting(t *testing.T, quota, durationMS int64) (uid, mid, assetID string) {
	t.Helper()
	u := testutil.CreateUser(t, e.pool, quota)
	mid = testutil.CreateMeeting(t, e.pool, u.ID)
	return u.ID, mid, e.audio(t, u.ID, mid, durationMS)
}

func (e *env) audio(t *testing.T, uid, mid string, durationMS int64) string {
	t.Helper()
	b := testutil.WAV(durationMS)
	sum := testutil.SHA256Hex(b)
	sid := ids.New()
	key := "u/" + uid + "/m/" + mid + "/audio/" + sid + ".wav"
	if _, err := e.store.Put(ctx, key, bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO upload_sessions (id, meeting_id, user_id, status, declared_content_type, size_bytes, sha256,
		declared_duration_ms, received_bytes, storage_key, expires_at, completed_at)
		VALUES ($1, $2, $3, 'completed', 'audio/wav', $4, $5, $6, $4, $7, now() + interval '1 day', now())`,
		sid, mid, uid, len(b), sum, durationMS, "u/"+uid+"/m/"+mid+"/uploads/"+sid+".part")
	assetID := ids.New()
	e.exec(t, `INSERT INTO audio_assets (id, meeting_id, upload_session_id, content_type, size_bytes, sha256, duration_ms, storage_key)
		VALUES ($1, $2, $3, 'audio/wav', $4, $5, $6, $7)`, assetID, mid, sid, len(b), sum, durationMS, key)
	e.exec(t, `UPDATE meetings SET pipeline_state = 'uploaded' WHERE id = $1`, mid)
	return assetID
}

func (e *env) start(t *testing.T, uid, mid string) string {
	t.Helper()
	j, created, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{}, nil)
	if err != nil || !created {
		t.Fatalf("start: %v created=%v", err, created)
	}
	return j.ID
}

// run makes every queued job due and runs one.
func (e *env) run(t *testing.T) {
	t.Helper()
	e.exec(t, `UPDATE jobs SET run_after = now() WHERE status = 'queued'`)
	ran, err := e.runner.RunOnce(ctx)
	if err != nil || !ran {
		t.Fatalf("RunOnce: ran=%v err=%v", ran, err)
	}
}

func (e *env) job(t *testing.T, id string) *jobs.Job {
	t.Helper()
	j, err := jobs.Get(ctx, e.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (e *env) state(t *testing.T, mid string) string {
	return e.str(t, `SELECT pipeline_state FROM meetings WHERE id = $1`, mid)
}

func (e *env) updatedAt(t *testing.T, mid string) time.Time {
	t.Helper()
	var at time.Time
	if err := e.pool.QueryRow(ctx, `SELECT updated_at FROM meetings WHERE id = $1`, mid).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func code(err error) apperr.Code { return apperr.CodeOf(err) }

func details(err error) map[string]any {
	if ae, ok := err.(*apperr.Error); ok {
		return ae.Details
	}
	return nil
}
