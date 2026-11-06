package documents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai"
	"phoebe/internal/ai/fake"
	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
	"phoebe/internal/testutil"
	"phoebe/internal/usage"
)

var bg = context.Background()

type denv struct {
	pool   *pgxpool.Pool
	cfg    *config.Config
	llm    ai.LLM
	svc    *Service
	runner *jobs.Runner
}

func newDEnv(t *testing.T, llm ai.LLM, tweak func(*config.Config)) *denv {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	if tweak != nil {
		tweak(cfg)
	}
	log := testutil.DiscardLogger()
	if llm == nil {
		llm = &fake.LLM{}
	}
	svc := New(pool, cfg, usage.New(pool, cfg, log), llm, log)
	r := jobs.NewRunner(pool, log, jobs.RunnerConfig{WorkerID: "test", Concurrency: 1, PollInterval: 10 * time.Millisecond,
		Lease: time.Minute, ShutdownGrace: time.Second, RetryBase: time.Millisecond, SweepInterval: time.Minute})
	r.Register(jobs.TypeGenerateNotes, svc.NotesSpec())
	r.Register(jobs.TypeGenerateMoM, svc.MoMSpec())
	return &denv{pool: pool, cfg: cfg, llm: llm, svc: svc, runner: r}
}

var seedSegments = []segment{
	{ID: "s1", StartMS: 0, EndMS: 4000, Text: "Selamat pagi semua, mari kita mulai rapatnya."},
	{ID: "s2", StartMS: 15000, EndMS: 19000, Text: "Kita sepakat rilis hari Jumat."},
	{ID: "s3", StartMS: 30000, EndMS: 34000, Text: "Budi akan menyiapkan laporan minggu depan."},
}

// seedMeeting creates a meeting in notes_ready with transcript revision 1 (timing "segment");
// returns (meeting, transcript rev id).
func (e *denv) seedMeeting(t *testing.T, uid string, segs []segment) (string, string) {
	t.Helper()
	return e.seedMeetingTiming(t, uid, segs, "segment")
}

func (e *denv) seedMeetingTiming(t *testing.T, uid string, segs []segment, timing string) (string, string) {
	t.Helper()
	mid := ids.New()
	if _, err := e.pool.Exec(bg, `INSERT INTO meetings (id, user_id, title, timezone, started_at, participants, pipeline_state)
		VALUES ($1, $2, 'Weekly sync', 'Asia/Jakarta', '2026-11-06T02:00:00Z', '{Budi,Sari}', 'notes_ready')`, mid, uid); err != nil {
		t.Fatal(err)
	}
	var jobID string
	if err := e.pool.QueryRow(bg, `INSERT INTO jobs (type, user_id, meeting_id, trigger, status, idempotency_key,
		request_fingerprint, result, max_attempts, correlation_id, finished_at)
		VALUES ('transcribe', $1, $2, 'user', 'succeeded', $3, repeat('a', 64), '{}', 3, gen_random_uuid(), now())
		RETURNING id`, uid, mid, ids.New()).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(segs)
	var trID string
	if err := e.pool.QueryRow(bg, `INSERT INTO transcript_revisions (meeting_id, revision_number, kind, language, provider, model, segments, job_id, timing)
		VALUES ($1, 1, 'original', 'id', 'fake', 'fake-stt', $2, $3, $4) RETURNING id`, mid, raw, jobID, timing).Scan(&trID); err != nil {
		t.Fatal(err)
	}
	return mid, trID
}

// correct inserts a transcript correction (next revision number) with the same segment ids.
func (e *denv) correct(t *testing.T, mid string) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(bg, `
		INSERT INTO transcript_revisions (meeting_id, revision_number, kind, parent_revision_id, language, provider, model,
		                                  segments, mutation_id, mutation_fingerprint, timing)
		SELECT meeting_id, revision_number + 1, 'correction', id, language, provider, model, segments, gen_random_uuid(), repeat('b', 64), timing
		FROM transcript_revisions WHERE meeting_id = $1 ORDER BY revision_number DESC LIMIT 1
		RETURNING id`, mid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := jsonx.Decode([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (e *denv) put(t *testing.T, uid, mid string, kind Kind, body string) (*View, bool, error) {
	t.Helper()
	return e.svc.Put(bg, uid, mid, kind, []byte(body), decode[PutInput](t, body))
}

func (e *denv) mustPut(t *testing.T, uid, mid string, kind Kind, body string) *View {
	t.Helper()
	v, created, err := e.put(t, uid, mid, kind, body)
	if err != nil || !created {
		t.Fatalf("put: %v created=%v", err, created)
	}
	return v
}

func (e *denv) generate(t *testing.T, uid, mid string, kind Kind, key, body string) (*jobsView, bool, error) {
	t.Helper()
	j, created, err := e.svc.Generate(bg, uid, mid, kind, key, []byte(body), decode[GenerateInput](t, body))
	if j == nil {
		return nil, created, err
	}
	return &jobsView{ID: j.ID, Status: j.Status}, created, err
}

type jobsView struct{ ID, Status string }

// runAll runs due jobs until none is left, forcing retry backoff to elapse.
func (e *denv) runAll(t *testing.T) int {
	t.Helper()
	n := 0
	for {
		if _, err := e.pool.Exec(bg, `UPDATE jobs SET run_after = now() WHERE status = 'queued'`); err != nil {
			t.Fatal(err)
		}
		ran, err := e.runner.RunOnce(bg)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return n
		}
		n++
	}
}

func (e *denv) job(t *testing.T, id string) *jobs.Job {
	t.Helper()
	j, err := jobs.Get(bg, e.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (e *denv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(bg, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *denv) state(t *testing.T, mid string) string {
	t.Helper()
	var s string
	if err := e.pool.QueryRow(bg, `SELECT pipeline_state FROM meetings WHERE id = $1`, mid).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *denv) updatedAt(t *testing.T, mid string) time.Time {
	t.Helper()
	var ts time.Time
	if err := e.pool.QueryRow(bg, `SELECT updated_at FROM meetings WHERE id = $1`, mid).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	return ts
}

// generateNotes runs a user-triggered notes generation to success and returns the job id.
func (e *denv) generateNotes(t *testing.T, uid, mid string) string {
	t.Helper()
	j, _, err := e.generate(t, uid, mid, KindAINotes, ids.New(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	e.runAll(t)
	if s := e.job(t, j.ID).Status; s != jobs.StatusSucceeded {
		t.Fatalf("notes job %s", s)
	}
	return j.ID
}

func (e *denv) generateMoM(t *testing.T, uid, mid string) string {
	t.Helper()
	j, _, err := e.generate(t, uid, mid, KindMoM, ids.New(), `{"ai_notes_revision_id": null}`)
	if err != nil {
		t.Fatal(err)
	}
	e.runAll(t)
	if s := e.job(t, j.ID).Status; s != jobs.StatusSucceeded {
		t.Fatalf("mom job %s %v", s, e.job(t, j.ID).LastErrorCategory)
	}
	return j.ID
}

func manualNotesBody(expected int, mutation string, texts ...string) string {
	var items []string
	for i, tx := range texts {
		items = append(items, `{"id":"n`+itoaTest(i+1)+`","text":"`+tx+`","at_ms":null}`)
	}
	return `{"expected_revision":` + itoaTest(expected) + `,"mutation_id":"` + mutation + `","content":{"schema_version":1,"items":[` +
		strings.Join(items, ",") + `]}}`
}

// putBody wraps content into a PUT body; base < 0 omits base_revision.
func putBody(expected, base int, mutation string, content json.RawMessage) string {
	b := `{"expected_revision":` + itoaTest(expected) + `,"mutation_id":"` + mutation + `","content":` + string(content)
	if base >= 0 {
		b += `,"base_revision":` + itoaTest(base)
	}
	return b + `}`
}

func code(err error) apperr.Code { return apperr.CodeOf(err) }

func (e *denv) slot(t *testing.T, jobID string) string {
	t.Helper()
	var st string
	err := e.pool.QueryRow(bg, `SELECT status FROM ai_generation_slots WHERE job_id = $1`, jobID).Scan(&st)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	return st
}

func (e *denv) remaining(t *testing.T, uid string) int {
	t.Helper()
	u, err := e.svc.usage.Get(bg, uid)
	if err != nil {
		t.Fatal(err)
	}
	return u.AIGenerations.Remaining
}

func details(err error) map[string]any {
	if ae, ok := err.(*apperr.Error); ok {
		return ae.Details
	}
	return nil
}
