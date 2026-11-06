package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"phoebe/internal/config"
	"phoebe/internal/testutil"
)

type appleEndpoints struct {
	pub *ecdsa.PublicKey
	srv *httptest.Server

	mu           sync.Mutex
	tokenForms   []url.Values
	revokeForms  []url.Values
	tokenStatus  int
	revokeStatus int
	revokeBody   string
	refreshToken string
}

func newAppleEndpoints(t *testing.T, cfg *config.Config) *appleEndpoints {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	a := &appleEndpoints{pub: &key.PublicKey, tokenStatus: 200, revokeStatus: 200, refreshToken: "rt-secret-1"}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		a.mu.Lock()
		defer a.mu.Unlock()
		switch r.URL.Path {
		case "/auth/token":
			a.tokenForms = append(a.tokenForms, r.PostForm)
			w.WriteHeader(a.tokenStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer",
				"expires_in": 3600, "refresh_token": a.refreshToken, "id_token": "x.y.z"})
		case "/auth/revoke":
			a.revokeForms = append(a.revokeForms, r.PostForm)
			w.WriteHeader(a.revokeStatus)
			_, _ = w.Write([]byte(a.revokeBody))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(a.srv.Close)
	cfg.AppleTeamID = "TEAM123456"
	cfg.AppleKeyID = "KEY1234567"
	cfg.ApplePrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	cfg.TokenEncKey = bytes.Repeat([]byte{7}, 32)
	cfg.AppleTokenURL = a.srv.URL + "/auth/token"
	cfg.AppleRevokeURL = a.srv.URL + "/auth/revoke"
	return a
}

func (a *appleEndpoints) verifySecret(t *testing.T, secret string) {
	t.Helper()
	parts := strings.Split(secret, ".")
	if len(parts) != 3 {
		t.Fatalf("client_secret not a JWT")
	}
	var hdr map[string]string
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(hb, &hdr)
	if hdr["alg"] != "ES256" || hdr["kid"] != "KEY1234567" {
		t.Fatalf("header %v", hdr)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("ES256 signature must be raw r‖s (64 bytes), got %d", len(sig))
	}
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(a.pub, d[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("client_secret signature invalid")
	}
	var c struct {
		Iss, Aud, Sub string
		Iat, Exp      int64
	}
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(cb, &c)
	now := time.Now().Unix()
	if c.Iss != "TEAM123456" || c.Aud != "https://appleid.apple.com" || c.Sub != testAud ||
		c.Iat > now+5 || c.Iat < now-60 || c.Exp <= c.Iat || c.Exp-c.Iat > 300 {
		t.Fatalf("claims %+v", c)
	}
}

func TestNewAppleClientNotConfigured(t *testing.T) {
	c, err := NewAppleClient(testutil.Config(t))
	if c != nil || err != nil {
		t.Fatalf("want nil, nil; got %v, %v", c, err)
	}
}

func TestAppleExchangeAndRevoke(t *testing.T) {
	cfg := testutil.Config(t)
	a := newAppleEndpoints(t, cfg)
	c, err := NewAppleClient(cfg)
	if err != nil || c == nil {
		t.Fatalf("NewAppleClient: %v", err)
	}
	ctx := context.Background()

	rt, err := c.Exchange(ctx, "auth-code-1")
	if err != nil || rt != "rt-secret-1" {
		t.Fatalf("exchange: %q %v", rt, err)
	}
	f := a.tokenForms[0]
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "auth-code-1" || f.Get("client_id") != testAud {
		t.Fatalf("token form %v", f)
	}
	a.verifySecret(t, f.Get("client_secret"))

	enc := c.Seal(rt)
	if bytes.Contains(enc, []byte(rt)) {
		t.Fatal("sealed token contains plaintext")
	}
	if err := c.RevokeRefreshToken(ctx, enc); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	f = a.revokeForms[0]
	if f.Get("token") != "rt-secret-1" || f.Get("token_type_hint") != "refresh_token" || f.Get("client_id") != testAud {
		t.Fatalf("revoke form %v", f)
	}
	a.verifySecret(t, f.Get("client_secret"))

	a.revokeStatus, a.revokeBody = 400, `{"error":"invalid_grant"}`
	if err := c.Revoke(ctx, "x"); err != nil {
		t.Fatalf("invalid_grant must count as revoked: %v", err)
	}
	a.revokeStatus, a.revokeBody = 400, `{"error":"invalid_client"}`
	if err := c.Revoke(ctx, "x"); err == nil {
		t.Fatal("invalid_client must fail")
	}
	a.revokeStatus, a.revokeBody = 503, ``
	if err := c.Revoke(ctx, "x"); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("5xx must fail with status: %v", err)
	}

	a.tokenStatus = 400
	if _, err := c.Exchange(ctx, "bad"); err == nil {
		t.Fatal("exchange 400 must fail")
	}

	if _, err := c.Open(append([]byte{}, enc[:len(enc)-1]...)); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}

func TestParseP8EscapedNewlines(t *testing.T) {
	cfg := testutil.Config(t)
	newAppleEndpoints(t, cfg)
	cfg.ApplePrivateKey = strings.ReplaceAll(cfg.ApplePrivateKey, "\n", `\n`)
	if c, err := NewAppleClient(cfg); err != nil || c == nil {
		t.Fatalf("escaped PEM: %v", err)
	}
	cfg.ApplePrivateKey = "garbage"
	if _, err := NewAppleClient(cfg); err == nil {
		t.Fatal("garbage key must fail")
	}
}
