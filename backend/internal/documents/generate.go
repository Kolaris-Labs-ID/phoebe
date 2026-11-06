package documents

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/idem"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
)

// Route patterns of the generate endpoints; part of the request fingerprint (backend.md §8).
const (
	RouteGenerateNotes = "POST /v1/meetings/{id}/documents/ai_notes/generate"
	RouteGenerateMoM   = "POST /v1/meetings/{id}/documents/mom/generate"
)

// Payload of generate_notes / generate_mom jobs (backend.md §3.4). Sources are pinned at
// enqueue; handlers never read "latest". AINotesRevisionID is set for generate_mom only.
type Payload struct {
	AINotesRevisionID     string  `json:"ai_notes_revision_id,omitempty"`
	TranscriptRevisionID  string  `json:"transcript_revision_id"`
	ManualNotesRevisionID *string `json:"manual_notes_revision_id"`
	Language              string  `json:"language"`
}

type GenerateInput struct {
	Language          *string `json:"language"`
	AINotesRevisionID *string `json:"ai_notes_revision_id"`
}

func latestTranscript(ctx context.Context, q db.Querier, meetingID string) (*string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM transcript_revisions WHERE meeting_id = $1 ORDER BY revision_number DESC LIMIT 1`,
		meetingID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &id, err
}

func currentRevisionID(ctx context.Context, q db.Querier, meetingID string, kind Kind) (*string, error) {
	var id string
	err := q.QueryRow(ctx, `
		SELECT r.id FROM documents d JOIN document_revisions r ON r.document_id = d.id AND r.revision_number = d.current_revision_number
		WHERE d.meeting_id = $1 AND d.kind = $2`, meetingID, kind).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &id, err
}

func userLanguage(ctx context.Context, q db.Querier, userID string) (string, error) {
	var lang string
	err := q.QueryRow(ctx, `SELECT language FROM users WHERE id = $1`, userID).Scan(&lang)
	return lang, err
}

// byKey is the §8.1 key lookup: same fingerprint → that job; different → idempotency_key_reused.
func byKey(ctx context.Context, q db.Querier, userID, key, fingerprint string) (*jobs.Job, error) {
	j, err := jobs.GetByKey(ctx, q, userID, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if j.RequestFingerprint == nil || *j.RequestFingerprint != fingerprint {
		return nil, apperr.New(apperr.IdempotencyKeyReused)
	}
	return j, nil
}

func jobInProgress(j *jobs.Job) error {
	return apperr.WithDetails(apperr.JobInProgress, map[string]any{"job_id": j.ID})
}

// Generate enqueues a user-triggered generate_notes (kind ai_notes) or generate_mom job
// following backend.md §8.1. created=false is an idempotent replay (200).
func (s *Service) Generate(ctx context.Context, userID, meetingID string, kind Kind, key string, raw []byte, in GenerateInput) (*meetings.Job, bool, error) {
	route, jobType := RouteGenerateNotes, jobs.TypeGenerateNotes
	if kind == KindMoM {
		route, jobType = RouteGenerateMoM, jobs.TypeGenerateMoM
	}
	fp, err := idem.RequestFingerprint(route, meetingID, raw)
	if err != nil {
		return nil, false, apperr.New(apperr.InvalidJSON)
	}
	if err := ownMeeting(ctx, s.pool, userID, meetingID); err != nil {
		return nil, false, err
	}
	if j, err := byKey(ctx, s.pool, userID, key, fp); j != nil || err != nil {
		return meetings.JobView(j), false, err
	}

	var fe fieldErrs
	if in.Language != nil && *in.Language != "id" && *in.Language != "en" {
		fe.add("language", apperr.ReasonInvalid)
	}
	var aiNotesID string
	if kind == KindMoM && in.AINotesRevisionID != nil {
		id, ok := ids.Parse(*in.AINotesRevisionID)
		if !ok {
			fe.add("ai_notes_revision_id", apperr.ReasonInvalid)
		}
		aiNotesID = id
	}
	if err := fe.err(); err != nil {
		return nil, false, err
	}

	var out *jobs.Job
	created := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		if j, err := byKey(ctx, tx, userID, key, fp); j != nil || err != nil {
			out = j
			return err
		}
		tr, err := latestTranscript(ctx, tx, meetingID)
		if err != nil {
			return err
		}
		if tr == nil {
			return apperr.New(apperr.TranscriptNotReady)
		}
		p := Payload{TranscriptRevisionID: *tr}
		if p.ManualNotesRevisionID, err = currentRevisionID(ctx, tx, meetingID, KindManualNotes); err != nil {
			return err
		}
		if kind == KindMoM {
			if aiNotesID != "" {
				var one int
				err := tx.QueryRow(ctx, `
					SELECT 1 FROM document_revisions r JOIN documents d ON d.id = r.document_id
					WHERE r.id = $1 AND d.meeting_id = $2 AND d.kind = 'ai_notes'`, aiNotesID, meetingID).Scan(&one)
				if errors.Is(err, pgx.ErrNoRows) {
					return apperr.ErrNotFound
				}
				if err != nil {
					return err
				}
				p.AINotesRevisionID = aiNotesID
			} else {
				cur, err := currentRevisionID(ctx, tx, meetingID, KindAINotes)
				if err != nil {
					return err
				}
				if cur == nil {
					return apperr.New(apperr.AINotesNotReady)
				}
				p.AINotesRevisionID = *cur
			}
		}
		if active, err := jobs.Active(ctx, tx, meetingID, jobType); err == nil {
			return jobInProgress(active)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := s.usage.CheckAIRate(ctx, tx, userID); err != nil {
			return err
		}
		if in.Language != nil {
			p.Language = *in.Language
		} else if p.Language, err = userLanguage(ctx, tx, userID); err != nil {
			return err
		}
		j, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Type: jobType, UserID: userID, MeetingID: meetingID,
			Trigger: jobs.TriggerUser, IdempotencyKey: key, RequestFingerprint: fp, Payload: p,
			MaxAttempts: jobs.AIMaxAttempts, CorrelationID: meetings.CorrelationID(ctx)})
		if errors.Is(err, jobs.ErrDuplicate) {
			return s.duplicate(ctx, tx, userID, meetingID, key, fp, jobType, &out)
		}
		if err != nil {
			return err
		}
		if kind == KindAINotes {
			if _, err := meetings.Transition(ctx, tx, meetingID, meetings.NotesRequested); err != nil {
				return err
			}
		}
		out, created = j, true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return meetings.JobView(out), created, nil
}

// duplicate resolves a unique violation on insert (§8.1 last paragraph).
func (s *Service) duplicate(ctx context.Context, q db.Querier, userID, meetingID, key, fp, jobType string, out **jobs.Job) error {
	j, err := byKey(ctx, q, userID, key, fp)
	if err != nil || j != nil {
		*out = j
		return err
	}
	active, err := jobs.Active(ctx, q, meetingID, jobType)
	if err == nil {
		return jobInProgress(active)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("documents: duplicate job vanished")
	}
	return err
}
