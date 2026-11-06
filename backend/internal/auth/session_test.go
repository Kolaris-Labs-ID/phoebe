package auth

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"phoebe/internal/apperr"
)

var ctx = context.Background()

func (e *env) dev(t *testing.T, sub string) *Result {
	t.Helper()
	r, err := e.svc.SignInDev(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func wantCode(t *testing.T, what string, err error, code apperr.Code) {
	t.Helper()
	if !apperr.Is(err, code) {
		t.Fatalf("%s: want %s, got %v", what, code, err)
	}
}

func TestSignInAppleResponseShape(t *testing.T) {
	e := newEnv(t, false)
	r, err := e.signIn(t, "001.shape", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := "access_token,access_token_expires_at,created,refresh_token,refresh_token_expires_at,session_expires_at,user"
	if strings.Join(keys, ",") != want {
		t.Fatalf("keys %v", keys)
	}
	acc, _ := time.Parse(time.RFC3339, m["access_token_expires_at"].(string))
	sess, _ := time.Parse(time.RFC3339, m["session_expires_at"].(string))
	if d := time.Until(acc); d < e.cfg.AccessTokenTTL-time.Minute || d > e.cfg.AccessTokenTTL+time.Minute {
		t.Fatalf("access ttl %v", d)
	}
	if d := time.Until(sess); d < e.cfg.SessionMaxAge-time.Minute || d > e.cfg.SessionMaxAge+time.Minute {
		t.Fatalf("session max age %v", d)
	}
	if m["refresh_token_expires_at"] != m["session_expires_at"] {
		t.Fatalf("refresh expiry must equal session expiry: %v", m)
	}
}

func TestAccessTokenExpired(t *testing.T) {
	e := newEnv(t, false)
	r := e.dev(t, "exp")
	e.exec(t, `UPDATE session_tokens SET access_expires_at = now() - interval '1 second' WHERE access_token_hash=$1`, hashToken(r.AccessToken))
	_, _, err := e.svc.ResolveSession(ctx, r.AccessToken)
	wantCode(t, "expired access", err, apperr.AccessTokenExpired)
	n, err := e.svc.Refresh(ctx, r.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, n.AccessToken); err != nil {
		t.Fatalf("refreshed access: %v", err)
	}
}

func TestRefreshRotates(t *testing.T) {
	e := newEnv(t, false)
	r := e.dev(t, "rot")
	n, err := e.svc.Refresh(ctx, r.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if n.AccessToken == r.AccessToken || n.RefreshToken == r.RefreshToken || n.SessionExpiresAt != r.SessionExpiresAt ||
		n.RefreshTokenExpiresAt != r.SessionExpiresAt {
		t.Fatalf("rotation %+v vs %+v", n, r)
	}
	uid, sid, err := e.svc.ResolveSession(ctx, n.AccessToken)
	if err != nil || uid != r.User.ID {
		t.Fatalf("new access: %v", err)
	}
	if _, sid0, err := e.svc.ResolveSession(ctx, r.AccessToken); err != nil || sid0 != sid {
		t.Fatalf("old access stays valid until it expires, same session: %v", err)
	}
	var rotated, successorOK bool
	_ = e.pool.QueryRow(ctx, `SELECT rotated_at IS NOT NULL, successor_id = (SELECT id FROM session_tokens WHERE refresh_token_hash=$2)
		FROM session_tokens WHERE refresh_token_hash=$1`, hashToken(r.RefreshToken), hashToken(n.RefreshToken)).Scan(&rotated, &successorOK)
	if !rotated || !successorOK {
		t.Fatal("old refresh token must be marked rotated with its successor")
	}
	for _, in := range []string{"", "nope"} {
		_, err := e.svc.Refresh(ctx, in)
		if in == "" {
			wantCode(t, "empty", err, apperr.ValidationFailed)
		} else {
			wantCode(t, "unknown", err, apperr.Unauthenticated)
		}
	}
	_, err = e.svc.Refresh(ctx, r.AccessToken)
	wantCode(t, "access token as refresh", err, apperr.Unauthenticated)
}

func TestRefreshReuseAfterGraceRevokesFamily(t *testing.T) {
	e := newEnv(t, false)
	r := e.dev(t, "reuse")
	n, err := e.svc.Refresh(ctx, r.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE session_tokens SET rotated_at = now() - make_interval(secs => $2) WHERE refresh_token_hash=$1`,
		hashToken(r.RefreshToken), e.cfg.RefreshReuseGrace.Seconds()+1)
	_, err = e.svc.Refresh(ctx, r.RefreshToken)
	wantCode(t, "replay after grace", err, apperr.SessionRevoked)
	for name, tok := range map[string]string{"old access": r.AccessToken, "new access": n.AccessToken} {
		_, _, err := e.svc.ResolveSession(ctx, tok)
		wantCode(t, name, err, apperr.SessionRevoked)
	}
	_, err = e.svc.Refresh(ctx, n.RefreshToken)
	wantCode(t, "new refresh", err, apperr.SessionRevoked)
	var reason string
	_ = e.pool.QueryRow(ctx, `SELECT revoked_reason FROM sessions WHERE user_id=$1`, r.User.ID).Scan(&reason)
	if reason != "refresh_reuse" {
		t.Fatalf("reason %q", reason)
	}
	if !strings.Contains(e.logs.String(), `"auth.refresh_reuse"`) || strings.Contains(e.logs.String(), r.RefreshToken) {
		t.Fatalf("reuse log: %s", e.logs)
	}
	other := e.dev(t, "reuse")
	if _, _, err := e.svc.ResolveSession(ctx, other.AccessToken); err != nil {
		t.Fatalf("other session must survive: %v", err)
	}
}

func TestRefreshReuseWithinGraceUnusedSuccessor(t *testing.T) {
	e := newEnv(t, false)
	r := e.dev(t, "grace")
	lost, err := e.svc.Refresh(ctx, r.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.Refresh(ctx, r.RefreshToken)
	if err != nil {
		t.Fatalf("replay within grace: %v", err)
	}
	if n.SessionExpiresAt != r.SessionExpiresAt || n.RefreshToken == lost.RefreshToken {
		t.Fatalf("%+v", n)
	}
	if _, _, err := e.svc.ResolveSession(ctx, n.AccessToken); err != nil {
		t.Fatal(err)
	}
	n2, err := e.svc.Refresh(ctx, n.RefreshToken)
	if err != nil {
		t.Fatalf("chain continues from the new pair: %v", err)
	}
	_, err = e.svc.Refresh(ctx, lost.RefreshToken)
	wantCode(t, "invalidated successor refresh", err, apperr.SessionRevoked)
	_, _, err = e.svc.ResolveSession(ctx, n2.AccessToken)
	wantCode(t, "family after replaying invalidated token", err, apperr.SessionRevoked)
}

// An invalidated pair is never delivered by a lost response, so using its access token means a replayer holds the family.
func TestInvalidatedSuccessorAccessRevokesFamily(t *testing.T) {
	e := newEnv(t, false)
	r := e.dev(t, "stolen")
	legit, err := e.svc.Refresh(ctx, r.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	replayer, err := e.svc.Refresh(ctx, r.RefreshToken)
	if err != nil {
		t.Fatalf("replay within grace: %v", err)
	}
	_, _, err = e.svc.ResolveSession(ctx, legit.AccessToken)
	wantCode(t, "invalidated successor access", err, apperr.SessionRevoked)
	_, _, err = e.svc.ResolveSession(ctx, replayer.AccessToken)
	wantCode(t, "replayer access", err, apperr.SessionRevoked)
	_, err = e.svc.Refresh(ctx, replayer.RefreshToken)
	wantCode(t, "replayer refresh", err, apperr.SessionRevoked)
	var reason string
	_ = e.pool.QueryRow(ctx, `SELECT revoked_reason FROM sessions WHERE user_id=$1`, r.User.ID).Scan(&reason)
	if reason != "refresh_reuse" || !strings.Contains(e.logs.String(), `"auth.refresh_reuse"`) {
		t.Fatalf("reason %q logs %s", reason, e.logs)
	}
}

func TestRefreshReuseWithinGraceUsedSuccessorRevokes(t *testing.T) {
	e := newEnv(t, false)
	r := e.dev(t, "used")
	n, err := e.svc.Refresh(ctx, r.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, n.AccessToken); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Refresh(ctx, r.RefreshToken)
	wantCode(t, "replay after successor used", err, apperr.SessionRevoked)
	_, err = e.svc.Refresh(ctx, n.RefreshToken)
	wantCode(t, "successor", err, apperr.SessionRevoked)

	r = e.dev(t, "used2")
	n, _ = e.svc.Refresh(ctx, r.RefreshToken)
	if _, err := e.svc.Refresh(ctx, n.RefreshToken); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Refresh(ctx, r.RefreshToken)
	wantCode(t, "replay after successor rotated", err, apperr.SessionRevoked)
}

func TestConcurrentRefreshRace(t *testing.T) {
	e := newEnv(t, false)
	r := e.dev(t, "race")
	const n = 8
	var wg sync.WaitGroup
	res := make([]*Tokens, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = e.svc.Refresh(ctx, r.RefreshToken)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil && !apperr.Is(err, apperr.SessionRevoked) {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}
	var sessions, live int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1`, r.User.ID).Scan(&sessions)
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM session_tokens t JOIN sessions s ON s.id=t.session_id
		WHERE s.user_id=$1 AND s.revoked_at IS NULL AND t.rotated_at IS NULL AND t.invalidated_at IS NULL`, r.User.ID).Scan(&live)
	if sessions != 1 || live > 1 {
		t.Fatalf("sessions=%d live refresh tokens=%d", sessions, live)
	}
	// Resolve live pairs first: presenting an invalidated pair's access token revokes the family.
	working, invalidated := 0, []string{}
	for _, t2 := range res {
		if t2 == nil {
			continue
		}
		var inv bool
		_ = e.pool.QueryRow(ctx, `SELECT invalidated_at IS NOT NULL FROM session_tokens WHERE access_token_hash=$1`,
			hashToken(t2.AccessToken)).Scan(&inv)
		if inv {
			invalidated = append(invalidated, t2.AccessToken)
		} else if _, _, err := e.svc.ResolveSession(ctx, t2.AccessToken); err == nil {
			working++
		}
	}
	if working != live {
		t.Fatalf("working access tokens %d, live pairs %d", working, live)
	}
	for _, tok := range invalidated {
		_, _, err := e.svc.ResolveSession(ctx, tok)
		wantCode(t, "invalidated pair", err, apperr.SessionRevoked)
	}
}

func TestSessionExpiryIsAbsolute(t *testing.T) {
	e := newEnv(t, false)
	r := e.dev(t, "abs")
	cur := r.Tokens
	for range 3 {
		n, err := e.svc.Refresh(ctx, cur.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		if n.SessionExpiresAt != r.SessionExpiresAt || n.RefreshTokenExpiresAt != r.SessionExpiresAt {
			t.Fatalf("session_expires_at moved: %v → %v", r.SessionExpiresAt, n.SessionExpiresAt)
		}
		cur = *n
	}

	e.exec(t, `UPDATE sessions SET expires_at = now() + interval '2 minutes' WHERE user_id=$1`, r.User.ID)
	n, err := e.svc.Refresh(ctx, cur.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if n.AccessTokenExpiresAt.After(n.SessionExpiresAt.Time) {
		t.Fatalf("access token outlives session: %v > %v", n.AccessTokenExpiresAt, n.SessionExpiresAt)
	}

	e.exec(t, `UPDATE sessions SET created_at = now() - interval '181 days', expires_at = now() - interval '1 second' WHERE user_id=$1`, r.User.ID)
	_, _, err = e.svc.ResolveSession(ctx, n.AccessToken)
	wantCode(t, "expired session access", err, apperr.SessionExpired)
	_, err = e.svc.Refresh(ctx, n.RefreshToken)
	wantCode(t, "expired session refresh", err, apperr.SessionExpired)
}

func TestSessionSweepDeletesLongEndedSessions(t *testing.T) {
	e := newEnv(t, false)
	live := e.dev(t, "live")
	if _, err := e.svc.Refresh(ctx, live.RefreshToken); err != nil {
		t.Fatal(err)
	}
	old := func(sub, set string) string {
		r := e.dev(t, sub)
		e.exec(t, `UPDATE sessions SET `+set+` WHERE user_id = $1`, r.User.ID)
		return r.User.ID
	}
	expired := old("expired", `created_at = now() - interval '200 days', expires_at = now() - interval '8 days'`)
	revoked := old("revoked", `revoked_at = now() - interval '8 days', revoked_reason = 'logout'`)
	recent := old("recent", `revoked_at = now() - interval '1 day', revoked_reason = 'logout'`)

	n, err := e.svc.SessionSweep(ctx)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	count := func(q string, args ...any) (c int) {
		_ = e.pool.QueryRow(ctx, q, args...).Scan(&c)
		return
	}
	for _, uid := range []string{expired, revoked} {
		if c := count(`SELECT count(*) FROM sessions WHERE user_id = $1`, uid); c != 0 {
			t.Fatal("not swept", uid)
		}
	}
	if count(`SELECT count(*) FROM sessions WHERE user_id = $1`, recent) != 1 {
		t.Fatal("recently revoked session must stay so clients still get session_revoked")
	}
	if c := count(`SELECT count(*) FROM session_tokens t JOIN sessions s ON s.id = t.session_id WHERE s.user_id = $1`, live.User.ID); c != 2 {
		t.Fatalf("live session keeps its rotated row for reuse detection: %d", c)
	}
}
