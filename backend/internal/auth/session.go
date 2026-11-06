package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/jsonx"
)

type Tokens struct {
	AccessToken           string     `json:"access_token"`
	AccessTokenExpiresAt  jsonx.Time `json:"access_token_expires_at"`
	RefreshToken          string     `json:"refresh_token"`
	RefreshTokenExpiresAt jsonx.Time `json:"refresh_token_expires_at"`
	SessionExpiresAt      jsonx.Time `json:"session_expires_at"`
}

func newSecret() (token string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	s := sha256.Sum256([]byte(token))
	return s[:]
}

// createSession opens a token family whose expiry is fixed at sign-in, plus its first pair.
func (s *Service) createSession(ctx context.Context, tx pgx.Tx, userID string) (*Tokens, error) {
	var sid string
	var exp time.Time
	if err := tx.QueryRow(ctx,
		`INSERT INTO sessions (user_id, expires_at) VALUES ($1, now() + make_interval(secs => $2)) RETURNING id, expires_at`,
		userID, s.cfg.SessionMaxAge.Seconds()).Scan(&sid, &exp); err != nil {
		return nil, err
	}
	_, t, err := s.issuePair(ctx, tx, sid, exp)
	return t, err
}

// The access token never outlives the session.
func (s *Service) issuePair(ctx context.Context, tx pgx.Tx, sessionID string, sessionExp time.Time) (string, *Tokens, error) {
	access, accessHash := newSecret()
	refresh, refreshHash := newSecret()
	var id string
	var accessExp time.Time
	err := tx.QueryRow(ctx,
		`INSERT INTO session_tokens (session_id, access_token_hash, access_expires_at, refresh_token_hash)
		 VALUES ($1, $2, LEAST(now() + make_interval(secs => $3), $4), $5) RETURNING id, access_expires_at`,
		sessionID, accessHash, s.cfg.AccessTokenTTL.Seconds(), sessionExp, refreshHash).Scan(&id, &accessExp)
	if err != nil {
		return "", nil, err
	}
	return id, &Tokens{
		AccessToken: access, AccessTokenExpiresAt: jsonx.NewTime(accessExp),
		RefreshToken: refresh, RefreshTokenExpiresAt: jsonx.NewTime(sessionExp),
		SessionExpiresAt: jsonx.NewTime(sessionExp),
	}, nil
}

// ResolveSession implements httpapi.SessionResolver.
func (s *Service) ResolveSession(ctx context.Context, token string) (string, string, error) {
	var uid, sid, tid string
	var revoked, sessionExpired, invalidated, accessExpired, used bool
	err := s.pool.QueryRow(ctx,
		`SELECT s.user_id, s.id, t.id, s.revoked_at IS NOT NULL OR u.deleted_at IS NOT NULL, s.expires_at <= now(),
		        t.invalidated_at IS NOT NULL, t.access_expires_at <= now(), t.access_used_at IS NOT NULL
		 FROM session_tokens t JOIN sessions s ON s.id = t.session_id JOIN users u ON u.id = s.user_id
		 WHERE t.access_token_hash = $1`,
		hashToken(token)).Scan(&uid, &sid, &tid, &revoked, &sessionExpired, &invalidated, &accessExpired, &used)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", "", unauth()
	case err != nil:
		return "", "", err
	case revoked:
		return "", "", apperr.New(apperr.SessionRevoked)
	case sessionExpired:
		return "", "", apperr.New(apperr.SessionExpired)
	case invalidated:
		// Only a grace replay invalidates a pair, so its use means two parties hold the family: revoke it.
		if _, err := s.pool.Exec(ctx,
			`UPDATE sessions SET revoked_at = now(), revoked_reason = 'refresh_reuse' WHERE id = $1 AND revoked_at IS NULL`, sid); err != nil {
			return "", "", err
		}
		s.log.WarnContext(ctx, "auth.refresh_reuse", "user_id", uid, "outcome", "session_revoked")
		return "", "", apperr.New(apperr.SessionRevoked)
	case accessExpired:
		return "", "", apperr.New(apperr.AccessTokenExpired)
	}
	if !used {
		if _, err := s.pool.Exec(ctx,
			`UPDATE session_tokens SET access_used_at = now() WHERE id = $1 AND access_used_at IS NULL`, tid); err != nil {
			return "", "", err
		}
	}
	return uid, sid, nil
}

// The session row lock serializes refreshes; a replay revokes the family unless rotated within REFRESH_REUSE_GRACE with an unused successor.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	if refreshToken == "" {
		return nil, apperr.Field("refresh_token", apperr.ReasonRequired)
	}
	var out *Tokens
	var reuse bool
	var uid string
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var tid, sid string
		var successor *string
		var revoked, sessionExpired, rotated, inGrace, invalidated bool
		var sessionExp time.Time
		err := tx.QueryRow(ctx,
			`SELECT t.id, s.id, s.user_id, s.expires_at, s.revoked_at IS NOT NULL OR u.deleted_at IS NOT NULL,
			        s.expires_at <= now(), t.rotated_at IS NOT NULL,
			        COALESCE(t.rotated_at > now() - make_interval(secs => $2), false),
			        t.successor_id, t.invalidated_at IS NOT NULL
			 FROM session_tokens t JOIN sessions s ON s.id = t.session_id JOIN users u ON u.id = s.user_id
			 WHERE t.refresh_token_hash = $1
			 FOR UPDATE OF s, t`,
			hashToken(refreshToken), s.cfg.RefreshReuseGrace.Seconds()).Scan(
			&tid, &sid, &uid, &sessionExp, &revoked, &sessionExpired, &rotated, &inGrace, &successor, &invalidated)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return unauth()
		case err != nil:
			return err
		case revoked:
			return apperr.New(apperr.SessionRevoked)
		case sessionExpired:
			return apperr.New(apperr.SessionExpired)
		case invalidated:
			reuse = true
		case !rotated:
		case inGrace && successor != nil:
			var unused bool
			if err := tx.QueryRow(ctx,
				`SELECT rotated_at IS NULL AND invalidated_at IS NULL AND access_used_at IS NULL
				 FROM session_tokens WHERE id = $1 FOR UPDATE`, *successor).Scan(&unused); err != nil {
				return err
			}
			if !unused {
				reuse = true
				break
			}
			if _, err := tx.Exec(ctx, `UPDATE session_tokens SET invalidated_at = now() WHERE id = $1`, *successor); err != nil {
				return err
			}
		default:
			reuse = true
		}
		if reuse {
			_, err := tx.Exec(ctx,
				`UPDATE sessions SET revoked_at = now(), revoked_reason = 'refresh_reuse' WHERE id = $1 AND revoked_at IS NULL`, sid)
			return err
		}
		nid, t, err := s.issuePair(ctx, tx, sid, sessionExp)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE session_tokens SET rotated_at = COALESCE(rotated_at, now()), successor_id = $2 WHERE id = $1`, tid, nid); err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	if reuse {
		s.log.WarnContext(ctx, "auth.refresh_reuse", "user_id", uid, "outcome", "session_revoked")
		return nil, apperr.New(apperr.SessionRevoked)
	}
	return out, nil
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now(), revoked_reason = 'logout' WHERE id = $1 AND revoked_at IS NULL`, sessionID)
	return err
}

// Ended sessions are kept so clients get session_expired / session_revoked rather than unauthenticated.
const SessionGrace = 7 * 24 * time.Hour

// Rotated rows of live sessions stay: they are what detects a replay.
func (s *Service) SessionSweep(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id IN (
		SELECT id FROM sessions WHERE expires_at < now() - make_interval(secs => $1)
		   OR revoked_at < now() - make_interval(secs => $1) LIMIT 1000)`, SessionGrace.Seconds())
	return int(tag.RowsAffected()), err
}
