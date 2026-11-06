package users

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/jsonx"
	"phoebe/internal/storage"
)

const MaxDisplayName = 100

type User struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Language    string     `json:"language"`
	Timezone    string     `json:"timezone"`
	CreatedAt   jsonx.Time `json:"created_at"`
}

// AppleRevoker revokes Apple tokens at account purge; implemented by *auth.AppleClient
// (nil when Apple credentials are not configured).
type AppleRevoker interface {
	RevokeRefreshToken(ctx context.Context, enc []byte) error
}

// Service owns profile reads/writes; account deletion and purge_account (delete.go) use
// the storage, apple and log dependencies.
type Service struct {
	pool    *pgxpool.Pool
	cfg     *config.Config
	storage storage.Storage
	apple   AppleRevoker
	log     *slog.Logger
}

func New(pool *pgxpool.Pool, cfg *config.Config, store storage.Storage, apple AppleRevoker, log *slog.Logger) *Service {
	return &Service{pool: pool, cfg: cfg, storage: store, apple: apple, log: log}
}

// Get returns a live (not tombstoned) user; miss → not_found.
func Get(ctx context.Context, q db.Querier, id string) (*User, error) {
	var u User
	var created time.Time
	err := q.QueryRow(ctx,
		`SELECT id, display_name, language, timezone, created_at FROM users WHERE id=$1 AND deleted_at IS NULL`,
		id).Scan(&u.ID, &u.DisplayName, &u.Language, &u.Timezone, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(apperr.NotFound)
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = jsonx.NewTime(created)
	return &u, nil
}

func (s *Service) Get(ctx context.Context, userID string) (*User, error) {
	return Get(ctx, s.pool, userID)
}

type Patch struct {
	DisplayName jsonx.Opt[string] `json:"display_name"`
	Language    jsonx.Opt[string] `json:"language"`
	Timezone    jsonx.Opt[string] `json:"timezone"`
}

// NormalizeDisplayName trims and validates a display name (≤ 100 characters).
func NormalizeDisplayName(field, s string) (string, *apperr.FieldError) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxDisplayName {
		return "", &apperr.FieldError{Field: field, Reason: apperr.ReasonTooLong}
	}
	return s, nil
}

func validTimezone(tz string) bool {
	if tz == "" || len(tz) > 64 || tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

func (s *Service) Update(ctx context.Context, userID string, p Patch) (*User, error) {
	var fields []apperr.FieldError
	var name, lang, tz *string
	if p.DisplayName.Set {
		if p.DisplayName.Null {
			fields = append(fields, apperr.FieldError{Field: "display_name", Reason: apperr.ReasonInvalid})
		} else if v, fe := NormalizeDisplayName("display_name", p.DisplayName.Value); fe != nil {
			fields = append(fields, *fe)
		} else {
			name = &v
		}
	}
	if p.Language.Set {
		if p.Language.Null || (p.Language.Value != "id" && p.Language.Value != "en") {
			fields = append(fields, apperr.FieldError{Field: "language", Reason: apperr.ReasonInvalid})
		} else {
			lang = &p.Language.Value
		}
	}
	if p.Timezone.Set {
		if p.Timezone.Null || !validTimezone(p.Timezone.Value) {
			fields = append(fields, apperr.FieldError{Field: "timezone", Reason: apperr.ReasonInvalid})
		} else {
			tz = &p.Timezone.Value
		}
	}
	if len(fields) > 0 {
		return nil, apperr.Validation(fields...)
	}
	if name == nil && lang == nil && tz == nil {
		return Get(ctx, s.pool, userID)
	}
	var u User
	var created time.Time
	err := s.pool.QueryRow(ctx,
		`UPDATE users SET display_name = COALESCE($2, display_name), language = COALESCE($3, language),
		        timezone = COALESCE($4, timezone), updated_at = now()
		 WHERE id=$1 AND deleted_at IS NULL
		 RETURNING id, display_name, language, timezone, created_at`,
		userID, name, lang, tz).Scan(&u.ID, &u.DisplayName, &u.Language, &u.Timezone, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.New(apperr.NotFound)
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = jsonx.NewTime(created)
	return &u, nil
}
