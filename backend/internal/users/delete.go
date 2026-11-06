package users

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
	"phoebe/internal/usage"
)

const PurgeAccountDeadline = 30 * time.Minute

type purgeAccountPayload struct {
	UserID string `json:"user_id"`
}

// Delete implements DELETE /v1/me (backend.md §11) and returns when the purge runs.
func (s *Service) Delete(ctx context.Context, userID string) (time.Time, error) {
	var at time.Time
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := meetings.TombstoneUser(ctx, tx, userID); err != nil {
			return err
		}
		if err := usage.ReleaseUser(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.New(apperr.Unauthenticated)
		}
		j, err := jobs.Enqueue(ctx, tx, jobs.NewJob{
			Type:           jobs.TypePurgeAccount,
			Trigger:        jobs.TriggerSystem,
			IdempotencyKey: "sys:purge_account:" + userID,
			Payload:        purgeAccountPayload{UserID: userID},
			MaxAttempts:    jobs.PurgeMaxAttempts,
			RunAfter:       time.Now().Add(s.cfg.PurgeDelay),
			CorrelationID:  meetings.CorrelationID(ctx),
		})
		if err != nil {
			return err
		}
		at = j.RunAfter
		return nil
	})
	return at, err
}

// PurgeAccount is the purge_account handler (backend.md §5.3): revoke Apple tokens, delete
// the user's files, then the user row (cascades everything except trial_grants). Idempotent.
func (s *Service) PurgeAccount(ctx context.Context, j *jobs.Job) error {
	var p purgeAccountPayload
	if err := j.DecodePayload(&p); err != nil || !ids.Valid(p.UserID) {
		return jobs.Permanent(jobs.CatInternal, "bad payload")
	}
	var enc []byte
	err := s.pool.QueryRow(ctx, `SELECT apple_refresh_token_enc FROM users WHERE id = $1 AND deleted_at IS NOT NULL`,
		p.UserID).Scan(&enc)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if enc != nil && s.apple != nil {
		if err := s.apple.RevokeRefreshToken(ctx, enc); err != nil {
			return jobs.Retryable(jobs.CatProviderUnavailable, revokeDetail(err), 0)
		}
	}
	if err := s.storage.DeletePrefix(ctx, "u/"+p.UserID+"/"); err != nil {
		return err
	}
	return jobs.Complete(ctx, s.pool, j, struct{}{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1 AND deleted_at IS NOT NULL`, p.UserID)
		return err
	})
}

// revokeDetail keeps last_error_detail to the HTTP status (never token or body).
func revokeDetail(err error) string {
	if m := httpStatus.FindString(err.Error()); m != "" {
		return "apple revoke " + m
	}
	return "apple revoke"
}

var httpStatus = regexp.MustCompile(`http \d{3}$`)

func (s *Service) PurgeAccountSpec() jobs.Spec {
	return jobs.Spec{
		Handler:  s.PurgeAccount,
		Deadline: func(*jobs.Job) time.Duration { return PurgeAccountDeadline },
		Backoff:  jobs.PurgeBackoff(),
	}
}
