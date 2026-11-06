package auth

import (
	"context"
	"errors"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/jsonx"
	"phoebe/internal/usage"
	"phoebe/internal/users"
)

type Service struct {
	pool     *pgxpool.Pool
	cfg      *config.Config
	log      *slog.Logger
	verifier *Verifier
	apple    *AppleClient
	usage    *usage.Service
}

// NewService: apple may be nil (exchange/revoke not configured).
func NewService(pool *pgxpool.Pool, cfg *config.Config, log *slog.Logger, apple *AppleClient) *Service {
	return &Service{pool: pool, cfg: cfg, log: log, verifier: NewVerifier(cfg.AppleJWKSURL, cfg.AppleBundleID), apple: apple,
		usage: usage.New(pool, cfg, log)}
}

type Result struct {
	Token     string      `json:"token"`
	ExpiresAt jsonx.Time  `json:"expires_at"`
	Created   bool        `json:"created"`
	User      *users.User `json:"user"`
}

type AppleSignIn struct {
	IdentityToken     string  `json:"identity_token"`
	Nonce             string  `json:"nonce"`
	DisplayName       *string `json:"display_name"`
	AuthorizationCode *string `json:"authorization_code"`
}

func (s *Service) SignInApple(ctx context.Context, in AppleSignIn) (*Result, error) {
	var fields []apperr.FieldError
	if in.IdentityToken == "" {
		fields = append(fields, apperr.FieldError{Field: "identity_token", Reason: apperr.ReasonRequired})
	}
	if in.Nonce == "" {
		fields = append(fields, apperr.FieldError{Field: "nonce", Reason: apperr.ReasonRequired})
	}
	name := ""
	if in.DisplayName != nil {
		n, fe := users.NormalizeDisplayName("display_name", *in.DisplayName)
		if fe != nil {
			fields = append(fields, *fe)
		}
		name = n
	}
	if len(fields) > 0 {
		return nil, apperr.Validation(fields...)
	}
	sub, err := s.verifier.Verify(ctx, in.IdentityToken, in.Nonce)
	if err != nil {
		return nil, err
	}
	var refreshEnc []byte
	if s.apple != nil && in.AuthorizationCode != nil && *in.AuthorizationCode != "" {
		rt, err := s.apple.Exchange(ctx, *in.AuthorizationCode)
		if err != nil {
			s.log.WarnContext(ctx, "auth.apple_exchange", "outcome", "error")
		} else {
			refreshEnc = s.apple.Seal(rt)
		}
	}
	return s.signIn(ctx, identity{column: "apple_sub", kind: "apple", sub: sub}, name, refreshEnc)
}

var devSubject = regexp.MustCompile(`^[a-z0-9._-]+$`)

func (s *Service) SignInDev(ctx context.Context, subject string) (*Result, error) {
	switch {
	case subject == "":
		return nil, apperr.Field("subject", apperr.ReasonRequired)
	case len(subject) > 64:
		return nil, apperr.Field("subject", apperr.ReasonTooLong)
	case !devSubject.MatchString(subject):
		return nil, apperr.Field("subject", apperr.ReasonInvalid)
	}
	return s.signIn(ctx, identity{column: "dev_sub", kind: "dev", sub: subject}, "", nil)
}

type identity struct {
	column string // trusted constant: apple_sub | dev_sub
	kind   string
	sub    string
}

// signIn finds or creates the live user for the identity and opens a session in one tx.
// A concurrent first sign-in loses the unique index race and retries as an existing user.
func (s *Service) signIn(ctx context.Context, id identity, name string, refreshEnc []byte) (*Result, error) {
	var res *Result
	var err error
	for range 2 {
		err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			r, err := s.signInTx(ctx, tx, id, name, refreshEnc)
			res = r
			return err
		})
		if !db.IsUniqueViolation(err, "users_"+id.column+"_key") {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) signInTx(ctx context.Context, tx pgx.Tx, id identity, name string, refreshEnc []byte) (*Result, error) {
	var uid string
	created := false
	err := tx.QueryRow(ctx,
		`SELECT id FROM users WHERE `+id.column+`=$1 AND deleted_at IS NULL FOR UPDATE`, id.sub).Scan(&uid)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		created = true
		if err := tx.QueryRow(ctx,
			`INSERT INTO users (`+id.column+`, display_name, apple_refresh_token_enc) VALUES ($1, $2, $3) RETURNING id`,
			id.sub, name, refreshEnc).Scan(&uid); err != nil {
			return nil, err
		}
		if err := s.grantEntitlement(ctx, tx, uid, id); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if _, err := tx.Exec(ctx,
			`UPDATE users SET display_name = CASE WHEN display_name = '' THEN $2 ELSE display_name END,
			        apple_refresh_token_enc = COALESCE($3, apple_refresh_token_enc), updated_at = now()
			 WHERE id=$1`, uid, name, refreshEnc); err != nil {
			return nil, err
		}
	}
	token, exp, err := CreateSession(ctx, tx, uid, s.cfg.SessionTTL)
	if err != nil {
		return nil, err
	}
	u, err := users.Get(ctx, tx, uid)
	if err != nil {
		return nil, err
	}
	return &Result{Token: token, ExpiresAt: jsonx.NewTime(exp), Created: created, User: u}, nil
}

// grantEntitlement: one trial per identity ever (trial_grants survives account purge).
func (s *Service) grantEntitlement(ctx context.Context, tx pgx.Tx, uid string, id identity) error {
	_, err := s.usage.CreateEntitlement(ctx, tx, uid, usage.SubjectHMAC(s.cfg.SubjectPepper, id.kind, id.sub))
	return err
}
