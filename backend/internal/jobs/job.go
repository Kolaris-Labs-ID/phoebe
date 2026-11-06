package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/db"
)

const (
	TypeTranscribe    = "transcribe"
	TypeGenerateNotes = "generate_notes"
	TypeGenerateMoM   = "generate_mom"
	TypePurgeMeeting  = "purge_meeting"
	TypePurgeAccount  = "purge_account"

	TriggerUser   = "user"
	TriggerSystem = "system"

	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// Error categories (jobs.last_error_category).
const (
	CatProviderUnavailable = "provider_unavailable"
	CatProviderTimeout     = "provider_timeout"
	CatProviderRateLimited = "provider_rate_limited"
	CatProviderRejected    = "provider_rejected"
	CatInvalidOutput       = "invalid_output"
	CatAudioUnavailable    = "audio_unavailable"
	CatAudioUnreadable     = "audio_unreadable"
	CatNoSpeech            = "no_speech"
	CatTimeout             = "timeout"
	CatInternal            = "internal"
)

// AIMaxAttempts = initial + 2 retries (PRD AI-03); not configurable.
const (
	AIMaxAttempts    = 3
	PurgeMaxAttempts = 10
)

type Job struct {
	ID                 string
	Type               string
	UserID             *string
	MeetingID          *string
	Trigger            string
	Status             string
	IdempotencyKey     string
	RequestFingerprint *string
	Payload            json.RawMessage
	Result             json.RawMessage
	Attempts           int
	MaxAttempts        int
	RunAfter           time.Time
	LeaseOwner         *string
	LeaseUntil         *time.Time
	HeartbeatAt        *time.Time
	LastErrorCategory  *string
	LastErrorDetail    *string
	CorrelationID      string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
}

// Token is the fencing token captured at claim (backend.md §4.2).
type Token struct {
	ID      string
	Owner   string
	Attempt int
}

func (j *Job) Token() Token {
	var owner string
	if j.LeaseOwner != nil {
		owner = *j.LeaseOwner
	}
	return Token{ID: j.ID, Owner: owner, Attempt: j.Attempts}
}

func (j *Job) DecodePayload(v any) error { return json.Unmarshal(j.Payload, v) }

// Columns is the select list matching ScanJob; use as "SELECT "+jobs.Columns+" FROM jobs j ...".
const Columns = `j.id, j.type, j.user_id, j.meeting_id, j.trigger, j.status, j.idempotency_key,
	j.request_fingerprint, j.payload, j.result, j.attempts, j.max_attempts, j.run_after,
	j.lease_owner, j.lease_until, j.heartbeat_at, j.last_error_category, j.last_error_detail,
	j.correlation_id, j.created_at, j.updated_at, j.started_at, j.finished_at`

func ScanJob(row pgx.Row) (*Job, error) {
	var j Job
	var payload, result []byte
	err := row.Scan(&j.ID, &j.Type, &j.UserID, &j.MeetingID, &j.Trigger, &j.Status, &j.IdempotencyKey,
		&j.RequestFingerprint, &payload, &result, &j.Attempts, &j.MaxAttempts, &j.RunAfter,
		&j.LeaseOwner, &j.LeaseUntil, &j.HeartbeatAt, &j.LastErrorCategory, &j.LastErrorDetail,
		&j.CorrelationID, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt)
	if err != nil {
		return nil, err
	}
	j.Payload = payload
	if result != nil {
		j.Result = result
	}
	return &j, nil
}

type NewJob struct {
	ID                 string // optional pre-generated id; "" = DB default
	Type               string
	UserID             string // "" = NULL (purge jobs)
	MeetingID          string // "" = NULL (purge jobs)
	Trigger            string
	IdempotencyKey     string
	RequestFingerprint string // "" = NULL (system jobs)
	Payload            any    // marshaled to a JSON object; nil = {}
	MaxAttempts        int
	RunAfter           time.Time // zero = now()
	CorrelationID      string
}

// ErrDuplicate: Enqueue hit a unique constraint ((user_id, idempotency_key) or one active
// job per (meeting, type)) and inserted nothing. The tx stays usable (ON CONFLICT DO NOTHING).
var ErrDuplicate = errors.New("jobs: duplicate job")

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Enqueue inserts a queued job inside the caller's tx and bumps the meeting's updated_at.
func Enqueue(ctx context.Context, q db.Querier, n NewJob) (*Job, error) {
	payload := []byte("{}")
	if n.Payload != nil {
		b, err := json.Marshal(n.Payload)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	var runAfter *time.Time
	if !n.RunAfter.IsZero() {
		runAfter = &n.RunAfter
	}
	row := q.QueryRow(ctx, `
		INSERT INTO jobs AS j (id, type, user_id, meeting_id, trigger, idempotency_key, request_fingerprint,
		                    payload, max_attempts, run_after, correlation_id)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10, now()), $11)
		ON CONFLICT DO NOTHING
		RETURNING `+Columns,
		nullable(n.ID), n.Type, nullable(n.UserID), nullable(n.MeetingID), n.Trigger, n.IdempotencyKey,
		nullable(n.RequestFingerprint), payload, n.MaxAttempts, runAfter, n.CorrelationID)
	j, err := ScanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDuplicate
	}
	if err != nil {
		return nil, err
	}
	if j.MeetingID != nil {
		if err := db.TouchMeeting(ctx, q, *j.MeetingID); err != nil {
			return nil, err
		}
	}
	return j, nil
}

// Get returns pgx.ErrNoRows when absent. Callers enforce ownership themselves.
func Get(ctx context.Context, q db.Querier, id string) (*Job, error) {
	return ScanJob(q.QueryRow(ctx, `SELECT `+Columns+` FROM jobs j WHERE j.id = $1`, id))
}

// GetByKey looks up by (user_id, idempotency_key); userID "" matches NULL (system purge jobs).
// Returns pgx.ErrNoRows when absent.
func GetByKey(ctx context.Context, q db.Querier, userID, key string) (*Job, error) {
	return ScanJob(q.QueryRow(ctx,
		`SELECT `+Columns+` FROM jobs j WHERE j.user_id IS NOT DISTINCT FROM $1 AND j.idempotency_key = $2`,
		nullable(userID), key))
}

// Active returns the queued/running job of type for meeting, or pgx.ErrNoRows.
func Active(ctx context.Context, q db.Querier, meetingID, jobType string) (*Job, error) {
	return ScanJob(q.QueryRow(ctx,
		`SELECT `+Columns+` FROM jobs j WHERE j.meeting_id = $1 AND j.type = $2 AND j.status IN ('queued', 'running')`,
		meetingID, jobType))
}

// Latest returns the newest job of type for meeting, or pgx.ErrNoRows.
func Latest(ctx context.Context, q db.Querier, meetingID, jobType string) (*Job, error) {
	return ScanJob(q.QueryRow(ctx,
		`SELECT `+Columns+` FROM jobs j WHERE j.meeting_id = $1 AND j.type = $2 ORDER BY j.created_at DESC, j.id DESC LIMIT 1`,
		meetingID, jobType))
}
