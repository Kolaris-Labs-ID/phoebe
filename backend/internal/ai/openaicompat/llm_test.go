package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"phoebe/internal/ai"
	"phoebe/internal/jobs"
)

func server(t *testing.T, status int, header map[string]string, body string, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer key-1" ||
				r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("request %s %q %q", r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"))
			}
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, got); err != nil {
				t.Errorf("body: %v", err)
			}
		}
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newLLM(url string) *LLM {
	return New(Config{BaseURL: url + "/v1/", APIKey: "key-1", Model: "deepseek-chat", Temperature: 0.2, Timeout: 5 * time.Second})
}

func completion(content, finish string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "c1", "object": "chat.completion",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 1200, "completion_tokens": 340, "total_tokens": 1540},
	})
	return string(b)
}

func TestCompleteJSONMode(t *testing.T) {
	var req map[string]any
	srv := server(t, 200, nil, completion(`{"sections":[]}`, "stop"), &req)
	l := newLLM(srv.URL)
	if p, m := l.Name(); p != "openaicompat" || m != "deepseek-chat" {
		t.Fatalf("name %s %s", p, m)
	}
	res, err := l.Complete(context.Background(), "SYS json", "<meeting_data>{}</meeting_data>")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != `{"sections":[]}` || res.FinishReason != "stop" || res.InputTokens != 1200 || res.OutputTokens != 340 {
		t.Errorf("result %+v", res)
	}
	if req["model"] != "deepseek-chat" || req["temperature"] != 0.2 {
		t.Errorf("model/temperature %v %v", req["model"], req["temperature"])
	}
	if rf, _ := req["response_format"].(map[string]any); rf["type"] != "json_object" {
		t.Errorf("response_format %v", req["response_format"])
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages %v", msgs)
	}
	m0, m1 := msgs[0].(map[string]any), msgs[1].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "SYS json" || m1["role"] != "user" || m1["content"] != "<meeting_data>{}</meeting_data>" {
		t.Errorf("messages %v", msgs)
	}
}

func TestCompleteInvalidJSONContentPassesThrough(t *testing.T) {
	srv := server(t, 200, nil, completion(`{"sections": [oops`, "stop"), nil)
	res, err := newLLM(srv.URL).Complete(context.Background(), "s", "u")
	if err != nil || res.Content != `{"sections": [oops` {
		t.Fatalf("got %+v %v", res, err)
	}
}

func TestCompleteFinishReasonLength(t *testing.T) {
	srv := server(t, 200, nil, completion(`{"sections":[{"heading":"a","it`, "length"), nil)
	res, err := newLLM(srv.URL).Complete(context.Background(), "s", "u")
	e := wantErr(t, err, jobs.CatInvalidOutput, true, 200)
	if e.Detail() != "http 200 finish_reason length" {
		t.Errorf("detail %q", e.Detail())
	}
	if res.InputTokens != 1200 || res.OutputTokens != 340 || res.FinishReason != "length" {
		t.Errorf("tokens must be returned with the error: %+v", res)
	}
}

func wantErr(t *testing.T, err error, category string, retryable bool, status int) *ai.Error {
	t.Helper()
	var e *ai.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *ai.Error, got %v", err)
	}
	if e.Category != category || e.Retryable != retryable || e.HTTPStatus != status {
		t.Fatalf("got %+v, want %s retryable=%v status=%d", e, category, retryable, status)
	}
	if strings.Contains(e.Error(), "secret") {
		t.Fatalf("provider body leaked: %q", e.Error())
	}
	return e
}

func TestCompleteErrors(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		header    map[string]string
		body      string
		category  string
		retryable bool
	}{
		{"rate limited", 429, map[string]string{"Retry-After": "Fri, 06 Nov 2099 02:00:00 GMT"}, `{"error":{"message":"secret"}}`, jobs.CatProviderRateLimited, true},
		{"server error", 500, nil, `{"error":{"message":"secret"}}`, jobs.CatProviderUnavailable, true},
		{"overloaded", 503, nil, ``, jobs.CatProviderUnavailable, true},
		{"request timeout", 408, nil, ``, jobs.CatProviderUnavailable, true},
		{"bad request", 400, nil, `{"error":{"message":"secret"}}`, jobs.CatProviderRejected, false},
		{"forbidden", 403, nil, ``, jobs.CatProviderRejected, false},
		{"not found", 404, nil, ``, jobs.CatProviderRejected, false},
		{"unprocessable", 422, nil, ``, jobs.CatProviderRejected, false},
		{"invalid json body", 200, nil, `{"choices": [ secret`, jobs.CatInvalidOutput, true},
		{"no choices", 200, nil, `{"choices": [], "usage": {"prompt_tokens": 5}}`, jobs.CatInvalidOutput, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := server(t, c.status, c.header, c.body, nil)
			_, err := newLLM(srv.URL).Complete(context.Background(), "s", "u")
			e := wantErr(t, err, c.category, c.retryable, c.status)
			if c.status == 429 && e.RetryAfter < time.Hour {
				t.Errorf("retry after %v", e.RetryAfter)
			}
		})
	}
}

func TestRateLimitedSecondsRetryAfter(t *testing.T) {
	srv := server(t, 429, map[string]string{"Retry-After": "15"}, ``, nil)
	_, err := newLLM(srv.URL).Complete(context.Background(), "s", "u")
	if e := wantErr(t, err, jobs.CatProviderRateLimited, true, 429); e.RetryAfter != 15*time.Second {
		t.Errorf("retry after %v", e.RetryAfter)
	}
}

func TestCompleteTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	l := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m", Timeout: 100 * time.Millisecond})
	_, err := l.Complete(context.Background(), "s", "u")
	wantErr(t, err, jobs.CatProviderTimeout, true, 0)
}

func TestCompleteParentDeadline(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := newLLM(srv.URL).Complete(ctx, "s", "u")
	wantErr(t, err, jobs.CatProviderTimeout, true, 0)
}

func TestCompleteNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := newLLM(url).Complete(context.Background(), "s", "u")
	wantErr(t, err, jobs.CatProviderUnavailable, true, 0)
}
