package transcripts

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/ai"
	"phoebe/internal/audio"
	"phoebe/internal/db"
	"phoebe/internal/events"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
	"phoebe/internal/storage"
	"phoebe/internal/usage"
)

const overrunToleranceMS = 1000

type NotesPayload struct {
	TranscriptRevisionID  string  `json:"transcript_revision_id"`
	ManualNotesRevisionID *string `json:"manual_notes_revision_id"`
	Language              string  `json:"language"`
}

type transcribeResult struct {
	TranscriptRevisionID string `json:"transcript_revision_id"`
}

// TranscribeSpec registers the transcribe job (backend.md §5.1).
func (s *Service) TranscribeSpec() jobs.Spec {
	return jobs.Spec{Handler: s.Transcribe, Deadline: s.deadline, OnFailure: s.onFailure}
}

// One Transcribe may make many provider calls, each timed out inside the adapter, so the attempt gets the whole budget.
func (s *Service) deadline(*jobs.Job) time.Duration { return s.cfg.JobTimeoutTranscribeMax }

// "" counts as chunk so times are never presented as more precise than they are.
func timing(t string) string {
	if t == ai.TimingSegment {
		return ai.TimingSegment
	}
	return ai.TimingChunk
}

// normalize is backend.md §5.1 steps 4–5. Only empty text is dropped here; the adapter already
// applied the silence + low-confidence drop rule and flags the rest uncertain.
func normalize(raw []ai.Segment, durationMS int64) ([]Segment, error) {
	out := []Segment{}
	for _, r := range raw {
		text := strings.TrimSpace(r.Text)
		if text == "" {
			continue
		}
		if r.StartMS > durationMS+overrunToleranceMS {
			return nil, jobs.Permanent(jobs.CatAudioUnreadable, "segment past audio end")
		}
		if r.StartMS < 0 || r.EndMS < 0 || r.StartMS > r.EndMS {
			return nil, jobs.Retryable(jobs.CatInvalidOutput, "segment times", 0)
		}
		g := Segment{StartMS: min(r.StartMS, durationMS), EndMS: min(r.EndMS, durationMS), Text: text, Uncertain: r.Uncertain}
		if r.Uncertain {
			reason := cmp.Or(r.UncertainReason, ai.UncertainLowConfidence)
			g.UncertainReason = &reason
		}
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil, jobs.Permanent(jobs.CatNoSpeech, "")
	}
	slices.SortStableFunc(out, func(a, b Segment) int { return cmp.Compare(a.StartMS, b.StartMS) })
	for i := range out {
		out[i].ID = "s" + strconv.Itoa(i+1)
	}
	return out, nil
}

type asset struct {
	contentType string
	size        int64
	durationMS  int64
	key         string
	deleted     bool
}

// Transcribe is the transcribe job handler (backend.md §5.1).
func (s *Service) Transcribe(ctx context.Context, j *jobs.Job) error {
	var p payload
	if err := j.DecodePayload(&p); err != nil {
		return jobs.Permanent(jobs.CatInternal, "bad payload")
	}
	var a asset
	err := s.pool.QueryRow(ctx, `SELECT content_type, size_bytes, duration_ms, storage_key, deleted_at IS NOT NULL
		FROM audio_assets WHERE id = $1`, p.AudioAssetID).Scan(&a.contentType, &a.size, &a.durationMS, &a.key, &a.deleted)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && a.deleted) {
		return jobs.Permanent(jobs.CatAudioUnavailable, "")
	}
	if err != nil {
		return err
	}
	sttLang := p.Language
	if sttLang == "auto" {
		sttLang = ""
	}
	obj, _, err := s.storage.Open(ctx, a.key)
	if errors.Is(err, storage.ErrNotFound) {
		return jobs.Permanent(jobs.CatAudioUnavailable, "")
	}
	if err != nil {
		return err
	}
	res, err := s.callSTT(ctx, j, ai.STTInput{Reader: obj, Filename: "audio." + audio.Ext(a.contentType),
		ContentType: a.contentType, Language: sttLang, DurationMS: a.durationMS})
	obj.Close()
	if err != nil {
		return err
	}
	segs, err := normalize(res.Segments, a.durationMS)
	if err != nil {
		return err
	}
	lang := res.Language
	if lang == "" {
		lang = p.Language
	}
	return s.finalize(ctx, j, p, segs, lang, timing(res.Timing))
}

// callSTT runs one adapter Transcribe (which may make several provider requests, each under
// its own STT_REQUEST_TIMEOUT) and records one ai_calls row for it (own statement, outside
// finalize; failures recorded too).
func (s *Service) callSTT(ctx context.Context, j *jobs.Job, in ai.STTInput) (ai.STTResult, error) {
	provider, model := s.stt.Name()
	start := time.Now()
	res, err := s.stt.Transcribe(ctx, in)
	call := usage.AICall{UserID: *j.UserID, MeetingID: *j.MeetingID, JobID: j.ID, JobType: j.Type, Attempt: j.Attempts,
		Capability: usage.CapabilitySTT, Provider: provider, Model: model, LatencyMS: time.Since(start).Milliseconds(),
		CorrelationID: j.CorrelationID}
	if err != nil {
		call.ErrorCategory = ai.CategoryOf(err)
	} else {
		ms := res.AudioMS
		if ms <= 0 {
			ms = in.DurationMS
		}
		call.AudioMS = &ms
	}
	if rerr := s.usage.RecordAICall(context.WithoutCancel(ctx), s.pool, call); rerr != nil {
		return ai.STTResult{}, rerr
	}
	if err != nil {
		return ai.STTResult{}, ai.JobError(err)
	}
	return res, nil
}

func (s *Service) finalize(ctx context.Context, j *jobs.Job, p payload, segs []Segment, lang, tm string) error {
	provider, model := s.stt.Name()
	trID := ids.New()
	b, err := json.Marshal(segs)
	if err != nil {
		return err
	}
	uid, mid := *j.UserID, *j.MeetingID
	return jobs.Complete(ctx, s.pool, j, transcribeResult{TranscriptRevisionID: trID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO transcript_revisions (id, meeting_id, revision_number, kind, audio_asset_id, language, provider, model, segments, job_id, timing)
			VALUES ($1, $2, 1, 'original', $3, $4, $5, $6, $7, $8, $9)`,
			trID, mid, p.AudioAssetID, lang, provider, model, b, j.ID, tm); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE audio_assets SET retain_until = now() + make_interval(days => $2) WHERE id = $1`,
			p.AudioAssetID, s.cfg.AudioRetentionDays); err != nil {
			return err
		}
		if err := upsertSearch(ctx, tx, mid, segs); err != nil {
			return err
		}
		if err := usage.Consume(ctx, tx, uid, p.ReservationID); err != nil {
			return err
		}
		if p.Notes == NotesSkip {
			if _, err := meetings.Transition(ctx, tx, mid, meetings.TranscriptOnly); err != nil {
				return err
			}
		} else if err := s.enqueueAutoNotes(ctx, tx, j, trID); err != nil {
			return err
		}
		if err := db.TouchMeeting(ctx, tx, mid); err != nil {
			return err
		}
		return events.Emit(ctx, tx, uid, trID, events.TranscriptionCompleted, mid)
	})
}

func (s *Service) enqueueAutoNotes(ctx context.Context, tx pgx.Tx, j *jobs.Job, trID string) error {
	uid, mid := *j.UserID, *j.MeetingID
	var manual *string
	err := tx.QueryRow(ctx, `SELECT r.id FROM documents d
		JOIN document_revisions r ON r.document_id = d.id AND r.revision_number = d.current_revision_number
		WHERE d.meeting_id = $1 AND d.kind = 'manual_notes'`, mid).Scan(&manual)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var userLang string
	if err := tx.QueryRow(ctx, `SELECT language FROM users WHERE id = $1`, uid).Scan(&userLang); err != nil {
		return err
	}
	nj, err := jobs.Enqueue(ctx, tx, jobs.NewJob{
		Type: jobs.TypeGenerateNotes, UserID: uid, MeetingID: mid, Trigger: jobs.TriggerSystem,
		IdempotencyKey: "sys:notes:" + trID, MaxAttempts: jobs.AIMaxAttempts, CorrelationID: j.CorrelationID,
		Payload: NotesPayload{TranscriptRevisionID: trID, ManualNotesRevisionID: manual, Language: userLang},
	})
	if err != nil && !errors.Is(err, jobs.ErrDuplicate) {
		return err
	}
	if nj != nil {
		if err := usage.AttachSlot(ctx, tx, j.ID, nj.ID); err != nil {
			return err
		}
	}
	_, err = meetings.Transition(ctx, tx, mid, meetings.TranscriptWritten)
	return err
}

// onFailure is the permanent-failure stage (backend.md §5.1 step 7): quota and the automatic
// notes slot are released.
func (s *Service) onFailure(ctx context.Context, tx pgx.Tx, j *jobs.Job, _ string) error {
	var p payload
	if err := j.DecodePayload(&p); err == nil && p.ReservationID != "" {
		if err := usage.Release(ctx, tx, p.ReservationID); err != nil && !errors.Is(err, usage.ErrNotReserved) {
			return err
		}
	}
	if err := usage.ReleaseSlot(ctx, tx, j.ID); err != nil {
		return err
	}
	_, err := meetings.Transition(ctx, tx, *j.MeetingID, meetings.TranscriptionFailed)
	return err
}
