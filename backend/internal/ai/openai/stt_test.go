package openai

import (
	"bytes"
	"context"
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

const verbose = `{"task":"transcribe","language":"indonesian","duration":12.345,"text":"Halo. Kita sepakat.",
 "segments":[
  {"id":0,"seek":0,"start":0.0,"end":4.2,"text":" Halo semua.","tokens":[1],"temperature":0.0,"avg_logprob":-0.25,"compression_ratio":1.3,"no_speech_prob":0.01},
  {"id":1,"seek":0,"start":4.2,"end":9.85,"text":" Terima kasih telah menonton","avg_logprob":-1.5,"compression_ratio":2.6,"no_speech_prob":0.9},
  {"id":2,"start":9.85,"end":12.3,"text":" Kita sepakat."}
 ]}`

type captured struct {
	fields      map[string][]string
	fileName    string
	fileType    string
	fileBody    []byte
	auth        string
	path        string
	contentType string
}

func server(t *testing.T, status int, header map[string]string, body string, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			got.auth = r.Header.Get("Authorization")
			got.path = r.URL.Path
			got.contentType = r.Header.Get("Content-Type")
			mr, err := r.MultipartReader()
			if err != nil {
				t.Errorf("multipart: %v", err)
				return
			}
			got.fields = map[string][]string{}
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("part: %v", err)
					return
				}
				b, _ := io.ReadAll(p)
				if p.FormName() == "file" {
					got.fileName, got.fileType, got.fileBody = p.FileName(), p.Header.Get("Content-Type"), b
				} else {
					got.fields[p.FormName()] = append(got.fields[p.FormName()], string(b))
				}
			}
		}
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSTT(url string) *STT {
	return New(Config{BaseURL: url + "/", APIKey: "sk-test", Model: "whisper-1", Timeout: 5 * time.Second})
}

func input(lang string) ai.STTInput {
	return ai.STTInput{Reader: bytes.NewReader([]byte("RIFFfakeaudio")), Filename: "s1.wav", ContentType: "audio/wav", Language: lang}
}

func TestTranscribeVerboseJSON(t *testing.T) {
	var got captured
	srv := server(t, 200, map[string]string{"Content-Type": "application/json"}, verbose, &got)
	s := newSTT(srv.URL)
	if p, m := s.Name(); p != "openai" || m != "whisper-1" {
		t.Fatalf("name %s %s", p, m)
	}
	res, err := s.Transcribe(context.Background(), input("id"))
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/audio/transcriptions" || got.auth != "Bearer sk-test" || !strings.HasPrefix(got.contentType, "multipart/form-data") {
		t.Errorf("request: %+v", got)
	}
	want := map[string]string{"model": "whisper-1", "response_format": "verbose_json", "timestamp_granularities[]": "segment", "temperature": "0", "language": "id"}
	for k, v := range want {
		if f := got.fields[k]; len(f) != 1 || f[0] != v {
			t.Errorf("field %s = %v", k, f)
		}
	}
	if got.fileName != "s1.wav" || got.fileType != "audio/wav" || string(got.fileBody) != "RIFFfakeaudio" {
		t.Errorf("file part: %q %q %q", got.fileName, got.fileType, got.fileBody)
	}
	if res.Language != "id" || res.AudioMS != 12345 || len(res.Segments) != 3 {
		t.Fatalf("result: %+v", res)
	}
	s0, s1, s2 := res.Segments[0], res.Segments[1], res.Segments[2]
	if s0.StartSec != 0 || s0.EndSec != 4.2 || s0.Text != " Halo semua." || s0.NoSpeechProb != 0.01 || s0.AvgLogprob != -0.25 || s0.CompressionRatio != 1.3 {
		t.Errorf("seg0 %+v", s0)
	}
	if s1.NoSpeechProb != 0.9 || s1.AvgLogprob != -1.5 || s1.CompressionRatio != 2.6 || s1.EndSec != 9.85 {
		t.Errorf("seg1 %+v", s1)
	}
	if s2.NoSpeechProb != 0 || s2.AvgLogprob != 0 || s2.CompressionRatio != 0 {
		t.Errorf("seg2 absent quality fields must be 0: %+v", s2)
	}
}

func TestTranscribeAutoLanguageNotSent(t *testing.T) {
	var got captured
	srv := server(t, 200, nil, `{"language":"english","duration":1,"segments":[]}`, &got)
	in := input("auto")
	in.Filename = ""
	in.ContentType = "audio/mp4"
	res, err := newSTT(srv.URL).Transcribe(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.fields["language"]; ok {
		t.Error("language must not be sent for auto")
	}
	if got.fileName != "audio.m4a" {
		t.Errorf("default filename %q", got.fileName)
	}
	if res.Language != "en" || len(res.Segments) != 0 || res.Segments == nil {
		t.Errorf("result %+v", res)
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
	if strings.Contains(e.Error(), "secret") || strings.Contains(e.Detail(), "secret") {
		t.Fatalf("provider body leaked: %q", e.Error())
	}
	return e
}

func TestTranscribeErrors(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		header    map[string]string
		body      string
		category  string
		retryable bool
	}{
		{"rate limited", 429, map[string]string{"Retry-After": "20"}, `{"error":{"message":"secret rate"}}`, jobs.CatProviderRateLimited, true},
		{"server error", 500, nil, `{"error":{"message":"secret"}}`, jobs.CatProviderUnavailable, true},
		{"unavailable", 503, nil, ``, jobs.CatProviderUnavailable, true},
		{"request timeout", 408, nil, ``, jobs.CatProviderUnavailable, true},
		{"unauthorized", 401, nil, `{"error":{"message":"secret key"}}`, jobs.CatProviderRejected, false},
		{"too large", 413, nil, ``, jobs.CatProviderRejected, false},
		{"bad param", 400, nil, `{"error":{"message":"secret: invalid model"}}`, jobs.CatProviderRejected, false},
		{"undecodable audio", 400, nil, `{"error":{"message":"Audio file might be corrupted or unsupported","type":"invalid_request_error"}}`, jobs.CatAudioUnreadable, false},
		{"invalid format", 400, nil, `{"error":{"message":"Invalid file format. Supported formats: ['m4a','mp3','wav']"}}`, jobs.CatAudioUnreadable, false},
		{"invalid json", 200, nil, `{"segments": [ secret`, jobs.CatInvalidOutput, true},
		{"missing segments", 200, nil, `{"text":"secret only text"}`, jobs.CatProviderRejected, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := server(t, c.status, c.header, c.body, nil)
			_, err := newSTT(srv.URL).Transcribe(context.Background(), input("id"))
			e := wantErr(t, err, c.category, c.retryable, c.status)
			if c.status == 429 && e.RetryAfter != 20*time.Second {
				t.Errorf("retry after %v", e.RetryAfter)
			}
		})
	}
}

func TestTranscribeTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	s := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "whisper-1", Timeout: 100 * time.Millisecond})
	_, err := s.Transcribe(context.Background(), input("id"))
	wantErr(t, err, jobs.CatProviderTimeout, true, 0)
}

func TestTranscribeNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := newSTT(url).Transcribe(context.Background(), input("id"))
	wantErr(t, err, jobs.CatProviderUnavailable, true, 0)
}
