package meetings

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/db"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
)

const sweepBatch = 100

// OrphanGrace must exceed the window between a storage write and the commit of the row
// that references it (export Put → insert, chunk append → session row), so in-flight
// objects are never mistaken for orphans.
const OrphanGrace = 24 * time.Hour

type assetItem struct{ id, meetingID, key string }

func (s *Service) assetItems(ctx context.Context, sql string) ([]assetItem, error) {
	rows, err := s.pool.Query(ctx, sql, sweepBatch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (assetItem, error) {
		var it assetItem
		return it, r.Scan(&it.id, &it.meetingID, &it.key)
	})
}

func (s *Service) purgeBytes(ctx context.Context, it assetItem) (bool, error) {
	if err := s.storage.Delete(ctx, it.key); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE audio_assets SET bytes_purged_at = now() WHERE id = $1 AND bytes_purged_at IS NULL`, it.id)
	return tag.RowsAffected() == 1, err
}

// RetentionSweep expires transcribed audio past retain_until (backend.md §4.7): asset
// marked deleted (reason expired) under the meeting lock, then bytes deleted after commit.
func (s *Service) RetentionSweep(ctx context.Context) (int, error) {
	items, err := s.assetItems(ctx, `SELECT id, meeting_id, storage_key FROM audio_assets
		WHERE deleted_at IS NULL AND retain_until IS NOT NULL AND retain_until < now() ORDER BY retain_until LIMIT $1`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		expired := false
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			ok, err := db.TryLockLiveMeeting(ctx, tx, it.meetingID)
			if err != nil || !ok {
				return err
			}
			if _, err := jobs.Active(ctx, tx, it.meetingID, jobs.TypeTranscribe); err == nil {
				return nil
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE audio_assets SET deleted_at = now(), deleted_reason = 'expired'
				WHERE id = $1 AND deleted_at IS NULL AND retain_until < now()`, it.id)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			expired = true
			return db.TouchMeeting(ctx, tx, it.meetingID)
		})
		if err != nil {
			return n, err
		}
		if expired {
			n++
			// A failed byte delete is left to BytePurgeSweep.
			_, _ = s.purgeBytes(ctx, it)
		}
	}
	return n, nil
}

// BytePurgeSweep finishes byte deletion for deleted audio whose delete crashed or failed
// after commit (backend.md §4.7, both audio deletion paths).
func (s *Service) BytePurgeSweep(ctx context.Context) (int, error) {
	items, err := s.assetItems(ctx, `SELECT id, meeting_id, storage_key FROM audio_assets
		WHERE deleted_at IS NOT NULL AND bytes_purged_at IS NULL AND deleted_at < now() - interval '1 minute'
		ORDER BY deleted_at LIMIT $1`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		ok, err := s.purgeBytes(ctx, it)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// OrphanSweep deletes stored objects older than OrphanGrace that no live row references
// (backend.md §2: a crash between a storage write and its commit leaves orphan bytes).
func (s *Service) OrphanSweep(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-OrphanGrace)
	var candidates []string
	err := s.storage.Walk(ctx, func(key string, mod time.Time) error {
		if mod.Before(cutoff) {
			candidates = append(candidates, key)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, key := range candidates {
		if n == sweepBatch {
			break
		}
		live, err := s.referenced(ctx, key)
		if err != nil {
			return n, err
		}
		if live {
			continue
		}
		if err := s.storage.Delete(ctx, key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// referenced: keys are u/<uid>/m/<mid>/{uploads/<session>.part|audio/<session>.<ext>|exports/<export>.<ext>};
// anything else (e.g. a crashed Put's temp file) is unreferenced.
func (s *Service) referenced(ctx context.Context, key string) (bool, error) {
	p := strings.Split(key, "/")
	if len(p) != 6 || p[0] != "u" || p[2] != "m" {
		return false, nil
	}
	id, _, _ := strings.Cut(p[5], ".")
	if !ids.Valid(id) {
		return false, nil
	}
	var q string
	switch p[4] {
	case "uploads":
		q = `SELECT EXISTS (SELECT 1 FROM upload_sessions WHERE id = $1 AND storage_key = $2 AND status IN ('open', 'verifying'))`
	case "audio":
		// verifying: bytes already moved to final, asset row not yet committed (complete crash recovery).
		q = `SELECT EXISTS (SELECT 1 FROM audio_assets WHERE upload_session_id = $1 AND storage_key = $2 AND bytes_purged_at IS NULL)
			OR EXISTS (SELECT 1 FROM upload_sessions WHERE id = $1 AND status = 'verifying')`
	case "exports":
		q = `SELECT EXISTS (SELECT 1 FROM exports WHERE id = $1 AND storage_key = $2)`
	default:
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx, q, id, key).Scan(&ok)
	return ok, err
}
