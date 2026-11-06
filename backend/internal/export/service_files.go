package export

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/ids"
	"phoebe/internal/storage"
)

// Files issues audio playback URLs and resolves signed file URLs (api.md §4.9).
type Files struct {
	pool   *pgxpool.Pool
	store  storage.Storage
	signer *storage.Signer
}

func NewFiles(pool *pgxpool.Pool, store storage.Storage, signer *storage.Signer) *Files {
	return &Files{pool: pool, store: store, signer: signer}
}

func (f *Files) AudioURL(ctx context.Context, userID, meetingID string) (string, time.Time, error) {
	var assetID *string
	var deleted *bool
	err := f.pool.QueryRow(ctx, `
		SELECT a.id, a.deleted_at IS NOT NULL
		FROM meetings m LEFT JOIN audio_assets a ON a.meeting_id = m.id
		WHERE m.id = $1 AND m.user_id = $2 AND m.deleted_at IS NULL`, meetingID, userID).Scan(&assetID, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, apperr.New(apperr.NotFound)
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if assetID == nil {
		return "", time.Time{}, apperr.New(apperr.AudioNotReady)
	}
	if *deleted {
		return "", time.Time{}, apperr.New(apperr.AudioUnavailable)
	}
	url, exp := f.signer.Sign("audio", *assetID, time.Now())
	return url, exp, nil
}

type File struct {
	Body        io.ReadSeekCloser
	ContentType string
	Filename    string // exports only
	ModTime     time.Time
}

// Open checks signature, then expiry, then that the object is still live
// (audio not deleted, meeting not tombstoned) and opens its bytes.
func (f *Files) Open(ctx context.Context, kind, id, exp, sig string) (*File, error) {
	var q string
	switch kind {
	case "audio":
		q = `SELECT a.storage_key, a.content_type, '', a.created_at
			FROM audio_assets a JOIN meetings m ON m.id = a.meeting_id
			WHERE a.id = $1 AND a.deleted_at IS NULL AND m.deleted_at IS NULL`
	case "export":
		q = `SELECT e.storage_key, e.content_type, e.filename, e.created_at
			FROM exports e JOIN meetings m ON m.id = e.meeting_id
			WHERE e.id = $1 AND m.deleted_at IS NULL`
	default:
		return nil, apperr.New(apperr.NotFound)
	}
	if err := f.signer.Verify(kind, id, exp, sig, time.Now()); err != nil {
		return nil, err
	}
	id, ok := ids.Parse(id)
	if !ok {
		return nil, apperr.New(apperr.NotFound)
	}
	var key string
	file := &File{}
	err := f.pool.QueryRow(ctx, q, id).Scan(&key, &file.ContentType, &file.Filename, &file.ModTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(apperr.NotFound)
	}
	if err != nil {
		return nil, err
	}
	file.Body, _, err = f.store.Open(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, apperr.New(apperr.NotFound)
	}
	if err != nil {
		return nil, err
	}
	return file, nil
}
