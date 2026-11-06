package db

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is satisfied by *pgxpool.Pool, pgx.Tx and *pgx.Conn, so domain functions
// compose into the caller's transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// InTx runs fn in a READ COMMITTED transaction; commits when fn returns nil, otherwise
// rolls back and returns fn's error. Panics roll back and re-panic.
func InTx(ctx context.Context, b TxBeginner, fn func(tx pgx.Tx) error) (err error) {
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func pgError(err error) *pgconn.PgError {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}

// IsUniqueViolation reports a 23505; constraint "" matches any constraint or unique index name.
func IsUniqueViolation(err error, constraint string) bool {
	pe := pgError(err)
	return pe != nil && pe.Code == "23505" && (constraint == "" || pe.ConstraintName == constraint)
}

func IsForeignKeyViolation(err error, constraint string) bool {
	pe := pgError(err)
	return pe != nil && pe.Code == "23503" && (constraint == "" || pe.ConstraintName == constraint)
}

func IsCheckViolation(err error, constraint string) bool {
	pe := pgError(err)
	return pe != nil && pe.Code == "23514" && (constraint == "" || pe.ConstraintName == constraint)
}

func ConstraintName(err error) string {
	if pe := pgError(err); pe != nil {
		return pe.ConstraintName
	}
	return ""
}

// IsUnavailable reports errors meaning the database cannot be reached (→ 503 unavailable).
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return true
	}
	if pe := pgError(err); pe != nil {
		return strings.HasPrefix(pe.Code, "08") || pe.Code == "57P01" || pe.Code == "57P03" || pe.Code == "53300"
	}
	var ne net.Error
	return errors.As(err, &ne)
}
