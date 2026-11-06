package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"phoebe/internal/apperr"
)

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// Page is the api.md §1.3 list envelope; Items is never encoded as null.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func NewPage[T any](items []T, next *string) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, NextCursor: next}
}

// pageParams parses limit (default 50, 1..200) and cursor into dst (nil dst when the
// request has no cursor). Invalid values → 422 validation_failed field limit/cursor.
func pageParams(r *http.Request, dst any) (limit int, hasCursor bool, err error) {
	limit = DefaultPageLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false, apperr.Field("limit", apperr.ReasonInvalid)
		}
		if n < 1 || n > MaxPageLimit {
			return 0, false, apperr.Field("limit", apperr.ReasonOutOfRange)
		}
		limit = n
	}
	c := r.URL.Query().Get("cursor")
	if c == "" {
		return limit, false, nil
	}
	if err := decodeCursor(c, dst); err != nil {
		return 0, false, err
	}
	return limit, true, nil
}

// encodeCursor makes an opaque keyset cursor (base64url JSON of v).
func encodeCursor(v any) *string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func decodeCursor(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, v) != nil {
		return apperr.Field("cursor", apperr.ReasonInvalid)
	}
	return nil
}
