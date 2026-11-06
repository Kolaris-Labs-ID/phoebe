package usage_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/httpapi"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
	"phoebe/internal/testutil"
	"phoebe/internal/usage"
)

var ctx = context.Background()

func newService(t *testing.T) (*usage.Service, *pgxpool.Pool) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	return usage.New(pool, cfg, testutil.Logger(t)), pool
}

func createAsset(t *testing.T, q db.Querier, userID, meetingID string, durationMS int64) string {
	t.Helper()
	sha := testutil.SHA256Hex([]byte(meetingID))
	var sess, asset string
	if err := q.QueryRow(ctx, `
		INSERT INTO upload_sessions (meeting_id, user_id, status, declared_content_type, size_bytes, sha256,
		                             declared_duration_ms, received_bytes, storage_key, expires_at, completed_at)
		VALUES ($1, $2, 'completed', 'audio/wav', 100, $3, $4, 100, 'k', now() + interval '1 day', now())
		RETURNING id`, meetingID, userID, sha, durationMS).Scan(&sess); err != nil {
		t.Fatal(err)
	}
	if err := q.QueryRow(ctx, `
		INSERT INTO audio_assets (meeting_id, upload_session_id, content_type, size_bytes, sha256, duration_ms, storage_key)
		VALUES ($1, $2, 'audio/wav', 100, $3, $4, 'k') RETURNING id`, meetingID, sess, sha, durationMS).Scan(&asset); err != nil {
		t.Fatal(err)
	}
	return asset
}

// startTranscription mirrors §8.1 steps 4–5: entitlement check, job insert, reservation insert.
func startTranscription(t *testing.T, s *usage.Service, pool *pgxpool.Pool, userID, meetingID, assetID string, durationMS int64) (reservationID, jobID string, err error) {
	t.Helper()
	reservationID = ids.New()
	err = db.InTx(ctx, pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		quote, err := s.CheckQuota(ctx, tx, userID, durationMS)
		if err != nil {
			return err
		}
		j, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Type: jobs.TypeTranscribe, UserID: userID, MeetingID: meetingID,
			Trigger: jobs.TriggerUser, IdempotencyKey: ids.New(), RequestFingerprint: testutil.SHA256Hex([]byte("x")),
			Payload: map[string]any{"reservation_id": reservationID}, MaxAttempts: 3, CorrelationID: ids.New()})
		if err != nil {
			return err
		}
		jobID = j.ID
		return usage.Reserve(ctx, tx, quote, usage.Reservation{ID: reservationID, MeetingID: meetingID, AudioAssetID: assetID, JobID: j.ID})
	})
	return
}

func mustGet(t *testing.T, s *usage.Service, userID string) usage.Usage {
	t.Helper()
	u, err := s.Get(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func checkLedger(t *testing.T, u usage.Usage, consumed, reserved, remaining int64) {
	t.Helper()
	if u.ConsumedSeconds != consumed || u.ReservedSeconds != reserved || u.RemainingSeconds != remaining {
		t.Fatalf("ledger consumed/reserved/remaining = %d/%d/%d, want %d/%d/%d",
			u.ConsumedSeconds, u.ReservedSeconds, u.RemainingSeconds, consumed, reserved, remaining)
	}
}

func TestSeconds(t *testing.T) {
	for ms, want := range map[int64]int64{1000: 1, 1001: 2, 1999: 2, 7_200_000: 7200, 3_600_500: 3601} {
		if got := usage.Seconds(ms); got != want {
			t.Errorf("Seconds(%d) = %d, want %d", ms, got, want)
		}
	}
}

func TestTrialGrantConfigurableAndOncePerSubject(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	cfg.TrialQuotaMinutes = 7
	cfg.TrialPeriodDays = 3
	s := usage.New(pool, cfg, testutil.DiscardLogger())

	mkUser := func(sub string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (dev_sub) VALUES ($1) RETURNING id`, sub).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	h := usage.SubjectHMAC(cfg.SubjectPepper, "apple", "sub-1")
	if len(h) != 32 || string(h) == string(usage.SubjectHMAC(cfg.SubjectPepper, "dev", "sub-1")) {
		t.Fatal("subject hmac must be 32 bytes and namespaced by kind")
	}

	u1 := mkUser("a1")
	granted, err := s.CreateEntitlement(ctx, pool, u1, h)
	if err != nil || !granted {
		t.Fatalf("first grant: %v %v", granted, err)
	}
	got := mustGet(t, s, u1)
	if got.Entitlement.Plan != "trial" || got.Entitlement.Status != usage.StatusActive || got.Entitlement.QuotaSeconds != 7*60 {
		t.Fatalf("entitlement %+v", got.Entitlement)
	}
	if d := got.Entitlement.PeriodEnd.Sub(got.Entitlement.PeriodStart.Time); d != 72*time.Hour {
		t.Fatalf("period length %v", d)
	}
	if d := time.Since(got.Entitlement.PeriodStart.Time); d < -time.Minute || d > time.Minute {
		t.Fatalf("trial period must start at activation (entitlement creation), started %v ago", d)
	}
	checkLedger(t, got, 0, 0, 7*60)

	// Account deleted + purged, same identity signs in again: no second trial.
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, u1); err != nil {
		t.Fatal(err)
	}
	u2 := mkUser("a2")
	granted, err = s.CreateEntitlement(ctx, pool, u2, h)
	if err != nil || granted {
		t.Fatalf("second grant: %v %v", granted, err)
	}
	got = mustGet(t, s, u2)
	if got.Entitlement.QuotaSeconds != 0 || got.Entitlement.Plan != "trial" {
		t.Fatalf("re-sign-in entitlement %+v", got.Entitlement)
	}
}

func TestReserveConsumeRelease(t *testing.T) {
	s, pool := newService(t)
	u := testutil.CreateUser(t, pool, 100)

	m1 := testutil.CreateMeeting(t, pool, u.ID)
	a1 := createAsset(t, pool, u.ID, m1, 60_001)
	r1, _, err := startTranscription(t, s, pool, u.ID, m1, a1, 60_001)
	if err != nil {
		t.Fatal(err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 0, 61, 39)

	m2 := testutil.CreateMeeting(t, pool, u.ID)
	a2 := createAsset(t, pool, u.ID, m2, 40_000)
	_, _, err = startTranscription(t, s, pool, u.ID, m2, a2, 40_000)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.QuotaExceeded || ae.Details["required_seconds"] != int64(40) || ae.Details["remaining_seconds"] != int64(39) {
		t.Fatalf("want quota_exceeded {40, 39}, got %v %+v", err, ae)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM jobs WHERE meeting_id = $1) + (SELECT count(*) FROM quota_reservations WHERE meeting_id = $1)`, m2).Scan(&n)
	if n != 0 {
		t.Fatal("402 must leave no job and no reservation")
	}

	if err := db.InTx(ctx, pool, func(tx pgx.Tx) error { return usage.Consume(ctx, tx, u.ID, r1) }); err != nil {
		t.Fatal(err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 61, 0, 39)
	if err := usage.Consume(ctx, pool, u.ID, r1); !errors.Is(err, usage.ErrNotReserved) {
		t.Fatalf("second consume: %v", err)
	}
	if err := usage.Release(ctx, pool, r1); !errors.Is(err, usage.ErrNotReserved) {
		t.Fatalf("release after consume: %v", err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 61, 0, 39)

	// The same audio can never be charged twice, even after its job finished.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'succeeded', result = '{}', finished_at = now() WHERE meeting_id = $1`, m1); err != nil {
		t.Fatal(err)
	}
	_, _, err = startTranscription(t, s, pool, u.ID, m1, a1, 1000)
	if !db.IsUniqueViolation(err, "quota_reservations_one_live_per_audio_key") {
		t.Fatalf("second charge for same audio: %v", err)
	}

	// Release on permanent failure frees the seconds; a new attempt may reserve again.
	r2, _, err := startTranscription(t, s, pool, u.ID, m2, a2, 39_000)
	if err != nil {
		t.Fatal(err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 61, 39, 0)
	if err := usage.Release(ctx, pool, r2); err != nil {
		t.Fatal(err)
	}
	if err := usage.Release(ctx, pool, r2); !errors.Is(err, usage.ErrNotReserved) {
		t.Fatalf("double release: %v", err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 61, 0, 39)
	if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'failed', finished_at = now() WHERE meeting_id = $1`, m2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := startTranscription(t, s, pool, u.ID, m2, a2, 39_000); err != nil {
		t.Fatalf("re-reserve after release: %v", err)
	}
}

func TestReleaseMeetingAndUser(t *testing.T) {
	s, pool := newService(t)
	u := testutil.CreateUser(t, pool, 1000)
	m1 := testutil.CreateMeeting(t, pool, u.ID)
	m2 := testutil.CreateMeeting(t, pool, u.ID)
	r1, _, err := startTranscription(t, s, pool, u.ID, m1, createAsset(t, pool, u.ID, m1, 10_000), 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := startTranscription(t, s, pool, u.ID, m2, createAsset(t, pool, u.ID, m2, 20_000), 20_000); err != nil {
		t.Fatal(err)
	}
	if err := usage.Consume(ctx, pool, u.ID, r1); err != nil {
		t.Fatal(err)
	}
	if err := usage.ReleaseMeeting(ctx, pool, m1); err != nil {
		t.Fatal(err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 10, 20, 970)
	if err := usage.ReleaseUser(ctx, pool, u.ID); err != nil {
		t.Fatal(err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 10, 0, 990)
}

func TestEntitlementInactiveOrExpired(t *testing.T) {
	s, pool := newService(t)
	for _, tc := range []struct{ name, sql, status string }{
		{"inactive", `UPDATE entitlements SET status = 'inactive' WHERE user_id = $1`, usage.StatusInactive},
		{"expired", `UPDATE entitlements SET period_start = now() - interval '31 days', period_end = now() - interval '1 day' WHERE user_id = $1`, usage.StatusExpired},
		{"not started", `UPDATE entitlements SET period_start = now() + interval '1 day', period_end = now() + interval '2 days' WHERE user_id = $1`, usage.StatusExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := testutil.CreateUser(t, pool, 1000)
			if _, err := pool.Exec(ctx, tc.sql, u.ID); err != nil {
				t.Fatal(err)
			}
			if got := mustGet(t, s, u.ID).Entitlement.Status; got != tc.status {
				t.Fatalf("status %s, want %s", got, tc.status)
			}
			m := testutil.CreateMeeting(t, pool, u.ID)
			_, _, err := startTranscription(t, s, pool, u.ID, m, createAsset(t, pool, u.ID, m, 5000), 5000)
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Code != apperr.EntitlementInactive || ae.Details["period_end"] == nil {
				t.Fatalf("want entitlement_inactive with period_end, got %v", err)
			}
			var n int
			_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM jobs WHERE meeting_id = $1) + (SELECT count(*) FROM quota_reservations WHERE meeting_id = $1)`, m).Scan(&n)
			if n != 0 {
				t.Fatal("402 must leave no job and no reservation")
			}
		})
	}
}

func TestPeriodChangeInFlight(t *testing.T) {
	s, pool := newService(t)
	u := testutil.CreateUser(t, pool, 300)

	mOld := testutil.CreateMeeting(t, pool, u.ID)
	rOld, _, err := startTranscription(t, s, pool, u.ID, mOld, createAsset(t, pool, u.ID, mOld, 50_000), 50_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := usage.Consume(ctx, pool, u.ID, rOld); err != nil {
		t.Fatal(err)
	}
	m := testutil.CreateMeeting(t, pool, u.ID)
	r, _, err := startTranscription(t, s, pool, u.ID, m, createAsset(t, pool, u.ID, m, 100_000), 100_000)
	if err != nil {
		t.Fatal(err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 50, 100, 150)

	// Admin moves the user to a new pilot period while the transcription is in flight.
	if _, err := pool.Exec(ctx, `UPDATE entitlements SET plan = 'pilot', quota_seconds = 600, period_start = now(),
		period_end = now() + interval '30 days', updated_at = now() WHERE user_id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, s, u.ID)
	if got.Entitlement.Plan != "pilot" {
		t.Fatalf("plan %s", got.Entitlement.Plan)
	}
	// Old-period consumption no longer counts; the in-flight reservation still does.
	checkLedger(t, got, 0, 100, 500)

	// A new reservation in the new period also sees the old-period reservation.
	m2 := testutil.CreateMeeting(t, pool, u.ID)
	_, _, err = startTranscription(t, s, pool, u.ID, m2, createAsset(t, pool, u.ID, m2, 501_000), 501_000)
	if !apperr.Is(err, apperr.QuotaExceeded) {
		t.Fatalf("want quota_exceeded counting the in-flight reservation, got %v", err)
	}

	if err := db.InTx(ctx, pool, func(tx pgx.Tx) error { return usage.Consume(ctx, tx, u.ID, r) }); err != nil {
		t.Fatal(err)
	}
	checkLedger(t, mustGet(t, s, u.ID), 100, 0, 500)
	var same bool
	if err := pool.QueryRow(ctx, `SELECT q.period_start = e.period_start FROM quota_reservations q
		JOIN entitlements e ON e.user_id = q.user_id WHERE q.id = $1`, r).Scan(&same); err != nil || !same {
		t.Fatalf("consumed reservation must carry the new period_start: %v %v", same, err)
	}
}

func insertSlot(t *testing.T, q db.Querier, userID, status string, age time.Duration) {
	t.Helper()
	if _, err := q.Exec(ctx, `
		INSERT INTO ai_generation_slots (user_id, status, reserved_at, consumed_at, released_at)
		VALUES ($1, $2, now() - $3 * interval '1 millisecond',
		        CASE WHEN $2 = 'consumed' THEN now() END, CASE WHEN $2 = 'released' THEN now() END)`,
		userID, status, age.Milliseconds()); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationSlotWindow(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	cfg.AIGenerationLimit = 3
	s := usage.New(pool, cfg, testutil.DiscardLogger())
	u := testutil.CreateUser(t, pool, 0)

	insertSlot(t, pool, u.ID, "consumed", 25*time.Hour) // outside the window
	insertSlot(t, pool, u.ID, "released", time.Hour)    // released: not counted
	insertSlot(t, pool, u.ID, "consumed", 3*time.Hour)
	insertSlot(t, pool, u.ID, "reserved", 2*time.Hour)

	got := mustGet(t, s, u.ID).AIGenerations
	if got.Limit != 3 || got.WindowHours != 24 || got.Used != 1 || got.Reserved != 1 || got.Remaining != 1 || got.NextSlotAt != nil {
		t.Fatalf("ai_generations %+v", got)
	}
	if err := s.CheckSlot(ctx, pool, u.ID); err != nil {
		t.Fatalf("under limit: %v", err)
	}

	insertSlot(t, pool, u.ID, "consumed", time.Hour)
	got = mustGet(t, s, u.ID).AIGenerations
	if got.Used != 2 || got.Reserved != 1 || got.Remaining != 0 || got.NextSlotAt == nil {
		t.Fatalf("ai_generations at limit %+v", got)
	}
	// Oldest counted (3 h ago) leaves the window in 21 h.
	if d := time.Until(got.NextSlotAt.Time); d < 21*time.Hour-time.Minute || d > 21*time.Hour+time.Minute {
		t.Fatalf("next_slot_at in %v, want ~21h", d)
	}
	err := s.CheckSlot(ctx, pool, u.ID)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.AIGenerationLimit || ae.Details["limit"] != 3 || ae.Details["remaining"] != 0 {
		t.Fatalf("want ai_generation_limit, got %v", err)
	}
	if at, ok := ae.Details["next_slot_at"].(*jsonx.Time); !ok || !at.Equal(got.NextSlotAt.Time) {
		t.Fatalf("next_slot_at %v", ae.Details)
	}

	// Window rollover: shift the oldest counted slot out of the window.
	if _, err := pool.Exec(ctx, `UPDATE ai_generation_slots SET reserved_at = reserved_at - interval '21 hours 1 second'
		WHERE user_id = $1 AND status = 'consumed' AND reserved_at < now() - interval '150 minutes' AND reserved_at > now() - interval '24 hours'`, u.ID); err != nil {
		t.Fatal(err)
	}
	got = mustGet(t, s, u.ID).AIGenerations
	if got.Used != 1 || got.Reserved != 1 || got.Remaining != 1 || got.NextSlotAt != nil {
		t.Fatalf("after rollover %+v", got)
	}
	if err := s.CheckSlot(ctx, pool, u.ID); err != nil {
		t.Fatal(err)
	}

	// A lowered limit below the counted slots: next_slot_at is when enough slots leave.
	cfg.AIGenerationLimit = 1
	got = mustGet(t, s, u.ID).AIGenerations
	if got.Remaining != 0 || got.NextSlotAt == nil {
		t.Fatalf("lowered limit %+v", got)
	}
	if d := time.Until(got.NextSlotAt.Time); d < 23*time.Hour-time.Minute || d > 23*time.Hour+time.Minute {
		t.Fatalf("next_slot_at in %v, want ~23h (second oldest)", d)
	}
}

func TestGenerationSlotRace(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	cfg.AIGenerationLimit = 4
	s := usage.New(pool, cfg, testutil.DiscardLogger())
	u := testutil.CreateUser(t, pool, 0)
	for range cfg.AIGenerationLimit - 1 {
		insertSlot(t, pool, u.ID, "consumed", time.Hour)
	}

	const n = 12
	meetings := make([]string, n)
	for i := range meetings {
		meetings[i] = testutil.CreateMeeting(t, pool, u.ID)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = db.InTx(ctx, pool, func(tx pgx.Tx) error {
				if err := db.LockMeeting(ctx, tx, meetings[i], u.ID); err != nil {
					return err
				}
				if err := s.CheckSlot(ctx, tx, u.ID); err != nil {
					return err
				}
				j, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Type: jobs.TypeGenerateMoM, UserID: u.ID, MeetingID: meetings[i],
					Trigger: jobs.TriggerUser, IdempotencyKey: ids.New(), RequestFingerprint: testutil.SHA256Hex([]byte("r")),
					MaxAttempts: 3, CorrelationID: ids.New()})
				if err != nil {
					return err
				}
				return usage.ReserveSlot(ctx, tx, usage.Slot{UserID: u.ID, MeetingID: meetings[i], JobID: j.ID})
			})
		})
	}
	close(start)
	wg.Wait()
	ok, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case apperr.Is(err, apperr.AIGenerationLimit):
			limited++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || limited != n-1 {
		t.Fatalf("ok=%d limited=%d, want exactly one success", ok, limited)
	}
	if got := mustGet(t, s, u.ID).AIGenerations; got.Used+got.Reserved != cfg.AIGenerationLimit || got.Remaining != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSlotTransitions(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	s := usage.New(pool, cfg, testutil.DiscardLogger())
	u := testutil.CreateUser(t, pool, 0)
	m := testutil.CreateMeeting(t, pool, u.ID)
	job := func(typ string) string {
		j, err := jobs.Enqueue(ctx, pool, jobs.NewJob{Type: typ, UserID: u.ID, MeetingID: m, Trigger: jobs.TriggerUser,
			IdempotencyKey: ids.New(), RequestFingerprint: testutil.SHA256Hex([]byte("s")), MaxAttempts: 3, CorrelationID: ids.New()})
		if err != nil {
			t.Fatal(err)
		}
		return j.ID
	}
	status := func(where string, arg string) string {
		var st string
		if err := pool.QueryRow(ctx, `SELECT status FROM ai_generation_slots WHERE `+where, arg).Scan(&st); err != nil {
			t.Fatal(where, err)
		}
		return st
	}
	tj := job(jobs.TypeTranscribe)
	if err := usage.ReserveSlot(ctx, pool, usage.Slot{UserID: u.ID, MeetingID: m, TranscribeJobID: tj}); err != nil {
		t.Fatal(err)
	}
	if err := usage.ReserveSlot(ctx, pool, usage.Slot{UserID: u.ID, MeetingID: m, TranscribeJobID: tj}); !db.IsUniqueViolation(err, "") {
		t.Fatal("second slot for the same job", err)
	}
	nj := job(jobs.TypeGenerateNotes)
	if err := usage.AttachSlot(ctx, pool, tj, nj); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := usage.ConsumeSlot(ctx, pool, nj); err != nil {
			t.Fatal(err)
		}
	}
	if err := usage.ReleaseSlot(ctx, pool, nj); err != nil {
		t.Fatal(err)
	}
	if status(`job_id = $1`, nj) != "consumed" || mustGet(t, s, u.ID).AIGenerations.Used != 1 {
		t.Fatal("consume once; release after consume is a no-op")
	}

	mj := job(jobs.TypeGenerateMoM)
	if err := usage.ReserveSlot(ctx, pool, usage.Slot{UserID: u.ID, MeetingID: m, JobID: mj}); err != nil {
		t.Fatal(err)
	}
	if err := usage.ReleaseSlot(ctx, pool, mj); err != nil {
		t.Fatal(err)
	}
	if status(`job_id = $1`, mj) != "released" {
		t.Fatal("release")
	}
	if got := mustGet(t, s, u.ID).AIGenerations; got.Used != 1 || got.Reserved != 0 || got.Remaining != cfg.AIGenerationLimit-1 {
		t.Fatalf("%+v", got)
	}

	m2 := testutil.CreateMeeting(t, pool, u.ID)
	if err := usage.ReserveSlot(ctx, pool, usage.Slot{UserID: u.ID, MeetingID: m2}); err != nil {
		t.Fatal(err)
	}
	if err := usage.ReleaseMeeting(ctx, pool, m2); err != nil {
		t.Fatal(err)
	}
	if status(`meeting_id = $1`, m2) != "released" {
		t.Fatal("release meeting")
	}
	if err := usage.ReserveSlot(ctx, pool, usage.Slot{UserID: u.ID, MeetingID: m2}); err != nil {
		t.Fatal(err)
	}
	if err := usage.ReleaseUser(ctx, pool, u.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, u.ID).AIGenerations; got.Reserved != 0 || got.Used != 1 {
		t.Fatalf("release user %+v", got)
	}
}

func TestRecordAICall(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	cfg.STTPricePerMin = 0.006
	cfg.LLMPriceInPerMTok = 0.27
	cfg.LLMPriceOutPerMTok = 1.10
	s := usage.New(pool, cfg, testutil.DiscardLogger())
	u := testutil.CreateUser(t, pool, 0)
	m := testutil.CreateMeeting(t, pool, u.ID)

	audio := int64(600_000)
	in, out := 10_000, 2_000
	corr := ids.New()
	if err := s.RecordAICall(ctx, pool, usage.AICall{UserID: u.ID, MeetingID: m, JobID: ids.New(), JobType: jobs.TypeTranscribe,
		Attempt: 1, Capability: usage.CapabilitySTT, Provider: "openai", Model: "whisper-1", AudioMS: &audio, LatencyMS: 1234,
		CorrelationID: corr}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAICall(ctx, pool, usage.AICall{UserID: u.ID, MeetingID: m, JobType: jobs.TypeGenerateNotes,
		Attempt: 2, Capability: usage.CapabilityLLM, Provider: "openaicompat", Model: "m", PromptVersion: "notes-v1",
		InputTokens: &in, OutputTokens: &out, LatencyMS: 50, ErrorCategory: "invalid_output", CorrelationID: corr}); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT capability, cost_usd_micros, outcome, COALESCE(error_category, ''), job_id IS NULL
		FROM ai_calls WHERE user_id = $1 ORDER BY capability DESC`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		cap     string
		cost    int64
		outcome string
		cat     string
		nullJob bool
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.cap, &x.cost, &x.outcome, &x.cat, &x.nullJob)
	})
	if err != nil {
		t.Fatal(err)
	}
	// STT: 10 min × $0.006 = 60 000 micros. LLM: 10 000 × 0.27 + 2 000 × 1.10 = 4 900 micros.
	want := []row{{"stt", 60_000, "ok", "", false}, {"llm", 4_900, "error", "invalid_output", true}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ai_calls %+v, want %+v", got, want)
	}
}

type fakeSessions map[string]string

func (f fakeSessions) ResolveSession(_ context.Context, token string) (string, string, error) {
	if id, ok := f[token]; ok {
		return id, ids.New(), nil
	}
	return "", "", apperr.New(apperr.Unauthenticated)
}

func TestGetUsageHTTP(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	s := usage.New(pool, cfg, testutil.DiscardLogger())
	u := testutil.CreateUser(t, pool, 7200)
	m := testutil.CreateMeeting(t, pool, u.ID)
	r, _, err := startTranscription(t, s, pool, u.ID, m, createAsset(t, pool, u.ID, m, 3_600_500), 3_600_500)
	if err != nil {
		t.Fatal(err)
	}
	if err := usage.Consume(ctx, pool, u.ID, r); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(httpapi.New(httpapi.Deps{Config: cfg, Log: testutil.DiscardLogger(), DB: pool,
		Sessions: fakeSessions{u.Token: u.ID}, Usage: s}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/usage", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}

	req.Header.Set("Authorization", "Bearer "+u.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	ent := body["entitlement"].(map[string]any)
	ai := body["ai_generations"].(map[string]any)
	if ent["plan"] != "trial" || ent["status"] != "active" || ent["quota_seconds"] != 7200.0 ||
		body["consumed_seconds"] != 3601.0 || body["reserved_seconds"] != 0.0 || body["remaining_seconds"] != 3599.0 ||
		ai["limit"] != 30.0 || ai["window_hours"] != 24.0 || ai["used"] != 0.0 || ai["reserved"] != 0.0 || ai["remaining"] != 30.0 {
		t.Fatalf("body %v", body)
	}
	if v, ok := ai["next_slot_at"]; !ok || v != nil {
		t.Fatalf("next_slot_at must be present and null: %v", ai)
	}
	if ps, _ := ent["period_start"].(string); len(ps) != len("2026-11-06T02:00:00.000Z") {
		t.Fatalf("period_start format %q", ps)
	}
}
