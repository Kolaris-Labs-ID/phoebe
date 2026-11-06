package testutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/ids"
	"phoebe/internal/obs"
	"phoebe/internal/storage"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DB creates a fresh migrated database phoebe_test_<hex> for this test and drops it
// (WITH (FORCE)) on cleanup. Missing DATABASE_URL fails the test; never skips.
func DB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Fatal("DATABASE_URL is not set (see backend/README.md)")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("testutil: connect DATABASE_URL: %v", err)
	}
	name := "phoebe_test_" + randHex(8)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatalf("testutil: create database: %v", err)
	}
	admin.Close(ctx)

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("testutil: parse DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("testutil: open test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, err := pgx.Connect(context.Background(), base)
		if err != nil {
			t.Errorf("testutil: drop database: %v", err)
			return
		}
		defer c.Close(context.Background())
		if _, err := c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("testutil: drop database: %v", err)
		}
	})
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("testutil: migrate: %v", err)
	}
	return pool
}

func Storage(t testing.TB) *storage.LocalFS {
	t.Helper()
	s, err := storage.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Config returns a valid config with fake providers, dev login on, and test secrets.
// DATABASE_URL is taken from the env when set. Override fields freely.
func Config(t testing.TB) *config.Config {
	t.Helper()
	env := map[string]string{
		"DATABASE_URL":      os.Getenv("DATABASE_URL"),
		"PUBLIC_BASE_URL":   "http://phoebe.test",
		"SIGNING_SECRET":    strings.Repeat("ab", 32),
		"SUBJECT_PEPPER":    strings.Repeat("cd", 32),
		"DEV_LOGIN_ENABLED": "true",
		"STORAGE_DIR":       t.TempDir(),
		"WORKER_ID":         "test-worker",
	}
	if env["DATABASE_URL"] == "" {
		env["DATABASE_URL"] = "postgres://unset"
	}
	c, err := config.LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Logger writes JSON logs to the test log.
func Logger(t testing.TB) *slog.Logger {
	return obs.NewLogger(testWriter{t}, slog.LevelDebug)
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// DiscardLogger drops all log output.
func DiscardLogger() *slog.Logger { return obs.NewLogger(io.Discard, slog.LevelError+1) }

type User struct {
	ID    string
	Token string
}

// CreateUser inserts a dev user with an active trial entitlement (quotaSeconds) and a
// session; Token is a valid access token (session_tokens.access_token_hash = sha256(token)).
func CreateUser(t testing.TB, q db.Querier, quotaSeconds int64) User {
	t.Helper()
	ctx := context.Background()
	var u User
	if err := q.QueryRow(ctx,
		`INSERT INTO users (dev_sub) VALUES ($1) RETURNING id`, "u"+randHex(6)).Scan(&u.ID); err != nil {
		t.Fatalf("testutil: insert user: %v", err)
	}
	if _, err := q.Exec(ctx,
		`INSERT INTO entitlements (user_id, plan, quota_seconds, period_start, period_end)
		 VALUES ($1, 'trial', $2, now() - interval '1 minute', now() + interval '30 days')`,
		u.ID, quotaSeconds); err != nil {
		t.Fatalf("testutil: insert entitlement: %v", err)
	}
	u.Token = CreateSession(t, q, u.ID)
	return u
}

func CreateSession(t testing.TB, q db.Querier, userID string) string {
	t.Helper()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := "phb_" + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	if _, err := q.Exec(context.Background(),
		`WITH s AS (INSERT INTO sessions (user_id, expires_at) VALUES ($1, now() + interval '1 day') RETURNING id)
		 INSERT INTO session_tokens (session_id, access_token_hash, access_expires_at, refresh_token_hash)
		 SELECT id, $2, now() + interval '1 day', sha256(gen_random_uuid()::text::bytea) FROM s`,
		userID, sum[:]); err != nil {
		t.Fatalf("testutil: insert session: %v", err)
	}
	return token
}

// CreateMeeting inserts a meeting in awaiting_audio and returns its id.
func CreateMeeting(t testing.TB, q db.Querier, userID string) string {
	t.Helper()
	id := ids.New()
	if _, err := q.Exec(context.Background(),
		`INSERT INTO meetings (id, user_id, timezone, started_at) VALUES ($1, $2, 'Asia/Jakarta', $3)`,
		id, userID, time.Now().UTC().Truncate(time.Millisecond)); err != nil {
		t.Fatalf("testutil: insert meeting: %v", err)
	}
	return id
}

// WAV returns a valid PCM WAV (16-bit mono, 8 kHz, 440 Hz tone) of durationMS,
// i.e. 16 000 bytes per second of audio plus a 44-byte header.
func WAV(durationMS int64) []byte {
	const rate, channels, bits = 8000, 1, 16
	samples := durationMS * rate / 1000
	dataSize := samples * channels * bits / 8
	buf := make([]byte, 44+dataSize)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataSize))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], channels)
	binary.LittleEndian.PutUint32(buf[24:], rate)
	binary.LittleEndian.PutUint32(buf[28:], rate*channels*bits/8)
	binary.LittleEndian.PutUint16(buf[32:], channels*bits/8)
	binary.LittleEndian.PutUint16(buf[34:], bits)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataSize))
	for i := int64(0); i < samples; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(buf[44+2*i:], uint16(v))
	}
	return buf
}

func SHA256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
