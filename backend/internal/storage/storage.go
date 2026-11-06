package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound   = errors.New("storage: object not found")
	ErrBusy       = errors.New("storage: object locked by another writer")
	ErrInvalidKey = errors.New("storage: invalid key")
	ErrShort      = errors.New("storage: object shorter than append offset")
)

// RenewEvery is how many appended bytes pass between renew callbacks (backend.md §7.2).
const RenewEvery = 4 << 20

// Storage is private object storage (backend.md §12). Keys are slash-separated relative
// paths such as u/<uid>/m/<mid>/audio/<id>.m4a.
type Storage interface {
	// Put writes atomically (temp + rename) and returns the size written.
	Put(ctx context.Context, key string, r io.Reader) (size int64, err error)
	// Append takes an exclusive non-blocking lock on key (held → ErrBusy), truncates it to
	// offset, streams r, and fsyncs. renew is called after every RenewEvery bytes; a renew
	// error stops writing and is returned. commit, when non-nil, runs after fsync while the
	// lock is still held, with the bytes persisted by this call (also after a read error
	// from r, e.g. client disconnect); it is skipped when locking/truncation or renew fail.
	// Returned err is the first of: lock/open/truncate error, renew error, read/write
	// error, commit error.
	Append(ctx context.Context, key string, offset int64, r io.Reader,
		renew func(written int64) error, commit func(written int64) error) (written int64, err error)
	// Open returns ErrNotFound when missing.
	Open(ctx context.Context, key string) (io.ReadSeekCloser, int64, error)
	Stat(ctx context.Context, key string) (size int64, exists bool, err error)
	// Move renames atomically, overwriting dst; missing src → ErrNotFound.
	Move(ctx context.Context, src, dst string) error
	// Delete: missing = nil.
	Delete(ctx context.Context, key string) error
	// DeletePrefix removes everything under prefix, which must end in "/"; missing = nil.
	DeletePrefix(ctx context.Context, prefix string) error
	// Walk calls fn for every stored object (orphan sweep); objects removed mid-walk are skipped.
	Walk(ctx context.Context, fn func(key string, modTime time.Time) error) error
}
