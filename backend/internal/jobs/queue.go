package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
)

// ErrLeaseLost: the attempt no longer owns the job (reclaimed, canceled, or meeting
// tombstoned/purged). Write nothing else; the runner logs job.lease_lost.
var ErrLeaseLost = errors.New("jobs: lease lost")

const fencing = `id = $1 AND status = 'running' AND lease_owner = $2 AND attempts = $3`

// Claim takes the next runnable job (queued and due, or running with an expired lease)
// with FOR UPDATE SKIP LOCKED (backend.md §4.1). nil, nil when none.
func Claim(ctx context.Context, q db.Querier, owner string, lease time.Duration) (*Job, error) {
	j, err := ScanJob(q.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM jobs
			WHERE (status = 'queued' AND run_after <= now())
			   OR (status = 'running' AND lease_until < now())
			ORDER BY run_after, created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE jobs j
		SET status = 'running', attempts = j.attempts + 1, lease_owner = $1,
		    lease_until = now() + $2 * interval '1 millisecond', heartbeat_at = now(),
		    started_at = COALESCE(j.started_at, now()), updated_at = now(),
		    last_error_category = CASE WHEN j.status = 'running' THEN 'timeout' ELSE j.last_error_category END
		FROM next WHERE j.id = next.id
		RETURNING `+Columns, owner, lease.Milliseconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if j.MeetingID != nil {
		if err := db.TouchMeeting(ctx, q, *j.MeetingID); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func fenced(ctx context.Context, q db.Querier, meetingID *string, sql string, args ...any) (bool, error) {
	tag, err := q.Exec(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if meetingID != nil {
		if err := db.TouchMeeting(ctx, q, *meetingID); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Heartbeat extends the lease; false = lease lost (cancel the attempt).
func Heartbeat(ctx context.Context, q db.Querier, t Token, lease time.Duration) (bool, error) {
	return fenced(ctx, q, nil, `
		UPDATE jobs SET lease_until = now() + $4 * interval '1 millisecond', heartbeat_at = now(), updated_at = now()
		WHERE `+fencing, t.ID, t.Owner, t.Attempt, lease.Milliseconds())
}

// Retry requeues the attempt after delay with the error recorded; bumps the meeting.
func Retry(ctx context.Context, q db.Querier, j *Job, delay time.Duration, category, detail string) (bool, error) {
	t := j.Token()
	return fenced(ctx, q, j.MeetingID, `
		UPDATE jobs SET status = 'queued', run_after = now() + $4 * interval '1 millisecond',
		    lease_owner = NULL, lease_until = NULL, last_error_category = $5, last_error_detail = $6, updated_at = now()
		WHERE `+fencing, t.ID, t.Owner, t.Attempt, delay.Milliseconds(), category, nullable(detail))
}

// Succeed marks the attempt succeeded with result (JSON object). Run inside the finalize tx.
func Succeed(ctx context.Context, q db.Querier, j *Job, result any) (bool, error) {
	b, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	t := j.Token()
	return fenced(ctx, q, j.MeetingID, `
		UPDATE jobs SET status = 'succeeded', result = $4, finished_at = now(), lease_owner = NULL,
		    lease_until = NULL, updated_at = now()
		WHERE `+fencing, t.ID, t.Owner, t.Attempt, b)
}

// Fail marks the attempt permanently failed. Run inside the failure-finalize tx.
func Fail(ctx context.Context, q db.Querier, j *Job, category, detail string) (bool, error) {
	t := j.Token()
	return fenced(ctx, q, j.MeetingID, `
		UPDATE jobs SET status = 'failed', finished_at = now(), lease_owner = NULL, lease_until = NULL,
		    last_error_category = $4, last_error_detail = $5, updated_at = now()
		WHERE `+fencing, t.ID, t.Owner, t.Attempt, category, nullable(detail))
}

// Release requeues an unfinished attempt at shutdown without charging it (backend.md §4.6).
func Release(ctx context.Context, q db.Querier, j *Job) (bool, error) {
	t := j.Token()
	return fenced(ctx, q, j.MeetingID, `
		UPDATE jobs SET status = 'queued', attempts = attempts - 1, run_after = now(), lease_owner = NULL,
		    lease_until = NULL, updated_at = now()
		WHERE `+fencing, t.ID, t.Owner, t.Attempt)
}

// finalizeTx runs the backend.md §4.5 skeleton: lock the meeting (meeting-scoped jobs),
// apply the fenced job update, then the stage writes. Tombstoned meeting or lost fencing
// → rollback, ErrLeaseLost.
func finalizeTx(ctx context.Context, b db.TxBeginner, j *Job,
	update func(tx pgx.Tx) (bool, error), stage func(tx pgx.Tx) error) error {
	return db.InTx(ctx, b, func(tx pgx.Tx) error {
		if j.MeetingID != nil {
			if _, err := db.LockLiveMeeting(ctx, tx, *j.MeetingID); err != nil {
				if errors.Is(err, apperr.ErrNotFound) {
					return ErrLeaseLost
				}
				return err
			}
		}
		ok, err := update(tx)
		if err != nil {
			return err
		}
		if !ok {
			return ErrLeaseLost
		}
		if stage == nil {
			return nil
		}
		return stage(tx)
	})
}

// Complete is the success finalize a handler calls as its last step: one tx that locks
// the meeting, marks the job succeeded with result, then runs stage (revision insert,
// quota consume, next enqueue, events...). Returns ErrLeaseLost when the attempt lost
// ownership or the meeting is gone; the handler must then delete any storage object it
// wrote and return ErrLeaseLost (or the error) to the runner.
func Complete(ctx context.Context, b db.TxBeginner, j *Job, result any, stage func(tx pgx.Tx) error) error {
	return finalizeTx(ctx, b, j, func(tx pgx.Tx) (bool, error) { return Succeed(ctx, tx, j, result) }, stage)
}
