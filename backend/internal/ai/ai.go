package ai

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"phoebe/internal/jobs"
)

type STT interface {
	Name() (provider, model string)
	Transcribe(ctx context.Context, in STTInput) (STTResult, error)
}

// DurationMS is the server-validated duration of the audio in Reader; real adapters ignore it,
// the fake derives its segments from it.
type STTInput struct {
	Reader      io.Reader
	Filename    string
	ContentType string
	Language    string
	DurationMS  int64
}

type RawSegment struct {
	StartSec, EndSec                           float64
	Text                                       string
	NoSpeechProb, AvgLogprob, CompressionRatio float64
}

type STTResult struct {
	Language string
	Segments []RawSegment
	AudioMS  int64
}

type LLM interface {
	Name() (provider, model string)
	Complete(ctx context.Context, system, user string) (LLMResult, error)
}

type LLMResult struct {
	Content      string
	FinishReason string
	InputTokens  int
	OutputTokens int
}

// Error carries only the category and HTTP status; never provider bodies (backend.md §15).
type Error struct {
	Category   string
	Retryable  bool
	RetryAfter time.Duration
	HTTPStatus int
	note       string
	err        error
}

func (e *Error) Error() string {
	if d := e.Detail(); d != "" {
		return "ai: " + e.Category + " (" + d + ")"
	}
	return "ai: " + e.Category
}

func (e *Error) Unwrap() error { return e.err }

func (e *Error) Detail() string {
	var parts []string
	if e.HTTPStatus != 0 {
		parts = append(parts, "http "+strconv.Itoa(e.HTTPStatus))
	}
	if e.note != "" {
		parts = append(parts, e.note)
	}
	return strings.Join(parts, " ")
}

func NewError(category string, retryable bool, httpStatus int, note string) *Error {
	return &Error{Category: category, Retryable: retryable, HTTPStatus: httpStatus, note: note}
}

func InvalidOutput(httpStatus int, note string) *Error {
	return NewError(jobs.CatInvalidOutput, true, httpStatus, note)
}

// StatusError classifies a non-2xx provider response.
func StatusError(status int, h http.Header, now time.Time) *Error {
	switch {
	case status == http.StatusTooManyRequests:
		e := NewError(jobs.CatProviderRateLimited, true, status, "")
		e.RetryAfter = ParseRetryAfter(h.Get("Retry-After"), now)
		return e
	case status == http.StatusRequestTimeout || status >= 500:
		return NewError(jobs.CatProviderUnavailable, true, status, "")
	case status >= 400:
		return NewError(jobs.CatProviderRejected, false, status, "")
	default:
		return NewError(jobs.CatProviderUnavailable, true, status, "")
	}
}

// TransportError classifies a failed round trip. ctx is the per-call context.
func TransportError(ctx context.Context, err error) *Error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout()) {
		e := NewError(jobs.CatProviderTimeout, true, 0, "")
		e.err = err
		return e
	}
	e := NewError(jobs.CatProviderUnavailable, true, 0, "network")
	e.err = err
	return e
}

func ParseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		if s <= 0 || math.IsNaN(s) || math.IsInf(s, 0) {
			return 0
		}
		return time.Duration(math.Ceil(s * float64(time.Second)))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// CategoryOf is the ai_calls.error_category for a failed call.
func CategoryOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Category
	}
	return jobs.CatInternal
}

// JobError converts an adapter error into a handler outcome for the job runner.
func JobError(err error) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	if e.Retryable {
		return jobs.Retryable(e.Category, e.Detail(), e.RetryAfter)
	}
	return jobs.Permanent(e.Category, e.Detail())
}

// LLMCostMicros: prices are USD per million tokens, so micro-USD per token equals the price.
func LLMCostMicros(inputTokens, outputTokens int, priceInPerMTok, priceOutPerMTok float64) int64 {
	return nonNegRound(float64(inputTokens)*priceInPerMTok + float64(outputTokens)*priceOutPerMTok)
}

func STTCostMicros(audioMS int64, pricePerMin float64) int64 {
	return nonNegRound(float64(audioMS) / 60000 * pricePerMin * 1e6)
}

func nonNegRound(v float64) int64 {
	if v <= 0 || math.IsNaN(v) {
		return 0
	}
	return int64(math.Round(v))
}
