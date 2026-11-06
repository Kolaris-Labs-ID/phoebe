package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"

	"phoebe/internal/apperr"
)

// Signer issues and verifies signed file URLs (backend.md §12):
// sig = base64url_nopad(HMAC-SHA256(secret, kind + "\n" + id + "\n" + exp)).
type Signer struct {
	secret  []byte
	baseURL string
	ttl     time.Duration
}

func NewSigner(secret []byte, publicBaseURL string, ttl time.Duration) *Signer {
	return &Signer{secret: secret, baseURL: strings.TrimRight(publicBaseURL, "/"), ttl: ttl}
}

func (s *Signer) mac(kind, id string, exp int64) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(kind + "\n" + id + "\n" + strconv.FormatInt(exp, 10)))
	return m.Sum(nil)
}

// Sign returns PUBLIC_BASE_URL/v1/files/{kind}/{id}?exp=&sig= valid until now+TTL.
// kind is "audio" or "export". Never store or log the URL.
func (s *Signer) Sign(kind, id string, now time.Time) (string, time.Time) {
	expAt := now.Add(s.ttl).Truncate(time.Second)
	exp := expAt.Unix()
	q := url.Values{}
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("sig", base64.RawURLEncoding.EncodeToString(s.mac(kind, id, exp)))
	return s.baseURL + "/v1/files/" + kind + "/" + id + "?" + q.Encode(), expAt
}

// Verify checks signature first (→ invalid_signature), then expiry (→ url_expired).
// The caller then checks liveness of the object (→ not_found).
func (s *Signer) Verify(kind, id, exp, sig string, now time.Time) error {
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return apperr.New(apperr.InvalidSignature)
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac(kind, id, e)) {
		return apperr.New(apperr.InvalidSignature)
	}
	if now.Unix() >= e {
		return apperr.New(apperr.URLExpired)
	}
	return nil
}
