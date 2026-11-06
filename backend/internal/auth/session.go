package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/db"
)

const tokenPrefix = "phb_"

func newToken() (token string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	s := sha256.Sum256([]byte(token))
	return s[:]
}

// CreateSession inserts a session and returns the opaque token (only its sha256 is stored).
func CreateSession(ctx context.Context, q db.Querier, userID string, ttl time.Duration) (string, time.Time, error) {
	token, hash := newToken()
	var exp time.Time
	err := q.QueryRow(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + make_interval(secs => $3)) RETURNING expires_at`,
		userID, hash, ttl.Seconds()).Scan(&exp)
	return token, exp, err
}

// ResolveSession implements httpapi.SessionResolver.
func (s *Service) ResolveSession(ctx context.Context, token string) (string, string, error) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return "", "", unauth()
	}
	var uid, sid string
	err := s.pool.QueryRow(ctx,
		`SELECT s.user_id, s.id FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at > now() AND u.deleted_at IS NULL`,
		hashToken(token)).Scan(&uid, &sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", unauth()
	}
	if err != nil {
		return "", "", err
	}
	return uid, sid, nil
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, sessionID)
	return err
}
