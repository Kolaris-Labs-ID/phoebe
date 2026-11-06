package transcripts

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
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

// Whisper hallucination heuristics (backend.md §5.1 step 4, §19.6).
const (
	noSpeechProbMax     = 0.6
	avgLogprobMin       = -1.0
	compressionRatioMax = 2.4
	overrunToleranceMS  = 1000
)

type NotesPayload struct {
	TranscriptRevisionID  string  `json:"transcript_revision_id"`
	ManualNotesRevisionID *string `json:"manual_notes_revision_id"`
	Language              string  `json:"language"`
}

type transcribeResult struct {
	TranscriptRevisionID string `json:"transcript_revision_id"`
}

func (s *Service) chunked(sizeBytes int64) bool {
	return s.cfg.STTProvider != "fake" && sizeBytes > s.cfg.STTMaxFileBytes
}

func (s *Service) chunker() audio.Chunker {
	return audio.Chunker{FFmpegPath: s.cfg.FFmpegPath, ChunkSeconds: s.cfg.STTChunkSeconds}
}

// TranscribeSpec registers the transcribe job (backend.md §5.1).
func (s *Service) TranscribeSpec() jobs.Spec {
	return jobs.Spec{Handler: s.Transcribe, Deadline: s.deadline, OnFailure: s.onFailure}
}

func (s *Service) deadline(j *jobs.Job) time.Duration {
	var p payload
	_ = j.DecodePayload(&p)
	n := 1
	if s.chunked(p.SizeBytes) {
		n = s.chunker().Count(p.DurationMS)
	}
	return min(time.Duration(n)*s.cfg.STTRequestTimeout+5*time.Minute, s.cfg.JobTimeoutTranscribeMax)
}

type rawMS struct {
	startMS, endMS int64
	seg            ai.RawSegment
}

func secToMS(sec float64) int64 { return int64(math.Floor(sec*1000 + 0.5)) }

func hallucinated(r ai.RawSegment) bool {
	return (r.NoSpeechProb > noSpeechProbMax && r.AvgLogprob < avgLogprobMin) || r.CompressionRatio > compressionRatioMax
}

// normalize is backend.md §5.1 steps 4–5.
func normalize(raw []rawMS, durationMS int64) ([]Segment, error) {
	out := []Segment{}
	for _, r := range raw {
		text := strings.TrimSpace(r.seg.Text)
		if text == "" || hallucinated(r.seg) {
			continue
		}
		if r.startMS > durationMS+overrunToleranceMS {
			return nil, jobs.Permanent(jobs.CatAudioUnreadable, "segment past audio end")
		}
		if r.seg.StartSec < 0 || r.seg.EndSec < 0 || r.startMS > r.endMS {
			return nil, jobs.Retryable(jobs.CatInvalidOutput, "segment times", 0)
		}
		out = append(out, Segment{StartMS: min(r.startMS, durationMS), EndMS: min(r.endMS, durationMS), Text: text})
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

	var raw []rawMS
	var detected string
	collect := func(res ai.STTResult, offsetMS int64) {
		if detected == "" {
			detected = res.Language
		}
		for _, r := range res.Segments {
			raw = append(raw, rawMS{startMS: offsetMS + secToMS(r.StartSec), endMS: offsetMS + secToMS(r.EndSec), seg: r})
		}
	}

	if s.chunked(a.size) {
		obj, _, err := s.storage.Open(ctx, a.key)
		if errors.Is(err, storage.ErrNotFound) {
			return jobs.Permanent(jobs.CatAudioUnavailable, "")
		}
		if err != nil {
			return err
		}
		c := s.chunker()
		chunks, err := c.Split(ctx, obj, audio.Ext(a.contentType), a.durationMS)
		obj.Close()
		if err != nil {
			return err
		}
		defer chunks.Close()
		for k, ch := range chunks.Items {
			dur := a.durationMS - ch.OffsetMS
			if k < len(chunks.Items)-1 {
				dur = int64(c.ChunkSeconds) * 1000
			}
			f, err := os.Open(ch.Path)
			if err != nil {
				return err
			}
			res, err := s.callSTT(ctx, j, ai.STTInput{Reader: f, Filename: ch.Filename, ContentType: audio.ChunkContentType,
				Language: sttLang, DurationMS: dur})
			f.Close()
			if err != nil {
				return err
			}
			collect(res, ch.OffsetMS)
		}
	} else {
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
		collect(res, 0)
	}

	segs, err := normalize(raw, a.durationMS)
	if err != nil {
		return err
	}
	lang := detected
	if lang == "" {
		lang = p.Language
	}
	return s.finalize(ctx, j, p, segs, lang)
}

// callSTT makes one provider call under STT_REQUEST_TIMEOUT and records its ai_calls row
// (own statement, outside finalize; failures recorded too).
func (s *Service) callSTT(ctx context.Context, j *jobs.Job, in ai.STTInput) (ai.STTResult, error) {
	provider, model := s.stt.Name()
	cctx, cancel := context.WithTimeout(ctx, s.cfg.STTRequestTimeout)
	start := time.Now()
	res, err := s.stt.Transcribe(cctx, in)
	cancel()
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

func (s *Service) finalize(ctx context.Context, j *jobs.Job, p payload, segs []Segment, lang string) error {
	provider, model := s.stt.Name()
	trID := ids.New()
	b, err := json.Marshal(segs)
	if err != nil {
		return err
	}
	uid, mid := *j.UserID, *j.MeetingID
	return jobs.Complete(ctx, s.pool, j, transcribeResult{TranscriptRevisionID: trID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO transcript_revisions (id, meeting_id, revision_number, kind, audio_asset_id, language, provider, model, segments, job_id)
			VALUES ($1, $2, 1, 'original', $3, $4, $5, $6, $7, $8)`,
			trID, mid, p.AudioAssetID, lang, provider, model, b, j.ID); err != nil {
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
		_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{
			Type: jobs.TypeGenerateNotes, UserID: uid, MeetingID: mid, Trigger: jobs.TriggerSystem,
			IdempotencyKey: "sys:notes:" + trID, MaxAttempts: jobs.AIMaxAttempts, CorrelationID: j.CorrelationID,
			Payload: NotesPayload{TranscriptRevisionID: trID, ManualNotesRevisionID: manual, Language: userLang},
		})
		if err != nil && !errors.Is(err, jobs.ErrDuplicate) {
			return err
		}
		if _, err := meetings.Transition(ctx, tx, mid, meetings.TranscriptWritten); err != nil {
			return err
		}
		if err := db.TouchMeeting(ctx, tx, mid); err != nil {
			return err
		}
		return events.Emit(ctx, tx, uid, trID, events.TranscriptionCompleted, mid)
	})
}

// onFailure is the permanent-failure stage (backend.md §5.1 step 7).
func (s *Service) onFailure(ctx context.Context, tx pgx.Tx, j *jobs.Job, _ string) error {
	var p payload
	if err := j.DecodePayload(&p); err == nil && p.ReservationID != "" {
		if err := usage.Release(ctx, tx, p.ReservationID); err != nil && !errors.Is(err, usage.ErrNotReserved) {
			return err
		}
	}
	_, err := meetings.Transition(ctx, tx, *j.MeetingID, meetings.TranscriptionFailed)
	return err
}
