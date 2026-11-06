package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/testutil"
)

type env struct {
	pool    *pgxpool.Pool
	user    string
	meeting string
}

func setup(t *testing.T) env {
	pool := testutil.DB(t)
	u := testutil.CreateUser(t, pool, 600)
	return env{pool: pool, user: u.ID, meeting: testutil.CreateMeeting(t, pool, u.ID)}
}

func runner(e env, lease time.Duration) *jobs.Runner {
	return jobs.NewRunner(e.pool, testutil.DiscardLogger(), jobs.RunnerConfig{
		WorkerID: "w", Concurrency: 2, PollInterval: 20 * time.Millisecond, Lease: lease,
		ShutdownGrace: 100 * time.Millisecond, RetryBase: 10 * time.Second, SweepInterval: time.Hour,
	})
}

func enqueue(t *testing.T, e env, typ, key string) *jobs.Job {
	t.Helper()
	j, err := jobs.Enqueue(context.Background(), e.pool, jobs.NewJob{
		Type: typ, UserID: e.user, MeetingID: e.meeting, Trigger: jobs.TriggerUser,
		IdempotencyKey: key, RequestFingerprint: testutil.SHA256Hex([]byte(key)),
		Payload: map[string]string{"language": "id"}, MaxAttempts: jobs.AIMaxAttempts, CorrelationID: ids.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func get(t *testing.T, e env, id string) *jobs.Job {
	t.Helper()
	j, err := jobs.Get(context.Background(), e.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func meetingUpdated(t *testing.T, e env) time.Time {
	var ts time.Time
	if err := e.pool.QueryRow(context.Background(), "SELECT updated_at FROM meetings WHERE id=$1", e.meeting).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	return ts
}

func spec(h jobs.Handler) jobs.Spec {
	return jobs.Spec{Handler: h, Deadline: func(*jobs.Job) time.Duration { return 5 * time.Second }}
}

func TestEnqueueDuplicateAndLookups(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	before := meetingUpdated(t, e)
	j := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	if j.Status != jobs.StatusQueued || j.Attempts != 0 || j.MaxAttempts != 3 || string(j.Payload) != `{"language": "id"}` {
		t.Fatalf("%+v %s", j, j.Payload)
	}
	if !meetingUpdated(t, e).After(before) {
		t.Fatal("enqueue must bump meetings.updated_at")
	}
	_, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{Type: jobs.TypeTranscribe, UserID: e.user, MeetingID: e.meeting,
		Trigger: jobs.TriggerUser, IdempotencyKey: j.IdempotencyKey, RequestFingerprint: *j.RequestFingerprint,
		MaxAttempts: 3, CorrelationID: ids.New()})
	if !errors.Is(err, jobs.ErrDuplicate) {
		t.Fatal("same key", err)
	}
	_, err = jobs.Enqueue(ctx, e.pool, jobs.NewJob{Type: jobs.TypeTranscribe, UserID: e.user, MeetingID: e.meeting,
		Trigger: jobs.TriggerUser, IdempotencyKey: ids.New(), RequestFingerprint: *j.RequestFingerprint,
		MaxAttempts: 3, CorrelationID: ids.New()})
	if !errors.Is(err, jobs.ErrDuplicate) {
		t.Fatal("one active per meeting/type", err)
	}
	if k, err := jobs.GetByKey(ctx, e.pool, e.user, j.IdempotencyKey); err != nil || k.ID != j.ID {
		t.Fatal("GetByKey", err)
	}
	if a, err := jobs.Active(ctx, e.pool, e.meeting, jobs.TypeTranscribe); err != nil || a.ID != j.ID {
		t.Fatal("Active", err)
	}
	if l, err := jobs.Latest(ctx, e.pool, e.meeting, jobs.TypeTranscribe); err != nil || l.ID != j.ID {
		t.Fatal("Latest", err)
	}
	if _, err := jobs.Active(ctx, e.pool, e.meeting, jobs.TypeGenerateMoM); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("Active none", err)
	}

	pid := ids.New()
	p, err := jobs.Enqueue(ctx, e.pool, jobs.NewJob{ID: pid, Type: jobs.TypePurgeMeeting, Trigger: jobs.TriggerSystem,
		IdempotencyKey: "sys:purge_meeting:" + e.meeting, Payload: map[string]string{"meeting_id": e.meeting},
		MaxAttempts: jobs.PurgeMaxAttempts, RunAfter: time.Now().Add(time.Hour), CorrelationID: ids.New()})
	if err != nil || p.ID != pid || p.UserID != nil || !p.RunAfter.After(time.Now()) {
		t.Fatal("purge enqueue", err)
	}
	if k, err := jobs.GetByKey(ctx, e.pool, "", "sys:purge_meeting:"+e.meeting); err != nil || k.ID != pid {
		t.Fatal("GetByKey NULL user", err)
	}
}

func TestClaimNoneAndOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if j, err := jobs.Claim(ctx, e.pool, "w/0", time.Minute); j != nil || err != nil {
		t.Fatal("expected none", j, err)
	}
	a := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	b := enqueue(t, e, jobs.TypeGenerateNotes, ids.New())
	_, _ = e.pool.Exec(ctx, "UPDATE jobs SET run_after = now() + interval '1 hour' WHERE id=$1", a.ID)
	j, err := jobs.Claim(ctx, e.pool, "w/0", time.Minute)
	if err != nil || j.ID != b.ID || j.Status != jobs.StatusRunning || j.Attempts != 1 || *j.LeaseOwner != "w/0" || j.StartedAt == nil {
		t.Fatalf("claim: %+v %v", j, err)
	}
	if j2, _ := jobs.Claim(ctx, e.pool, "w/1", time.Minute); j2 != nil {
		t.Fatal("future job claimed")
	}
}

func TestSuccessViaComplete(t *testing.T) {
	e := setup(t)
	j := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	r := runner(e, time.Minute)
	staged := false
	r.Register(jobs.TypeTranscribe, spec(func(ctx context.Context, j *jobs.Job) error {
		var p struct{ Language string }
		if err := j.DecodePayload(&p); err != nil || p.Language != "id" {
			t.Errorf("payload %v %v", p, err)
		}
		return jobs.Complete(ctx, e.pool, j, map[string]string{"transcript_revision_id": "x"}, func(tx pgx.Tx) error {
			staged = true
			return nil
		})
	}))
	ran, err := r.RunOnce(context.Background())
	if !ran || err != nil {
		t.Fatal(ran, err)
	}
	got := get(t, e, j.ID)
	if got.Status != jobs.StatusSucceeded || string(got.Result) != `{"transcript_revision_id": "x"}` || got.FinishedAt == nil || got.LeaseOwner != nil || !staged {
		t.Fatalf("%+v %s", got, got.Result)
	}
	if ran, _ := r.RunOnce(context.Background()); ran {
		t.Fatal("nothing should be left")
	}
}

func TestRetryThenPermanentAfterMaxAttempts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	j := enqueue(t, e, jobs.TypeGenerateNotes, ids.New())
	r := runner(e, time.Minute)
	var calls, failures atomic.Int32
	var failCat string
	r.Register(jobs.TypeGenerateNotes, jobs.Spec{
		Handler: func(ctx context.Context, j *jobs.Job) error {
			calls.Add(1)
			return jobs.Retryable(jobs.CatInvalidOutput, "", 0)
		},
		Deadline: func(*jobs.Job) time.Duration { return time.Second },
		OnFailure: func(ctx context.Context, tx pgx.Tx, j *jobs.Job, category string) error {
			failures.Add(1)
			failCat = category
			_, err := tx.Exec(ctx, "UPDATE meetings SET pipeline_state='notes_failed' WHERE id=$1", *j.MeetingID)
			return err
		},
	})
	for attempt := 1; attempt <= 3; attempt++ {
		ran, err := r.RunOnce(ctx)
		if !ran || err != nil {
			t.Fatal(attempt, ran, err)
		}
		got := get(t, e, j.ID)
		if attempt < 3 {
			wait := time.Until(got.RunAfter)
			lo, hi := time.Duration(float64(10*time.Second)*0.75), time.Duration(float64(10*time.Second)*1.25)
			if attempt == 2 {
				lo, hi = lo*3, hi*3
			}
			if got.Status != jobs.StatusQueued || got.Attempts != attempt || *got.LastErrorCategory != jobs.CatInvalidOutput || wait < lo || wait > hi {
				t.Fatalf("attempt %d: status=%s attempts=%d wait=%v", attempt, got.Status, got.Attempts, wait)
			}
			if ran, _ := r.RunOnce(ctx); ran {
				t.Fatal("ran before backoff elapsed")
			}
			_, _ = e.pool.Exec(ctx, "UPDATE jobs SET run_after = now() WHERE id=$1", j.ID)
		}
	}
	got := get(t, e, j.ID)
	var state string
	_ = e.pool.QueryRow(ctx, "SELECT pipeline_state FROM meetings WHERE id=$1", e.meeting).Scan(&state)
	if got.Status != jobs.StatusFailed || got.Attempts != 3 || calls.Load() != 3 || failures.Load() != 1 || failCat != jobs.CatInvalidOutput || state != "notes_failed" {
		t.Fatalf("status=%s attempts=%d calls=%d failures=%d state=%s", got.Status, got.Attempts, calls.Load(), failures.Load(), state)
	}
}

func TestPermanentPanicAndDeadline(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	r := runner(e, time.Minute)
	r.Register(jobs.TypeTranscribe, spec(func(context.Context, *jobs.Job) error {
		return jobs.Permanent(jobs.CatNoSpeech, "")
	}))
	r.Register(jobs.TypeGenerateNotes, spec(func(context.Context, *jobs.Job) error { panic("boom") }))
	r.Register(jobs.TypeGenerateMoM, jobs.Spec{
		Handler:  func(ctx context.Context, j *jobs.Job) error { <-ctx.Done(); return ctx.Err() },
		Deadline: func(*jobs.Job) time.Duration { return 50 * time.Millisecond },
	})
	a := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	b := enqueue(t, e, jobs.TypeGenerateNotes, ids.New())
	c := enqueue(t, e, jobs.TypeGenerateMoM, ids.New())
	for i := 0; i < 3; i++ {
		if ran, err := r.RunOnce(ctx); !ran || err != nil {
			t.Fatal(ran, err)
		}
	}
	if g := get(t, e, a.ID); g.Status != jobs.StatusFailed || *g.LastErrorCategory != jobs.CatNoSpeech || g.Attempts != 1 {
		t.Fatalf("permanent: %s", g.Status)
	}
	if g := get(t, e, b.ID); g.Status != jobs.StatusQueued || *g.LastErrorCategory != jobs.CatInternal {
		t.Fatalf("panic: %s", g.Status)
	}
	if g := get(t, e, c.ID); g.Status != jobs.StatusQueued || *g.LastErrorCategory != jobs.CatTimeout {
		t.Fatalf("deadline: %s %v", g.Status, g.LastErrorCategory)
	}
}

func TestCompleteOnTombstonedMeetingIsLeaseLost(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	j := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	r := runner(e, time.Minute)
	var completeErr error
	staged := false
	r.Register(jobs.TypeTranscribe, spec(func(ctx context.Context, j *jobs.Job) error {
		_, _ = e.pool.Exec(ctx, "UPDATE meetings SET deleted_at = now() WHERE id=$1", e.meeting)
		_, _ = e.pool.Exec(ctx, `UPDATE jobs SET status='canceled', finished_at=now(), lease_owner=NULL, lease_until=NULL WHERE id=$1`, j.ID)
		completeErr = jobs.Complete(ctx, e.pool, j, map[string]int{}, func(pgx.Tx) error { staged = true; return nil })
		return completeErr
	}))
	if ran, err := r.RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if !errors.Is(completeErr, jobs.ErrLeaseLost) || staged || get(t, e, j.ID).Status != jobs.StatusCanceled {
		t.Fatal("finalize must write nothing on a tombstoned meeting", completeErr, staged)
	}
}

func TestFencingRejectsStaleAttempt(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	enqueue(t, e, jobs.TypeTranscribe, ids.New())
	j, _ := jobs.Claim(ctx, e.pool, "w/0", time.Minute)
	_, _ = e.pool.Exec(ctx, "UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id=$1", j.ID)
	j2, _ := jobs.Claim(ctx, e.pool, "w/1", time.Minute)
	if j2 == nil || j2.ID != j.ID || j2.Attempts != 2 || *j2.LastErrorCategory != jobs.CatTimeout {
		t.Fatal("expired lease not reclaimed", j2)
	}
	for name, f := range map[string]func() (bool, error){
		"heartbeat": func() (bool, error) { return jobs.Heartbeat(ctx, e.pool, j.Token(), time.Minute) },
		"retry":     func() (bool, error) { return jobs.Retry(ctx, e.pool, j, 0, jobs.CatInternal, "") },
		"succeed":   func() (bool, error) { return jobs.Succeed(ctx, e.pool, j, map[string]int{}) },
		"fail":      func() (bool, error) { return jobs.Fail(ctx, e.pool, j, jobs.CatInternal, "") },
		"release":   func() (bool, error) { return jobs.Release(ctx, e.pool, j) },
	} {
		if ok, err := f(); ok || err != nil {
			t.Fatalf("%s with stale token succeeded (err=%v)", name, err)
		}
	}
	if ok, err := jobs.Heartbeat(ctx, e.pool, j2.Token(), time.Minute); !ok || err != nil {
		t.Fatal("current token heartbeat", err)
	}
}

func TestAttemptsExhaustedByExpiredLeases(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	j := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	_, _ = e.pool.Exec(ctx, `UPDATE jobs SET status='running', attempts=3, lease_owner='dead', lease_until=now()-interval '1 second' WHERE id=$1`, j.ID)
	r := runner(e, time.Minute)
	called := false
	var cat string
	r.Register(jobs.TypeTranscribe, jobs.Spec{
		Handler:   func(context.Context, *jobs.Job) error { called = true; return nil },
		Deadline:  func(*jobs.Job) time.Duration { return time.Second },
		OnFailure: func(_ context.Context, _ pgx.Tx, _ *jobs.Job, c string) error { cat = c; return nil },
	})
	if ran, err := r.RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	g := get(t, e, j.ID)
	if called || g.Status != jobs.StatusFailed || g.Attempts != 4 || cat != jobs.CatTimeout {
		t.Fatalf("called=%v status=%s attempts=%d cat=%s", called, g.Status, g.Attempts, cat)
	}
}

func TestHeartbeatLossCancelsHandler(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	j := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	r := runner(e, 300*time.Millisecond)
	var cause error
	r.Register(jobs.TypeTranscribe, spec(func(hctx context.Context, j *jobs.Job) error {
		_, _ = e.pool.Exec(ctx, `UPDATE jobs SET status='canceled', finished_at=now(), lease_owner=NULL, lease_until=NULL WHERE id=$1`, j.ID)
		select {
		case <-hctx.Done():
			cause = context.Cause(hctx)
			return hctx.Err()
		case <-time.After(3 * time.Second):
			return nil
		}
	}))
	if ran, err := r.RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if !errors.Is(cause, jobs.ErrLeaseLost) || get(t, e, j.ID).Status != jobs.StatusCanceled {
		t.Fatal("heartbeat loss must cancel the attempt and leave the row alone", cause)
	}
}

func TestShutdownReleasesWithoutChargingAttempt(t *testing.T) {
	e := setup(t)
	j := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	r := runner(e, time.Minute)
	started := make(chan struct{})
	r.Register(jobs.TypeTranscribe, spec(func(ctx context.Context, j *jobs.Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- r.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("job not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after grace")
	}
	g := get(t, e, j.ID)
	if g.Status != jobs.StatusQueued || g.Attempts != 0 || g.LeaseOwner != nil {
		t.Fatalf("status=%s attempts=%d", g.Status, g.Attempts)
	}
}

func TestRunProcessesConcurrently(t *testing.T) {
	e := setup(t)
	u2 := testutil.CreateUser(t, e.pool, 0)
	var n atomic.Int32
	for i := 0; i < 4; i++ {
		e2 := env{pool: e.pool, user: u2.ID, meeting: testutil.CreateMeeting(t, e.pool, u2.ID)}
		enqueue(t, e2, jobs.TypeTranscribe, ids.New())
	}
	r := runner(e, time.Minute)
	r.Register(jobs.TypeTranscribe, spec(func(ctx context.Context, j *jobs.Job) error {
		n.Add(1)
		return jobs.Complete(ctx, e.pool, j, map[string]int{}, nil)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- r.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	var succeeded int
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE status='succeeded'").Scan(&succeeded)
	if n.Load() != 4 || succeeded != 4 {
		t.Fatalf("ran=%d succeeded=%d", n.Load(), succeeded)
	}
}

func TestBackoff(t *testing.T) {
	b := jobs.AIBackoff(10 * time.Second)
	for i := 0; i < 200; i++ {
		d1, d2, d9 := b.Delay(1, 0), b.Delay(2, 0), b.Delay(9, 0)
		if d1 < 8*time.Second || d1 > 12*time.Second || d2 < 24*time.Second || d2 > 36*time.Second || d9 > 6*time.Minute || d9 < 4*time.Minute {
			t.Fatal(d1, d2, d9)
		}
	}
	if d := b.Delay(1, 2*time.Minute); d != 2*time.Minute {
		t.Fatal("Retry-After not honored", d)
	}
	if d := b.Delay(1, 11*time.Minute); d > 12*time.Second {
		t.Fatal("Retry-After > 10 min must be ignored", d)
	}
	p := jobs.PurgeBackoff()
	if d := p.Delay(20, 0); d > 72*time.Minute || d < 48*time.Minute {
		t.Fatal("purge cap", d)
	}
}

func TestSweepsAndAlerts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	r := runner(e, time.Minute)
	var swept atomic.Int32
	r.RegisterSweep("count", func(context.Context) (int, error) { swept.Add(1); return 3, nil })
	alerts := jobs.AlertSweep(e.pool, testutil.DiscardLogger(), 10*time.Minute, 1)
	r.RegisterSweep("alerts", alerts)
	if err := r.RunSweeps(ctx); err != nil || swept.Load() != 1 {
		t.Fatal(err)
	}
	if n, err := alerts(ctx); err != nil || n != 0 {
		t.Fatal("no alerts expected", n, err)
	}
	j := enqueue(t, e, jobs.TypeTranscribe, ids.New())
	_, _ = e.pool.Exec(ctx, "UPDATE jobs SET run_after = now() - interval '11 minutes' WHERE id=$1", j.ID)
	_, _ = e.pool.Exec(ctx, `INSERT INTO ai_calls (user_id, job_type, attempt, capability, provider, model, latency_ms,
		cost_usd_micros, outcome, correlation_id) VALUES ($1, 'transcribe', 1, 'stt', 'p', 'm', 1, 1500000, 'ok', $2)`, e.user, ids.New())
	if n, err := alerts(ctx); err != nil || n != 2 {
		t.Fatal("expected queue_stuck + cost_spike", n, err)
	}
	failing := runner(e, time.Minute)
	failing.RegisterSweep("bad", func(context.Context) (int, error) { return 0, errors.New("x") })
	if failing.RunSweeps(ctx) == nil {
		t.Fatal("sweep error not reported")
	}
}
