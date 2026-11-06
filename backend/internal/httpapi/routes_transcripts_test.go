package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai/fake"
	"phoebe/internal/auth"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
	"phoebe/internal/transcripts"
	"phoebe/internal/usage"
)

type transcriptsEnv struct {
	s      *Server
	srv    *httptest.Server
	pool   *pgxpool.Pool
	store  *storage.LocalFS
	runner *jobs.Runner
}

func newTranscriptsServer(t *testing.T) *transcriptsEnv {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	log := testutil.DiscardLogger()
	store := testutil.Storage(t)
	a := auth.NewService(pool, cfg, log, nil)
	ts := transcripts.New(pool, cfg, store, usage.New(pool, cfg, log), &fake.STT{}, log)
	s := New(Deps{Config: cfg, Log: log, DB: pool, Storage: store, Sessions: a, Auth: a, Transcripts: ts})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	r := jobs.NewRunner(pool, log, jobs.RunnerConfig{WorkerID: "test", Lease: time.Minute, RetryBase: time.Millisecond})
	r.Register(jobs.TypeTranscribe, ts.TranscribeSpec())
	return &transcriptsEnv{s: s, srv: srv, pool: pool, store: store, runner: r}
}

func (e *transcriptsEnv) uploadedMeeting(t *testing.T, uid string, durationMS int64) string {
	t.Helper()
	mid := testutil.CreateMeeting(t, e.pool, uid)
	b := testutil.WAV(durationMS)
	sid, key := ids.New(), "u/"+uid+"/m/"+mid+"/audio/x.wav"
	if _, err := e.store.Put(context.Background(), key, bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		if _, err := e.pool.Exec(context.Background(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO upload_sessions (id, meeting_id, user_id, status, declared_content_type, size_bytes, sha256,
		declared_duration_ms, received_bytes, storage_key, expires_at, completed_at)
		VALUES ($1, $2, $3, 'completed', 'audio/wav', $4, $5, $6, $4, 'u/x.part', now() + interval '1 day', now())`,
		sid, mid, uid, len(b), testutil.SHA256Hex(b), durationMS)
	exec(`INSERT INTO audio_assets (meeting_id, upload_session_id, content_type, size_bytes, sha256, duration_ms, storage_key)
		VALUES ($1, $2, 'audio/wav', $3, $4, $5, $6)`, mid, sid, len(b), testutil.SHA256Hex(b), durationMS, key)
	exec(`UPDATE meetings SET pipeline_state = 'uploaded' WHERE id = $1`, mid)
	return mid
}

func callKey(t *testing.T, srv *httptest.Server, method, path, token, key, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestTranscriptsHTTPFlow(t *testing.T) {
	e := newTranscriptsServer(t)
	u := testutil.CreateUser(t, e.pool, 3600)
	mid := e.uploadedMeeting(t, u.ID, 30_000)
	base := "/v1/meetings/" + mid

	if st, body := callKey(t, e.srv, "POST", base+"/transcription", u.Token, "", ""); st != 400 || errCode(body) != "missing_idempotency_key" {
		t.Fatal(st, body)
	}
	if st, body := call(t, e.srv, "GET", base+"/transcript", u.Token, ""); st != 409 || errCode(body) != "transcript_not_ready" {
		t.Fatal(st, body)
	}
	key := ids.New()
	st, body := callKey(t, e.srv, "POST", base+"/transcription", u.Token, key, `{"language":"id"}`)
	job, _ := body["job"].(map[string]any)
	if st != 202 || job["type"] != "transcribe" || job["status"] != "queued" || job["trigger"] != "user" || job["result"] != nil {
		t.Fatal(st, body)
	}
	jobID := job["id"].(string)
	if st, body = callKey(t, e.srv, "POST", base+"/transcription", u.Token, key, `{"language":"id"}`); st != 200 || body["job"].(map[string]any)["id"] != jobID {
		t.Fatal("replay", st, body)
	}
	if st, body = callKey(t, e.srv, "POST", base+"/transcription", u.Token, key, `{}`); st != 422 || errCode(body) != "idempotency_key_reused" {
		t.Fatal(st, body)
	}
	if st, body = callKey(t, e.srv, "POST", base+"/transcription", u.Token, ids.New(), `{"language":5}`); st != 400 || errCode(body) != "invalid_json" {
		t.Fatal(st, body)
	}
	if st, body = callKey(t, e.srv, "POST", base+"/transcription", u.Token, ids.New(), ``); st != 409 || errCode(body) != "job_in_progress" {
		t.Fatal(st, body)
	}

	if ran, err := e.runner.RunOnce(context.Background()); !ran || err != nil {
		t.Fatal(ran, err)
	}
	st, body = call(t, e.srv, "GET", "/v1/jobs/"+jobID, u.Token, "")
	job, _ = body["job"].(map[string]any)
	if st != 200 || job["status"] != "succeeded" || job["attempts"] != float64(1) || job["finished_at"] == nil {
		t.Fatal(st, body)
	}
	trID := job["result"].(map[string]any)["transcript_revision_id"].(string)
	st, body = call(t, e.srv, "GET", base+"/jobs", u.Token, "")
	items, _ := body["items"].([]any)
	if st != 200 || len(items) != 2 || items[0].(map[string]any)["type"] != "generate_notes" || items[1].(map[string]any)["id"] != jobID {
		t.Fatal(st, body)
	}

	st, body = call(t, e.srv, "GET", base+"/transcript", u.Token, "")
	rev, _ := body["revision"].(map[string]any)
	if st != 200 || rev["id"] != trID || rev["kind"] != "original" || rev["revision_number"] != float64(1) || len(rev["segments"].([]any)) != 2 {
		t.Fatal(st, body)
	}
	seg := rev["segments"].([]any)[1].(map[string]any)
	if seg["id"] != "s2" || seg["start_ms"] != float64(15000) || seg["end_ms"] != float64(30000) {
		t.Fatal(seg)
	}

	mut := ids.New()
	corr := `{"expected_revision":1,"mutation_id":"` + mut + `","edits":[{"segment_id":"s1","text":"Halo semua."}]}`
	st, body = call(t, e.srv, "POST", base+"/transcript/revisions", u.Token, corr)
	if st != 201 || body["revision"].(map[string]any)["revision_number"] != float64(2) || body["revision"].(map[string]any)["kind"] != "correction" {
		t.Fatal(st, body)
	}
	if st, body = call(t, e.srv, "POST", base+"/transcript/revisions", u.Token, corr); st != 200 {
		t.Fatal("replay", st, body)
	}
	st, body = call(t, e.srv, "POST", base+"/transcript/revisions", u.Token, `{"expected_revision":1,"mutation_id":"`+ids.New()+`","edits":[{"segment_id":"s2","text":"x"}]}`)
	if d := body["error"].(map[string]any)["details"].(map[string]any); st != 409 || errCode(body) != "revision_conflict" || d["current_revision_number"] != float64(2) {
		t.Fatal(st, body)
	}
	st, body = call(t, e.srv, "POST", base+"/transcript/revisions", u.Token, `{"expected_revision":2,"mutation_id":"`+ids.New()+`","edits":[{"segment_id":"s9","text":"x"}]}`)
	if d := body["error"].(map[string]any)["details"].(map[string]any); st != 422 || errCode(body) != "invalid_content" || d["path"] != "/edits/0/segment_id" {
		t.Fatal(st, body)
	}

	st, body = call(t, e.srv, "GET", base+"/transcript/revisions?limit=1", u.Token, "")
	items, _ = body["items"].([]any)
	if st != 200 || len(items) != 1 || items[0].(map[string]any)["revision_number"] != float64(2) || body["next_cursor"] == nil {
		t.Fatal(st, body)
	}
	if _, has := items[0].(map[string]any)["segments"]; has {
		t.Fatal("list items carry segments")
	}
	st, body = call(t, e.srv, "GET", base+"/transcript/revisions?limit=1&cursor="+url.QueryEscape(body["next_cursor"].(string)), u.Token, "")
	items, _ = body["items"].([]any)
	if st != 200 || len(items) != 1 || items[0].(map[string]any)["revision_number"] != float64(1) || body["next_cursor"] != nil {
		t.Fatal(st, body)
	}
	if st, body = call(t, e.srv, "GET", base+"/transcript/revisions?cursor=%21%21", u.Token, ""); st != 422 {
		t.Fatal(st, body)
	}
	st, body = call(t, e.srv, "GET", base+"/transcript/revisions/1", u.Token, "")
	if st != 200 || body["revision"].(map[string]any)["segments"].([]any)[0].(map[string]any)["text"] == "Halo semua." {
		t.Fatal(st, body)
	}
	for _, p := range []string{"/transcript/revisions/3", "/transcript/revisions/0", "/transcript/revisions/x"} {
		if st, body = call(t, e.srv, "GET", base+p, u.Token, ""); st != 404 {
			t.Fatal(p, st, body)
		}
	}
	if st, body = call(t, e.srv, "GET", "/v1/jobs/"+ids.New(), u.Token, ""); st != 404 {
		t.Fatal(st, body)
	}
}

func TestTranscriptsQuotaHTTP(t *testing.T) {
	e := newTranscriptsServer(t)
	u := testutil.CreateUser(t, e.pool, 5)
	mid := e.uploadedMeeting(t, u.ID, 30_000)
	st, body := callKey(t, e.srv, "POST", "/v1/meetings/"+mid+"/transcription", u.Token, ids.New(), "")
	d, _ := body["error"].(map[string]any)["details"].(map[string]any)
	if st != 402 || errCode(body) != "quota_exceeded" || d["required_seconds"] != float64(30) || d["remaining_seconds"] != float64(5) {
		t.Fatal(st, body)
	}
}

func TestTranscriptsCrossAccount(t *testing.T) {
	e := newTranscriptsServer(t)
	a := testutil.CreateUser(t, e.pool, 3600)
	b := testutil.CreateUser(t, e.pool, 3600)
	am := e.uploadedMeeting(t, a.ID, 15_000)
	st, body := callKey(t, e.srv, "POST", "/v1/meetings/"+am+"/transcription", a.Token, ids.New(), "")
	if st != 202 {
		t.Fatal(st, body)
	}
	aj := body["job"].(map[string]any)["id"].(string)
	if ran, err := e.runner.RunOnce(context.Background()); !ran || err != nil {
		t.Fatal(ran, err)
	}
	before := snapshot(t, e.pool)
	var trBefore string
	if err := e.pool.QueryRow(context.Background(), `SELECT string_agg(t::text, '|') FROM transcript_revisions t`).Scan(&trBefore); err != nil {
		t.Fatal(err)
	}

	m := "/v1/meetings/" + am
	corr := `{"expected_revision":1,"mutation_id":"` + ids.New() + `","edits":[{"segment_id":"s1","text":"pwn"}]}`
	cases := map[string][]struct{ method, path, body string }{
		"POST /v1/meetings/{id}/transcription":           {{"POST", m + "/transcription", ""}},
		"GET /v1/meetings/{id}/transcript":               {{"GET", m + "/transcript", ""}},
		"GET /v1/meetings/{id}/transcript/revisions":     {{"GET", m + "/transcript/revisions", ""}},
		"GET /v1/meetings/{id}/transcript/revisions/{n}": {{"GET", m + "/transcript/revisions/1", ""}},
		"POST /v1/meetings/{id}/transcript/revisions":    {{"POST", m + "/transcript/revisions", corr}},
		"GET /v1/meetings/{id}/jobs":                     {{"GET", m + "/jobs", ""}},
		"GET /v1/jobs/{job_id}":                          {{"GET", "/v1/jobs/" + aj, ""}},
	}
	for _, rt := range e.s.routesTranscripts() {
		if _, ok := cases[rt.Pattern()]; !ok {
			t.Errorf("no cross-account case for %s", rt.Pattern())
		}
	}
	for route, list := range cases {
		for _, c := range list {
			st, body := callKey(t, e.srv, c.method, c.path, b.Token, ids.New(), c.body)
			if st != 404 || errCode(body) != "not_found" {
				t.Errorf("%s: %d %v", route, st, body)
			}
		}
	}
	var trAfter string
	if err := e.pool.QueryRow(context.Background(), `SELECT string_agg(t::text, '|') FROM transcript_revisions t`).Scan(&trAfter); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, e.pool); after != before || trAfter != trBefore {
		t.Fatalf("user B's requests wrote data:\nbefore %s\nafter %s", before, after)
	}
}
