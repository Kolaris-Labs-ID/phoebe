package uploads

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/audio"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
	"phoebe/internal/meetings"
	"phoebe/internal/storage"
)

// Starter is the transcripts seam for auto-start at verify (backend.md §7.3), implemented by
// *transcripts.Service: Start transcription inside the caller's tx (meeting lock held) as
// POST …/transcription with Idempotency-Key key and body {"language": language}, leaving
// tx usable on error. Client-visible refusals are *apperr.Error.
type Starter interface {
	StartInTx(ctx context.Context, tx pgx.Tx, userID, meetingID, key, language string) (*jobs.Job, bool, error)
}

type Service struct {
	pool    *pgxpool.Pool
	cfg     *config.Config
	storage storage.Storage
	log     *slog.Logger
	starter Starter
}

// New: starter nil disables auto-start (sessions still record the request).
func New(pool *pgxpool.Pool, cfg *config.Config, store storage.Storage, log *slog.Logger, starter Starter) *Service {
	return &Service{pool: pool, cfg: cfg, storage: store, log: log, starter: starter}
}

type CreateInput struct {
	SizeBytes     *int64              `json:"size_bytes"`
	SHA256        *string             `json:"sha256"`
	ContentType   *string             `json:"content_type"`
	DurationMS    *int64              `json:"duration_ms"`
	Transcription *TranscriptionInput `json:"transcription"`
}

type TranscriptionInput struct {
	IdempotencyKey *string `json:"idempotency_key"`
	Language       *string `json:"language"`
}

// Result is the complete / last-chunk response body (api.md §4.4).
type Result struct {
	Upload *meetings.UploadSession `json:"upload"`
	Audio  *meetings.AudioAsset    `json:"audio"`
	Job    *meetings.Job           `json:"job"`
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

type session struct {
	ID, MeetingID, UserID, Status, ContentType, SHA256, StorageKey string
	SizeBytes, ReceivedBytes, DurationMS                           int64
	TranscribeKey, TranscribeLang, FailureReason                   *string
	LeaseLive, Expired                                             bool
	ExpiresAt, CreatedAt                                           time.Time
}

const sessionCols = `s.id, s.meeting_id, s.user_id, s.status, s.declared_content_type, s.sha256, s.storage_key,
	s.size_bytes, s.received_bytes, s.declared_duration_ms, s.transcribe_idempotency_key, s.transcribe_language,
	s.failure_reason, COALESCE(s.writer_lease_until > now(), false), s.expires_at <= now(), s.expires_at, s.created_at`

func scanSession(row pgx.Row) (*session, error) {
	var x session
	err := row.Scan(&x.ID, &x.MeetingID, &x.UserID, &x.Status, &x.ContentType, &x.SHA256, &x.StorageKey,
		&x.SizeBytes, &x.ReceivedBytes, &x.DurationMS, &x.TranscribeKey, &x.TranscribeLang,
		&x.FailureReason, &x.LeaseLive, &x.Expired, &x.ExpiresAt, &x.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &x, nil
}

func (x *session) view(chunkMax int64) *meetings.UploadSession {
	return &meetings.UploadSession{
		ID: x.ID, MeetingID: x.MeetingID, Status: x.Status, ContentType: x.ContentType,
		SizeBytes: x.SizeBytes, ReceivedBytes: x.ReceivedBytes, SHA256: x.SHA256, DurationMS: x.DurationMS,
		ChunkMaxBytes: chunkMax, FailureReason: x.FailureReason, AutoTranscription: x.TranscribeKey != nil,
		ExpiresAt: jsonx.NewTime(x.ExpiresAt), CreatedAt: jsonx.NewTime(x.CreatedAt),
	}
}

// loadOwned reads a session through its live, owned meeting; a miss is not_found.
func loadOwned(ctx context.Context, q db.Querier, userID, uploadID string) (*session, error) {
	return scanSession(q.QueryRow(ctx, `SELECT `+sessionCols+` FROM upload_sessions s
		JOIN meetings m ON m.id = s.meeting_id AND m.user_id = s.user_id
		WHERE s.id = $1 AND m.user_id = $2 AND m.deleted_at IS NULL`, uploadID, userID))
}

func lockSession(ctx context.Context, tx pgx.Tx, uploadID, meetingID string) (*session, error) {
	return scanSession(tx.QueryRow(ctx, `SELECT `+sessionCols+` FROM upload_sessions s
		WHERE s.id = $1 AND s.meeting_id = $2 FOR UPDATE`, uploadID, meetingID))
}

func tempKey(userID, meetingID, sessionID string) string {
	return "u/" + userID + "/m/" + meetingID + "/uploads/" + sessionID + ".part"
}

func finalKey(x *session, contentType string) string {
	return "u/" + x.UserID + "/m/" + x.MeetingID + "/audio/" + x.ID + "." + audio.Ext(contentType)
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

func validate(in CreateInput, maxBytes int64) (ct, sha, key, lang string, err error) {
	var f []apperr.FieldError
	add := func(field, reason string) { f = append(f, apperr.FieldError{Field: field, Reason: reason}) }
	switch {
	case in.SizeBytes == nil:
		add("size_bytes", apperr.ReasonRequired)
	case *in.SizeBytes <= 0:
		add("size_bytes", apperr.ReasonOutOfRange)
	}
	if in.SHA256 == nil {
		add("sha256", apperr.ReasonRequired)
	} else if sha = strings.ToLower(*in.SHA256); !sha256Re.MatchString(sha) {
		add("sha256", apperr.ReasonInvalid)
	}
	if in.ContentType == nil {
		add("content_type", apperr.ReasonRequired)
	} else if c, ok := audio.Normalize(*in.ContentType); ok {
		ct = c
	} else {
		add("content_type", apperr.ReasonInvalid)
	}
	switch {
	case in.DurationMS == nil:
		add("duration_ms", apperr.ReasonRequired)
	case *in.DurationMS <= 0:
		add("duration_ms", apperr.ReasonOutOfRange)
	}
	if t := in.Transcription; t != nil {
		if t.IdempotencyKey == nil {
			add("transcription.idempotency_key", apperr.ReasonRequired)
		} else if k, ok := ids.Parse(*t.IdempotencyKey); ok {
			key = k
		} else {
			add("transcription.idempotency_key", apperr.ReasonInvalid)
		}
		switch {
		case t.Language == nil:
			add("transcription.language", apperr.ReasonRequired)
		case *t.Language == "auto" || *t.Language == "id" || *t.Language == "en":
			lang = *t.Language
		default:
			add("transcription.language", apperr.ReasonInvalid)
		}
	}
	if len(f) > 0 {
		return "", "", "", "", apperr.Validation(f...)
	}
	if *in.SizeBytes > maxBytes {
		return "", "", "", "", apperr.New(apperr.UploadTooLarge)
	}
	return ct, sha, key, lang, nil
}

// Create implements POST /v1/meetings/{id}/uploads (backend.md §7.1). created=false is a resume (200).
func (s *Service) Create(ctx context.Context, userID, meetingID string, in CreateInput) (*meetings.UploadSession, bool, error) {
	var out *session
	var created bool
	var staleKey string
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		ct, sha, key, lang, err := validate(in, s.cfg.MaxUploadBytes)
		if err != nil {
			return err
		}
		if err := audio.CheckDuration(*in.DurationMS, s.cfg.MaxAudioSeconds*1000); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM audio_assets WHERE meeting_id = $1)`, meetingID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return apperr.New(apperr.AudioExists)
		}
		live, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionCols+` FROM upload_sessions s
			WHERE s.meeting_id = $1 AND s.status IN ('open', 'verifying') FOR UPDATE`, meetingID))
		if err != nil && !errors.Is(err, apperr.ErrNotFound) {
			return err
		}
		if live != nil {
			stale := live.Status == "open" && live.Expired && !live.LeaseLive
			switch {
			case !stale && live.SHA256 == sha && live.SizeBytes == *in.SizeBytes:
				if live.TranscribeKey == nil && key != "" {
					if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET transcribe_idempotency_key = $2, transcribe_language = $3,
						updated_at = now() WHERE id = $1`, live.ID, key, lang); err != nil {
						return err
					}
					live.TranscribeKey, live.TranscribeLang = &key, &lang
					if err := db.TouchMeeting(ctx, tx, meetingID); err != nil {
						return err
					}
				}
				out = live
				return nil
			case !stale && (live.LeaseLive || live.Status == "verifying"):
				return apperr.New(apperr.UploadBusy)
			}
			status := "canceled"
			if stale {
				status = "expired"
			}
			if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET status = $2, writer_lease_id = NULL, writer_lease_until = NULL,
				updated_at = now() WHERE id = $1`, live.ID, status); err != nil {
				return err
			}
			staleKey = live.StorageKey
		}
		id := ids.New()
		var tk, tl *string
		if key != "" {
			tk, tl = &key, &lang
		}
		out, err = scanSession(tx.QueryRow(ctx, `
			INSERT INTO upload_sessions AS s (id, meeting_id, user_id, declared_content_type, size_bytes, sha256,
			    declared_duration_ms, storage_key, transcribe_idempotency_key, transcribe_language, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now() + $11 * interval '1 millisecond')
			RETURNING `+sessionCols,
			id, meetingID, userID, ct, *in.SizeBytes, sha, *in.DurationMS, tempKey(userID, meetingID, id), tk, tl,
			ms(s.cfg.UploadSessionTTL)))
		if err != nil {
			return err
		}
		created = true
		if _, err := meetings.Transition(ctx, tx, meetingID, meetings.UploadCreated); err != nil {
			return err
		}
		return db.TouchMeeting(ctx, tx, meetingID)
	})
	if err != nil {
		return nil, false, err
	}
	if staleKey != "" {
		_ = s.storage.Delete(ctx, staleKey)
	}
	return out.view(s.cfg.UploadChunkMaxBytes), created, nil
}

// Get implements GET /v1/uploads/{id}.
func (s *Service) Get(ctx context.Context, userID, uploadID string) (*meetings.UploadSession, error) {
	x, err := loadOwned(ctx, s.pool, userID, uploadID)
	if err != nil {
		return nil, err
	}
	return x.view(s.cfg.UploadChunkMaxBytes), nil
}

// releaseLease clears a writer lease this request still holds (best effort).
func (s *Service) releaseLease(ctx context.Context, uploadID, lease string) {
	_, _ = s.pool.Exec(context.WithoutCancel(ctx), `UPDATE upload_sessions SET writer_lease_id = NULL, writer_lease_until = NULL,
		updated_at = now() WHERE id = $1 AND writer_lease_id = $2`, uploadID, lease)
}
