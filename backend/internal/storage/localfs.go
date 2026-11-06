package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// LocalFS stores objects as files under root (STORAGE_DIR); files 0600, dirs 0700.
type LocalFS struct{ root string }

var _ Storage = (*LocalFS)(nil)

func NewLocalFS(root string) (*LocalFS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &LocalFS{root: abs}, nil
}

func (s *LocalFS) path(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || strings.Contains(key, "\x00") {
		return "", ErrInvalidKey
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrInvalidKey
		}
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

func (s *LocalFS) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	p, err := s.path(key)
	if err != nil {
		return 0, err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(dir, ".tmp-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, ctxReader{ctx, r})
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

func (s *LocalFS) Append(ctx context.Context, key string, offset int64, r io.Reader,
	renew func(written int64) error, commit func(written int64) error) (int64, error) {
	p, err := s.path(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return 0, ErrBusy
		}
		return 0, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() < offset {
		return 0, ErrShort
	}
	if err := f.Truncate(offset); err != nil {
		return 0, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}

	var written, sinceRenew int64
	var ioErr error
	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			ioErr = err
			break
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				ioErr = werr
				break
			}
			written += int64(n)
			sinceRenew += int64(n)
			if sinceRenew >= RenewEvery && renew != nil {
				sinceRenew = 0
				if err := renew(written); err != nil {
					_ = f.Sync()
					return written, err
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			ioErr = rerr
			break
		}
	}
	if err := f.Sync(); err != nil {
		return written, err
	}
	if commit != nil {
		if err := commit(written); err != nil {
			return written, err
		}
	}
	return written, ioErr
}

func (s *LocalFS) Open(ctx context.Context, key string) (io.ReadSeekCloser, int64, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (s *LocalFS) Stat(ctx context.Context, key string) (int64, bool, error) {
	p, err := s.path(key)
	if err != nil {
		return 0, false, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return st.Size(), true, nil
}

func (s *LocalFS) Move(ctx context.Context, src, dst string) error {
	sp, err := s.path(src)
	if err != nil {
		return err
	}
	dp, err := s.path(dst)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dp), 0o700); err != nil {
		return err
	}
	err = os.Rename(sp, dp)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

func (s *LocalFS) Delete(ctx context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *LocalFS) DeletePrefix(ctx context.Context, prefix string) error {
	if !strings.HasSuffix(prefix, "/") {
		return ErrInvalidKey
	}
	p, err := s.path(path.Clean(prefix))
	if err != nil {
		return err
	}
	return os.RemoveAll(p)
}

func (s *LocalFS) Walk(ctx context.Context, fn func(key string, modTime time.Time) error) error {
	return filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), info.ModTime())
	})
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
