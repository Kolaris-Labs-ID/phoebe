package transcripts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai"
	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/idem"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
	"phoebe/internal/meetings"
	"phoebe/internal/storage"
	"phoebe/internal/usage"
)

const (
	KindOriginal   = "original"
	KindCorrection = "correction"

	MaxSegmentText = 2000
	MaxJobsListed  = 50
)

type Service struct {
	pool    *pgxpool.Pool
	cfg     *config.Config
	storage storage.Storage
	usage   *usage.Service
	stt     ai.STT
	log     *slog.Logger
}

// stt may be nil in the API process (only the transcribe handler uses it).
func New(pool *pgxpool.Pool, cfg *config.Config, store storage.Storage, us *usage.Service, stt ai.STT, log *slog.Logger) *Service {
	return &Service{pool: pool, cfg: cfg, storage: store, usage: us, stt: stt, log: log}
}

type Segment struct {
	ID      string `json:"id"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

type RevisionSummary struct {
	ID             string     `json:"id"`
	RevisionNumber int        `json:"revision_number"`
	Kind           string     `json:"kind"`
	Language       string     `json:"language"`
	CreatedAt      jsonx.Time `json:"created_at"`
}

type Revision struct {
	RevisionSummary
	Segments []Segment `json:"segments"`
}

type revisionRow struct {
	Revision
	audioAssetID        *string
	provider, model     string
	mutationFingerprint *string
}

const revisionCols = `id, revision_number, kind, language, created_at, segments, audio_asset_id, provider, model, mutation_fingerprint`

func scanRevision(row pgx.Row) (*revisionRow, error) {
	var r revisionRow
	var created time.Time
	var segs []byte
	if err := row.Scan(&r.ID, &r.RevisionNumber, &r.Kind, &r.Language, &created, &segs, &r.audioAssetID,
		&r.provider, &r.model, &r.mutationFingerprint); err != nil {
		return nil, err
	}
	r.CreatedAt = jsonx.NewTime(created)
	if err := json.Unmarshal(segs, &r.Segments); err != nil {
		return nil, err
	}
	return &r, nil
}

func ownMeeting(ctx context.Context, q db.Querier, uid, mid string) error {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM meetings WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, mid, uid).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	return err
}

func currentRevision(ctx context.Context, q db.Querier, mid string) (*revisionRow, error) {
	r, err := scanRevision(q.QueryRow(ctx, `SELECT `+revisionCols+` FROM transcript_revisions
		WHERE meeting_id = $1 ORDER BY revision_number DESC LIMIT 1`, mid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(apperr.TranscriptNotReady)
	}
	return r, err
}

// Current is GET /v1/meetings/{id}/transcript.
func (s *Service) Current(ctx context.Context, uid, mid string) (*Revision, error) {
	if err := ownMeeting(ctx, s.pool, uid, mid); err != nil {
		return nil, err
	}
	r, err := currentRevision(ctx, s.pool, mid)
	if err != nil {
		return nil, err
	}
	return &r.Revision, nil
}

// GetRevision is GET /v1/meetings/{id}/transcript/revisions/{n}.
func (s *Service) GetRevision(ctx context.Context, uid, mid string, n int) (*Revision, error) {
	if err := ownMeeting(ctx, s.pool, uid, mid); err != nil {
		return nil, err
	}
	r, err := scanRevision(s.pool.QueryRow(ctx, `SELECT `+revisionCols+` FROM transcript_revisions
		WHERE meeting_id = $1 AND revision_number = $2`, mid, n))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r.Revision, nil
}

type Cursor struct {
	N int `json:"n"`
}

// ListRevisions is GET /v1/meetings/{id}/transcript/revisions: newest first, keyset on revision_number.
func (s *Service) ListRevisions(ctx context.Context, uid, mid string, limit int, after *Cursor) ([]RevisionSummary, *Cursor, error) {
	if err := ownMeeting(ctx, s.pool, uid, mid); err != nil {
		return nil, nil, err
	}
	var before *int
	if after != nil {
		before = &after.N
	}
	rows, err := s.pool.Query(ctx, `SELECT id, revision_number, kind, language, created_at FROM transcript_revisions
		WHERE meeting_id = $1 AND ($2::int IS NULL OR revision_number < $2)
		ORDER BY revision_number DESC LIMIT $3`, mid, before, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := []RevisionSummary{}
	for rows.Next() {
		var r RevisionSummary
		var created time.Time
		if err := rows.Scan(&r.ID, &r.RevisionNumber, &r.Kind, &r.Language, &created); err != nil {
			return nil, nil, err
		}
		r.CreatedAt = jsonx.NewTime(created)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(out) > limit {
		out = out[:limit]
		return out, &Cursor{N: out[limit-1].RevisionNumber}, nil
	}
	return out, nil, nil
}

type Edit struct {
	SegmentID *string `json:"segment_id"`
	Text      *string `json:"text"`
}

type CorrectionInput struct {
	ExpectedRevision *int    `json:"expected_revision"`
	MutationID       *string `json:"mutation_id"`
	Edits            []Edit  `json:"edits"`
}

// Correct is POST /v1/meetings/{id}/transcript/revisions (backend.md §10). created=false on a
// mutation_id replay with the same body.
func (s *Service) Correct(ctx context.Context, uid, mid string, in CorrectionInput, body []byte) (*Revision, bool, error) {
	var fe []apperr.FieldError
	if in.ExpectedRevision == nil {
		fe = append(fe, apperr.FieldError{Field: "expected_revision", Reason: apperr.ReasonRequired})
	} else if *in.ExpectedRevision < 1 {
		fe = append(fe, apperr.FieldError{Field: "expected_revision", Reason: apperr.ReasonOutOfRange})
	}
	var mutationID string
	if in.MutationID == nil {
		fe = append(fe, apperr.FieldError{Field: "mutation_id", Reason: apperr.ReasonRequired})
	} else if id, ok := ids.Parse(*in.MutationID); ok {
		mutationID = id
	} else {
		fe = append(fe, apperr.FieldError{Field: "mutation_id", Reason: apperr.ReasonInvalid})
	}
	if len(fe) > 0 {
		return nil, false, apperr.Validation(fe...)
	}
	fp, err := idem.MutationFingerprint(body)
	if err != nil {
		return nil, false, err
	}

	var out *Revision
	created := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, mid, uid); err != nil {
			return err
		}
		prev, err := scanRevision(tx.QueryRow(ctx, `SELECT `+revisionCols+` FROM transcript_revisions
			WHERE meeting_id = $1 AND mutation_id = $2`, mid, mutationID))
		if err == nil {
			if prev.mutationFingerprint == nil || *prev.mutationFingerprint != fp {
				return apperr.New(apperr.IdempotencyKeyReused)
			}
			out = &prev.Revision
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		cur, err := currentRevision(ctx, tx, mid)
		if err != nil {
			return err
		}
		if cur.RevisionNumber != *in.ExpectedRevision {
			return apperr.WithDetails(apperr.RevisionConflict, map[string]any{
				"current_revision_number": cur.RevisionNumber, "current_revision_id": cur.ID})
		}
		segs, err := applyEdits(cur.Segments, in.Edits)
		if err != nil {
			return err
		}
		b, err := json.Marshal(segs)
		if err != nil {
			return err
		}
		r, err := scanRevision(tx.QueryRow(ctx, `
			INSERT INTO transcript_revisions (meeting_id, revision_number, kind, parent_revision_id, audio_asset_id,
			                                  language, provider, model, segments, mutation_id, mutation_fingerprint)
			VALUES ($1, $2, 'correction', $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING `+revisionCols,
			mid, cur.RevisionNumber+1, cur.ID, cur.audioAssetID, cur.Language, cur.provider, cur.model, b, mutationID, fp))
		if err != nil {
			return err
		}
		if err := upsertSearch(ctx, tx, mid, segs); err != nil {
			return err
		}
		if err := db.TouchMeeting(ctx, tx, mid); err != nil {
			return err
		}
		out, created = &r.Revision, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

func applyEdits(cur []Segment, edits []Edit) ([]Segment, error) {
	if len(edits) == 0 {
		return nil, apperr.Content("/edits", apperr.ReasonTooShort)
	}
	idx := make(map[string]int, len(cur))
	for i, s := range cur {
		idx[s.ID] = i
	}
	out := append([]Segment(nil), cur...)
	seen := map[string]bool{}
	for i, e := range edits {
		p := fmt.Sprintf("/edits/%d", i)
		if e.SegmentID == nil {
			return nil, apperr.Content(p+"/segment_id", apperr.ReasonRequired)
		}
		k, ok := idx[*e.SegmentID]
		if !ok {
			return nil, apperr.Content(p+"/segment_id", apperr.ReasonNotFound)
		}
		if seen[*e.SegmentID] {
			return nil, apperr.Content(p+"/segment_id", apperr.ReasonDuplicate)
		}
		seen[*e.SegmentID] = true
		if e.Text == nil {
			return nil, apperr.Content(p+"/text", apperr.ReasonRequired)
		}
		if utf8.RuneCountInString(*e.Text) > MaxSegmentText {
			return nil, apperr.Content(p+"/text", apperr.ReasonTooLong)
		}
		out[k].Text = *e.Text
	}
	return out, nil
}

func upsertSearch(ctx context.Context, q db.Querier, mid string, segs []Segment) error {
	texts := make([]string, len(segs))
	for i, s := range segs {
		texts[i] = s.Text
	}
	_, err := q.Exec(ctx, `
		INSERT INTO search_documents (meeting_id, source, body) VALUES ($1, 'transcript', $2)
		ON CONFLICT (meeting_id, source) DO UPDATE SET body = EXCLUDED.body, updated_at = now()`,
		mid, strings.Join(texts, "\n"))
	return err
}

// MeetingJobs is GET /v1/meetings/{id}/jobs: newest first, at most 50.
func (s *Service) MeetingJobs(ctx context.Context, uid, mid string) ([]*meetings.Job, error) {
	if err := ownMeeting(ctx, s.pool, uid, mid); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+jobs.Columns+` FROM jobs j
		WHERE j.meeting_id = $1 AND j.type IN ('transcribe', 'generate_notes', 'generate_mom')
		ORDER BY j.created_at DESC, j.id DESC LIMIT $2`, mid, MaxJobsListed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*meetings.Job{}
	for rows.Next() {
		j, err := jobs.ScanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, meetings.JobView(j))
	}
	return out, rows.Err()
}

// Job is GET /v1/jobs/{job_id}; purge jobs and jobs of other owners or tombstoned meetings are not found.
func (s *Service) Job(ctx context.Context, uid, jobID string) (*meetings.Job, error) {
	j, err := jobs.ScanJob(s.pool.QueryRow(ctx, `SELECT `+jobs.Columns+` FROM jobs j
		JOIN meetings m ON m.id = j.meeting_id AND m.user_id = $2 AND m.deleted_at IS NULL
		WHERE j.id = $1 AND j.type IN ('transcribe', 'generate_notes', 'generate_mom')`, jobID, uid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return meetings.JobView(j), nil
}
