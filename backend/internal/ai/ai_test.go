package ai

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"phoebe/internal/jobs"
)

func TestStatusErrorClassification(t *testing.T) {
	now := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)
	cases := []struct {
		status    int
		category  string
		retryable bool
	}{
		{500, jobs.CatProviderUnavailable, true},
		{502, jobs.CatProviderUnavailable, true},
		{503, jobs.CatProviderUnavailable, true},
		{408, jobs.CatProviderUnavailable, true},
		{429, jobs.CatProviderRateLimited, true},
		{400, jobs.CatProviderRejected, false},
		{401, jobs.CatProviderRejected, false},
		{403, jobs.CatProviderRejected, false},
		{404, jobs.CatProviderRejected, false},
		{413, jobs.CatProviderRejected, false},
		{422, jobs.CatProviderRejected, false},
	}
	for _, c := range cases {
		e := StatusError(c.status, http.Header{}, now)
		if e.Category != c.category || e.Retryable != c.retryable || e.HTTPStatus != c.status {
			t.Errorf("status %d: got %+v", c.status, e)
		}
		if e.Detail() != "http "+strconv.Itoa(c.status) {
			t.Errorf("status %d: detail %q", c.status, e.Detail())
		}
	}
	h := http.Header{}
	h.Set("Retry-After", "7")
	if e := StatusError(429, h, now); e.RetryAfter != 7*time.Second {
		t.Errorf("retry after = %v", e.RetryAfter)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"12":                            12 * time.Second,
		"1.5":                           1500 * time.Millisecond,
		"0":                             0,
		"-3":                            0,
		"soon":                          0,
		"Fri, 06 Nov 2026 02:00:30 GMT": 30 * time.Second,
		"Fri, 06 Nov 2026 01:59:00 GMT": 0,
	}
	for in, want := range cases {
		if got := ParseRetryAfter(in, now); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestTransportError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	e := TransportError(ctx, ctx.Err())
	if e.Category != jobs.CatProviderTimeout || !e.Retryable || !errors.Is(e, context.DeadlineExceeded) {
		t.Errorf("deadline: %+v", e)
	}
	e = TransportError(context.Background(), errors.New("connection refused"))
	if e.Category != jobs.CatProviderUnavailable || !e.Retryable {
		t.Errorf("network: %+v", e)
	}
}

func TestJobErrorAndCategory(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "3")
	var je *jobs.Error
	if !errors.As(JobError(StatusError(429, h, time.Now())), &je) ||
		je.Permanent || je.Category != jobs.CatProviderRateLimited || je.RetryAfter != 3*time.Second || je.Detail != "http 429" {
		t.Errorf("429: %+v", je)
	}
	if !errors.As(JobError(StatusError(401, nil, time.Now())), &je) || !je.Permanent || je.Category != jobs.CatProviderRejected {
		t.Errorf("401: %+v", je)
	}
	if !errors.As(JobError(InvalidOutput(200, "finish_reason length")), &je) ||
		je.Permanent || je.Category != jobs.CatInvalidOutput || je.Detail != "http 200 finish_reason length" {
		t.Errorf("invalid output: %+v", je)
	}
	plain := errors.New("x")
	if JobError(plain) != plain {
		t.Error("non-ai error must pass through")
	}
	if CategoryOf(plain) != jobs.CatInternal || CategoryOf(StatusError(503, nil, time.Now())) != jobs.CatProviderUnavailable {
		t.Error("CategoryOf")
	}
}

func TestCost(t *testing.T) {
	if got := LLMCostMicros(1000, 500, 0.27, 1.10); got != 820 {
		t.Errorf("llm cost = %d", got)
	}
	if got := LLMCostMicros(3, 0, 0.5, 0); got != 2 {
		t.Errorf("llm round half up = %d", got)
	}
	if got := LLMCostMicros(1000, 1000, 0, 0); got != 0 {
		t.Errorf("zero prices = %d", got)
	}
	if got := STTCostMicros(90000, 0.006); got != 9000 {
		t.Errorf("stt cost = %d", got)
	}
	if got := STTCostMicros(1, 0.006); got != 0 {
		t.Errorf("stt tiny = %d", got)
	}
}
