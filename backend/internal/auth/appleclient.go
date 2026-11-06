package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"phoebe/internal/config"
)

// AppleClient exchanges authorization codes for refresh tokens and revokes them
// (account deletion, App Store requirement). Refresh tokens are stored sealed with
// AES-256-GCM(TOKEN_ENC_KEY) as nonce ‖ ciphertext.
type AppleClient struct {
	tokenURL  string
	revokeURL string
	teamID    string
	keyID     string
	clientID  string
	key       *ecdsa.PrivateKey
	encKey    []byte
	client    *http.Client
}

// NewAppleClient returns nil, nil when Apple credentials are not configured.
func NewAppleClient(cfg *config.Config) (*AppleClient, error) {
	if !cfg.AppleConfigured() {
		return nil, nil
	}
	key, err := parseP8(cfg.ApplePrivateKey)
	if err != nil {
		return nil, err
	}
	if len(cfg.TokenEncKey) != 32 {
		return nil, errors.New("TOKEN_ENC_KEY: must be 32 bytes")
	}
	return &AppleClient{
		tokenURL: cfg.AppleTokenURL, revokeURL: cfg.AppleRevokeURL,
		teamID: cfg.AppleTeamID, keyID: cfg.AppleKeyID, clientID: cfg.AppleBundleID,
		key: key, encKey: cfg.TokenEncKey, client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func parseP8(s string) (*ecdsa.PrivateKey, error) {
	if !strings.Contains(s, "\n") {
		s = strings.ReplaceAll(s, `\n`, "\n")
	}
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("APPLE_PRIVATE_KEY: not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("APPLE_PRIVATE_KEY: not a PKCS#8 key")
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("APPLE_PRIVATE_KEY: not an EC key")
	}
	return ek, nil
}

// clientSecret is the ES256 JWT Apple expects as client_secret (valid 5 minutes).
func (c *AppleClient) clientSecret(now time.Time) (string, error) {
	hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": c.keyID})
	claims, _ := json.Marshal(map[string]any{
		"iss": c.teamID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"aud": appleIssuer, "sub": c.clientID,
	})
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c *AppleClient) post(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	secret, err := c.clientSecret(time.Now())
	if err != nil {
		return 0, nil, err
	}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// Exchange trades an authorization code for a refresh token.
func (c *AppleClient) Exchange(ctx context.Context, code string) (string, error) {
	status, body, err := c.post(ctx, c.tokenURL, url.Values{"grant_type": {"authorization_code"}, "code": {code}})
	if err != nil {
		return "", fmt.Errorf("apple token: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("apple token: http %d", status)
	}
	var r struct {
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(body, &r) != nil || r.RefreshToken == "" {
		return "", errors.New("apple token: no refresh_token")
	}
	return r.RefreshToken, nil
}

// Revoke revokes a refresh token. 200, or 400 invalid_grant (already revoked/expired), is success.
func (c *AppleClient) Revoke(ctx context.Context, refreshToken string) error {
	status, body, err := c.post(ctx, c.revokeURL, url.Values{"token": {refreshToken}, "token_type_hint": {"refresh_token"}})
	if err != nil {
		return fmt.Errorf("apple revoke: %w", err)
	}
	if status == http.StatusOK {
		return nil
	}
	if status == http.StatusBadRequest {
		var r struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &r) == nil && r.Error == "invalid_grant" {
			return nil
		}
	}
	return fmt.Errorf("apple revoke: http %d", status)
}

// RevokeRefreshToken decrypts a stored users.apple_refresh_token_enc and revokes it.
func (c *AppleClient) RevokeRefreshToken(ctx context.Context, enc []byte) error {
	tok, err := c.Open(enc)
	if err != nil {
		return err
	}
	return c.Revoke(ctx, tok)
}

func (c *AppleClient) gcm() cipher.AEAD {
	b, _ := aes.NewCipher(c.encKey)
	g, _ := cipher.NewGCM(b)
	return g
}

func (c *AppleClient) Seal(plain string) []byte {
	g := c.gcm()
	nonce := make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	return g.Seal(nonce, nonce, []byte(plain), nil)
}

func (c *AppleClient) Open(enc []byte) (string, error) {
	g := c.gcm()
	if len(enc) < g.NonceSize() {
		return "", errors.New("apple refresh token: ciphertext too short")
	}
	p, err := g.Open(nil, enc[:g.NonceSize()], enc[g.NonceSize():], nil)
	if err != nil {
		return "", errors.New("apple refresh token: decrypt failed")
	}
	return string(p), nil
}
