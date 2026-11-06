package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"phoebe/internal/apperr"
	"phoebe/internal/jsonx"
	"phoebe/internal/obs"
	"phoebe/internal/testutil"
)

type fakeSessions map[string][2]string

func (f fakeSessions) ResolveSession(_ context.Context, token string) (string, string, error) {
	v, ok := f[token]
	if !ok {
		return "", "", apperr.New(apperr.Unauthenticated)
	}
	return v[0], v[1], nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newTestServer(t *testing.T, withDB bool) (*Server, *syncBuf) {
	logs := &syncBuf{}
	d := Deps{
		Config:   testutil.Config(t),
		Log:      obs.NewLogger(logs, slog.LevelDebug),
		Sessions: fakeSessions{"good": {"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}},
	}
	if withDB {
		d.DB = testutil.DB(t)
	}
	return New(d), logs
}

// serve runs one extra route through exactly the middleware stack New uses.
func serve(s *Server, rt Route, req *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle(rt.Pattern(), s.wrapRoute(rt))
	h := s.requestID(s.accessLog(s.recoverer(mux)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) errorObj {
	t.Helper()
	var b errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("not an error envelope: %s", rec.Body)
	}
	if b.Error.Details == nil || b.Error.RequestID == "" || b.Error.Message == "" {
		t.Fatalf("incomplete envelope: %s", rec.Body)
	}
	return b.Error
}

func TestCodeTableCoversAllCodes(t *testing.T) {
	want := map[apperr.Code]int{
		apperr.InvalidJSON: 400, apperr.MissingIdempotencyKey: 400, apperr.Unauthenticated: 401,
		apperr.AccessTokenExpired: 401, apperr.SessionRevoked: 401, apperr.SessionExpired: 401,
		apperr.QuotaExceeded: 402, apperr.EntitlementInactive: 402, apperr.InvalidSignature: 403, apperr.NotFound: 404,
		apperr.RevisionConflict: 409, apperr.JobInProgress: 409, apperr.AlreadyTranscribed: 409, apperr.AudioExists: 409,
		apperr.AudioNotReady: 409, apperr.TranscriptNotReady: 409, apperr.AINotesNotReady: 409,
		apperr.UploadOffsetMismatch: 409, apperr.UploadBusy: 409, apperr.UploadNotOpen: 409, apperr.UploadIncomplete: 409,
		apperr.NameTaken: 409, apperr.MeetingIDConflict: 409, apperr.IDConflict: 409, apperr.MetadataConflict: 409,
		apperr.UploadExpired: 410, apperr.AudioUnavailable: 410, apperr.URLExpired: 410,
		apperr.PayloadTooLarge: 413, apperr.ChunkTooLarge: 413, apperr.UploadTooLarge: 413,
		apperr.ValidationFailed: 422, apperr.InvalidContent: 422, apperr.IdempotencyKeyReused: 422,
		apperr.ChecksumMismatch: 422, apperr.AudioUnsupported: 422, apperr.AudioCorrupt: 422, apperr.AudioEmpty: 422,
		apperr.AudioTooLong: 422, apperr.AIGenerationLimit: 429, apperr.Internal: 500, apperr.Unavailable: 503,
	}
	if len(codeTable) != len(want) {
		t.Fatalf("table has %d codes, api.md §1.2 has %d", len(codeTable), len(want))
	}
	for c, st := range want {
		if codeTable[c].status != st {
			t.Errorf("%s → %d, want %d", c, codeTable[c].status, st)
		}
	}
}

func TestRequestIDEchoAndGeneration(t *testing.T) {
	s, _ := newTestServer(t, false)
	req := httptest.NewRequest("GET", "/v1/config", nil)
	req.Header.Set("X-Request-ID", "8F0C1A2B-3C4D-4E5F-8A9B-0C1D2E3F4A5B")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-ID") != "8f0c1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b" {
		t.Fatal("client id not echoed", rec.Header())
	}
	req = httptest.NewRequest("GET", "/v1/config", nil)
	req.Header.Set("X-Request-ID", "not-a-uuid")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if id := rec.Header().Get("X-Request-ID"); len(id) != 36 || id == "not-a-uuid" {
		t.Fatal("id not generated", id)
	}
}

func TestConfigEndpoint(t *testing.T) {
	s, _ := newTestServer(t, false)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/config", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatal(rec.Code, rec.Header())
	}
	want := `{"max_audio_duration_ms":7200000,"max_upload_bytes":262144000,"upload_chunk_max_bytes":33554432,"supported_content_types":["audio/mp4","audio/mpeg","audio/wav"],"audio_retention_days":30,"languages":["id","en"]}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("got %s", rec.Body)
	}
}

func TestHealth(t *testing.T) {
	s, _ := newTestServer(t, true)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatal(rec.Code, rec.Body)
	}
	s.DB.Close()
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 503 || decodeErr(t, rec).Code != apperr.Unavailable {
		t.Fatal(rec.Code, rec.Body)
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	s, _ := newTestServer(t, false)
	for _, r := range []*http.Request{
		httptest.NewRequest("GET", "/v1/nope", nil),
		httptest.NewRequest("DELETE", "/v1/config", nil),
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, r)
		if rec.Code != 404 || decodeErr(t, rec).Code != apperr.NotFound {
			t.Fatal(r.Method, r.URL, rec.Code, rec.Body)
		}
	}
}

func TestRouteTable(t *testing.T) {
	s, _ := newTestServer(t, false)
	seen := map[string]bool{}
	for _, rt := range s.Routes() {
		if seen[rt.Pattern()] {
			t.Fatal("duplicate", rt.Pattern())
		}
		seen[rt.Pattern()] = true
	}
	if !seen["GET /healthz"] || !seen["GET /v1/config"] {
		t.Fatal(seen)
	}
}

func TestAuthMiddleware(t *testing.T) {
	s, logs := newTestServer(t, false)
	rt := Route{Method: "GET", Path: "/v1/me", Handle: func(w http.ResponseWriter, r *http.Request) error {
		writeJSON(w, 200, map[string]string{"user": userID(r), "session": sessionID(r)})
		return nil
	}}
	for _, h := range []string{"", "Bearer", "Basic good", "Bearer bad"} {
		req := httptest.NewRequest("GET", "/v1/me", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		rec := serve(s, rt, req)
		if rec.Code != 401 || decodeErr(t, rec).Code != apperr.Unauthenticated {
			t.Fatalf("%q → %d", h, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "/v1/me?q=secret-search", nil)
	req.Header.Set("Authorization", "Bearer good")
	rec := serve(s, rt, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user":"11111111-1111-4111-8111-111111111111"`) ||
		!strings.Contains(rec.Body.String(), `"session":"22222222-2222-4222-8222-222222222222"`) {
		t.Fatal(rec.Code, rec.Body)
	}
	l := logs.String()
	if !strings.Contains(l, `"msg":"http.request"`) || !strings.Contains(l, `"route":"GET /v1/me"`) ||
		!strings.Contains(l, `"user_id":"11111111-1111-4111-8111-111111111111"`) || !strings.Contains(l, `"correlation_id"`) {
		t.Fatalf("access log: %s", l)
	}
	if strings.Contains(l, "secret-search") || strings.Contains(l, "good") {
		t.Fatalf("access log leaks query or token: %s", l)
	}
}

func TestBodyLimitAndDecode(t *testing.T) {
	s, _ := newTestServer(t, false)
	type body struct {
		Name string `json:"name"`
	}
	var gotRaw []byte
	rt := Route{Method: "POST", Path: "/x", Public: true, Handle: func(w http.ResponseWriter, r *http.Request) error {
		var b body
		raw, err := decodeJSON(r, &b)
		if err != nil {
			return err
		}
		gotRaw = raw
		writeJSON(w, 201, b)
		return nil
	}}
	cases := []struct {
		body string
		code int
		ec   apperr.Code
	}{
		{`{"name":"a","extra":1}`, 201, ""},
		{``, 201, ""},
		{`{"name":1}`, 400, apperr.InvalidJSON},
		{`{"name":`, 400, apperr.InvalidJSON},
		{`{"name":"a"}{}`, 400, apperr.InvalidJSON},
		{`{"name":"` + strings.Repeat("x", 2<<20) + `"}`, 413, apperr.PayloadTooLarge},
	}
	for _, c := range cases {
		rec := serve(s, rt, httptest.NewRequest("POST", "/x", strings.NewReader(c.body)))
		if rec.Code != c.code {
			t.Fatalf("%.30q → %d %s", c.body, rec.Code, rec.Body)
		}
		if c.ec != "" && decodeErr(t, rec).Code != c.ec {
			t.Fatalf("%.30q code", c.body)
		}
	}
	serve(s, rt, httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"z"}`)))
	if string(gotRaw) != `{"name":"z"}` {
		t.Fatal("raw body not returned", string(gotRaw))
	}
	big := Route{Method: "PUT", Path: "/big", Public: true, BodyLimit: NoBodyLimit, Handle: func(w http.ResponseWriter, r *http.Request) error {
		n, _ := io.Copy(io.Discard, r.Body)
		writeJSON(w, 200, map[string]int64{"n": n})
		return nil
	}}
	rec := serve(s, big, httptest.NewRequest("PUT", "/big", bytes.NewReader(make([]byte, 3<<20))))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "3145728") {
		t.Fatal("NoBodyLimit", rec.Code)
	}
}

func TestErrorMappingAndRecover(t *testing.T) {
	s, logs := newTestServer(t, false)
	h := func(err error) Route {
		return Route{Method: "GET", Path: "/e", Public: true, Handle: func(http.ResponseWriter, *http.Request) error { return err }}
	}
	rec := serve(s, h(apperr.WithDetails(apperr.RevisionConflict, map[string]any{"current_revision_number": 7})), httptest.NewRequest("GET", "/e", nil))
	e := decodeErr(t, rec)
	if rec.Code != 409 || e.Code != apperr.RevisionConflict || e.Details["current_revision_number"] != float64(7) || e.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatal(rec.Code, rec.Body)
	}
	next := time.Now().Add(90*time.Second + 500*time.Millisecond)
	rec = serve(s, h(apperr.WithDetails(apperr.AIGenerationLimit, map[string]any{"limit": 30, "remaining": 0, "next_slot_at": jsonx.TimePtr(&next)})),
		httptest.NewRequest("GET", "/e", nil))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "91" || decodeErr(t, rec).Details["next_slot_at"] != jsonx.FormatTime(next) {
		t.Fatal(rec.Code, rec.Header(), rec.Body)
	}
	rec = serve(s, h(apperr.Field("title", apperr.ReasonTooLong)), httptest.NewRequest("GET", "/e", nil))
	if !strings.Contains(rec.Body.String(), `"details":{"fields":[{"field":"title","reason":"too_long"}]}`) {
		t.Fatal(rec.Body)
	}
	rec = serve(s, h(io.ErrUnexpectedEOF), httptest.NewRequest("GET", "/e", nil))
	if rec.Code != 500 || decodeErr(t, rec).Code != apperr.Internal {
		t.Fatal(rec.Code)
	}
	panicky := Route{Method: "GET", Path: "/p", Public: true, Handle: func(http.ResponseWriter, *http.Request) error { panic("kaboom") }}
	rec = serve(s, panicky, httptest.NewRequest("GET", "/p", nil))
	if rec.Code != 500 || decodeErr(t, rec).Code != apperr.Internal || !strings.Contains(logs.String(), `"http_status":500`) {
		t.Fatal(rec.Code, logs.String())
	}
}

func TestHelpers(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	if _, err := idempotencyKey(r); !apperr.Is(err, apperr.MissingIdempotencyKey) {
		t.Fatal("missing key")
	}
	r.Header.Set("Idempotency-Key", "nope")
	if _, err := idempotencyKey(r); !apperr.Is(err, apperr.MissingIdempotencyKey) {
		t.Fatal("non-uuid key")
	}
	r.Header.Set("Idempotency-Key", "8F0C1A2B-3C4D-4E5F-8A9B-0C1D2E3F4A5B")
	if k, err := idempotencyKey(r); err != nil || k != "8f0c1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b" {
		t.Fatal(k, err)
	}
	r.SetPathValue("id", "abc")
	if _, err := pathUUID(r, "id"); !apperr.Is(err, apperr.NotFound) {
		t.Fatal("non-uuid path id")
	}

	type cur struct {
		S string `json:"s"`
		I string `json:"i"`
	}
	c := encodeCursor(cur{"2026-11-06T02:00:00.000Z", "x"})
	var got cur
	limit, has, err := pageParams(httptest.NewRequest("GET", "/?limit=10&cursor="+*c, nil), &got)
	if err != nil || limit != 10 || !has || got.I != "x" {
		t.Fatal(limit, has, err, got)
	}
	if limit, has, err := pageParams(httptest.NewRequest("GET", "/", nil), &got); err != nil || limit != 50 || has {
		t.Fatal("defaults")
	}
	for q, field := range map[string]string{"limit=0": "limit", "limit=201": "limit", "limit=x": "limit", "cursor=!!": "cursor", "cursor=e30x": "cursor"} {
		_, _, err := pageParams(httptest.NewRequest("GET", "/?"+q, nil), &got)
		if !apperr.Is(err, apperr.ValidationFailed) || !strings.Contains(jsonOf(err.(*apperr.Error).Details), `"field":"`+field+`"`) {
			t.Fatal(q, err)
		}
	}
	b, _ := json.Marshal(NewPage[int](nil, nil))
	if string(b) != `{"items":[],"next_cursor":null}` {
		t.Fatal(string(b))
	}
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }
