package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/db"
	"phoebe/internal/obs"
)

// Handler runs one attempt. Return nil only after a successful Complete; otherwise
// Retryable/Permanent, ErrLeaseLost, or any error (= retryable internal). Panics are
// recovered as retryable internal.
type Handler func(ctx context.Context, j *Job) error

type Spec struct {
	Handler Handler
	// Deadline is the per-attempt deadline (backend.md §3.4); expiry = retryable timeout.
	Deadline func(j *Job) time.Duration
	// Backoff for retries; zero value = AIBackoff(RunnerConfig.RetryBase).
	Backoff Backoff
	// OnFailure runs the stage writes of a permanent failure inside the failure-finalize
	// tx, after the meeting lock and the fenced job update (release quota, pipeline_state).
	OnFailure func(ctx context.Context, tx pgx.Tx, j *Job, category string) error
}

type SweepFunc func(ctx context.Context) (count int, err error)

type RunnerConfig struct {
	WorkerID      string
	Concurrency   int
	PollInterval  time.Duration
	Lease         time.Duration
	ShutdownGrace time.Duration
	RetryBase     time.Duration
	SweepInterval time.Duration
}

type Pool interface {
	db.Querier
	db.TxBeginner
}

type namedSweep struct {
	name string
	fn   SweepFunc
}

type Runner struct {
	pool   Pool
	log    *slog.Logger
	cfg    RunnerConfig
	specs  map[string]Spec
	sweeps []namedSweep
}

func NewRunner(pool Pool, log *slog.Logger, cfg RunnerConfig) *Runner {
	return &Runner{pool: pool, log: log, cfg: cfg, specs: map[string]Spec{}}
}

func (r *Runner) Register(jobType string, s Spec) {
	if s.Handler == nil || s.Deadline == nil {
		panic("jobs: Register " + jobType + ": Handler and Deadline are required")
	}
	if s.Backoff == (Backoff{}) {
		s.Backoff = AIBackoff(r.cfg.RetryBase)
	}
	r.specs[jobType] = s
}

func (r *Runner) RegisterSweep(name string, fn SweepFunc) {
	r.sweeps = append(r.sweeps, namedSweep{name, fn})
}

// Run claims and runs jobs on Concurrency goroutines and runs sweeps every SweepInterval
// until ctx is canceled; then stops claiming, waits ShutdownGrace for running attempts,
// cancels the rest and requeues them without charging the attempt (backend.md §4.6).
func (r *Runner) Run(ctx context.Context) error {
	hardCtx, hardCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer hardCancel()
	var wg sync.WaitGroup
	for i := 0; i < r.cfg.Concurrency; i++ {
		owner := r.cfg.WorkerID + "/" + strconv.Itoa(i)
		wg.Go(func() { r.loop(ctx, hardCtx, owner) })
	}
	if len(r.sweeps) > 0 {
		wg.Go(func() { r.sweepLoop(ctx) })
	}
	<-ctx.Done()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(r.cfg.ShutdownGrace):
		hardCancel()
		<-done
	}
	return nil
}

// RunOnce claims and runs one job synchronously (tests). ran=false when none was due.
// err reports infrastructure failures only; handler outcomes are applied to the job row.
func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	j, err := Claim(ctx, r.pool, r.cfg.WorkerID+"/once", r.cfg.Lease)
	if err != nil || j == nil {
		return false, err
	}
	return true, r.execute(ctx, j)
}

// RunSweeps runs every registered sweep once, logging sweep.run per sweep.
func (r *Runner) RunSweeps(ctx context.Context) error {
	var errs []error
	for _, s := range r.sweeps {
		start := time.Now()
		n, err := s.fn(ctx)
		attrs := []any{"sweep", s.name, "count", n, "duration_ms", time.Since(start).Milliseconds()}
		if err != nil {
			errs = append(errs, fmt.Errorf("sweep %s: %w", s.name, err))
			r.log.ErrorContext(ctx, "sweep.run", append(attrs, "status", "error")...)
			continue
		}
		r.log.InfoContext(ctx, "sweep.run", append(attrs, "status", "ok")...)
	}
	return errors.Join(errs...)
}

func (r *Runner) sweepLoop(ctx context.Context) {
	t := time.NewTicker(r.cfg.SweepInterval)
	defer t.Stop()
	for {
		_ = r.RunSweeps(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Runner) loop(ctx, hardCtx context.Context, owner string) {
	for ctx.Err() == nil {
		j, err := Claim(hardCtx, r.pool, owner, r.cfg.Lease)
		if err != nil {
			r.log.ErrorContext(ctx, "job.claim_error", "error_category", CatInternal, "error", err.Error())
		}
		if j == nil {
			sleep(ctx, jitter(r.cfg.PollInterval))
			continue
		}
		if err := r.execute(hardCtx, j); err != nil {
			r.log.ErrorContext(ctx, "job.settle_error", "job_id", j.ID, "job_type", j.Type, "error", err.Error())
		}
	}
}

func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (r *Runner) jobCtx(ctx context.Context, j *Job) context.Context {
	ctx = obs.WithCorrelationID(ctx, j.CorrelationID)
	if j.UserID != nil {
		ctx = obs.WithUserID(ctx, *j.UserID)
	}
	return ctx
}

func (r *Runner) jobAttrs(j *Job) []any {
	a := []any{"job_id", j.ID, "job_type", j.Type, "attempt", j.Attempts}
	if j.MeetingID != nil {
		a = append(a, "meeting_id", *j.MeetingID)
	}
	return a
}

// execute runs one claimed attempt and applies its outcome. hardCtx is canceled only on
// forced shutdown.
func (r *Runner) execute(hardCtx context.Context, j *Job) error {
	start := time.Now()
	ctx := r.jobCtx(hardCtx, j)
	bg := context.WithoutCancel(ctx)

	queueAge := time.Since(j.RunAfter)
	if j.Attempts == 1 {
		queueAge = time.Since(j.CreatedAt)
	}
	r.log.InfoContext(ctx, "job.claimed", append(r.jobAttrs(j), "queue_age_ms", queueAge.Milliseconds())...)

	spec, ok := r.specs[j.Type]
	if !ok {
		return r.settle(bg, j, Spec{Backoff: AIBackoff(r.cfg.RetryBase)}, &Error{Category: CatInternal, Detail: "no handler"}, start)
	}
	if j.Attempts > j.MaxAttempts {
		cat := CatTimeout
		if j.LastErrorCategory != nil {
			cat = *j.LastErrorCategory
		}
		return r.fail(bg, j, spec, cat, "", start)
	}

	cctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	hctx, cancelDeadline := context.WithTimeout(cctx, spec.Deadline(j))
	defer cancelDeadline()

	stopHB := make(chan struct{})
	var hbWG sync.WaitGroup
	hbWG.Go(func() { r.heartbeat(bg, j, cancel, stopHB) })
	err := runHandler(hctx, spec.Handler, j)
	close(stopHB)
	hbWG.Wait()

	switch {
	case err == nil:
		r.finished(ctx, j, StatusSucceeded, "", start)
		return nil
	case errors.Is(err, ErrLeaseLost) || errors.Is(context.Cause(hctx), ErrLeaseLost):
		r.log.WarnContext(ctx, "job.lease_lost", r.jobAttrs(j)...)
		return nil
	case hardCtx.Err() != nil:
		if _, rerr := Release(bg, r.pool, j); rerr != nil {
			return rerr
		}
		r.log.InfoContext(ctx, "job.released", r.jobAttrs(j)...)
		return nil
	case errors.Is(hctx.Err(), context.DeadlineExceeded):
		return r.settle(bg, j, spec, &Error{Category: CatTimeout}, start)
	default:
		if classify(err).Category == CatInternal {
			r.log.ErrorContext(ctx, "job.internal_error", append(r.jobAttrs(j), "error", err.Error())...)
		}
		return r.settle(bg, j, spec, classify(err), start)
	}
}

func runHandler(ctx context.Context, h Handler, j *Job) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("jobs: handler panic: %v", p)
		}
	}()
	return h(ctx, j)
}

func (r *Runner) heartbeat(ctx context.Context, j *Job, cancel context.CancelCauseFunc, stop <-chan struct{}) {
	t := time.NewTicker(r.cfg.Lease / 3)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			ok, err := Heartbeat(ctx, r.pool, j.Token(), r.cfg.Lease)
			if err == nil && !ok {
				cancel(ErrLeaseLost)
				return
			}
		}
	}
}

func (r *Runner) settle(ctx context.Context, j *Job, spec Spec, e *Error, start time.Time) error {
	if e.Permanent || j.Attempts >= j.MaxAttempts {
		return r.fail(ctx, j, spec, e.Category, e.Detail, start)
	}
	ok, err := Retry(ctx, r.pool, j, spec.Backoff.Delay(j.Attempts, e.RetryAfter), e.Category, e.Detail)
	if err != nil {
		return err
	}
	if !ok {
		r.log.WarnContext(ctx, "job.lease_lost", r.jobAttrs(j)...)
		return nil
	}
	r.log.InfoContext(ctx, "job.retry_scheduled", append(r.jobAttrs(j), "error_category", e.Category)...)
	return nil
}

func (r *Runner) fail(ctx context.Context, j *Job, spec Spec, category, detail string, start time.Time) error {
	var stage func(tx pgx.Tx) error
	if spec.OnFailure != nil {
		stage = func(tx pgx.Tx) error { return spec.OnFailure(ctx, tx, j, category) }
	}
	err := finalizeTx(ctx, r.pool, j, func(tx pgx.Tx) (bool, error) { return Fail(ctx, tx, j, category, detail) }, stage)
	if errors.Is(err, ErrLeaseLost) {
		r.log.WarnContext(ctx, "job.lease_lost", r.jobAttrs(j)...)
		return nil
	}
	if err != nil {
		return err
	}
	r.finished(ctx, j, StatusFailed, category, start)
	return nil
}

func (r *Runner) finished(ctx context.Context, j *Job, status, category string, start time.Time) {
	attrs := append(r.jobAttrs(j), "status", status, "attempts", j.Attempts, "duration_ms", time.Since(start).Milliseconds())
	if category != "" {
		attrs = append(attrs, "error_category", category)
	}
	r.log.InfoContext(ctx, "job.finished", attrs...)
}
