package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"phoebe/internal/auth"
	"phoebe/internal/testutil"
	"phoebe/internal/users"
)

func newAuthServer(t *testing.T, devLogin bool) *httptest.Server {
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
	return srv
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
	tok := m["token"].(string)
	if !strings.HasPrefix(tok, "phb_") || !strings.HasSuffix(m["expires_at"].(string), "Z") {
		t.Fatalf("token response %v", m)
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
	if st != 401 || errCode(m) != "unauthenticated" {
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
