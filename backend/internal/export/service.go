package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/documents"
	"phoebe/internal/ids"
	"phoebe/internal/jsonx"
	"phoebe/internal/storage"
)

type Service struct {
	pool   *pgxpool.Pool
	store  storage.Storage
	signer *storage.Signer
	log    *slog.Logger
}

func NewService(pool *pgxpool.Pool, store storage.Storage, signer *storage.Signer, log *slog.Logger) *Service {
	return &Service{pool: pool, store: store, signer: signer, log: log}
}

type Export struct {
	ID                 string     `json:"id"`
	DocumentRevisionID string     `json:"document_revision_id"`
	Kind               string     `json:"kind"`
	Format             string     `json:"format"`
	Filename           string     `json:"filename"`
	ContentType        string     `json:"content_type"`
	SizeBytes          int64      `json:"size_bytes"`
	IsDraft            bool       `json:"is_draft"`
	CreatedAt          jsonx.Time `json:"created_at"`
}

type Result struct {
	Export            Export     `json:"export"`
	DownloadURL       string     `json:"download_url"`
	DownloadExpiresAt jsonx.Time `json:"download_expires_at"`
	Text              *string    `json:"text"`
}

type CreateInput struct {
	DocumentRevisionID *string `json:"document_revision_id"`
	Format             *string `json:"format"`
}

type snapshot struct {
	meetingID string
	kind      documents.Kind
	in        Input
}

var errReviewChanged = errors.New("export: review status changed during export")

func (s *Service) Create(ctx context.Context, userID string, in CreateInput) (*Result, error) {
	var fields []apperr.FieldError
	var revID string
	switch {
	case in.DocumentRevisionID == nil:
		fields = append(fields, apperr.FieldError{Field: "document_revision_id", Reason: apperr.ReasonRequired})
	default:
		var ok bool
		if revID, ok = ids.Parse(*in.DocumentRevisionID); !ok {
			fields = append(fields, apperr.FieldError{Field: "document_revision_id", Reason: apperr.ReasonInvalid})
		}
	}
	var f Format
	switch {
	case in.Format == nil:
		fields = append(fields, apperr.FieldError{Field: "format", Reason: apperr.ReasonRequired})
	default:
		var ok bool
		if f, ok = ParseFormat(*in.Format); !ok {
			fields = append(fields, apperr.FieldError{Field: "format", Reason: apperr.ReasonInvalid})
		}
	}
	if len(fields) > 0 {
		return nil, apperr.Validation(fields...)
	}

	// A review toggle between the snapshot read and the insert would make the stored
	// is_draft disagree with the rendered label; re-render on the new snapshot.
	for attempt := 0; ; attempt++ {
		res, err := s.create(ctx, userID, revID, f)
		if errors.Is(err, errReviewChanged) && attempt < 2 {
			continue
		}
		if err != nil && apperr.CodeOf(err) == "" {
			s.log.ErrorContext(ctx, "export.finished", "outcome", "error", "format", string(f))
		}
		return res, err
	}
}

func (s *Service) create(ctx context.Context, userID, revID string, f Format) (*Result, error) {
	snap, err := s.load(ctx, userID, revID)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	out, err := Render(snap.in, f)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(out)
	e := Export{
		ID:                 ids.New(),
		DocumentRevisionID: revID,
		Kind:               string(snap.kind),
		Format:             string(f),
		Filename:           Filename(snap.in, f),
		ContentType:        ContentType(f),
		SizeBytes:          int64(len(out)),
		IsDraft:            snap.in.IsDraft,
	}
	key := fmt.Sprintf("u/%s/m/%s/exports/%s.%s", userID, snap.meetingID, e.ID, Extension(f))
	if _, err := s.store.Put(ctx, key, bytes.NewReader(out)); err != nil {
		return nil, err
	}
	var created time.Time
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, snap.meetingID, userID); err != nil {
			return err
		}
		var draft bool
		if err := tx.QueryRow(ctx, `SELECT reviewed_at IS NULL FROM document_revisions WHERE id = $1`, revID).Scan(&draft); err != nil {
			return err
		}
		if draft != e.IsDraft {
			return errReviewChanged
		}
		return tx.QueryRow(ctx, `
			INSERT INTO exports (id, meeting_id, document_revision_id, format, is_draft, filename, content_type, size_bytes, sha256, storage_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING created_at`,
			e.ID, snap.meetingID, revID, e.Format, e.IsDraft, e.Filename, e.ContentType, e.SizeBytes, hex.EncodeToString(sum[:]), key).
			Scan(&created)
	})
	if err != nil {
		_ = s.store.Delete(context.WithoutCancel(ctx), key)
		return nil, err
	}
	e.CreatedAt = jsonx.NewTime(created)
	s.log.InfoContext(ctx, "export.finished", "outcome", "ok", "meeting_id", snap.meetingID, "format", e.Format,
		"size_bytes", e.SizeBytes, "duration_ms", time.Since(start).Milliseconds())
	res := s.result(e, time.Now())
	if f == MD || f == WhatsApp {
		t := string(out)
		res.Text = &t
	}
	return res, nil
}

// load reads the immutable revision and, in the same statement, the meeting header and
// user language, so the snapshot is consistent.
func (s *Service) load(ctx context.Context, userID, revID string) (*snapshot, error) {
	var snap snapshot
	var kind string
	var raw []byte
	var reviewed *time.Time
	var h Header
	err := s.pool.QueryRow(ctx, `
		SELECT m.id, d.kind, r.content, r.reviewed_at, r.created_at, m.title, m.started_at, m.timezone, m.participants, u.language
		FROM document_revisions r
		JOIN documents d ON d.id = r.document_id
		JOIN meetings m ON m.id = d.meeting_id
		JOIN users u ON u.id = m.user_id
		WHERE r.id = $1 AND m.user_id = $2 AND m.deleted_at IS NULL`, revID, userID).
		Scan(&snap.meetingID, &kind, &raw, &reviewed, &snap.in.CreatedAt, &h.Title, &h.StartedAt, &h.Timezone, &h.Participants, &snap.in.Language)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(apperr.NotFound)
	}
	if err != nil {
		return nil, err
	}
	snap.kind = documents.Kind(kind)
	if snap.in.Content, err = documents.DecodeContent(snap.kind, raw); err != nil {
		return nil, err
	}
	snap.in.Header = h
	snap.in.IsDraft = reviewed == nil
	return &snap, nil
}

func (s *Service) Get(ctx context.Context, userID, exportID string) (*Result, error) {
	var e Export
	var created time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT e.id, e.document_revision_id, d.kind, e.format, e.filename, e.content_type, e.size_bytes, e.is_draft, e.created_at
		FROM exports e
		JOIN meetings m ON m.id = e.meeting_id
		JOIN document_revisions r ON r.id = e.document_revision_id
		JOIN documents d ON d.id = r.document_id
		WHERE e.id = $1 AND m.user_id = $2 AND m.deleted_at IS NULL`, exportID, userID).
		Scan(&e.ID, &e.DocumentRevisionID, &e.Kind, &e.Format, &e.Filename, &e.ContentType, &e.SizeBytes, &e.IsDraft, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(apperr.NotFound)
	}
	if err != nil {
		return nil, err
	}
	e.CreatedAt = jsonx.NewTime(created)
	return s.result(e, time.Now()), nil
}

func (s *Service) result(e Export, now time.Time) *Result {
	url, exp := s.signer.Sign("export", e.ID, now)
	return &Result{Export: e, DownloadURL: url, DownloadExpiresAt: jsonx.NewTime(exp)}
}
