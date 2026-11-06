package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/httpapi"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
)

var ctx = context.Background()

type world struct {
	t      *testing.T
	pool   *pgxpool.Pool
	srv    *httptest.Server
	api    *httpapi.Server
	runner *jobs.Runner
}

func newWorld(t *testing.T) *world {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	cfg.UploadChunkMaxBytes = 1 << 20
	log := testutil.DiscardLogger()
	store := testutil.Storage(t)
	signer := storage.NewSigner(cfg.SigningSecret, cfg.PublicBaseURL, cfg.SignedURLTTL)
	deps, err := APIDeps(cfg, log, pool, store, signer)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(deps)
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	r := jobs.NewRunner(pool, log, jobs.RunnerConfig{WorkerID: "test", Lease: time.Minute, RetryBase: time.Millisecond})
	if err := RegisterWorker(ctx, r, cfg, log, pool, store); err != nil {
		t.Fatal(err)
	}
	return &world{t: t, pool: pool, srv: srv, api: api, runner: r}
}

type req struct {
	method, path, token, key, body string
	offset                         string
	raw                            []byte
}

func (w *world) do(r req) (int, map[string]any) {
	w.t.Helper()
	var body io.Reader = strings.NewReader(r.body)
	if r.raw != nil {
		body = bytes.NewReader(r.raw)
	}
	hr, _ := http.NewRequest(r.method, w.srv.URL+r.path, body)
	if r.token != "" {
		hr.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.key != "" {
		hr.Header.Set("Idempotency-Key", r.key)
	}
	if r.offset != "" {
		hr.Header.Set("Upload-Offset", r.offset)
		hr.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

func (w *world) must(status int, r req) map[string]any {
	w.t.Helper()
	st, m := w.do(r)
	if st != status {
		w.t.Fatalf("%s %s: %d %v", r.method, r.path, st, m)
	}
	return m
}

func (w *world) drain() {
	w.t.Helper()
	for range 20 {
		ran, err := w.runner.RunOnce(ctx)
		if err != nil {
			w.t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	w.t.Fatal("jobs did not drain")
}

func (w *world) login(subject string) string {
	return w.must(200, req{method: "POST", path: "/v1/auth/dev", body: `{"subject":"` + subject + `"}`})["token"].(string)
}

func get(m map[string]any, path ...string) any {
	var v any = m
	for _, k := range path {
		v = v.(map[string]any)[k]
	}
	return v
}

func errCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

// seeded is user A's full data set, built through the real router and worker.
type seeded struct {
	folder, tag, meeting, openMeeting, openUpload, upload, job string
	aiNotesRev, momRev, export, audioURL, exportURL            string
}

func (w *world) seed(token, title string) seeded {
	var s seeded
	s.folder = get(w.must(201, req{method: "POST", path: "/v1/folders", token: token, body: `{"name":"Klien A"}`}), "folder", "id").(string)
	s.tag = get(w.must(201, req{method: "POST", path: "/v1/tags", token: token, body: `{"name":"Rahasia"}`}), "tag", "id").(string)
	s.meeting = get(w.must(201, req{method: "POST", path: "/v1/meetings", token: token,
		body: `{"title":"` + title + `","timezone":"Asia/Jakarta","started_at":"2026-11-06T02:00:00Z","folder_id":"` + s.folder + `","tag_ids":["` + s.tag + `"]}`}),
		"meeting", "id").(string)

	wav := testutil.WAV(60_000)
	half := len(wav) / 2
	s.upload = get(w.must(201, req{method: "POST", path: "/v1/meetings/" + s.meeting + "/uploads", token: token,
		body: `{"size_bytes":` + strconv.Itoa(len(wav)) + `,"sha256":"` + testutil.SHA256Hex(wav) +
			`","content_type":"audio/wav","duration_ms":60000,"transcription":{"idempotency_key":"` + ids.New() + `","language":"id"}}`}),
		"upload", "id").(string)
	w.must(200, req{method: "PUT", path: "/v1/uploads/" + s.upload, token: token, offset: "0", raw: wav[:half]})
	last := w.must(200, req{method: "PUT", path: "/v1/uploads/" + s.upload, token: token, offset: strconv.Itoa(half), raw: wav[half:]})
	s.job = get(last, "job", "id").(string)
	w.drain()

	m := w.must(200, req{method: "GET", path: "/v1/meetings/" + s.meeting, token: token})
	if get(m, "meeting", "pipeline_state") != "notes_ready" {
		w.t.Fatalf("pipeline: %v", m)
	}
	s.aiNotesRev = get(m, "meeting", "documents", "ai_notes", "current_revision_id").(string)
	w.must(202, req{method: "POST", path: "/v1/meetings/" + s.meeting + "/documents/mom/generate", token: token, key: ids.New(), body: `{}`})
	w.drain()
	s.momRev = get(w.must(200, req{method: "GET", path: "/v1/meetings/" + s.meeting + "/documents/mom", token: token}), "revision", "id").(string)
	ex := w.must(201, req{method: "POST", path: "/v1/exports", token: token, body: `{"document_revision_id":"` + s.momRev + `","format":"pdf"}`})
	s.export = get(ex, "export", "id").(string)
	s.exportURL = ex["download_url"].(string)
	s.audioURL = w.must(200, req{method: "GET", path: "/v1/meetings/" + s.meeting + "/audio/url", token: token})["url"].(string)

	s.openMeeting = get(w.must(201, req{method: "POST", path: "/v1/meetings", token: token, body: `{"timezone":"UTC"}`}), "meeting", "id").(string)
	s.openUpload = get(w.must(201, req{method: "POST", path: "/v1/meetings/" + s.openMeeting + "/uploads", token: token,
		body: `{"size_bytes":` + strconv.Itoa(len(wav)) + `,"sha256":"` + testutil.SHA256Hex(wav) + `","content_type":"audio/wav","duration_ms":60000}`}),
		"upload", "id").(string)
	w.must(200, req{method: "PUT", path: "/v1/uploads/" + s.openUpload, token: token, offset: "0", raw: wav[:half]})
	return s
}

// snapshot dumps every table except sessions and events, which B's own requests
// legitimately write (B's events are checked separately).
func snapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = 'public'
		AND tablename NOT IN ('sessions', 'events') ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var out strings.Builder
	for _, tbl := range tables {
		var s *string
		if err := pool.QueryRow(ctx, `SELECT string_agg(t::text, '|' ORDER BY t::text) FROM "`+tbl+`" t`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s != nil {
			out.WriteString(tbl + ":" + *s + "\n")
		}
	}
	return out.String()
}

func forge(signed string) string {
	u, _ := url.Parse(signed)
	q := u.Query()
	q.Set("sig", "AAAA"+q.Get("sig")[4:])
	return u.Path + "?" + q.Encode()
}

// TestCrossAccountAllRoutes is the backend.md §18 cross-account test over the production
// route table: every route needs a case or an explicit reason it takes no foreign id.
func TestCrossAccountAllRoutes(t *testing.T) {
	w := newWorld(t)
	at, bt := w.login("alice"), w.login("bob")
	a := w.seed(at, "Secret A")
	bm := w.seed(bt, "Bob B").meeting
	wav := testutil.WAV(60_000)
	before := snapshot(t, w.pool)

	m := "/v1/meetings/" + a.meeting
	nf := func(r req) req { r.token = bt; return r }
	type want struct {
		status int
		code   string
		field  string
	}
	notFound := want{404, "not_found", ""}
	type tc struct {
		r req
		w want
	}
	cases := map[string][]tc{
		"POST /v1/meetings": {
			{nf(req{method: "POST", path: "/v1/meetings", body: `{"id":"` + a.meeting + `","timezone":"UTC"}`}), want{409, "meeting_id_conflict", ""}},
			{nf(req{method: "POST", path: "/v1/meetings", body: `{"timezone":"UTC","folder_id":"` + a.folder + `"}`}), want{422, "validation_failed", "folder_id"}},
			{nf(req{method: "POST", path: "/v1/meetings", body: `{"timezone":"UTC","tag_ids":["` + a.tag + `"]}`}), want{422, "validation_failed", "tag_ids"}},
		},
		"GET /v1/meetings": {
			{nf(req{method: "GET", path: "/v1/meetings?folder_id=" + a.folder}), want{422, "validation_failed", "folder_id"}},
			{nf(req{method: "GET", path: "/v1/meetings?tag_id=" + a.tag}), want{422, "validation_failed", "tag_id"}},
		},
		"GET /v1/meetings/{id}": {{nf(req{method: "GET", path: m}), notFound}},
		"PATCH /v1/meetings/{id}": {
			{nf(req{method: "PATCH", path: m, body: `{"title":"pwn"}`}), notFound},
			{nf(req{method: "PATCH", path: "/v1/meetings/" + bm, body: `{"folder_id":"` + a.folder + `"}`}), want{422, "validation_failed", "folder_id"}},
			{nf(req{method: "PATCH", path: "/v1/meetings/" + bm, body: `{"tag_ids":["` + a.tag + `"]}`}), want{422, "validation_failed", "tag_ids"}},
		},
		"DELETE /v1/meetings/{id}":        {{nf(req{method: "DELETE", path: m}), notFound}},
		"DELETE /v1/meetings/{id}/audio":  {{nf(req{method: "DELETE", path: m + "/audio"}), notFound}},
		"GET /v1/meetings/{id}/audio/url": {{nf(req{method: "GET", path: m + "/audio/url"}), notFound}},
		"POST /v1/folders":                {{nf(req{method: "POST", path: "/v1/folders", body: `{"id":"` + a.folder + `","name":"x"}`}), want{409, "id_conflict", ""}}},
		"PATCH /v1/folders/{id}":          {{nf(req{method: "PATCH", path: "/v1/folders/" + a.folder, body: `{"name":"x"}`}), notFound}},
		"DELETE /v1/folders/{id}":         {{nf(req{method: "DELETE", path: "/v1/folders/" + a.folder}), notFound}},
		"POST /v1/tags":                   {{nf(req{method: "POST", path: "/v1/tags", body: `{"id":"` + a.tag + `","name":"x"}`}), want{409, "id_conflict", ""}}},
		"PATCH /v1/tags/{id}":             {{nf(req{method: "PATCH", path: "/v1/tags/" + a.tag, body: `{"name":"x"}`}), notFound}},
		"DELETE /v1/tags/{id}":            {{nf(req{method: "DELETE", path: "/v1/tags/" + a.tag}), notFound}},
		"POST /v1/meetings/{id}/uploads": {{nf(req{method: "POST", path: "/v1/meetings/" + a.openMeeting + "/uploads",
			body: `{"size_bytes":` + strconv.Itoa(len(wav)) + `,"sha256":"` + testutil.SHA256Hex(wav) + `","content_type":"audio/wav","duration_ms":60000}`}), notFound}},
		"GET /v1/uploads/{id}":                           {{nf(req{method: "GET", path: "/v1/uploads/" + a.openUpload}), notFound}},
		"PUT /v1/uploads/{id}":                           {{nf(req{method: "PUT", path: "/v1/uploads/" + a.openUpload, offset: strconv.Itoa(len(wav) / 2), raw: wav[len(wav)/2:]}), notFound}},
		"POST /v1/uploads/{id}/complete":                 {{nf(req{method: "POST", path: "/v1/uploads/" + a.upload + "/complete"}), notFound}},
		"POST /v1/meetings/{id}/transcription":           {{nf(req{method: "POST", path: m + "/transcription", key: ids.New(), body: `{}`}), notFound}},
		"GET /v1/meetings/{id}/transcript":               {{nf(req{method: "GET", path: m + "/transcript"}), notFound}},
		"GET /v1/meetings/{id}/transcript/revisions":     {{nf(req{method: "GET", path: m + "/transcript/revisions"}), notFound}},
		"GET /v1/meetings/{id}/transcript/revisions/{n}": {{nf(req{method: "GET", path: m + "/transcript/revisions/1"}), notFound}},
		"POST /v1/meetings/{id}/transcript/revisions": {{nf(req{method: "POST", path: m + "/transcript/revisions",
			body: `{"expected_revision":1,"mutation_id":"` + ids.New() + `","edits":[{"segment_id":"s1","text":"pwn"}]}`}), notFound}},
		"GET /v1/meetings/{id}/jobs":             {{nf(req{method: "GET", path: m + "/jobs"}), notFound}},
		"GET /v1/jobs/{job_id}":                  {{nf(req{method: "GET", path: "/v1/jobs/" + a.job}), notFound}},
		"GET /v1/meetings/{id}/documents/{kind}": {{nf(req{method: "GET", path: m + "/documents/mom"}), notFound}},
		"PUT /v1/meetings/{id}/documents/{kind}": {{nf(req{method: "PUT", path: m + "/documents/manual_notes",
			body: `{"expected_revision":0,"mutation_id":"` + ids.New() + `","content":{"schema_version":1,"items":[]}}`}), notFound}},
		"GET /v1/meetings/{id}/documents/{kind}/revisions":     {{nf(req{method: "GET", path: m + "/documents/ai_notes/revisions"}), notFound}},
		"GET /v1/meetings/{id}/documents/{kind}/revisions/{n}": {{nf(req{method: "GET", path: m + "/documents/ai_notes/revisions/1"}), notFound}},
		"POST /v1/meetings/{id}/documents/{kind}/restore": {{nf(req{method: "POST", path: m + "/documents/mom/restore",
			body: `{"revision_number":1,"expected_revision":1,"mutation_id":"` + ids.New() + `"}`}), notFound}},
		"PUT /v1/meetings/{id}/documents/{kind}/review":      {{nf(req{method: "PUT", path: m + "/documents/mom/review", body: `{"status":"reviewed","expected_revision":1}`}), notFound}},
		"POST /v1/meetings/{id}/documents/ai_notes/generate": {{nf(req{method: "POST", path: m + "/documents/ai_notes/generate", key: ids.New(), body: `{}`}), notFound}},
		"POST /v1/meetings/{id}/documents/mom/generate": {
			{nf(req{method: "POST", path: m + "/documents/mom/generate", key: ids.New(), body: `{}`}), notFound},
			{nf(req{method: "POST", path: "/v1/meetings/" + bm + "/documents/mom/generate", key: ids.New(), body: `{"ai_notes_revision_id":"` + a.aiNotesRev + `"}`}), notFound},
		},
		"POST /v1/exports":     {{nf(req{method: "POST", path: "/v1/exports", body: `{"document_revision_id":"` + a.momRev + `","format":"md"}`}), notFound}},
		"GET /v1/exports/{id}": {{nf(req{method: "GET", path: "/v1/exports/" + a.export}), notFound}},
		"GET /v1/files/{kind}/{id}": {
			{req{method: "GET", path: forge(a.audioURL)}, want{403, "invalid_signature", ""}},
			{req{method: "GET", path: forge(a.exportURL)}, want{403, "invalid_signature", ""}},
		},
	}
	noForeignID := map[string]string{
		"GET /healthz":         "public, no user data",
		"GET /v1/config":       "public, no user data",
		"POST /v1/auth/apple":  "sign-in, identity from the token only",
		"POST /v1/auth/dev":    "sign-in, identity from the subject only",
		"POST /v1/auth/logout": "acts on the caller's session",
		"GET /v1/me":           "caller only",
		"PATCH /v1/me":         "caller only",
		"DELETE /v1/me":        "caller only",
		"GET /v1/usage":        "caller only",
		"GET /v1/folders":      "caller's list; leak checked below",
		"GET /v1/tags":         "caller's list; leak checked below",
		"POST /v1/events":      "foreign meeting_id stored as null; checked below",
	}
	routes := w.api.Routes()
	if len(routes) < 47 {
		t.Fatalf("route table has %d routes, api.md §4 has 47", len(routes))
	}
	for _, rt := range routes {
		_, hasCase := cases[rt.Pattern()]
		_, exempt := noForeignID[rt.Pattern()]
		if !hasCase && !exempt {
			t.Errorf("no cross-account case for %s", rt.Pattern())
		}
	}
	for route, list := range cases {
		for _, c := range list {
			st, body := w.do(c.r)
			if st != c.w.status || errCode(body) != c.w.code {
				t.Errorf("%s %s: got %d %v, want %d %s", route, c.r.path, st, body, c.w.status, c.w.code)
				continue
			}
			if c.w.field != "" {
				f := get(body, "error", "details").(map[string]any)["fields"].([]any)[0].(map[string]any)
				if f["field"] != c.w.field || f["reason"] != "not_found" {
					t.Errorf("%s: %v", route, f)
				}
			}
		}
	}

	w.must(202, req{method: "POST", path: "/v1/events", token: bt,
		body: `{"events":[{"event_id":"` + ids.New() + `","name":"recording_started","occurred_at":"2026-11-06T02:00:00Z","meeting_id":"` + a.meeting + `"}]}`})
	var leaked int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE meeting_id = $1 AND user_id <> (SELECT user_id FROM meetings WHERE id = $1)`,
		a.meeting).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("B's event linked to A's meeting", leaked, err)
	}
	for path, n := range map[string]int{"/v1/meetings": 2, "/v1/meetings?q=secret": 0, "/v1/folders": 1, "/v1/tags": 1} {
		if items := w.must(200, req{method: "GET", path: path, token: bt})["items"].([]any); len(items) != n {
			t.Errorf("%s leaked: %v", path, items)
		}
	}
	if after := snapshot(t, w.pool); after != before {
		t.Fatalf("user B's requests wrote data:\nbefore %s\nafter %s", before, after)
	}
	if items := w.must(200, req{method: "GET", path: "/v1/meetings?q=secret", token: at})["items"].([]any); len(items) != 1 {
		t.Fatal("A's own search", items)
	}
}

// TestPipelineThroughProductionWiring runs the full PRD flow in-process: upload with
// auto-start → transcribe → notes → MoM → edit → review → every export format → delete.
func TestPipelineThroughProductionWiring(t *testing.T) {
	w := newWorld(t)
	tok := w.login("carol")
	s := w.seed(tok, "Weekly sync")
	m := "/v1/meetings/" + s.meeting

	mom := w.must(200, req{method: "GET", path: m + "/documents/mom", token: tok})
	rev := get(mom, "revision")
	content, _ := json.Marshal(rev.(map[string]any)["content"])
	n := int(rev.(map[string]any)["revision_number"].(float64))
	edit := `{"expected_revision":` + strconv.Itoa(n) + `,"mutation_id":"` + ids.New() + `","content":` + string(content) + `}`
	w.must(201, req{method: "PUT", path: m + "/documents/mom", token: tok, body: edit})
	if st, body := w.do(req{method: "PUT", path: m + "/documents/mom", token: tok,
		body: `{"expected_revision":` + strconv.Itoa(n) + `,"mutation_id":"` + ids.New() + `","content":` + string(content) + `}`}); st != 409 || errCode(body) != "revision_conflict" {
		t.Fatal(st, body)
	}
	reviewed := w.must(200, req{method: "PUT", path: m + "/documents/mom/review", token: tok, body: `{"status":"reviewed","expected_revision":` + strconv.Itoa(n+1) + `}`})
	if get(reviewed, "document", "review_status") != "reviewed" {
		t.Fatal(reviewed)
	}
	cur := get(w.must(200, req{method: "GET", path: m + "/documents/mom", token: tok}), "revision", "id").(string)
	for _, f := range []string{"md", "docx", "pdf", "whatsapp"} {
		ex := w.must(201, req{method: "POST", path: "/v1/exports", token: tok, body: `{"document_revision_id":"` + cur + `","format":"` + f + `"}`})
		if get(ex, "export", "is_draft") != false {
			t.Fatal(f, "reviewed revision exported as draft")
		}
		u, _ := url.Parse(ex["download_url"].(string))
		resp, err := http.Get(w.srv.URL + u.Path + "?" + u.RawQuery)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || int64(len(b)) != int64(get(ex, "export", "size_bytes").(float64)) {
			t.Fatal(f, resp.StatusCode, len(b))
		}
	}
	w.must(202, req{method: "DELETE", path: m, token: tok})
	w.must(404, req{method: "GET", path: m, token: tok})
}
