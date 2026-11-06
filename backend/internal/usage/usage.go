package usage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"log/slog"
	"math"
	"time"

	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/jsonx"
	"phoebe/internal/obs"
)

const (
	StatusActive   = "active"
	StatusExpired  = "expired"
	StatusInactive = "inactive"

	CapabilitySTT = "stt"
	CapabilityLLM = "llm"
)

// ErrNotReserved: a consume/release found no row in status 'reserved' (backend.md §4.5:
// reservation transitions must affect exactly one row).
var ErrNotReserved = errors.New("usage: reservation is not reserved")

type Service struct {
	db  db.Querier
	cfg *config.Config
	log *slog.Logger
}

func New(pool db.Querier, cfg *config.Config, log *slog.Logger) *Service {
	return &Service{db: pool, cfg: cfg, log: log}
}

type Entitlement struct {
	Plan         string
	Status       string // effective: active|expired|inactive
	PeriodStart  time.Time
	PeriodEnd    time.Time
	QuotaSeconds int64
}

const effectiveStatus = `CASE WHEN status = 'inactive' THEN 'inactive'
	WHEN now() >= period_start AND now() < period_end THEN 'active' ELSE 'expired' END`

const liveUsageSQL = `
	SELECT COALESCE(sum(seconds) FILTER (WHERE status = 'consumed' AND period_start = $2), 0)::bigint,
	       COALESCE(sum(seconds) FILTER (WHERE status = 'reserved'), 0)::bigint
	FROM quota_reservations WHERE user_id = $1 AND status IN ('consumed', 'reserved')`

// SubjectHMAC keys trial grants: kind is "apple" or "dev".
func SubjectHMAC(pepper []byte, kind, subject string) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(kind + ":" + subject))
	return m.Sum(nil)
}

// CreateEntitlement runs in the first-login tx (backend.md §9). An identity that had a
// trial before gets the same entitlement with quota 0. Returns whether quota was granted.
func (s *Service) CreateEntitlement(ctx context.Context, q db.Querier, userID string, subjectHMAC []byte) (bool, error) {
	tag, err := q.Exec(ctx, `INSERT INTO trial_grants (subject_hmac) VALUES ($1) ON CONFLICT DO NOTHING`, subjectHMAC)
	if err != nil {
		return false, err
	}
	granted := tag.RowsAffected() == 1
	var quota int64
	if granted {
		quota = s.cfg.TrialQuotaMinutes * 60
	}
	_, err = q.Exec(ctx, `
		INSERT INTO entitlements (user_id, plan, quota_seconds, period_start, period_end)
		VALUES ($1, 'trial', $2, now(), now() + make_interval(days => $3))`,
		userID, quota, s.cfg.TrialPeriodDays)
	return granted, err
}

func lockEntitlement(ctx context.Context, q db.Querier, userID string) (Entitlement, error) {
	var e Entitlement
	err := q.QueryRow(ctx, `SELECT plan, `+effectiveStatus+`, period_start, period_end, quota_seconds
		FROM entitlements WHERE user_id = $1 FOR UPDATE`, userID).
		Scan(&e.Plan, &e.Status, &e.PeriodStart, &e.PeriodEnd, &e.QuotaSeconds)
	return e, err
}

// Seconds is the quota charge for an audio duration: whole seconds, rounded up once.
func Seconds(durationMS int64) int64 { return (durationMS + 999) / 1000 }

// Quote is a passed quota check; insert it with Reserve in the same tx.
type Quote struct {
	UserID      string
	Seconds     int64
	PeriodStart time.Time
}

// CheckQuota is §8.1 step 4 for transcribe: locks the entitlement row, then requires an
// active entitlement and enough remaining seconds (402 otherwise).
func (s *Service) CheckQuota(ctx context.Context, q db.Querier, userID string, durationMS int64) (Quote, error) {
	e, err := lockEntitlement(ctx, q, userID)
	if err != nil {
		return Quote{}, err
	}
	if e.Status != StatusActive {
		return Quote{}, apperr.WithDetails(apperr.EntitlementInactive, map[string]any{"period_end": jsonx.NewTime(e.PeriodEnd)})
	}
	var consumed, reserved int64
	if err := q.QueryRow(ctx, liveUsageSQL, userID, e.PeriodStart).Scan(&consumed, &reserved); err != nil {
		return Quote{}, err
	}
	remaining := max(0, e.QuotaSeconds-consumed-reserved)
	need := Seconds(durationMS)
	if need > remaining {
		return Quote{}, apperr.WithDetails(apperr.QuotaExceeded, map[string]any{"required_seconds": need, "remaining_seconds": remaining})
	}
	return Quote{UserID: userID, Seconds: need, PeriodStart: e.PeriodStart}, nil
}

type Reservation struct {
	ID           string
	MeetingID    string
	AudioAssetID string
	JobID        string
}

// Reserve inserts the reservation after the job (§8.1 step 5). A second live charge for the
// same audio asset fails with unique violation quota_reservations_one_live_per_audio_key.
func Reserve(ctx context.Context, q db.Querier, quote Quote, r Reservation) error {
	_, err := q.Exec(ctx, `
		INSERT INTO quota_reservations (id, user_id, meeting_id, audio_asset_id, job_id, seconds, period_start)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		r.ID, quote.UserID, r.MeetingID, r.AudioAssetID, r.JobID, quote.Seconds, quote.PeriodStart)
	return err
}

// Consume charges a reservation in transcribe success finalize. The seconds land in the
// entitlement period current at consume time, so a period change in flight loses nothing.
func Consume(ctx context.Context, q db.Querier, userID, reservationID string) error {
	var periodStart time.Time
	if err := q.QueryRow(ctx, `SELECT period_start FROM entitlements WHERE user_id = $1 FOR UPDATE`, userID).Scan(&periodStart); err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `
		UPDATE quota_reservations SET status = 'consumed', consumed_at = now(), period_start = $3
		WHERE id = $1 AND user_id = $2 AND status = 'reserved'`, reservationID, userID, periodStart)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotReserved
	}
	return nil
}

// Release frees a reservation in transcribe permanent-failure finalize.
func Release(ctx context.Context, q db.Querier, reservationID string) error {
	tag, err := q.Exec(ctx, `
		UPDATE quota_reservations SET status = 'released', released_at = now()
		WHERE id = $1 AND status = 'reserved'`, reservationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotReserved
	}
	return nil
}

// ReleaseMeeting frees every in-flight quota reservation and generation slot of a meeting
// (meeting delete tx).
func ReleaseMeeting(ctx context.Context, q db.Querier, meetingID string) error {
	if _, err := q.Exec(ctx, `
		UPDATE quota_reservations SET status = 'released', released_at = now()
		WHERE meeting_id = $1 AND status = 'reserved'`, meetingID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `UPDATE ai_generation_slots SET status = 'released', released_at = now()
		WHERE meeting_id = $1 AND status = 'reserved'`, meetingID)
	return err
}

// ReleaseUser frees every in-flight quota reservation and generation slot of a user (account
// delete tx).
func ReleaseUser(ctx context.Context, q db.Querier, userID string) error {
	if _, err := q.Exec(ctx, `
		UPDATE quota_reservations SET status = 'released', released_at = now()
		WHERE user_id = $1 AND status = 'reserved'`, userID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `UPDATE ai_generation_slots SET status = 'released', released_at = now()
		WHERE user_id = $1 AND status = 'reserved'`, userID)
	return err
}

// slotWindowSQL: the user's counted generation slots (reserved or consumed, reserved_at
// inside the window; $2 = limit, $3 = window in ms). next = when one more slot frees: the
// (counted-limit+1)-th oldest leaves the window (the oldest when counted = limit).
const slotWindowSQL = `
	SELECT count(*) FILTER (WHERE status = 'consumed')::int,
	       count(*) FILTER (WHERE status = 'reserved')::int,
	       (array_agg(reserved_at ORDER BY reserved_at))[GREATEST(count(*)::int - $2 + 1, 1)] + $3 * interval '1 millisecond'
	FROM ai_generation_slots
	WHERE user_id = $1 AND status IN ('reserved', 'consumed') AND reserved_at > now() - $3 * interval '1 millisecond'`

type SlotStatus struct {
	Limit, Used, Reserved, Remaining int
	NextSlotAt                       *time.Time
}

func (s *Service) slotStatus(ctx context.Context, q db.Querier, userID string) (SlotStatus, error) {
	st := SlotStatus{Limit: s.cfg.AIGenerationLimit}
	var next *time.Time
	if err := q.QueryRow(ctx, slotWindowSQL, userID, st.Limit, s.cfg.AIGenerationWindow.Milliseconds()).
		Scan(&st.Used, &st.Reserved, &next); err != nil {
		return SlotStatus{}, err
	}
	st.Remaining = max(0, st.Limit-st.Used-st.Reserved)
	if st.Remaining == 0 {
		st.NextSlotAt = next
	}
	return st, nil
}

func LimitError(st SlotStatus) error {
	return apperr.WithDetails(apperr.AIGenerationLimit, map[string]any{
		"limit": st.Limit, "remaining": 0, "next_slot_at": jsonx.TimePtr(st.NextSlotAt)})
}

// The entitlement row lock serializes a user's concurrent reservations; insert with ReserveSlot in the same tx.
func (s *Service) CheckSlot(ctx context.Context, q db.Querier, userID string) error {
	if _, err := q.Exec(ctx, `SELECT 1 FROM entitlements WHERE user_id = $1 FOR UPDATE`, userID); err != nil {
		return err
	}
	st, err := s.slotStatus(ctx, q, userID)
	if err != nil {
		return err
	}
	if st.Remaining == 0 {
		return LimitError(st)
	}
	return nil
}

type Slot struct {
	UserID, MeetingID, JobID, TranscribeJobID string
}

func ReserveSlot(ctx context.Context, q db.Querier, sl Slot) error {
	_, err := q.Exec(ctx, `INSERT INTO ai_generation_slots (user_id, meeting_id, job_id, transcribe_job_id)
		VALUES ($1, $2, $3, $4)`, sl.UserID, sl.MeetingID, nullable(sl.JobID), nullable(sl.TranscribeJobID))
	return err
}

func AttachSlot(ctx context.Context, q db.Querier, transcribeJobID, notesJobID string) error {
	_, err := q.Exec(ctx, `UPDATE ai_generation_slots SET job_id = $2
		WHERE transcribe_job_id = $1 AND status = 'reserved'`, transcribeJobID, notesJobID)
	return err
}

// The status = 'reserved' guard keeps a rerun from consuming twice.
func ConsumeSlot(ctx context.Context, q db.Querier, jobID string) error {
	_, err := q.Exec(ctx, `UPDATE ai_generation_slots SET status = 'consumed', consumed_at = now()
		WHERE job_id = $1 AND status = 'reserved'`, jobID)
	return err
}

func ReleaseSlot(ctx context.Context, q db.Querier, jobID string) error {
	_, err := q.Exec(ctx, `UPDATE ai_generation_slots SET status = 'released', released_at = now()
		WHERE (job_id = $1 OR transcribe_job_id = $1) AND status = 'reserved'`, jobID)
	return err
}

type Usage struct {
	Entitlement      EntitlementView `json:"entitlement"`
	ConsumedSeconds  int64           `json:"consumed_seconds"`
	ReservedSeconds  int64           `json:"reserved_seconds"`
	RemainingSeconds int64           `json:"remaining_seconds"`
	AIGenerations    AIGenerations   `json:"ai_generations"`
}

type EntitlementView struct {
	Plan         string     `json:"plan"`
	Status       string     `json:"status"`
	PeriodStart  jsonx.Time `json:"period_start"`
	PeriodEnd    jsonx.Time `json:"period_end"`
	QuotaSeconds int64      `json:"quota_seconds"`
}

type AIGenerations struct {
	Limit       int         `json:"limit"`
	WindowHours int         `json:"window_hours"`
	Used        int         `json:"used"`
	Reserved    int         `json:"reserved"`
	Remaining   int         `json:"remaining"`
	NextSlotAt  *jsonx.Time `json:"next_slot_at"`
}

// Get is GET /v1/usage, read in one statement (one snapshot).
func (s *Service) Get(ctx context.Context, userID string) (Usage, error) {
	var u Usage
	var e Entitlement
	st := SlotStatus{Limit: s.cfg.AIGenerationLimit}
	var next *time.Time
	err := s.db.QueryRow(ctx, `
		WITH e AS (
			SELECT plan, `+effectiveStatus+` AS status, period_start, period_end, quota_seconds
			FROM entitlements WHERE user_id = $1
		), r AS (
			SELECT COALESCE(sum(seconds) FILTER (WHERE status = 'consumed' AND period_start = (SELECT period_start FROM e)), 0)::bigint AS consumed,
			       COALESCE(sum(seconds) FILTER (WHERE status = 'reserved'), 0)::bigint AS reserved
			FROM quota_reservations WHERE user_id = $1 AND status IN ('consumed', 'reserved')
		), a AS (`+slotWindowSQL+`)
		SELECT e.plan, e.status, e.period_start, e.period_end, e.quota_seconds, r.consumed, r.reserved, a.*
		FROM e, r, a`, userID, st.Limit, s.cfg.AIGenerationWindow.Milliseconds()).
		Scan(&e.Plan, &e.Status, &e.PeriodStart, &e.PeriodEnd, &e.QuotaSeconds,
			&u.ConsumedSeconds, &u.ReservedSeconds, &st.Used, &st.Reserved, &next)
	if err != nil {
		return Usage{}, err
	}
	u.Entitlement = EntitlementView{Plan: e.Plan, Status: e.Status, PeriodStart: jsonx.NewTime(e.PeriodStart),
		PeriodEnd: jsonx.NewTime(e.PeriodEnd), QuotaSeconds: e.QuotaSeconds}
	u.RemainingSeconds = max(0, e.QuotaSeconds-u.ConsumedSeconds-u.ReservedSeconds)
	st.Remaining = max(0, st.Limit-st.Used-st.Reserved)
	u.AIGenerations = AIGenerations{Limit: st.Limit, WindowHours: int(s.cfg.AIGenerationWindow / time.Hour),
		Used: st.Used, Reserved: st.Reserved, Remaining: st.Remaining}
	if st.Remaining == 0 {
		u.AIGenerations.NextSlotAt = jsonx.TimePtr(next)
	}
	return u, nil
}

// AICall is one provider call (backend.md §5.1 step 3, §5.2 step 2). Empty strings are NULL
// where the column is nullable; ErrorCategory "" means outcome ok.
type AICall struct {
	UserID        string
	MeetingID     string
	JobID         string
	JobType       string
	Attempt       int
	Capability    string
	Provider      string
	Model         string
	PromptVersion string
	InputTokens   *int
	OutputTokens  *int
	AudioMS       *int64
	LatencyMS     int64
	ErrorCategory string
	CorrelationID string
}

// CostMicros prices a call from the configured prices (backend.md §6.1).
func (s *Service) CostMicros(c AICall) int64 {
	if c.Capability == CapabilitySTT {
		if c.AudioMS == nil {
			return 0
		}
		return int64(math.Round(float64(*c.AudioMS) / 60000 * s.cfg.STTPricePerMin * 1e6))
	}
	var in, out float64
	if c.InputTokens != nil {
		in = float64(*c.InputTokens)
	}
	if c.OutputTokens != nil {
		out = float64(*c.OutputTokens)
	}
	return int64(math.Round(in*s.cfg.LLMPriceInPerMTok + out*s.cfg.LLMPriceOutPerMTok))
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RecordAICall inserts an ai_calls row (own short tx, outside finalize) and logs ai.call.
func (s *Service) RecordAICall(ctx context.Context, q db.Querier, c AICall) error {
	outcome := "ok"
	if c.ErrorCategory != "" {
		outcome = "error"
	}
	corr := c.CorrelationID
	if corr == "" {
		corr = obs.CorrelationID(ctx)
	}
	cost := s.CostMicros(c)
	if _, err := q.Exec(ctx, `
		INSERT INTO ai_calls (user_id, meeting_id, job_id, job_type, attempt, capability, provider, model,
		                      prompt_version, input_tokens, output_tokens, audio_ms, latency_ms, cost_usd_micros,
		                      outcome, error_category, correlation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		c.UserID, nullable(c.MeetingID), nullable(c.JobID), c.JobType, c.Attempt, c.Capability, c.Provider, c.Model,
		nullable(c.PromptVersion), c.InputTokens, c.OutputTokens, c.AudioMS, c.LatencyMS, cost,
		outcome, nullable(c.ErrorCategory), corr); err != nil {
		return err
	}
	attrs := []any{"meeting_id", c.MeetingID, "job_id", c.JobID, "job_type", c.JobType, "attempt", c.Attempt,
		"provider", c.Provider, "model", c.Model, "latency_ms", c.LatencyMS, "cost_usd", float64(cost) / 1e6, "status", outcome}
	if c.PromptVersion != "" {
		attrs = append(attrs, "prompt_version", c.PromptVersion)
	}
	if c.InputTokens != nil {
		attrs = append(attrs, "input_tokens", *c.InputTokens)
	}
	if c.OutputTokens != nil {
		attrs = append(attrs, "output_tokens", *c.OutputTokens)
	}
	if c.AudioMS != nil {
		attrs = append(attrs, "audio_ms", *c.AudioMS)
	}
	if c.ErrorCategory != "" {
		attrs = append(attrs, "error_category", c.ErrorCategory)
	}
	s.log.InfoContext(ctx, "ai.call", attrs...)
	return nil
}
