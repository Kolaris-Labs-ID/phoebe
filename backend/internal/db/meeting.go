package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
)

// LockMeeting is the first statement of every write tx touching a meeting or its
// children (backend.md §2): owner filter + tombstone check + per-meeting serialization.
// Absent, not owned, or tombstoned → apperr.ErrNotFound.
func LockMeeting(ctx context.Context, q Querier, meetingID, userID string) error {
	var one int
	err := q.QueryRow(ctx,
		`SELECT 1 FROM meetings WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL FOR UPDATE`,
		meetingID, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	return err
}

// LockLiveMeeting is the owner-agnostic variant used by worker finalize (backend.md §4.5).
// Returns the owner id; tombstoned or purged → apperr.ErrNotFound.
func LockLiveMeeting(ctx context.Context, q Querier, meetingID string) (userID string, err error) {
	err = q.QueryRow(ctx,
		`SELECT user_id FROM meetings WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`,
		meetingID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperr.ErrNotFound
	}
	return userID, err
}

// TryLockLiveMeeting is the sweep variant (FOR UPDATE SKIP LOCKED). ok=false when the
// meeting is locked by someone else, tombstoned, or gone: the sweep skips the item.
func TryLockLiveMeeting(ctx context.Context, q Querier, meetingID string) (ok bool, err error) {
	var one int
	err = q.QueryRow(ctx,
		`SELECT 1 FROM meetings WHERE id = $1 AND deleted_at IS NULL FOR UPDATE SKIP LOCKED`,
		meetingID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// TouchMeeting bumps meetings.updated_at (change detection, backend.md §3.5).
func TouchMeeting(ctx context.Context, q Querier, meetingID string) error {
	_, err := q.Exec(ctx, `UPDATE meetings SET updated_at = now() WHERE id = $1`, meetingID)
	return err
}
