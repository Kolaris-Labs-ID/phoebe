package users_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/auth"
	"phoebe/internal/config"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
	"phoebe/internal/testutil"
	"phoebe/internal/usage"
	"phoebe/internal/users"
)

var ctx = context.Background()

type revokeServer struct {
	mu     sync.Mutex
	status int
	tokens []string
}

func newApple(t *testing.T, cfg *config.Config) (*auth.AppleClient, *revokeServer) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	rs := &revokeServer{status: 200}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		rs.mu.Lock()
		defer rs.mu.Unlock()
		rs.tokens = append(rs.tokens, r.PostForm.Get("token"))
		w.WriteHeader(rs.status)
	}))
	t.Cleanup(srv.Close)
	cfg.AppleTeamID, cfg.AppleKeyID = "TEAM123456", "KEY1234567"
	cfg.ApplePrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	cfg.TokenEncKey = bytes.Repeat([]byte{7}, 32)
	cfg.AppleRevokeURL = srv.URL + "/auth/revoke"
	c, err := auth.NewAppleClient(cfg)
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	return c, rs
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func runner(pool *pgxpool.Pool, svc *users.Service) *jobs.Runner {
	r := jobs.NewRunner(pool, testutil.DiscardLogger(), jobs.RunnerConfig{WorkerID: "test", Lease: time.Minute, RetryBase: time.Second})
	r.Register(jobs.TypePurgeAccount, svc.PurgeAccountSpec())
	return r
}

func TestDeleteAccount(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	store := testutil.Storage(t)
	svc := users.New(pool, cfg, store, nil, testutil.DiscardLogger())
	authSvc := auth.NewService(pool, cfg, testutil.DiscardLogger(), nil)
	u := testutil.CreateUser(t, pool, 3600)
	other := testutil.CreateUser(t, pool, 3600)
	token2 := testutil.CreateSession(t, pool, u.ID)
	m1 := testutil.CreateMeeting(t, pool, u.ID)
	m2 := testutil.CreateMeeting(t, pool, u.ID)
	om := testutil.CreateMeeting(t, pool, other.ID)

	exec(t, pool, `INSERT INTO upload_sessions (meeting_id, user_id, declared_content_type, size_bytes, sha256, declared_duration_ms,
		storage_key, expires_at) VALUES ($1, $2, 'audio/wav', 10, $3, 5000, 'k', now() + interval '1 day')`, m2, u.ID, strings.Repeat("a", 64))
	exec(t, pool, `INSERT INTO upload_sessions (meeting_id, user_id, status, declared_content_type, size_bytes, sha256, declared_duration_ms,
		storage_key, expires_at, received_bytes, completed_at) VALUES ($1, $2, 'completed', 'audio/wav', 10, $3, 5000, 'k', now(), 10, now())`,
		m1, u.ID, strings.Repeat("b", 64))
	var sid string
	_ = pool.QueryRow(ctx, `SELECT id FROM upload_sessions WHERE meeting_id = $1`, m1).Scan(&sid)
	assetID := ids.New()
	exec(t, pool, `INSERT INTO audio_assets (id, meeting_id, upload_session_id, content_type, size_bytes, sha256, duration_ms, storage_key)
		VALUES ($1, $2, $3, 'audio/wav', 10, $4, 5000, 'k')`, assetID, m1, sid, strings.Repeat("b", 64))
	us := usage.New(pool, cfg, testutil.DiscardLogger())
	resID := ids.New()
	var jobID string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q, err := us.CheckQuota(ctx, tx, u.ID, 5000)
		if err != nil {
			return err
		}
		j, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Type: jobs.TypeTranscribe, UserID: u.ID, MeetingID: m1, Trigger: jobs.TriggerUser,
			IdempotencyKey: ids.New(), RequestFingerprint: strings.Repeat("c", 64), MaxAttempts: 3, CorrelationID: ids.New()})
		if err != nil {
			return err
		}
		jobID = j.ID
		return usage.Reserve(ctx, tx, q, usage.Reservation{ID: resID, MeetingID: m1, AudioAssetID: assetID, JobID: j.ID})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Claim(ctx, pool, "w/0", time.Minute); err != nil {
		t.Fatal(err)
	}

	at, err := svc.Delete(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(at); d < cfg.PurgeDelay-time.Minute || d > cfg.PurgeDelay+time.Minute {
		t.Fatalf("purge_scheduled_at in %v", d)
	}
	checks := map[string]int{
		`SELECT count(*) FROM users WHERE id = $1 AND deleted_at IS NOT NULL`:                                1,
		`SELECT count(*) FROM meetings WHERE user_id = $1 AND deleted_at IS NULL`:                            0,
		`SELECT count(*) FROM deleted_meetings WHERE user_id = $1`:                                           2,
		`SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`:                            0,
		`SELECT count(*) FROM upload_sessions WHERE user_id = $1 AND status IN ('open', 'verifying')`:        0,
		`SELECT count(*) FROM jobs WHERE user_id = $1 AND status IN ('queued', 'running')`:                   0,
		`SELECT count(*) FROM quota_reservations WHERE user_id = $1 AND status = 'reserved'`:                 0,
		`SELECT count(*) FROM jobs WHERE idempotency_key = 'sys:purge_account:' || $1 AND status = 'queued'`: 1,
	}
	for sql, want := range checks {
		if got := count(t, pool, sql, u.ID); got != want {
			t.Errorf("%s = %d, want %d", sql, got, want)
		}
	}
	if count(t, pool, `SELECT count(*) FROM jobs WHERE id = $1 AND status = 'canceled'`, jobID) != 1 {
		t.Fatal("running job not canceled")
	}
	for _, tok := range []string{u.Token, token2} {
		if _, _, err := authSvc.ResolveSession(ctx, tok); !apperr.Is(err, apperr.Unauthenticated) {
			t.Fatal("old session still valid:", err)
		}
	}
	if count(t, pool, `SELECT count(*) FROM meetings WHERE id = $1 AND deleted_at IS NULL`, om) != 1 {
		t.Fatal("other user's meeting touched")
	}
	if _, _, err := authSvc.ResolveSession(ctx, other.Token); err != nil {
		t.Fatal("other user's session revoked")
	}
	if _, err := svc.Delete(ctx, u.ID); !apperr.Is(err, apperr.Unauthenticated) {
		t.Fatal("repeat delete:", err)
	}
}

func TestPurgeAccountRevokesApple(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	store := testutil.Storage(t)
	apple, rs := newApple(t, cfg)
	svc := users.New(pool, cfg, store, apple, testutil.DiscardLogger())
	ms := meetings.New(pool, cfg, store, testutil.DiscardLogger())
	r := runner(pool, svc)

	u := testutil.CreateUser(t, pool, 60)
	other := testutil.CreateUser(t, pool, 60)
	exec(t, pool, `UPDATE users SET apple_sub = 'apple-sub-1', apple_refresh_token_enc = $2 WHERE id = $1`, u.ID, apple.Seal("rt-secret-1"))
	exec(t, pool, `INSERT INTO trial_grants (subject_hmac) VALUES ($1)`, usage.SubjectHMAC(cfg.SubjectPepper, "apple", "apple-sub-1"))
	mid := testutil.CreateMeeting(t, pool, u.ID)
	for _, k := range []string{"u/" + u.ID + "/m/" + mid + "/audio/a.wav", "u/" + other.ID + "/m/x/audio/b.wav"} {
		if _, err := store.Put(ctx, k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	// an earlier deleted meeting's purge job must not conflict with the account purge
	if _, err := ms.Delete(ctx, u.ID, testutil.CreateMeeting(t, pool, u.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Delete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `UPDATE jobs SET run_after = now() WHERE type = 'purge_account'`)

	rs.status = 503
	if ran, err := r.RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	var status, cat, detail string
	var attempts int
	_ = pool.QueryRow(ctx, `SELECT status, attempts, last_error_category, last_error_detail FROM jobs WHERE type = 'purge_account'`).
		Scan(&status, &attempts, &cat, &detail)
	if status != "queued" || attempts != 1 || cat != jobs.CatProviderUnavailable || detail != "apple revoke http 503" {
		t.Fatalf("after 5xx: %s %d %s %q", status, attempts, cat, detail)
	}
	if count(t, pool, `SELECT count(*) FROM users WHERE id = $1`, u.ID) != 1 {
		t.Fatal("user purged before revoke succeeded")
	}

	rs.status = 200
	exec(t, pool, `UPDATE jobs SET run_after = now() WHERE type = 'purge_account'`)
	if ran, err := r.RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	rs.mu.Lock()
	if len(rs.tokens) != 2 || rs.tokens[0] != "rt-secret-1" || rs.tokens[1] != "rt-secret-1" {
		t.Fatalf("revoke tokens %v", rs.tokens)
	}
	rs.mu.Unlock()
	_ = pool.QueryRow(ctx, `SELECT status FROM jobs WHERE type = 'purge_account'`).Scan(&status)
	if status != "succeeded" {
		t.Fatal(status)
	}
	for sql, want := range map[string]int{
		`SELECT count(*) FROM users WHERE id = $1`:                 0,
		`SELECT count(*) FROM meetings WHERE user_id = $1`:         0,
		`SELECT count(*) FROM deleted_meetings WHERE user_id = $1`: 0,
		`SELECT count(*) FROM entitlements WHERE user_id = $1`:     0,
		`SELECT count(*) FROM sessions WHERE user_id = $1`:         0,
	} {
		if got := count(t, pool, sql, u.ID); got != want {
			t.Errorf("%s = %d", sql, got)
		}
	}
	if count(t, pool, `SELECT count(*) FROM trial_grants`) != 1 {
		t.Fatal("trial grant removed by purge")
	}
	if _, ok, _ := store.Stat(ctx, "u/"+u.ID+"/m/"+mid+"/audio/a.wav"); ok {
		t.Fatal("user files kept")
	}
	if _, ok, _ := store.Stat(ctx, "u/"+other.ID+"/m/x/audio/b.wav"); !ok {
		t.Fatal("other user's files removed")
	}
	if count(t, pool, `SELECT count(*) FROM users WHERE id = $1`, other.ID) != 1 {
		t.Fatal("other user purged")
	}
}

func TestPurgeAccountWithoutAppleOrToken(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	svc := users.New(pool, cfg, testutil.Storage(t), nil, testutil.DiscardLogger())
	u := testutil.CreateUser(t, pool, 0)
	exec(t, pool, `UPDATE users SET apple_refresh_token_enc = '\x00' WHERE id = $1`, u.ID)
	if _, err := svc.Delete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `UPDATE jobs SET run_after = now()`)
	if ran, err := runner(pool, svc).RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if count(t, pool, `SELECT count(*) FROM users`) != 0 || count(t, pool, `SELECT count(*) FROM jobs WHERE status = 'succeeded'`) != 1 {
		t.Fatal("purge without Apple credentials did not complete")
	}
}

func TestTrialAbuseAfterAccountDelete(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	svc := users.New(pool, cfg, testutil.Storage(t), nil, testutil.DiscardLogger())
	authSvc := auth.NewService(pool, cfg, testutil.DiscardLogger(), nil)
	quota := func(uid string) int64 {
		var q int64
		_ = pool.QueryRow(ctx, `SELECT quota_seconds FROM entitlements WHERE user_id = $1`, uid).Scan(&q)
		return q
	}
	first, err := authSvc.SignInDev(ctx, "alice")
	if err != nil || quota(first.User.ID) != cfg.TrialQuotaMinutes*60 {
		t.Fatal(err)
	}
	if _, err := svc.Delete(ctx, first.User.ID); err != nil {
		t.Fatal(err)
	}
	second, err := authSvc.SignInDev(ctx, "alice")
	if err != nil || !second.Created || second.User.ID == first.User.ID || quota(second.User.ID) != 0 {
		t.Fatalf("re-sign-in: %+v %v quota=%d", second, err, quota(second.User.ID))
	}
	if _, _, err := authSvc.ResolveSession(ctx, first.Token); !apperr.Is(err, apperr.Unauthenticated) {
		t.Fatal(err)
	}

	exec(t, pool, `UPDATE jobs SET run_after = now()`)
	if ran, err := runner(pool, svc).RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if _, err := svc.Delete(ctx, second.User.ID); err != nil {
		t.Fatal(err)
	}
	third, err := authSvc.SignInDev(ctx, "alice")
	if err != nil || quota(third.User.ID) != 0 {
		t.Fatal("trial regranted after purge", err)
	}
}
