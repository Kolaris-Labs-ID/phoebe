package uploads

import (
	"context"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/ids"
	"phoebe/internal/storage"
)

var errLeaseLost = errors.New("uploads: writer lease lost")

// PutChunk implements PUT /v1/uploads/{id} (backend.md §7.2): writes [offset, offset+length)
// under a fenced writer lease and the storage flock, then runs Complete inline when the
// last byte arrived. The Result has Audio/Job nil until then.
func (s *Service) PutChunk(ctx context.Context, userID, uploadID string, offset, length int64, body io.Reader) (*Result, error) {
	lease := ids.New()
	var size int64
	var key string
	err := s.pool.QueryRow(ctx, `
		UPDATE upload_sessions s SET writer_lease_id = $3, writer_lease_until = now() + $4 * interval '1 millisecond'
		FROM meetings m
		WHERE s.id = $1 AND m.id = s.meeting_id AND m.user_id = $2 AND s.user_id = $2 AND m.deleted_at IS NULL
		  AND s.status = 'open' AND s.expires_at > now() AND s.received_bytes = $5
		  AND (s.writer_lease_until IS NULL OR s.writer_lease_until < now()) AND $5 + $6 <= s.size_bytes AND $6 <= $7
		RETURNING s.size_bytes, s.storage_key`,
		uploadID, userID, lease, ms(s.cfg.UploadWriterLease), offset, length, s.cfg.UploadChunkMaxBytes).Scan(&size, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.diagnosePut(ctx, userID, uploadID, offset, length)
	}
	if err != nil {
		return nil, err
	}

	bg := context.WithoutCancel(ctx)
	limit := min(s.cfg.UploadChunkMaxBytes, size-offset)
	var committed, over, mismatch bool
	var received int64
	_, err = s.storage.Append(ctx, key, offset, io.LimitReader(body, limit+1),
		func(int64) error {
			tag, err := s.pool.Exec(bg, `UPDATE upload_sessions SET writer_lease_until = now() + $3 * interval '1 millisecond'
				WHERE id = $1 AND writer_lease_id = $2 AND status = 'open'`, uploadID, lease, ms(s.cfg.UploadWriterLease))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errLeaseLost
			}
			return nil
		},
		func(written int64) error {
			committed = true
			if written > limit {
				over = true
				s.releaseLease(bg, uploadID, lease)
				return nil
			}
			err := s.pool.QueryRow(bg, `
				UPDATE upload_sessions SET received_bytes = $3 + $4, writer_lease_id = NULL, writer_lease_until = NULL, updated_at = now()
				WHERE id = $1 AND writer_lease_id = $2 AND status = 'open' AND received_bytes = $3
				RETURNING received_bytes`, uploadID, lease, offset, written).Scan(&received)
			if errors.Is(err, pgx.ErrNoRows) {
				mismatch = true
				return nil
			}
			return err
		})
	if !committed {
		s.releaseLease(bg, uploadID, lease)
	}
	switch {
	case errors.Is(err, storage.ErrBusy), errors.Is(err, errLeaseLost):
		return nil, apperr.New(apperr.UploadBusy)
	case over && limit == s.cfg.UploadChunkMaxBytes:
		return nil, apperr.New(apperr.ChunkTooLarge)
	case over:
		return nil, apperr.New(apperr.UploadTooLarge)
	case mismatch:
		x, lerr := loadOwned(bg, s.pool, userID, uploadID)
		if lerr != nil {
			return nil, lerr
		}
		return nil, apperr.WithDetails(apperr.UploadOffsetMismatch, map[string]any{"received_bytes": x.ReceivedBytes})
	case err != nil:
		return nil, err
	case received == size:
		return s.Complete(ctx, userID, uploadID)
	}
	x, err := loadOwned(ctx, s.pool, userID, uploadID)
	if err != nil {
		return nil, err
	}
	return &Result{Upload: x.view(s.cfg.UploadChunkMaxBytes)}, nil
}

// diagnosePut maps a failed lease claim to its api.md error.
func (s *Service) diagnosePut(ctx context.Context, userID, uploadID string, offset, length int64) error {
	x, err := loadOwned(ctx, s.pool, userID, uploadID)
	if err != nil {
		return err
	}
	switch {
	case length > s.cfg.UploadChunkMaxBytes:
		return apperr.New(apperr.ChunkTooLarge)
	case x.Status == "expired" || (x.Status == "open" && x.Expired):
		return apperr.New(apperr.UploadExpired)
	case x.Status != "open" && x.Status != "verifying":
		return apperr.WithDetails(apperr.UploadNotOpen, map[string]any{"status": x.Status})
	case x.ReceivedBytes != offset:
		return apperr.WithDetails(apperr.UploadOffsetMismatch, map[string]any{"received_bytes": x.ReceivedBytes})
	case x.Status == "open" && !x.LeaseLive && offset+length > x.SizeBytes:
		return apperr.New(apperr.UploadTooLarge)
	}
	return apperr.New(apperr.UploadBusy)
}
