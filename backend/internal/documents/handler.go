package documents

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/ai"
	"phoebe/internal/events"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
	"phoebe/internal/meetings"
	"phoebe/internal/usage"
)

// NotesSpec is the generate_notes job (backend.md §5.2); permanent failure → notes_failed.
func (s *Service) NotesSpec() jobs.Spec {
	return jobs.Spec{
		Handler:  func(ctx context.Context, j *jobs.Job) error { return s.generate(ctx, j, KindAINotes) },
		Deadline: s.deadline,
		OnFailure: func(ctx context.Context, tx pgx.Tx, j *jobs.Job, _ string) error {
			_, err := meetings.Transition(ctx, tx, *j.MeetingID, meetings.NotesFailed)
			return err
		},
	}
}

// MoMSpec is the generate_mom job; permanent failure changes nothing besides job status.
func (s *Service) MoMSpec() jobs.Spec {
	return jobs.Spec{
		Handler:  func(ctx context.Context, j *jobs.Job) error { return s.generate(ctx, j, KindMoM) },
		Deadline: s.deadline,
	}
}

// deadline = n_chunks × LLM_REQUEST_TIMEOUT + 1 min, capped at JOB_TIMEOUT_GENERATE_MAX.
func (s *Service) deadline(j *jobs.Job) time.Duration {
	limit := s.cfg.JobTimeoutGenerateMax
	var p Payload
	if j.DecodePayload(&p) != nil {
		return limit
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	segs, err := loadSegments(ctx, s.pool, p.TranscriptRevisionID)
	if err != nil {
		return limit
	}
	n := len(chunkSegments(promptSegments(segs), s.cfg.LLMMaxTranscriptChars))
	return min(time.Duration(n)*s.cfg.LLMRequestTimeout+time.Minute, limit)
}

func promptSegments(segs []segment) []promptSegment {
	out := make([]promptSegment, len(segs))
	for i, g := range segs {
		out[i] = promptSegment(g)
	}
	return out
}

type genInputs struct {
	startedAt *time.Time
	meeting   promptMeeting
	info      MeetingInfo
	segs      []segment
	notes     *ManualNotes
	aiNotes   json.RawMessage
}

// load reads the meeting metadata and the pinned source revisions. A tombstoned or purged
// meeting means the delete path already canceled this job: ErrLeaseLost, write nothing.
func (s *Service) load(ctx context.Context, meetingID string, kind Kind, p Payload) (*genInputs, error) {
	var in genInputs
	err := s.pool.QueryRow(ctx, `SELECT title, started_at, timezone, participants FROM meetings WHERE id = $1 AND deleted_at IS NULL`,
		meetingID).Scan(&in.info.Title, &in.startedAt, &in.info.Timezone, &in.info.Participants)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, jobs.ErrLeaseLost
	}
	if err != nil {
		return nil, err
	}
	if in.info.Participants == nil {
		in.info.Participants = []string{}
	}
	in.info.StartedAt = jsonx.TimePtr(in.startedAt)
	in.meeting = promptMeeting{Title: in.info.Title, Date: dateString(meetingDate(in.startedAt, in.info.Timezone)),
		Timezone: in.info.Timezone, Participants: in.info.Participants}

	if in.segs, err = loadSegments(ctx, s.pool, p.TranscriptRevisionID); err != nil {
		return nil, s.missing(ctx, meetingID, err)
	}
	if p.ManualNotesRevisionID != nil {
		if in.notes, err = loadManualNotes(ctx, s.pool, *p.ManualNotesRevisionID); err != nil {
			return nil, s.missing(ctx, meetingID, err)
		}
	}
	if kind == KindMoM {
		var raw []byte
		if err := s.pool.QueryRow(ctx, `SELECT content FROM document_revisions WHERE id = $1`, p.AINotesRevisionID).Scan(&raw); err != nil {
			return nil, s.missing(ctx, meetingID, err)
		}
		if in.aiNotes, err = stripProvenance(raw); err != nil {
			return nil, err
		}
	}
	return &in, nil
}

// missing maps an absent pinned revision: purged meeting → ErrLeaseLost; else a permanent bug.
func (s *Service) missing(ctx context.Context, meetingID string, err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var one int
	if e := s.pool.QueryRow(ctx, `SELECT 1 FROM meetings WHERE id = $1 AND deleted_at IS NULL`, meetingID).Scan(&one); errors.Is(e, pgx.ErrNoRows) {
		return jobs.ErrLeaseLost
	}
	return jobs.Permanent(jobs.CatInternal, "pinned revision missing")
}

func (s *Service) generate(ctx context.Context, j *jobs.Job, kind Kind) error {
	var p Payload
	if err := j.DecodePayload(&p); err != nil {
		return jobs.Permanent(jobs.CatInternal, "bad payload")
	}
	if p.Language != "en" {
		p.Language = "id"
	}
	userID, meetingID := *j.UserID, *j.MeetingID
	in, err := s.load(ctx, meetingID, kind, p)
	if err != nil {
		return err
	}
	promptNotes := []promptNote{}
	if in.notes != nil {
		for _, n := range in.notes.Items {
			promptNotes = append(promptNotes, promptNote(n))
		}
	}

	provider, model := s.llm.Name()
	version := promptVersion(kind)
	var notesParts []llmNotes
	var momParts []llmMoM
	for _, chunk := range chunkSegments(promptSegments(in.segs), s.cfg.LLMMaxTranscriptChars) {
		user, err := userMessage(promptData{Meeting: in.meeting, OutputLanguage: p.Language, ManualNotes: promptNotes,
			AINotes: in.aiNotes, Transcript: chunk})
		if err != nil {
			return err
		}
		start := time.Now()
		res, err := s.llm.Complete(ctx, systemPrompt(kind), user)
		latency := time.Since(start).Milliseconds()
		if err == nil {
			var derr error
			if kind == KindMoM {
				var m llmMoM
				m, derr = decodeMoM(res.Content)
				momParts = append(momParts, m)
			} else {
				var n llmNotes
				n, derr = decodeNotes(res.Content)
				notesParts = append(notesParts, n)
			}
			if derr != nil {
				err = ai.InvalidOutput(0, "undecodable json")
			}
		}
		call := usage.AICall{UserID: userID, MeetingID: meetingID, JobID: j.ID, JobType: j.Type, Attempt: j.Attempts,
			Capability: "llm", Provider: provider, Model: model, PromptVersion: version, LatencyMS: latency,
			CorrelationID: j.CorrelationID}
		if err == nil || res.InputTokens > 0 || res.OutputTokens > 0 {
			call.InputTokens, call.OutputTokens = &res.InputTokens, &res.OutputTokens
		}
		if err != nil {
			call.ErrorCategory = ai.CategoryOf(err)
		}
		if rerr := s.usage.RecordAICall(context.WithoutCancel(ctx), s.pool, call); rerr != nil {
			return rerr
		}
		if err != nil {
			return ai.JobError(err)
		}
	}

	src := newSources(in.segs, in.notes, in.info.Participants, meetingDate(in.startedAt, in.info.Timezone), p.Language)
	var c Content
	dropped := 0
	if kind == KindMoM {
		c, dropped, err = src.buildMoM(mergeMoM(momParts), in.info)
	} else {
		c, err = src.buildNotes(mergeNotes(notesParts))
	}
	if errors.Is(err, errEmptyOutput) {
		return jobs.Retryable(jobs.CatInvalidOutput, "empty output", 0)
	}
	if err != nil {
		return err
	}
	content, err := finalizeContent(kind, c, src.pins())
	if err != nil {
		return jobs.Retryable(jobs.CatInvalidOutput, "invalid content", 0)
	}
	if c, err = DecodeContent(kind, content); err != nil {
		return err
	}

	revID := ids.New()
	r := newRevision{ID: revID, Origin: OriginAIGenerated, Content: content, JobID: &j.ID,
		Sources:  sourceIDs{transcript: &p.TranscriptRevisionID, manualNotes: p.ManualNotesRevisionID},
		Provider: &provider, Model: &model, PromptVersion: &version}
	if kind == KindMoM {
		r.Sources.aiNotes = &p.AINotesRevisionID
	}
	result := map[string]any{"document_revision_id": revID, "dropped_items": dropped}
	return jobs.Complete(ctx, s.pool, j, result, func(tx pgx.Tx) error {
		d, err := ensureDocument(ctx, tx, meetingID, kind)
		if err != nil {
			return err
		}
		r.Number = d.current + 1
		if err := commitRevision(ctx, tx, meetingID, kind, d, r, c); err != nil {
			return err
		}
		if kind == KindMoM {
			return events.Emit(ctx, tx, userID, revID, events.MoMGenerated, meetingID)
		}
		if _, err := meetings.Transition(ctx, tx, meetingID, meetings.NotesWritten); err != nil {
			return err
		}
		return events.Emit(ctx, tx, userID, revID, events.NotesReady, meetingID)
	})
}
