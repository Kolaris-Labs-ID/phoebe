package uploads

import (
	"context"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/db"
	"phoebe/internal/meetings"
)

// ExpireSweep is the upload-expiry sweep (backend.md §4.7, a jobs.SweepFunc): open sessions
// past expires_at with no live writer lease → expired; meeting back to awaiting_audio when
// no live session is left; temp object deleted after commit. verifying is never expired.
func (s *Service) ExpireSweep(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, meeting_id, storage_key FROM upload_sessions
		WHERE status = 'open' AND expires_at < now() ORDER BY expires_at LIMIT 100`)
	if err != nil {
		return 0, err
	}
	type item struct{ id, meetingID, key string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.meetingID, &it.key); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
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
			tag, err := tx.Exec(ctx, `UPDATE upload_sessions SET status = 'expired', writer_lease_id = NULL,
				writer_lease_until = NULL, updated_at = now()
				WHERE id = $1 AND status = 'open' AND expires_at < now()
				  AND (writer_lease_until IS NULL OR writer_lease_until < now())`, it.id)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			expired = true
			if _, err := meetings.Transition(ctx, tx, it.meetingID, meetings.UploadAbandoned); err != nil {
				return err
			}
			return db.TouchMeeting(ctx, tx, it.meetingID)
		})
		if err != nil {
			return n, err
		}
		if expired {
			n++
			_ = s.storage.Delete(ctx, it.key)
		}
	}
	return n, nil
}
