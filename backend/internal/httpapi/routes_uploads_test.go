package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai/fake"
	"phoebe/internal/auth"
	"phoebe/internal/ids"
	"phoebe/internal/meetings"
	"phoebe/internal/testutil"
	"phoebe/internal/transcripts"
	"phoebe/internal/uploads"
	"phoebe/internal/usage"
)

func newUploadsServer(t *testing.T) (*Server, *httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	cfg.UploadChunkMaxBytes = 30000
	log := testutil.DiscardLogger()
	store := testutil.Storage(t)
	a := auth.NewService(pool, cfg, log, nil)
	ms := meetings.New(pool, cfg, store, log)
	ts := transcripts.New(pool, cfg, store, usage.New(pool, cfg, log), &fake.STT{}, log)
	up := uploads.New(pool, cfg, store, log, ts)
	s := New(Deps{Config: cfg, Log: log, DB: pool, Storage: store, Sessions: a, Auth: a, Meetings: ms, FoldersTags: ms,
		Uploads: up, Transcripts: ts})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv, pool
}

func putChunk(t *testing.T, srv *httptest.Server, token, id, offset string, body []byte) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("PUT", srv.URL+"/v1/uploads/"+id, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	if offset != "" {
		req.Header.Set("Upload-Offset", offset)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("non-JSON %q", b)
	}
	return resp.StatusCode, m
}

func createBody(b []byte, extra string) string {
	return `{"size_bytes":` + strconv.Itoa(len(b)) + `,"sha256":"` + testutil.SHA256Hex(b) +
		`","content_type":"audio/m4a","duration_ms":3000` + extra + `}`
}

func TestUploadsHTTPFlow(t *testing.T) {
	_, srv, pool := newUploadsServer(t)
	u := testutil.CreateUser(t, pool, 3600)
	mid := ids.New()
	for range 2 {
		if st, body := call(t, srv, "POST", "/v1/meetings", u.Token, `{"id":"`+mid+`","timezone":"UTC"}`); st/100 != 2 {
			t.Fatal(st, body)
		}
	}
	b := testutil.WAV(3000)
	key := ids.New()
	auto := `,"transcription":{"idempotency_key":"` + key + `","language":"auto"}`

	st, body := call(t, srv, "POST", "/v1/meetings/"+mid+"/uploads", u.Token, createBody(b, auto))
	up, _ := body["upload"].(map[string]any)
	if st != 201 || up["status"] != "open" || up["content_type"] != "audio/mp4" || up["auto_transcription"] != true ||
		up["chunk_max_bytes"] != float64(30000) || up["failure_reason"] != nil || up["received_bytes"] != float64(0) {
		t.Fatal(st, body)
	}
	id := up["id"].(string)
	if st, body = call(t, srv, "POST", "/v1/meetings/"+mid+"/uploads", u.Token, createBody(b, "")); st != 200 || body["upload"].(map[string]any)["id"] != id {
		t.Fatal("resume", st, body)
	}
	if st, body = call(t, srv, "POST", "/v1/meetings/"+mid+"/uploads", u.Token, `{"size_bytes":1}`); st != 422 || errCode(body) != "validation_failed" {
		t.Fatal(st, body)
	}
	_, body = call(t, srv, "GET", "/v1/meetings/"+mid, u.Token, "")
	if m := body["meeting"].(map[string]any); m["pipeline_state"] != "uploading" || m["upload"].(map[string]any)["id"] != id {
		t.Fatal(body)
	}

	if st, body = putChunk(t, srv, u.Token, id, "", b[:100]); st != 422 || errCode(body) != "validation_failed" {
		t.Fatal(st, body)
	}
	if st, body = putChunk(t, srv, u.Token, id, "-1", b[:100]); st != 422 {
		t.Fatal(st, body)
	}
	if st, body = putChunk(t, srv, u.Token, id, "0", make([]byte, 30001)); st != 413 || errCode(body) != "chunk_too_large" {
		t.Fatal(st, body)
	}
	st, body = putChunk(t, srv, u.Token, id, "0", b[:30000])
	if st != 200 || body["upload"].(map[string]any)["received_bytes"] != float64(30000) || body["audio"] != nil || body["job"] != nil {
		t.Fatal(st, body)
	}
	st, body = putChunk(t, srv, u.Token, id, "0", b[:30000])
	if st != 409 || errCode(body) != "upload_offset_mismatch" || body["error"].(map[string]any)["details"].(map[string]any)["received_bytes"] != float64(30000) {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "POST", "/v1/uploads/"+id+"/complete", u.Token, ""); st != 409 || errCode(body) != "upload_incomplete" {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "GET", "/v1/uploads/"+id, u.Token, ""); st != 200 || body["upload"].(map[string]any)["received_bytes"] != float64(30000) {
		t.Fatal(st, body)
	}
	st, body = putChunk(t, srv, u.Token, id, "30000", b[30000:])
	if st != 200 || body["upload"].(map[string]any)["status"] != "completed" || body["audio"] == nil || body["job"] == nil {
		t.Fatal(st, body)
	}
	audio := body["audio"].(map[string]any)
	job := body["job"].(map[string]any)
	if audio["content_type"] != "audio/wav" || audio["duration_ms"] != float64(3000) || job["type"] != "transcribe" || job["trigger"] != "user" {
		t.Fatal(body)
	}
	st, body = call(t, srv, "POST", "/v1/uploads/"+id+"/complete", u.Token, "")
	if st != 200 || body["audio"].(map[string]any)["id"] != audio["id"] || body["job"].(map[string]any)["id"] != job["id"] {
		t.Fatal(st, body)
	}
	tr := func(k, body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/meetings/"+mid+"/transcription", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+u.Token)
		req.Header.Set("Idempotency-Key", k)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	if st, body = tr(key, `{"language":"auto"}`); st != 200 || body["job"].(map[string]any)["id"] != job["id"] {
		t.Fatal("explicit start with the auto-start key/body must replay", st, body)
	}
	if st, body = tr(key, `{"language":"id"}`); st != 422 || errCode(body) != "idempotency_key_reused" {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "POST", "/v1/meetings/"+mid+"/uploads", u.Token, createBody(b, "")); st != 409 || errCode(body) != "audio_exists" {
		t.Fatal(st, body)
	}
	_, body = call(t, srv, "GET", "/v1/meetings/"+mid, u.Token, "")
	if m := body["meeting"].(map[string]any); m["pipeline_state"] != "transcribing" || m["upload"] != nil || m["audio"].(map[string]any)["id"] != audio["id"] {
		t.Fatal(body)
	}

	mid2 := testutil.CreateMeeting(t, pool, u.ID)
	bad := make([]byte, 2000)
	_, body = call(t, srv, "POST", "/v1/meetings/"+mid2+"/uploads", u.Token, createBody(bad, ""))
	id2 := body["upload"].(map[string]any)["id"].(string)
	if st, body = putChunk(t, srv, u.Token, id2, "0", bad); st != 422 || errCode(body) != "audio_unsupported" {
		t.Fatal(st, body)
	}
	if _, body = call(t, srv, "GET", "/v1/uploads/"+id2, u.Token, ""); body["upload"].(map[string]any)["failure_reason"] != "audio_unsupported" {
		t.Fatal(body)
	}
}

func TestUploadsCrossAccount(t *testing.T) {
	s, srv, pool := newUploadsServer(t)
	a := testutil.CreateUser(t, pool, 3600)
	b := testutil.CreateUser(t, pool, 3600)
	am := testutil.CreateMeeting(t, pool, a.ID)
	wav := testutil.WAV(2000)
	_, body := call(t, srv, "POST", "/v1/meetings/"+am+"/uploads", a.Token, createBody(wav, ""))
	aid := body["upload"].(map[string]any)["id"].(string)
	if st, _ := putChunk(t, srv, a.Token, aid, "0", wav[:1000]); st != 200 {
		t.Fatal(st)
	}
	before := snapshot(t, pool)

	type tc struct {
		do func() (int, map[string]any)
	}
	cases := map[string][]tc{
		"POST /v1/meetings/{id}/uploads": {{func() (int, map[string]any) {
			return call(t, srv, "POST", "/v1/meetings/"+am+"/uploads", b.Token, createBody(wav, ""))
		}}},
		"GET /v1/uploads/{id}": {{func() (int, map[string]any) { return call(t, srv, "GET", "/v1/uploads/"+aid, b.Token, "") }}},
		"PUT /v1/uploads/{id}": {{func() (int, map[string]any) { return putChunk(t, srv, b.Token, aid, "1000", wav[1000:]) }}},
		"POST /v1/uploads/{id}/complete": {{func() (int, map[string]any) {
			return call(t, srv, "POST", "/v1/uploads/"+aid+"/complete", b.Token, "")
		}}},
	}
	for _, rt := range s.routesUploads() {
		if len(cases[rt.Pattern()]) == 0 {
			t.Errorf("no cross-account case for %s", rt.Pattern())
		}
	}
	for route, list := range cases {
		for _, c := range list {
			if st, body := c.do(); st != 404 || errCode(body) != "not_found" {
				t.Errorf("%s: %d %v", route, st, body)
			}
		}
	}
	if after := snapshot(t, pool); after != before {
		t.Fatalf("user B's requests wrote data:\nbefore %s\nafter %s", before, after)
	}
}
