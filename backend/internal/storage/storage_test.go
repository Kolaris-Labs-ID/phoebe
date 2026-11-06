package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"phoebe/internal/apperr"
)

func newFS(t *testing.T) *LocalFS {
	s, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func read(t *testing.T, s *LocalFS, key string) string {
	t.Helper()
	f, _, err := s.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return string(b)
}

func TestPutOpenStatMoveDelete(t *testing.T) {
	s := newFS(t)
	ctx := context.Background()
	n, err := s.Put(ctx, "u/1/m/2/exports/a.md", strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatal(n, err)
	}
	if got := read(t, s, "u/1/m/2/exports/a.md"); got != "hello" {
		t.Fatal(got)
	}
	st, _ := os.Stat(filepath.Join(s.root, "u/1/m/2/exports/a.md"))
	dt, _ := os.Stat(filepath.Join(s.root, "u/1/m/2/exports"))
	if st.Mode().Perm() != 0o600 || dt.Mode().Perm() != 0o700 {
		t.Fatalf("perms file=%v dir=%v", st.Mode().Perm(), dt.Mode().Perm())
	}
	if size, ok, err := s.Stat(ctx, "u/1/m/2/exports/a.md"); !ok || size != 5 || err != nil {
		t.Fatal("stat")
	}
	if _, ok, _ := s.Stat(ctx, "u/1/missing"); ok {
		t.Fatal("stat missing")
	}
	if _, _, err := s.Open(ctx, "u/1/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("open missing", err)
	}
	if err := s.Move(ctx, "u/1/m/2/exports/a.md", "u/1/m/2/audio/b.m4a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Move(ctx, "u/1/m/2/exports/a.md", "u/1/m/2/audio/b.m4a"); !errors.Is(err, ErrNotFound) {
		t.Fatal("repeat move should report missing src", err)
	}
	if read(t, s, "u/1/m/2/audio/b.m4a") != "hello" {
		t.Fatal("moved content")
	}
	if err := s.Delete(ctx, "u/1/m/2/audio/b.m4a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "u/1/m/2/audio/b.m4a"); err != nil {
		t.Fatal("delete missing", err)
	}
	_, _ = s.Put(ctx, "u/1/m/2/x", strings.NewReader("x"))
	_, _ = s.Put(ctx, "u/1/m/3/y", strings.NewReader("y"))
	if err := s.DeletePrefix(ctx, "u/1/m/2/"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Stat(ctx, "u/1/m/2/x"); ok {
		t.Fatal("prefix not deleted")
	}
	if _, ok, _ := s.Stat(ctx, "u/1/m/3/y"); !ok {
		t.Fatal("sibling deleted")
	}
	if err := s.DeletePrefix(ctx, "u/9/"); err != nil {
		t.Fatal("missing prefix", err)
	}
	if err := s.DeletePrefix(ctx, "u/1"); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("prefix without slash accepted")
	}
}

func TestKeyValidation(t *testing.T) {
	s := newFS(t)
	for _, k := range []string{"", "/etc/passwd", "../x", "a/../../x", "a//b", "a/./b", `a\b`, "a/"} {
		if _, err := s.Put(context.Background(), k, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("key %q accepted", k)
		}
	}
}

func TestAppendTruncatesAndCommits(t *testing.T) {
	s := newFS(t)
	ctx := context.Background()
	key := "u/1/m/2/uploads/s.part"
	var committed int64 = -1
	w, err := s.Append(ctx, key, 0, strings.NewReader("abcdef"), nil, func(n int64) error { committed = n; return nil })
	if err != nil || w != 6 || committed != 6 {
		t.Fatal(w, err, committed)
	}
	w, err = s.Append(ctx, key, 3, strings.NewReader("XY"), nil, nil)
	if err != nil || w != 2 || read(t, s, key) != "abcXY" {
		t.Fatalf("truncate to offset: %q", read(t, s, key))
	}
	if _, err := s.Append(ctx, key, 99, strings.NewReader("z"), nil, nil); !errors.Is(err, ErrShort) {
		t.Fatal("offset past end accepted", err)
	}
}

type failingReader struct{ data []byte }

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestAppendCommitsPersistedBytesOnReadError(t *testing.T) {
	s := newFS(t)
	var committed int64
	w, err := s.Append(context.Background(), "k", 0, &failingReader{[]byte("12345")}, nil,
		func(n int64) error { committed = n; return nil })
	if !errors.Is(err, io.ErrUnexpectedEOF) || w != 5 || committed != 5 || read(t, s, "k") != "12345" {
		t.Fatal(w, err, committed)
	}
}

func TestAppendRenewEvery4MiBAndAbort(t *testing.T) {
	s := newFS(t)
	data := bytes.Repeat([]byte{1}, 3*RenewEvery+10)
	var calls []int64
	w, err := s.Append(context.Background(), "k", 0, bytes.NewReader(data),
		func(n int64) error { calls = append(calls, n); return nil }, nil)
	if err != nil || w != int64(len(data)) || len(calls) != 3 {
		t.Fatal(w, err, calls)
	}
	lost := errors.New("lease lost")
	committed := false
	w, err = s.Append(context.Background(), "k", 0, bytes.NewReader(data),
		func(int64) error { return lost }, func(int64) error { committed = true; return nil })
	if !errors.Is(err, lost) || committed || w < RenewEvery || w >= int64(len(data)) {
		t.Fatal("renew failure must stop writing without commit", w, err, committed)
	}
}

func TestAppendBusyWhenLocked(t *testing.T) {
	s := newFS(t)
	_, _ = s.Put(context.Background(), "k", strings.NewReader("abc"))
	f, err := os.OpenFile(filepath.Join(s.root, "k"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(context.Background(), "k", 3, strings.NewReader("d"), nil, nil); !errors.Is(err, ErrBusy) {
		t.Fatal("expected ErrBusy", err)
	}
	if read(t, s, "k") != "abc" {
		t.Fatal("busy append modified file")
	}
}

func TestSigner(t *testing.T) {
	now := time.Unix(1762400000, 0)
	sg := NewSigner([]byte("0123456789abcdef0123456789abcdef"), "https://api.example.com/", 15*time.Minute)
	u, exp := sg.Sign("export", "e1", now)
	if !strings.HasPrefix(u, "https://api.example.com/v1/files/export/e1?exp=") || !exp.Equal(now.Add(15*time.Minute)) {
		t.Fatal(u, exp)
	}
	q := strings.SplitN(u, "?", 2)[1]
	var expS, sig string
	for _, kv := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(kv, "=")
		if k == "exp" {
			expS = v
		} else {
			sig = v
		}
	}
	if err := sg.Verify("export", "e1", expS, sig, now); err != nil {
		t.Fatal(err)
	}
	if !apperr.Is(sg.Verify("audio", "e1", expS, sig, now), apperr.InvalidSignature) ||
		!apperr.Is(sg.Verify("export", "e2", expS, sig, now), apperr.InvalidSignature) ||
		!apperr.Is(sg.Verify("export", "e1", expS+"1", sig, now), apperr.InvalidSignature) ||
		!apperr.Is(sg.Verify("export", "e1", expS, sig[:len(sig)-2]+"AA", now), apperr.InvalidSignature) ||
		!apperr.Is(sg.Verify("export", "e1", "x", sig, now), apperr.InvalidSignature) {
		t.Fatal("tampering not detected")
	}
	if !apperr.Is(sg.Verify("export", "e1", expS, sig, now.Add(15*time.Minute)), apperr.URLExpired) {
		t.Fatal("expiry not enforced")
	}
	other := NewSigner([]byte("ffffffffffffffffffffffffffffffff"), "https://api.example.com", 15*time.Minute)
	if !apperr.Is(other.Verify("export", "e1", expS, sig, now), apperr.InvalidSignature) {
		t.Fatal("wrong secret accepted")
	}
}

func TestWalk(t *testing.T) {
	s := newFS(t)
	ctx := context.Background()
	for _, k := range []string{"u/1/m/2/exports/a.md", "u/1/m/2/audio/b.wav", "u/3/x"} {
		if _, err := s.Put(ctx, k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	if err := s.Walk(ctx, func(key string, mod time.Time) error {
		if time.Since(mod) > time.Minute {
			t.Errorf("modTime %v", mod)
		}
		got = append(got, key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "u/1/m/2/audio/b.wav,u/1/m/2/exports/a.md,u/3/x" {
		t.Fatal(got)
	}
}
