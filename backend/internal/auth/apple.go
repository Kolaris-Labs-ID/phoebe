package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"phoebe/internal/apperr"
)

const (
	appleIssuer     = "https://appleid.apple.com"
	jwksCacheTTL    = 24 * time.Hour
	jwksRefetchWait = time.Minute
	clockSkew       = 60 * time.Second
)

// Verifier checks Sign in with Apple identity tokens against Apple's JWKS (backend.md §13).
type Verifier struct {
	jwksURL  string
	audience string
	client   *http.Client
	now      func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	triedAt   time.Time
}

func NewVerifier(jwksURL, audience string) *Verifier {
	return &Verifier{jwksURL: jwksURL, audience: audience, client: &http.Client{Timeout: 10 * time.Second}, now: time.Now}
}

func unauth() error { return apperr.New(apperr.Unauthenticated) }

type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = []string{s}
		return nil
	}
	var l []string
	if err := json.Unmarshal(b, &l); err != nil {
		return err
	}
	*a = l
	return nil
}

type appleClaims struct {
	Iss   string   `json:"iss"`
	Aud   audience `json:"aud"`
	Sub   string   `json:"sub"`
	Exp   *float64 `json:"exp"`
	Iat   *float64 `json:"iat"`
	Nonce string   `json:"nonce"`
}

// Verify returns the Apple subject. Any token defect → unauthenticated; JWKS fetch
// failure → plain error (server-side).
func (v *Verifier) Verify(ctx context.Context, token, rawNonce string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", unauth()
	}
	hb, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	pb, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", unauth()
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(hb, &hdr) != nil || hdr.Alg != "RS256" || hdr.Kid == "" {
		return "", unauth()
	}
	key, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return "", unauth()
	}
	var c appleClaims
	if json.Unmarshal(pb, &c) != nil {
		return "", unauth()
	}
	now := v.now()
	if c.Iss != appleIssuer || c.Sub == "" || c.Exp == nil || c.Iat == nil {
		return "", unauth()
	}
	audOK := false
	for _, a := range c.Aud {
		if a == v.audience {
			audOK = true
		}
	}
	if !audOK {
		return "", unauth()
	}
	if !unixTime(*c.Exp).After(now.Add(-clockSkew)) || !unixTime(*c.Iat).Before(now.Add(clockSkew)) {
		return "", unauth()
	}
	want := sha256.Sum256([]byte(rawNonce))
	if rawNonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(hex.EncodeToString(want[:]))) != 1 {
		return "", unauth()
	}
	return c.Sub, nil
}

func unixTime(f float64) time.Time {
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9))
}

// key serves from the 24 h cache; an unknown kid triggers at most one refetch per minute.
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	stale := v.keys == nil || now.Sub(v.fetchedAt) >= jwksCacheTTL
	if k, ok := v.keys[kid]; ok && !stale {
		return k, nil
	}
	if !stale && now.Sub(v.triedAt) < jwksRefetchWait {
		return nil, unauth()
	}
	v.triedAt = now
	keys, err := v.fetch(ctx)
	if err != nil {
		return nil, err
	}
	v.keys, v.fetchedAt = keys, now
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, unauth()
}

func (v *Verifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apple jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apple jwks: http %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("apple jwks: decode: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return keys, nil
}
