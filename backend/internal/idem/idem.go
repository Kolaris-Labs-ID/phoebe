package idem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CanonicalJSON re-encodes a JSON body with object keys sorted and number literals kept
// as received. An empty body is treated as {} (absent optional body).
func CanonicalJSON(body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return []byte("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// RequestFingerprint = sha256hex(route_pattern + "\n" + meeting_id + "\n" + canonical_json(body))
// (backend.md §8). routePattern e.g. "POST /v1/meetings/{id}/transcription".
func RequestFingerprint(routePattern, meetingID string, body []byte) (string, error) {
	c, err := CanonicalJSON(body)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(routePattern + "\n" + meetingID + "\n"))
	h.Write(c)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MutationFingerprint = sha256hex(canonical_json(body)) (backend.md §8.2).
func MutationFingerprint(body []byte) (string, error) {
	c, err := CanonicalJSON(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}
