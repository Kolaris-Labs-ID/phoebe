package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const TimeLayout = "2006-01-02T15:04:05.000Z07:00"

func FormatTime(t time.Time) string { return t.UTC().Format(TimeLayout) }

// Time marshals as RFC3339 UTC with millisecond fraction (api.md §1) and accepts any
// RFC3339 offset on input, normalized to UTC and truncated to milliseconds.
type Time struct{ time.Time }

func NewTime(t time.Time) Time { return Time{t.UTC().Truncate(time.Millisecond)} }

// TimePtr maps a nullable DB timestamp to a nullable JSON timestamp.
func TimePtr(t *time.Time) *Time {
	if t == nil {
		return nil
	}
	v := NewTime(*t)
	return &v
}

func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(`"` + FormatTime(t.Time) + `"`), nil
}

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	p, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	t.Time = p.UTC().Truncate(time.Millisecond)
	return nil
}

// Opt distinguishes an absent key (Set=false) from explicit null (Set, Null) for PATCH
// semantics and for "nullable keys must be present" checks.
type Opt[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

func (o Opt[T]) MarshalJSON() ([]byte, error) {
	if !o.Set || o.Null {
		return []byte("null"), nil
	}
	return json.Marshal(o.Value)
}

var errTrailing = errors.New("jsonx: trailing data after JSON value")

// Decode is strict about syntax and types but ignores unknown fields (api.md §1).
func Decode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errTrailing
	}
	return nil
}
