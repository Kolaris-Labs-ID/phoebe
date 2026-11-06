package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"phoebe/internal/apperr"
	"phoebe/internal/ids"
	"phoebe/internal/jsonx"
)

// decodeJSON reads the (size-limited) body into v and returns the raw bytes for
// fingerprinting. Empty body decodes as {}. Syntax/type errors → invalid_json; over the
// limit → payload_too_large. Unknown fields are ignored.
func decodeJSON(r *http.Request, v any) ([]byte, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, apperr.New(apperr.PayloadTooLarge)
		}
		return nil, err
	}
	b := raw
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}")
	}
	if err := jsonx.Decode(b, v); err != nil {
		return nil, apperr.New(apperr.InvalidJSON)
	}
	return raw, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeNoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// pathUUID returns the lowercased UUID path value; a non-UUID can never name a resource → 404.
func pathUUID(r *http.Request, name string) (string, error) {
	id, ok := ids.Parse(r.PathValue(name))
	if !ok {
		return "", apperr.ErrNotFound
	}
	return id, nil
}

// idempotencyKey parses the required Idempotency-Key header (api.md §1.4).
func idempotencyKey(r *http.Request) (string, error) {
	k, ok := ids.Parse(r.Header.Get("Idempotency-Key"))
	if !ok {
		return "", apperr.New(apperr.MissingIdempotencyKey)
	}
	return k, nil
}
