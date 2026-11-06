package transcripts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/idem"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
	"phoebe/internal/usage"
)

// StartRoute is the route pattern in the request fingerprint (backend.md §8), shared by the
// explicit call and the upload auto-start so both produce the same fingerprint.
const StartRoute = "POST /v1/meetings/{id}/transcription"

const (
	NotesAuto = "auto"
	NotesSkip = "skip"
)

type StartInput struct {
	Language *string `json:"language"`
	Notes    *string `json:"notes"`
}

// Notes "" (jobs enqueued before notes modes existed) behaves as auto.
type payload struct {
	AudioAssetID  string `json:"audio_asset_id"`
	ReservationID string `json:"reservation_id"`
	Language      string `json:"language"`
	Notes         string `json:"notes"`
	SizeBytes     int64  `json:"size_bytes"`
	DurationMS    int64  `json:"duration_ms"`
}

// errRace: a unique constraint (job key, one active job, one live charge per audio) fired;
// the outcome is decided by re-reading after rollback (backend.md §8.1).
var errRace = errors.New("transcripts: start raced")

func validLanguage(l string) bool { return l == "auto" || l == "id" || l == "en" }

func ValidNotes(n string) bool { return n == NotesAuto || n == NotesSkip }

// An absent "notes" fingerprints as "auto" so a confirm without it replays the auto-start, whose body always carries it.
func fingerprintBody(body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte(`{}`)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, apperr.New(apperr.InvalidJSON)
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	if _, ok := m["notes"]; !ok {
		m["notes"] = json.RawMessage(`"auto"`)
	}
	return json.Marshal(m)
}

func autoStartBody(language, notes string) ([]byte, error) {
	return json.Marshal(map[string]string{"language": language, "notes": notes})
}

func replay(j *jobs.Job, fp string) (*jobs.Job, error) {
	if j.RequestFingerprint == nil || *j.RequestFingerprint != fp {
		return nil, apperr.New(apperr.IdempotencyKeyReused)
	}
	return j, nil
}

func lookupKey(ctx context.Context, q db.Querier, uid, key, fp string) (*jobs.Job, error) {
	j, err := jobs.GetByKey(ctx, q, uid, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return replay(j, fp)
}

// Start is POST /v1/meetings/{id}/transcription in the backend.md §8.1 order. created=false
// is a replay (200). body is the request body as received (fingerprint).
func (s *Service) Start(ctx context.Context, uid, mid, key string, in StartInput, body []byte) (*meetings.Job, bool, error) {
	var meetingLang string
	err := s.pool.QueryRow(ctx, `SELECT language FROM meetings WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		mid, uid).Scan(&meetingLang)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, apperr.ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	fb, err := fingerprintBody(body)
	if err != nil {
		return nil, false, err
	}
	fp, err := idem.RequestFingerprint(StartRoute, mid, fb)
	if err != nil {
		return nil, false, err
	}
	if j, err := lookupKey(ctx, s.pool, uid, key, fp); err != nil || j != nil {
		return meetings.JobView(j), false, err
	}
	lang := meetingLang
	var fe []apperr.FieldError
	if in.Language != nil {
		if validLanguage(*in.Language) {
			lang = *in.Language
		} else {
			fe = append(fe, apperr.FieldError{Field: "language", Reason: apperr.ReasonInvalid})
		}
	}
	notes := NotesAuto
	if in.Notes != nil {
		if ValidNotes(*in.Notes) {
			notes = *in.Notes
		} else {
			fe = append(fe, apperr.FieldError{Field: "notes", Reason: apperr.ReasonInvalid})
		}
	}
	if len(fe) > 0 {
		return nil, false, apperr.Validation(fe...)
	}

	var j *jobs.Job
	var created bool
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, mid, uid); err != nil {
			return err
		}
		var err error
		j, created, err = s.start(ctx, tx, uid, mid, key, fp, lang, notes)
		return err
	})
	if errors.Is(err, errRace) {
		j, err = resolveRace(ctx, s.pool, uid, mid, key, fp)
		created = false
	}
	if err != nil {
		return nil, false, err
	}
	return meetings.JobView(j), created, nil
}

// StartInTx is the upload auto-start (backend.md §7.3): run inside the caller's finalize tx,
// which already holds the meeting lock and has inserted the audio asset. It behaves exactly
// like POST …/transcription with Idempotency-Key key and body {"language": language, "notes":
// notes}. Its writes sit in a savepoint, so on any error (402/409/422/429 apperr or a race)
// nothing of it remains and tx stays usable.
func (s *Service) StartInTx(ctx context.Context, tx pgx.Tx, uid, mid, key, language, notes string) (*jobs.Job, bool, error) {
	body, err := autoStartBody(language, notes)
	if err != nil {
		return nil, false, err
	}
	fp, err := idem.RequestFingerprint(StartRoute, mid, body)
	if err != nil {
		return nil, false, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	j, created, err := s.start(ctx, sp, uid, mid, key, fp, language, notes)
	if err != nil {
		if rerr := sp.Rollback(ctx); rerr != nil {
			return nil, false, rerr
		}
		if errors.Is(err, errRace) {
			j, err = resolveRace(ctx, tx, uid, mid, key, fp)
			return j, false, err
		}
		return nil, false, err
	}
	return j, created, sp.Commit(ctx)
}

// start is §8.1 steps 2–6; q holds the meeting lock. With notes auto, quota seconds and one
// generation slot are reserved together, or neither (429 leaves no job).
func (s *Service) start(ctx context.Context, q db.Querier, uid, mid, key, fp, lang, notes string) (*jobs.Job, bool, error) {
	if j, err := lookupKey(ctx, q, uid, key, fp); err != nil || j != nil {
		return j, false, err
	}
	var assetID string
	var size, duration int64
	var deleted bool
	err := q.QueryRow(ctx, `SELECT id, size_bytes, duration_ms, deleted_at IS NOT NULL FROM audio_assets WHERE meeting_id = $1`,
		mid).Scan(&assetID, &size, &duration, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, apperr.New(apperr.AudioNotReady)
	}
	if err != nil {
		return nil, false, err
	}
	if deleted {
		return nil, false, apperr.New(apperr.AudioUnavailable)
	}
	var transcribed bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transcript_revisions WHERE meeting_id = $1)`, mid).Scan(&transcribed); err != nil {
		return nil, false, err
	}
	if transcribed {
		return nil, false, apperr.New(apperr.AlreadyTranscribed)
	}
	active, err := jobs.Active(ctx, q, mid, jobs.TypeTranscribe)
	if err == nil {
		return nil, false, apperr.WithDetails(apperr.JobInProgress, map[string]any{"job_id": active.ID})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	quote, err := s.usage.CheckQuota(ctx, q, uid, duration)
	if err != nil {
		return nil, false, err
	}
	if notes == NotesAuto {
		if err := s.usage.CheckSlot(ctx, q, uid); err != nil {
			return nil, false, err
		}
	}
	resID := ids.New()
	j, err := jobs.Enqueue(ctx, q, jobs.NewJob{
		Type: jobs.TypeTranscribe, UserID: uid, MeetingID: mid, Trigger: jobs.TriggerUser,
		IdempotencyKey: key, RequestFingerprint: fp, MaxAttempts: jobs.AIMaxAttempts,
		Payload: payload{AudioAssetID: assetID, ReservationID: resID, Language: lang, Notes: notes,
			SizeBytes: size, DurationMS: duration},
		CorrelationID: meetings.CorrelationID(ctx),
	})
	if errors.Is(err, jobs.ErrDuplicate) {
		return nil, false, errRace
	}
	if err != nil {
		return nil, false, err
	}
	err = usage.Reserve(ctx, q, quote, usage.Reservation{ID: resID, MeetingID: mid, AudioAssetID: assetID, JobID: j.ID})
	if db.IsUniqueViolation(err, "") {
		return nil, false, errRace
	}
	if err != nil {
		return nil, false, err
	}
	if notes == NotesAuto {
		if err := usage.ReserveSlot(ctx, q, usage.Slot{UserID: uid, MeetingID: mid, TranscribeJobID: j.ID}); err != nil {
			return nil, false, err
		}
	}
	if _, err := meetings.Transition(ctx, q, mid, meetings.TranscriptionStarted); err != nil {
		return nil, false, err
	}
	return j, true, nil
}

// resolveRace is the §8.1 unique-violation outcome, read after rollback.
func resolveRace(ctx context.Context, q db.Querier, uid, mid, key, fp string) (*jobs.Job, error) {
	if j, err := lookupKey(ctx, q, uid, key, fp); err != nil || j != nil {
		return j, err
	}
	active, err := jobs.Active(ctx, q, mid, jobs.TypeTranscribe)
	if err == nil {
		return nil, apperr.WithDetails(apperr.JobInProgress, map[string]any{"job_id": active.ID})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return nil, apperr.New(apperr.AlreadyTranscribed)
}
