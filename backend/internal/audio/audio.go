package audio

import (
	"errors"
	"io"
	"mime"
	"strings"

	"phoebe/internal/apperr"
)

const (
	M4A = "audio/mp4"
	MP3 = "audio/mpeg"
	WAV = "audio/wav"

	MinDurationMS        = 1000
	maxCompressedBitrate = 1_000_000
)

var aliases = map[string]string{
	"audio/mp4":   M4A,
	"audio/m4a":   M4A,
	"audio/x-m4a": M4A,
	"audio/mpeg":  MP3,
	"audio/wav":   WAV,
	"audio/x-wav": WAV,
	"audio/wave":  WAV,
}

// Normalize maps a declared content type (api.md §4.6 aliases, any case, parameters ignored) to its stored form.
func Normalize(contentType string) (string, bool) {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false
	}
	ct, ok := aliases[strings.ToLower(mt)]
	return ct, ok
}

func Ext(contentType string) string {
	switch contentType {
	case M4A:
		return "m4a"
	case MP3:
		return "mp3"
	case WAV:
		return "wav"
	}
	return ""
}

// Probe detects the real format by content and computes the duration (backend.md §7.4).
// Client-visible failures are *apperr.Error (audio_unsupported, audio_corrupt, audio_empty); other errors are I/O.
func Probe(r io.ReadSeeker, size int64) (contentType string, durationMS int64, err error) {
	if size <= 0 {
		return "", 0, apperr.New(apperr.AudioEmpty)
	}
	ra := readerAt{r}
	head := make([]byte, 12)
	n, err := ra.ReadAt(head[:min(int64(len(head)), size)], 0)
	if err != nil && !isEOF(err) {
		return "", 0, err
	}
	head = head[:n]

	var payload int64
	compressed := true
	switch {
	case n >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		contentType, compressed = WAV, false
		durationMS, err = probeWAV(ra, size)
	case n >= 8 && string(head[4:8]) == "ftyp":
		contentType = M4A
		durationMS, payload, err = probeM4A(ra, size)
	case looksMP3(head):
		contentType = MP3
		durationMS, payload, err = probeMP3(r, size)
	default:
		return "", 0, unsupported()
	}
	if err != nil {
		return "", 0, err
	}
	if compressed && payload*8000 > maxCompressedBitrate*durationMS {
		return "", 0, corrupt()
	}
	if durationMS < MinDurationMS {
		return "", 0, apperr.New(apperr.AudioEmpty)
	}
	return contentType, durationMS, nil
}

// CheckDuration enforces the strict upper limit (no tolerance) for declared and probed durations.
func CheckDuration(durationMS, maxDurationMS int64) error {
	if durationMS > maxDurationMS {
		return apperr.WithDetails(apperr.AudioTooLong, map[string]any{
			"duration_ms":     durationMS,
			"max_duration_ms": maxDurationMS,
		})
	}
	return nil
}

func corrupt() error     { return apperr.New(apperr.AudioCorrupt) }
func unsupported() error { return apperr.New(apperr.AudioUnsupported) }

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

type readerAt struct{ rs io.ReadSeeker }

func (r readerAt) ReadAt(p []byte, off int64) (int, error) {
	if _, err := r.rs.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r.rs, p)
}

// readFull treats a short read as corruption: every caller has already bounds-checked against size.
func readFull(r io.ReaderAt, p []byte, off int64) error {
	_, err := r.ReadAt(p, off)
	if err != nil && isEOF(err) {
		return corrupt()
	}
	return err
}
