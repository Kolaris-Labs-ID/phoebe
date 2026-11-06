package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/testutil"
)

func TestMigrateIdempotentAndConcurrent(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { errs[i] = db.Migrate(ctx, pool) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = 1").Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_migrations rows=%d err=%v", n, err)
	}
	var view int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM daily_ai_cost").Scan(&view); err != nil {
		t.Fatalf("schema incomplete: %v", err)
	}
}

func TestInTxCommitRollback(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO trial_grants (subject_hmac) VALUES (decode(repeat('aa', 32), 'hex'))"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = db.InTx(ctx, pool, func(tx pgx.Tx) error {
			_, _ = tx.Exec(ctx, "INSERT INTO trial_grants (subject_hmac) VALUES (decode(repeat('bb', 32), 'hex'))")
			panic("x")
		})
	}()
	var n int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM trial_grants").Scan(&n)
	if n != 0 {
		t.Fatalf("rollback failed, rows=%d", n)
	}
	if err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO trial_grants (subject_hmac) VALUES (decode(repeat('aa', 32), 'hex'))")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM trial_grants").Scan(&n)
	if n != 1 {
		t.Fatal("commit failed")
	}
}

func TestErrorHelpers(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	u := testutil.CreateUser(t, pool, 60)
	_, err := pool.Exec(ctx, "INSERT INTO folders (user_id, name) VALUES ($1, 'A'), ($1, 'a')", u.ID)
	if !db.IsUniqueViolation(err, "folders_user_name_key") || !db.IsUniqueViolation(err, "") || db.IsUniqueViolation(err, "other") {
		t.Fatalf("unique: %v (%s)", err, db.ConstraintName(err))
	}
	_, err = pool.Exec(ctx, "INSERT INTO folders (user_id, name) VALUES ($1, 'x')", "00000000-0000-4000-8000-000000000000")
	if !db.IsForeignKeyViolation(err, "") {
		t.Fatalf("fk: %v", err)
	}
	_, err = pool.Exec(ctx, "INSERT INTO folders (user_id, name) VALUES ($1, ' x')", u.ID)
	if !db.IsCheckViolation(err, "") {
		t.Fatalf("check: %v", err)
	}
	if db.IsUnavailable(err) || !db.IsUnavailable(&pgconn.PgError{Code: "08006"}) {
		t.Fatal("unavailable classification")
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = db.Open(cctx, "postgres://phoebe:phoebe@127.0.0.1:1/phoebe?sslmode=disable&connect_timeout=1")
	if !db.IsUnavailable(err) {
		t.Fatalf("connect error not unavailable: %v", err)
	}
}

func TestMeetingLocks(t *testing.T) {
	pool := testutil.DB(t)
	ctx := context.Background()
	a := testutil.CreateUser(t, pool, 60)
	b := testutil.CreateUser(t, pool, 60)
	m := testutil.CreateMeeting(t, pool, a.ID)

	err := db.InTx(ctx, pool, func(tx pgx.Tx) error { return db.LockMeeting(ctx, tx, m, a.ID) })
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTx(ctx, pool, func(tx pgx.Tx) error { return db.LockMeeting(ctx, tx, m, b.ID) })
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("other owner: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if uid, err := db.LockLiveMeeting(ctx, tx, m); err != nil || uid != a.ID {
		t.Fatalf("LockLiveMeeting %s %v", uid, err)
	}
	ok, err := db.TryLockLiveMeeting(ctx, pool, m)
	if err != nil || ok {
		t.Fatalf("SKIP LOCKED should skip a held lock: ok=%v err=%v", ok, err)
	}
	_ = tx.Rollback(ctx)

	var before time.Time
	_ = pool.QueryRow(ctx, "SELECT updated_at FROM meetings WHERE id=$1", m).Scan(&before)
	if err := db.TouchMeeting(ctx, pool, m); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	_ = pool.QueryRow(ctx, "SELECT updated_at FROM meetings WHERE id=$1", m).Scan(&after)
	if !after.After(before) {
		t.Fatal("TouchMeeting did not bump updated_at")
	}

	if _, err := pool.Exec(ctx, "UPDATE meetings SET deleted_at = now() WHERE id=$1", m); err != nil {
		t.Fatal(err)
	}
	err = db.InTx(ctx, pool, func(tx pgx.Tx) error { return db.LockMeeting(ctx, tx, m, a.ID) })
	if !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("tombstoned: %v", err)
	}
	if _, err := db.LockLiveMeeting(ctx, pool, m); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("tombstoned live lock: %v", err)
	}
	if ok, _ := db.TryLockLiveMeeting(ctx, pool, m); ok {
		t.Fatal("tombstoned try lock")
	}
}
