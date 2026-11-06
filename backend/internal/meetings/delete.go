package meetings

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/obs"
	"phoebe/internal/usage"
)

const PurgeMeetingDeadline = 10 * time.Minute

const cancelJobsSQL = `UPDATE jobs SET status = 'canceled', finished_at = now(), lease_owner = NULL, lease_until = NULL, updated_at = now()
	WHERE status IN ('queued', 'running') AND `

const cancelUploadsSQL = `UPDATE upload_sessions SET status = 'canceled', writer_lease_id = NULL, writer_lease_until = NULL, updated_at = now()
	WHERE status IN ('open', 'verifying') AND `

// CorrelationID returns the ctx correlation id, or a fresh one (jobs.correlation_id is required).
func CorrelationID(ctx context.Context) string {
	if id := obs.CorrelationID(ctx); id != "" {
		return id
	}
	return ids.New()
}

// Delete implements DELETE /v1/meetings/{id} (backend.md §11) and returns when the purge runs.
func (s *Service) Delete(ctx context.Context, userID, meetingID string) (time.Time, error) {
	var at time.Time
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			WITH m AS (UPDATE meetings SET deleted_at = now(), updated_at = now() WHERE id = $1 RETURNING id, user_id, deleted_at)
			INSERT INTO deleted_meetings (id, user_id, deleted_at) SELECT id, user_id, deleted_at FROM m`, meetingID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, cancelUploadsSQL+`meeting_id = $1`, meetingID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, cancelJobsSQL+`meeting_id = $1`, meetingID); err != nil {
			return err
		}
		if err := usage.ReleaseMeeting(ctx, tx, meetingID); err != nil {
			return err
		}
		j, err := jobs.Enqueue(ctx, tx, jobs.NewJob{
			Type:           jobs.TypePurgeMeeting,
			Trigger:        jobs.TriggerSystem,
			IdempotencyKey: "sys:purge_meeting:" + meetingID,
			Payload:        purgeMeetingPayload{MeetingID: meetingID, UserID: userID},
			MaxAttempts:    jobs.PurgeMaxAttempts,
			RunAfter:       time.Now().Add(s.cfg.PurgeDelay),
			CorrelationID:  CorrelationID(ctx),
		})
		if err != nil {
			return err
		}
		at = j.RunAfter
		return nil
	})
	return at, err
}

// TombstoneUser is the meetings part of account deletion (backend.md §11), run inside the
// account-delete tx before anything else: locks the user's live meetings (id order),
// tombstones them with deleted_meetings rows, cancels their live upload sessions and the
// user's queued/running jobs. Quota release, sessions and the user row are the caller's.
func TombstoneUser(ctx context.Context, tx pgx.Tx, userID string) error {
	if _, err := tx.Exec(ctx,
		`SELECT id FROM meetings WHERE user_id = $1 AND deleted_at IS NULL ORDER BY id FOR UPDATE`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		WITH m AS (UPDATE meetings SET deleted_at = now(), updated_at = now()
		           WHERE user_id = $1 AND deleted_at IS NULL RETURNING id, user_id, deleted_at)
		INSERT INTO deleted_meetings (id, user_id, deleted_at) SELECT id, user_id, deleted_at FROM m`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, cancelUploadsSQL+`user_id = $1`, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, cancelJobsSQL+`user_id = $1`, userID)
	return err
}

// DeleteAudio implements DELETE /v1/meetings/{id}/audio (api.md §4.11, backend.md §11).
func (s *Service) DeleteAudio(ctx context.Context, userID, meetingID string) error {
	var assetID, key string
	deleted := false
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT id, storage_key, deleted_at IS NOT NULL FROM audio_assets WHERE meeting_id = $1`,
			meetingID).Scan(&assetID, &key, &deleted)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.AudioNotReady)
		}
		if err != nil || deleted {
			return err
		}
		if j, err := jobs.Active(ctx, tx, meetingID, jobs.TypeTranscribe); err == nil {
			return apperr.WithDetails(apperr.JobInProgress, map[string]any{"job_id": j.ID})
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE audio_assets SET deleted_at = now(), deleted_reason = 'user' WHERE id = $1`, assetID); err != nil {
			return err
		}
		return db.TouchMeeting(ctx, tx, meetingID)
	})
	if err != nil || deleted {
		return err
	}
	// Crash or failure here leaves bytes_purged_at NULL; the byte-purge sweep finishes it.
	if err := s.storage.Delete(ctx, key); err != nil {
		s.log.WarnContext(ctx, "audio.delete_bytes", "meeting_id", meetingID, "status", "deferred")
		return nil
	}
	if _, err := s.pool.Exec(ctx, `UPDATE audio_assets SET bytes_purged_at = now() WHERE id = $1`, assetID); err != nil {
		s.log.WarnContext(ctx, "audio.delete_bytes", "meeting_id", meetingID, "status", "deferred")
	}
	return nil
}

type purgeMeetingPayload struct {
	MeetingID string `json:"meeting_id"`
	UserID    string `json:"user_id"`
}

// PurgeMeeting is the purge_meeting handler (backend.md §5.3). Idempotent.
func (s *Service) PurgeMeeting(ctx context.Context, j *jobs.Job) error {
	var p purgeMeetingPayload
	if err := j.DecodePayload(&p); err != nil || !ids.Valid(p.MeetingID) || !ids.Valid(p.UserID) {
		return jobs.Permanent(jobs.CatInternal, "bad payload")
	}
	if err := s.storage.DeletePrefix(ctx, "u/"+p.UserID+"/m/"+p.MeetingID+"/"); err != nil {
		return err
	}
	return jobs.Complete(ctx, s.pool, j, struct{}{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM meetings WHERE id = $1 AND deleted_at IS NOT NULL`, p.MeetingID)
		return err
	})
}

func (s *Service) PurgeMeetingSpec() jobs.Spec {
	return jobs.Spec{
		Handler:  s.PurgeMeeting,
		Deadline: func(*jobs.Job) time.Duration { return PurgeMeetingDeadline },
		Backoff:  jobs.PurgeBackoff(),
	}
}
