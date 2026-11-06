package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/obs"
	"phoebe/internal/testutil"
)

type env struct {
	pool  *pgxpool.Pool
	cfg   *config.Config
	jwks  *jwks
	apple *appleEndpoints
	svc   *Service
	logs  *bytes.Buffer
}

type lockedBuf struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func newEnv(t *testing.T, withApple bool) *env {
	t.Helper()
	e := &env{pool: testutil.DB(t), cfg: testutil.Config(t), jwks: newJWKS(t, "k1"), logs: &bytes.Buffer{}}
	e.cfg.AppleBundleID = testAud
	e.cfg.AppleJWKSURL = e.jwks.srv.URL
	var client *AppleClient
	if withApple {
		e.apple = newAppleEndpoints(t, e.cfg)
		c, err := NewAppleClient(e.cfg)
		if err != nil {
			t.Fatal(err)
		}
		client = c
	}
	e.svc = NewService(e.pool, e.cfg, obs.NewLogger(&lockedBuf{b: e.logs}, slog.LevelDebug), client)
	return e
}

func (e *env) signIn(t *testing.T, sub string, name, code *string) (*Result, error) {
	t.Helper()
	return e.svc.SignInApple(context.Background(), AppleSignIn{
		IdentityToken: appleToken(t, e.jwks, "k1", validClaims(sub, "raw-n")), Nonce: "raw-n",
		DisplayName: name, AuthorizationCode: code,
	})
}

func ptr(s string) *string { return &s }

func entitlementQuota(t *testing.T, e *env, uid string) int64 {
	t.Helper()
	var q int64
	if err := e.pool.QueryRow(context.Background(), `SELECT quota_seconds FROM entitlements WHERE user_id=$1`, uid).Scan(&q); err != nil {
		t.Fatal(err)
	}
	return q
}

func TestSignInAppleFirstAndRepeat(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()

	r, err := e.signIn(t, "001.alice", ptr("  Ari Rudiana "), ptr("code-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Created || len(r.AccessToken) != 43 || len(r.RefreshToken) != 43 || r.AccessToken == r.RefreshToken || r.User.DisplayName != "Ari Rudiana" ||
		r.User.Language != "id" || r.User.Timezone != "Asia/Jakarta" {
		t.Fatalf("first sign-in %+v %+v", r, r.User)
	}
	if q := entitlementQuota(t, e, r.User.ID); q != e.cfg.TrialQuotaMinutes*60 {
		t.Fatalf("trial quota %d", q)
	}
	var days float64
	_ = e.pool.QueryRow(ctx, `SELECT extract(epoch FROM period_end - period_start)/86400 FROM entitlements WHERE user_id=$1`, r.User.ID).Scan(&days)
	if int(days+0.5) != e.cfg.TrialPeriodDays {
		t.Fatalf("trial period %v days", days)
	}

	var hash, rhash, enc []byte
	if err := e.pool.QueryRow(ctx, `SELECT t.access_token_hash, t.refresh_token_hash, u.apple_refresh_token_enc
		FROM session_tokens t JOIN sessions s ON s.id=t.session_id JOIN users u ON u.id=s.user_id WHERE u.id=$1`,
		r.User.ID).Scan(&hash, &rhash, &enc); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash, hashToken(r.AccessToken)) || !bytes.Equal(rhash, hashToken(r.RefreshToken)) {
		t.Fatal("session must store sha256 of both tokens")
	}
	c, _ := NewAppleClient(e.cfg)
	if tok, err := c.Open(enc); err != nil || tok != "rt-secret-1" {
		t.Fatalf("stored refresh token: %q %v", tok, err)
	}

	uid, sid, err := e.svc.ResolveSession(ctx, r.AccessToken)
	if err != nil || uid != r.User.ID || sid == "" {
		t.Fatalf("resolve: %s %s %v", uid, sid, err)
	}

	e.apple.refreshToken = "rt-secret-2"
	r2, err := e.signIn(t, "001.alice", ptr("Other Name"), ptr("code-2"))
	if err != nil {
		t.Fatal(err)
	}
	if r2.Created || r2.User.ID != r.User.ID || r2.User.DisplayName != "Ari Rudiana" || r2.AccessToken == r.AccessToken {
		t.Fatalf("repeat sign-in %+v %+v", r2, r2.User)
	}
	_ = e.pool.QueryRow(ctx, `SELECT apple_refresh_token_enc FROM users WHERE id=$1`, r.User.ID).Scan(&enc)
	if tok, _ := c.Open(enc); tok != "rt-secret-2" {
		t.Fatalf("latest refresh token must win, got %q", tok)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM entitlements WHERE user_id=$1`, r.User.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("entitlements %d", n)
	}
}

func TestSignInAppleFillsEmptyName(t *testing.T) {
	e := newEnv(t, false)
	r, err := e.signIn(t, "001.bob", nil, nil)
	if err != nil || r.User.DisplayName != "" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = e.signIn(t, "001.bob", ptr("Bob"), nil)
	if err != nil || r.User.DisplayName != "Bob" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestSignInAppleExchangeFailureDoesNotFail(t *testing.T) {
	e := newEnv(t, true)
	e.apple.tokenStatus = 500
	r, err := e.signIn(t, "001.carol", nil, ptr("code-secret-xyz"))
	if err != nil || !r.Created {
		t.Fatalf("%+v %v", r, err)
	}
	var enc []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT apple_refresh_token_enc FROM users WHERE id=$1`, r.User.ID).Scan(&enc)
	if enc != nil {
		t.Fatal("no refresh token expected")
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `"auth.apple_exchange"`) || !strings.Contains(logs, `"outcome":"error"`) {
		t.Fatalf("missing exchange log: %s", logs)
	}
	if strings.Contains(logs, "code-secret-xyz") {
		t.Fatal("authorization code logged")
	}
}

func TestSignInAppleWithoutAppleConfigSkipsExchange(t *testing.T) {
	e := newEnv(t, false)
	if _, err := e.signIn(t, "001.dave", nil, ptr("code")); err != nil {
		t.Fatal(err)
	}
}

func TestSignInAppleRejects(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	good := appleToken(t, e.jwks, "k1", validClaims("001.x", "raw-n"))
	c := validClaims("001.x", "raw-n")
	c["aud"] = "com.other"
	wrongAud := appleToken(t, e.jwks, "k1", c)

	for name, in := range map[string]AppleSignIn{
		"bad nonce": {IdentityToken: good, Nonce: "other"},
		"wrong aud": {IdentityToken: wrongAud, Nonce: "raw-n"},
		"garbage":   {IdentityToken: "abc", Nonce: "raw-n"},
	} {
		if _, err := e.svc.SignInApple(ctx, in); !apperr.Is(err, apperr.Unauthenticated) {
			t.Errorf("%s: want unauthenticated, got %v", name, err)
		}
	}
	if _, err := e.svc.SignInApple(ctx, AppleSignIn{}); !apperr.Is(err, apperr.ValidationFailed) {
		t.Errorf("empty: want validation_failed, got %v", err)
	}
	if _, err := e.svc.SignInApple(ctx, AppleSignIn{IdentityToken: good, Nonce: "raw-n", DisplayName: ptr(strings.Repeat("é", 101))}); !apperr.Is(err, apperr.ValidationFailed) {
		t.Errorf("long name: want validation_failed, got %v", err)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatalf("rejected sign-ins created %d users", n)
	}
}

func TestTrialAbuseDeleteAndResignIn(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	r, err := e.signIn(t, "001.eve", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET deleted_at=now() WHERE id=$1`, r.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, r.AccessToken); !apperr.Is(err, apperr.SessionRevoked) {
		t.Fatalf("tombstoned user session: %v", err)
	}
	r2, err := e.signIn(t, "001.eve", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Created || r2.User.ID == r.User.ID {
		t.Fatalf("want new account, got %+v", r2)
	}
	if q := entitlementQuota(t, e, r2.User.ID); q != 0 {
		t.Fatalf("re-sign-in must get quota 0, got %d", q)
	}

	// Purge removes the user row; trial_grants survives.
	if _, err := e.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, r.User.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM trial_grants`).Scan(&n)
	if n != 1 {
		t.Fatalf("trial_grants %d", n)
	}

	// Same subject string via dev login is a different identity.
	d, err := e.svc.SignInDev(ctx, "001.eve")
	if err != nil || entitlementQuota(t, e, d.User.ID) == 0 {
		t.Fatalf("dev identity must get its own trial: %v", err)
	}
}

func TestConcurrentFirstSignIn(t *testing.T) {
	e := newEnv(t, false)
	var wg sync.WaitGroup
	results := make([]*Result, 8)
	errs := make([]error, 8)
	tokens := make([]string, 8)
	for i := range tokens {
		tokens[i] = appleToken(t, e.jwks, "k1", validClaims("001.race", "raw-n"))
	}
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = e.svc.SignInApple(context.Background(), AppleSignIn{IdentityToken: tokens[i], Nonce: "raw-n"})
		}()
	}
	wg.Wait()
	created := 0
	for i := range 8 {
		if errs[i] != nil {
			t.Fatalf("sign-in %d: %v", i, errs[i])
		}
		if results[i].User.ID != results[0].User.ID {
			t.Fatal("different users for one subject")
		}
		if results[i].Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
}

func TestSignInDev(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	r, err := e.svc.SignInDev(ctx, "alice")
	if err != nil || !r.Created {
		t.Fatalf("%+v %v", r, err)
	}
	r2, err := e.svc.SignInDev(ctx, "alice")
	if err != nil || r2.Created || r2.User.ID != r.User.ID {
		t.Fatalf("%+v %v", r2, err)
	}
	for in, reason := range map[string]string{
		"": apperr.ReasonRequired, "Alice": apperr.ReasonInvalid, "a b": apperr.ReasonInvalid,
		strings.Repeat("a", 65): apperr.ReasonTooLong,
	} {
		_, err := e.svc.SignInDev(ctx, in)
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Code != apperr.ValidationFailed ||
			ae.Details["fields"].([]apperr.FieldError)[0].Reason != reason {
			t.Errorf("%q: want %s, got %v", in, reason, err)
		}
	}
	if _, err := e.svc.SignInDev(ctx, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSessionAndLogout(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	r, err := e.svc.SignInDev(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	_, sid, err := e.svc.ResolveSession(ctx, r.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := e.svc.SignInDev(ctx, "bob")

	for _, tok := range []string{"", "unknown", r.AccessToken + "x", r.RefreshToken} {
		if _, _, err := e.svc.ResolveSession(ctx, tok); !apperr.Is(err, apperr.Unauthenticated) {
			t.Errorf("%q: %v", tok, err)
		}
	}
	if err := e.svc.Logout(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, r.AccessToken); !apperr.Is(err, apperr.SessionRevoked) {
		t.Fatalf("revoked access: %v", err)
	}
	if _, err := e.svc.Refresh(ctx, r.RefreshToken); !apperr.Is(err, apperr.SessionRevoked) {
		t.Fatalf("revoked refresh: %v", err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, other.AccessToken); err != nil {
		t.Fatalf("logout must revoke only the calling session family: %v", err)
	}
	var reason string
	_ = e.pool.QueryRow(ctx, `SELECT revoked_reason FROM sessions WHERE id=$1`, sid).Scan(&reason)
	if reason != "logout" {
		t.Fatalf("reason %q", reason)
	}

	var secs float64
	_ = e.pool.QueryRow(ctx, `SELECT extract(epoch FROM s.expires_at - s.created_at) FROM sessions s JOIN session_tokens t ON t.session_id=s.id
		WHERE t.access_token_hash=$1`, hashToken(other.AccessToken)).Scan(&secs)
	if int64(secs+0.5) != int64(e.cfg.SessionMaxAge.Seconds()) {
		t.Fatalf("session max age %v", secs)
	}
}
