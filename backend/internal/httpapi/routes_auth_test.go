package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/auth"
	"phoebe/internal/testutil"
	"phoebe/internal/users"
)

func newAuthServer(t *testing.T, devLogin bool) *httptest.Server {
	t.Helper()
	srv, _ := newAuthServerDB(t, devLogin)
	return srv
}

func newAuthServerDB(t *testing.T, devLogin bool) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	cfg.DevLoginEnabled = devLogin
	log := testutil.DiscardLogger()
	a := auth.NewService(pool, cfg, log, nil)
	srv := httptest.NewServer(New(Deps{
		Config: cfg, Log: log, DB: pool, Sessions: a, Auth: a,
		Users: users.New(pool, cfg, nil, nil, log),
	}))
	t.Cleanup(srv.Close)
	return srv, pool
}

func call(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(b) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s %s: non-JSON %q", method, path, b)
		}
	}
	return resp.StatusCode, m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestAuthAndProfileFlow(t *testing.T) {
	srv := newAuthServer(t, true)

	st, m := call(t, srv, "POST", "/v1/auth/dev", "", `{"subject":"alice"}`)
	if st != 200 || m["created"] != true {
		t.Fatalf("dev login %d %v", st, m)
	}
	tok := m["access_token"].(string)
	for _, k := range []string{"access_token_expires_at", "refresh_token_expires_at", "session_expires_at"} {
		if v, _ := m[k].(string); !strings.HasSuffix(v, "Z") {
			t.Fatalf("token response %s: %v", k, m)
		}
	}
	if r, _ := m["refresh_token"].(string); r == "" || r == tok {
		t.Fatalf("refresh token %v", m)
	}
	user := m["user"].(map[string]any)
	for _, k := range []string{"id", "display_name", "language", "timezone", "created_at"} {
		if _, ok := user[k]; !ok {
			t.Fatalf("user missing %s: %v", k, user)
		}
	}

	st, m = call(t, srv, "GET", "/v1/me", tok, "")
	if st != 200 || m["user"].(map[string]any)["id"] != user["id"] {
		t.Fatalf("me %d %v", st, m)
	}

	st, m = call(t, srv, "PATCH", "/v1/me", tok, `{"display_name":"Ari","language":"en","timezone":"Asia/Makassar"}`)
	u := m["user"].(map[string]any)
	if st != 200 || u["display_name"] != "Ari" || u["language"] != "en" || u["timezone"] != "Asia/Makassar" {
		t.Fatalf("patch %d %v", st, m)
	}
	st, m = call(t, srv, "PATCH", "/v1/me", tok, `{"timezone":"Nowhere/City"}`)
	if st != 422 || errCode(m) != "validation_failed" {
		t.Fatalf("bad tz %d %v", st, m)
	}
	st, m = call(t, srv, "PATCH", "/v1/me", tok, `{"language":5}`)
	if st != 400 || errCode(m) != "invalid_json" {
		t.Fatalf("wrong type %d %v", st, m)
	}

	st, m = call(t, srv, "POST", "/v1/auth/dev", "", `{"subject":"Bad Subject"}`)
	if st != 422 || errCode(m) != "validation_failed" {
		t.Fatalf("bad subject %d %v", st, m)
	}
	st, m = call(t, srv, "POST", "/v1/auth/apple", "", `{"identity_token":"a.b.c","nonce":"n"}`)
	if st != 401 || errCode(m) != "unauthenticated" {
		t.Fatalf("bad apple token %d %v", st, m)
	}
	st, m = call(t, srv, "POST", "/v1/auth/apple", "", `{}`)
	if st != 422 {
		t.Fatalf("empty apple body %d %v", st, m)
	}

	if st, _ = call(t, srv, "POST", "/v1/auth/logout", tok, ""); st != 204 {
		t.Fatalf("logout %d", st)
	}
	st, m = call(t, srv, "GET", "/v1/me", tok, "")
	if st != 401 || errCode(m) != "session_revoked" {
		t.Fatalf("after logout %d %v", st, m)
	}
	if st, _ = call(t, srv, "POST", "/v1/auth/logout", "", ""); st != 401 {
		t.Fatalf("logout without token %d", st)
	}
}

func TestDevLoginDisabled(t *testing.T) {
	srv := newAuthServer(t, false)
	st, m := call(t, srv, "POST", "/v1/auth/dev", "", `{"subject":"alice"}`)
	if st != 404 || errCode(m) != "not_found" {
		t.Fatalf("dev login disabled: %d %v", st, m)
	}
}

func devLogin(t *testing.T, srv *httptest.Server, subject string) (access, refresh string) {
	t.Helper()
	st, m := call(t, srv, "POST", "/v1/auth/dev", "", `{"subject":"`+subject+`"}`)
	if st != 200 {
		t.Fatalf("dev login %d %v", st, m)
	}
	return m["access_token"].(string), m["refresh_token"].(string)
}

func refreshCall(t *testing.T, srv *httptest.Server, refresh string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"refresh_token": refresh})
	return call(t, srv, "POST", "/v1/auth/refresh", "", string(b))
}

func TestRefreshHTTP(t *testing.T) {
	srv, pool := newAuthServerDB(t, true)
	access, refresh := devLogin(t, srv, "carol")

	if _, err := pool.Exec(context.Background(), `UPDATE session_tokens SET access_expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if st, m := call(t, srv, "GET", "/v1/me", access, ""); st != 401 || errCode(m) != "access_token_expired" {
		t.Fatalf("expired access %d %v", st, m)
	}
	st, m := refreshCall(t, srv, refresh)
	if st != 200 || m["user"] != nil || m["access_token"] == nil || m["session_expires_at"] == nil || m["refresh_token"] == refresh {
		t.Fatalf("refresh %d %v", st, m)
	}
	access2, refresh2 := m["access_token"].(string), m["refresh_token"].(string)
	if st, _ := call(t, srv, "GET", "/v1/me", access2, ""); st != 200 {
		t.Fatalf("refreshed access %d", st)
	}

	if st, m := refreshCall(t, srv, "unknown"); st != 401 || errCode(m) != "unauthenticated" {
		t.Fatalf("unknown refresh %d %v", st, m)
	}
	if st, m := call(t, srv, "POST", "/v1/auth/refresh", "", `{}`); st != 422 || errCode(m) != "validation_failed" {
		t.Fatalf("empty refresh %d %v", st, m)
	}
	// refresh2's access token was used above, so replaying the rotated token is theft, not a lost response.
	if st, m := refreshCall(t, srv, refresh); st != 401 || errCode(m) != "session_revoked" {
		t.Fatalf("reuse %d %v", st, m)
	}
	if st, m := refreshCall(t, srv, refresh2); st != 401 || errCode(m) != "session_revoked" {
		t.Fatalf("family after reuse %d %v", st, m)
	}
	if st, m := call(t, srv, "GET", "/v1/me", access2, ""); st != 401 || errCode(m) != "session_revoked" {
		t.Fatalf("access after reuse %d %v", st, m)
	}
}

func TestDeleteMeRevokesAllSessions(t *testing.T) {
	srv, _ := newAuthServerDB(t, true)
	a1, r1 := devLogin(t, srv, "dave")
	a2, r2 := devLogin(t, srv, "dave")
	other, _ := devLogin(t, srv, "erin")

	if st, m := call(t, srv, "DELETE", "/v1/me", a1, ""); st != 202 {
		t.Fatalf("delete %d %v", st, m)
	}
	for _, tok := range []string{a1, a2} {
		if st, m := call(t, srv, "GET", "/v1/me", tok, ""); st != 401 || errCode(m) != "session_revoked" {
			t.Fatalf("access after delete %d %v", st, m)
		}
	}
	for _, tok := range []string{r1, r2} {
		if st, m := refreshCall(t, srv, tok); st != 401 || errCode(m) != "session_revoked" {
			t.Fatalf("refresh after delete %d %v", st, m)
		}
	}
	if st, _ := call(t, srv, "GET", "/v1/me", other, ""); st != 200 {
		t.Fatalf("other account %d", st)
	}
}
