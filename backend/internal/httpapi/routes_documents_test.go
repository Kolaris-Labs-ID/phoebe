package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai/fake"
	"phoebe/internal/auth"
	"phoebe/internal/documents"
	"phoebe/internal/ids"
	"phoebe/internal/meetings"
	"phoebe/internal/testutil"
	"phoebe/internal/usage"
)

func newDocumentsServer(t *testing.T) (*Server, *httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	log := testutil.DiscardLogger()
	store := testutil.Storage(t)
	a := auth.NewService(pool, cfg, log, nil)
	ms := meetings.New(pool, cfg, store, log)
	ds := documents.New(pool, cfg, usage.New(pool, cfg, log), &fake.LLM{}, log)
	s := New(Deps{Config: cfg, Log: log, DB: pool, Storage: store, Sessions: a, Auth: a, Meetings: ms, FoldersTags: ms, Documents: ds})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv, pool
}

func docCall(t *testing.T, srv *httptest.Server, method, path, token, key, body string) (int, map[string]any) {
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
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(b) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s %s: non-JSON %q", method, path, b)
		}
	}
	return resp.StatusCode, m
}

// seedTranscribedMeeting inserts a meeting with transcript revision 1 (one segment s1).
func seedTranscribedMeeting(t *testing.T, pool *pgxpool.Pool, uid string) string {
	t.Helper()
	ctx := context.Background()
	mid := ids.New()
	if _, err := pool.Exec(ctx, `INSERT INTO meetings (id, user_id, title, timezone, pipeline_state) VALUES ($1, $2, 'Rapat', 'UTC', 'notes_ready')`, mid, uid); err != nil {
		t.Fatal(err)
	}
	var jid string
	if err := pool.QueryRow(ctx, `INSERT INTO jobs (type, user_id, meeting_id, trigger, status, idempotency_key, request_fingerprint,
		result, max_attempts, correlation_id, finished_at)
		VALUES ('transcribe', $1, $2, 'user', 'succeeded', $3, repeat('a', 64), '{}', 3, gen_random_uuid(), now()) RETURNING id`,
		uid, mid, ids.New()).Scan(&jid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO transcript_revisions (meeting_id, revision_number, kind, language, provider, model, segments, job_id, timing)
		VALUES ($1, 1, 'original', 'id', 'fake', 'fake-stt', '[{"id":"s1","start_ms":0,"end_ms":4000,"text":"Kita sepakat rilis Jumat."}]', $2, 'segment')`,
		mid, jid); err != nil {
		t.Fatal(err)
	}
	return mid
}

const notesContent = `{"schema_version":1,"language":"id","sections":[{"id":"sec_1","heading":"Rilis","style":"bullet","items":[
	{"id":"it_1","text":"Rilis Jumat","origin":"human","edited":false,"sources":[]}]}]}`

func TestDocumentsHTTPFlow(t *testing.T) {
	_, srv, pool := newDocumentsServer(t)
	u := testutil.CreateUser(t, pool, 0)
	mid := seedTranscribedMeeting(t, pool, u.ID)
	base := "/v1/meetings/" + mid + "/documents"

	if st, body := docCall(t, srv, "GET", base+"/ai_notes", u.Token, "", ""); st != 404 || errCode(body) != "not_found" {
		t.Fatal(st, body)
	}
	m1 := ids.New()
	put := `{"expected_revision":0,"mutation_id":"` + m1 + `","content":` + notesContent + `}`
	st, body := docCall(t, srv, "PUT", base+"/ai_notes", u.Token, "", put)
	rev, _ := body["revision"].(map[string]any)
	doc, _ := body["document"].(map[string]any)
	if st != 201 || rev["revision_number"] != float64(1) || rev["origin"] != "user_edit" || doc["review_status"] != "draft" ||
		doc["kind"] != "ai_notes" || rev["content"] == nil {
		t.Fatal(st, body)
	}
	src := rev["source"].(map[string]any)
	for _, k := range []string{"transcript_revision_id", "transcript_revision_number", "manual_notes_revision_id", "ai_notes_revision_id"} {
		if v, ok := src[k]; !ok || v != nil {
			t.Fatalf("source.%s must be present and null: %v", k, src)
		}
	}
	if st, _ = docCall(t, srv, "PUT", base+"/ai_notes", u.Token, "", put); st != 200 {
		t.Fatal("replay", st)
	}
	put2 := `{"expected_revision":1,"mutation_id":"` + ids.New() + `","content":` + notesContent + `}`
	if st, body = docCall(t, srv, "PUT", base+"/ai_notes", u.Token, "", put2); st != 201 {
		t.Fatal(st, body)
	}
	st, body = docCall(t, srv, "PUT", base+"/ai_notes", u.Token, "", put2[:len(put2)-1]+`,"x":1}`)
	if st != 422 || errCode(body) != "idempotency_key_reused" {
		t.Fatal("same mutation_id, different body", st, body)
	}
	if st, body = docCall(t, srv, "PUT", base+"/ai_notes", u.Token, "", `{"expected_revision":1,"mutation_id":"`+ids.New()+`","content":`+notesContent+`}`); st != 409 ||
		errCode(body) != "revision_conflict" || body["error"].(map[string]any)["details"].(map[string]any)["current_revision_number"] != float64(2) {
		t.Fatal(st, body)
	}
	if st, body = docCall(t, srv, "PUT", base+"/ai_notes", u.Token, "", `{"expected_revision":"1"}`); st != 400 || errCode(body) != "invalid_json" {
		t.Fatal(st, body)
	}

	st, body = docCall(t, srv, "GET", base+"/ai_notes/revisions?limit=1", u.Token, "", "")
	items := body["items"].([]any)
	if st != 200 || len(items) != 1 || items[0].(map[string]any)["revision_number"] != float64(2) || body["next_cursor"] == nil {
		t.Fatal(st, body)
	}
	if _, has := items[0].(map[string]any)["content"]; has {
		t.Fatal("listing must omit content")
	}
	st, body = docCall(t, srv, "GET", base+"/ai_notes/revisions?limit=1&cursor="+url.QueryEscape(body["next_cursor"].(string)), u.Token, "", "")
	if items = body["items"].([]any); st != 200 || len(items) != 1 || items[0].(map[string]any)["revision_number"] != float64(1) || body["next_cursor"] != nil {
		t.Fatal(st, body)
	}
	if st, _ = docCall(t, srv, "GET", base+"/ai_notes/revisions?cursor=bad!", u.Token, "", ""); st != 422 {
		t.Fatal(st)
	}
	if st, body = docCall(t, srv, "GET", base+"/ai_notes/revisions/1", u.Token, "", ""); st != 200 || body["revision"].(map[string]any)["content"] == nil {
		t.Fatal(st, body)
	}
	for _, p := range []string{base + "/ai_notes/revisions/0", base + "/ai_notes/revisions/x", base + "/notes", "/v1/meetings/x/documents/mom"} {
		if st, _ = docCall(t, srv, "GET", p, u.Token, "", ""); st != 404 {
			t.Fatal(p, st)
		}
	}

	st, body = docCall(t, srv, "POST", base+"/ai_notes/restore", u.Token, "", `{"revision_number":1,"expected_revision":2,"mutation_id":"`+ids.New()+`"}`)
	if st != 201 || body["revision"].(map[string]any)["restored_from_revision_number"] != float64(1) {
		t.Fatal(st, body)
	}
	st, body = docCall(t, srv, "PUT", base+"/ai_notes/review", u.Token, "", `{"status":"reviewed","expected_revision":3}`)
	if st != 200 || body["document"].(map[string]any)["review_status"] != "reviewed" {
		t.Fatal(st, body)
	}

	if st, body = docCall(t, srv, "POST", base+"/ai_notes/generate", u.Token, "", `{}`); st != 400 || errCode(body) != "missing_idempotency_key" {
		t.Fatal(st, body)
	}
	key := ids.New()
	st, body = docCall(t, srv, "POST", base+"/ai_notes/generate", u.Token, key, `{"language":null}`)
	job, _ := body["job"].(map[string]any)
	if st != 202 || job["type"] != "generate_notes" || job["status"] != "queued" || job["trigger"] != "user" {
		t.Fatal(st, body)
	}
	if st, body = docCall(t, srv, "POST", base+"/ai_notes/generate", u.Token, key, `{"language":null}`); st != 200 || body["job"].(map[string]any)["id"] != job["id"] {
		t.Fatal(st, body)
	}
	if st, body = docCall(t, srv, "POST", base+"/ai_notes/generate", u.Token, key, `{"language":"en"}`); st != 422 || errCode(body) != "idempotency_key_reused" {
		t.Fatal(st, body)
	}
	if st, body = docCall(t, srv, "POST", base+"/ai_notes/generate", u.Token, ids.New(), ``); st != 409 || errCode(body) != "job_in_progress" {
		t.Fatal(st, body)
	}
	if st, _ = docCall(t, srv, "POST", base+"/manual_notes/generate", u.Token, ids.New(), `{}`); st != 404 {
		t.Fatal(st)
	}
	st, body = docCall(t, srv, "POST", base+"/mom/generate", u.Token, ids.New(), `{"ai_notes_revision_id":null,"language":null}`)
	if st != 202 || body["job"].(map[string]any)["type"] != "generate_mom" {
		t.Fatal(st, body)
	}
	st, body = docCall(t, srv, "GET", "/v1/meetings/"+mid, u.Token, "", "")
	m := body["meeting"].(map[string]any)
	if st != 200 || m["documents"].(map[string]any)["ai_notes"].(map[string]any)["review_status"] != "reviewed" || m["pipeline_state"] != "generating_notes" {
		t.Fatal(st, body)
	}
}

func docSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var out strings.Builder
	for _, tbl := range []string{"documents", "document_revisions", "jobs", "search_documents", "events", "meetings"} {
		var s *string
		if err := pool.QueryRow(context.Background(), `SELECT string_agg(t::text, '|' ORDER BY t::text) FROM `+tbl+` t`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s != nil {
			out.WriteString(tbl + ":" + *s + "\n")
		}
	}
	return out.String()
}

func TestDocumentsCrossAccount(t *testing.T) {
	s, srv, pool := newDocumentsServer(t)
	a := testutil.CreateUser(t, pool, 0)
	b := testutil.CreateUser(t, pool, 0)
	am := seedTranscribedMeeting(t, pool, a.ID)
	bm := seedTranscribedMeeting(t, pool, b.ID)
	st, body := docCall(t, srv, "PUT", "/v1/meetings/"+am+"/documents/ai_notes", a.Token, "",
		`{"expected_revision":0,"mutation_id":"`+ids.New()+`","content":`+notesContent+`}`)
	if st != 201 {
		t.Fatal(st, body)
	}
	aRev := body["revision"].(map[string]any)["id"].(string)
	before := docSnapshot(t, pool)

	base := "/v1/meetings/" + am + "/documents"
	type tc struct{ method, path, key, body string }
	cases := map[string][]tc{
		"GET /v1/meetings/{id}/documents/{kind}":               {{"GET", base + "/ai_notes", "", ""}},
		"PUT /v1/meetings/{id}/documents/{kind}":               {{"PUT", base + "/ai_notes", "", `{"expected_revision":1,"mutation_id":"` + ids.New() + `","content":` + notesContent + `}`}},
		"GET /v1/meetings/{id}/documents/{kind}/revisions":     {{"GET", base + "/ai_notes/revisions", "", ""}},
		"GET /v1/meetings/{id}/documents/{kind}/revisions/{n}": {{"GET", base + "/ai_notes/revisions/1", "", ""}},
		"POST /v1/meetings/{id}/documents/{kind}/restore":      {{"POST", base + "/ai_notes/restore", "", `{"revision_number":1,"expected_revision":1,"mutation_id":"` + ids.New() + `"}`}},
		"PUT /v1/meetings/{id}/documents/{kind}/review":        {{"PUT", base + "/ai_notes/review", "", `{"status":"reviewed","expected_revision":1}`}},
		"POST /v1/meetings/{id}/documents/ai_notes/generate":   {{"POST", base + "/ai_notes/generate", ids.New(), `{}`}},
		"POST /v1/meetings/{id}/documents/mom/generate": {
			{"POST", base + "/mom/generate", ids.New(), `{}`},
			{"POST", "/v1/meetings/" + bm + "/documents/mom/generate", ids.New(), `{"ai_notes_revision_id":"` + aRev + `"}`},
		},
	}
	for _, rt := range s.routesDocuments() {
		if _, ok := cases[rt.Pattern()]; !ok {
			t.Errorf("no cross-account case for %s", rt.Pattern())
		}
	}
	for route, list := range cases {
		for _, c := range list {
			if st, body := docCall(t, srv, c.method, c.path, b.Token, c.key, c.body); st != 404 || errCode(body) != "not_found" {
				t.Errorf("%s %s: %d %v", route, c.path, st, body)
			}
		}
	}
	if after := docSnapshot(t, pool); after != before {
		t.Fatalf("user B's requests wrote data:\nbefore %s\nafter %s", before, after)
	}
}
