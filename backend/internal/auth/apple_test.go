package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"phoebe/internal/apperr"
)

const testAud = "com.kolarislabs.phoebe"

type jwks struct {
	mu   sync.Mutex
	keys map[string]*rsa.PrivateKey
	hits atomic.Int32
	srv  *httptest.Server
	fail atomic.Bool
}

func newJWKS(t *testing.T, kids ...string) *jwks {
	t.Helper()
	j := &jwks{keys: map[string]*rsa.PrivateKey{}}
	for _, k := range kids {
		j.add(t, k)
	}
	j.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.hits.Add(1)
		if j.fail.Load() {
			w.WriteHeader(500)
			return
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		var out struct {
			Keys []map[string]string `json:"keys"`
		}
		for kid, k := range j.keys {
			out.Keys = append(out.Keys, map[string]string{
				"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(j.srv.Close)
	return j
}

func (j *jwks) add(t *testing.T, kid string) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	j.mu.Lock()
	j.keys[kid] = k
	j.mu.Unlock()
	return k
}

func (j *jwks) key(kid string) *rsa.PrivateKey {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.keys[kid]
}

func nonceHash(raw string) string {
	s := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(s[:])
}

func signRS256(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	d := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func validClaims(sub, rawNonce string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": "https://appleid.apple.com", "aud": testAud, "sub": sub,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
		"nonce": nonceHash(rawNonce), "nonce_supported": true,
	}
}

func appleToken(t *testing.T, j *jwks, kid string, claims map[string]any) string {
	t.Helper()
	return signRS256(t, j.key(kid), map[string]any{"alg": "RS256", "kid": kid}, claims)
}

func TestVerifyAppleToken(t *testing.T) {
	j := newJWKS(t, "k1")
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	ctx := context.Background()

	mod := func(f func(c map[string]any)) map[string]any {
		c := validClaims("001.abc", "raw-nonce")
		f(c)
		return c
	}
	cases := []struct {
		name  string
		token string
		ok    bool
	}{
		{"valid", appleToken(t, j, "k1", validClaims("001.abc", "raw-nonce")), true},
		{"aud array", appleToken(t, j, "k1", mod(func(c map[string]any) { c["aud"] = []string{"x", testAud} })), true},
		{"wrong aud", appleToken(t, j, "k1", mod(func(c map[string]any) { c["aud"] = "com.other.app" })), false},
		{"wrong iss", appleToken(t, j, "k1", mod(func(c map[string]any) { c["iss"] = "https://evil.example" })), false},
		{"expired", appleToken(t, j, "k1", mod(func(c map[string]any) { c["exp"] = time.Now().Add(-2 * time.Minute).Unix() })), false},
		{"within skew", appleToken(t, j, "k1", mod(func(c map[string]any) { c["exp"] = time.Now().Add(-30 * time.Second).Unix() })), true},
		{"iat future", appleToken(t, j, "k1", mod(func(c map[string]any) { c["iat"] = time.Now().Add(5 * time.Minute).Unix() })), false},
		{"no exp", appleToken(t, j, "k1", mod(func(c map[string]any) { delete(c, "exp") })), false},
		{"bad nonce", appleToken(t, j, "k1", mod(func(c map[string]any) { c["nonce"] = nonceHash("other") })), false},
		{"raw nonce in token", appleToken(t, j, "k1", mod(func(c map[string]any) { c["nonce"] = "raw-nonce" })), false},
		{"no sub", appleToken(t, j, "k1", mod(func(c map[string]any) { c["sub"] = "" })), false},
		{"bad signature", signRS256(t, other, map[string]any{"alg": "RS256", "kid": "k1"}, validClaims("001.abc", "raw-nonce")), false},
		{"alg none", signRS256(t, j.key("k1"), map[string]any{"alg": "none", "kid": "k1"}, validClaims("001.abc", "raw-nonce")), false},
		{"unknown kid", signRS256(t, other, map[string]any{"alg": "RS256", "kid": "zz"}, validClaims("001.abc", "raw-nonce")), false},
		{"garbage", "a.b", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := NewVerifier(j.srv.URL, testAud)
			sub, err := v.Verify(ctx, c.token, "raw-nonce")
			if c.ok {
				if err != nil || sub != "001.abc" {
					t.Fatalf("want ok, got sub=%q err=%v", sub, err)
				}
				return
			}
			if !apperr.Is(err, apperr.Unauthenticated) {
				t.Fatalf("want unauthenticated, got %v", err)
			}
		})
	}
}

func TestVerifierJWKSCacheAndRefetch(t *testing.T) {
	j := newJWKS(t, "k1")
	ctx := context.Background()
	v := NewVerifier(j.srv.URL, testAud)
	now := time.Now()
	v.now = func() time.Time { return now }

	tok := appleToken(t, j, "k1", validClaims("s", "n"))
	for range 3 {
		if _, err := v.Verify(ctx, tok, "n"); err != nil {
			t.Fatal(err)
		}
	}
	if h := j.hits.Load(); h != 1 {
		t.Fatalf("cached: want 1 fetch, got %d", h)
	}

	// Apple rotates keys: unknown kid refetches, but at most once per minute.
	j.add(t, "k2")
	tok2 := appleToken(t, j, "k2", validClaims("s", "n"))
	now = now.Add(30 * time.Second)
	if _, err := v.Verify(ctx, tok2, "n"); !apperr.Is(err, apperr.Unauthenticated) {
		t.Fatalf("within a minute of last fetch: want unauthenticated, got %v", err)
	}
	if h := j.hits.Load(); h != 1 {
		t.Fatalf("rate limited: want 1 fetch, got %d", h)
	}
	now = now.Add(31 * time.Second)
	if _, err := v.Verify(ctx, tok2, "n"); err != nil {
		t.Fatalf("after a minute: %v", err)
	}
	if h := j.hits.Load(); h != 2 {
		t.Fatalf("want 2 fetches, got %d", h)
	}

	v.now = time.Now
	before := j.hits.Load()
	v.fetchedAt = time.Now().Add(-25 * time.Hour)
	if _, err := v.Verify(ctx, appleToken(t, j, "k1", validClaims("s", "n")), "n"); err != nil {
		t.Fatal(err)
	}
	if j.hits.Load() != before+1 {
		t.Fatal("cache older than 24h must refetch")
	}

	j.fail.Store(true)
	v2 := NewVerifier(j.srv.URL, testAud)
	_, err := v2.Verify(ctx, tok, "n")
	if err == nil || apperr.CodeOf(err) != "" {
		t.Fatalf("JWKS outage must be a server error, got %v", err)
	}
}
