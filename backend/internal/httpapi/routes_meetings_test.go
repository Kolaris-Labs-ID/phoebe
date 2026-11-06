package httpapi

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/auth"
	"phoebe/internal/ids"
	"phoebe/internal/meetings"
	"phoebe/internal/testutil"
	"phoebe/internal/users"
)

func newMeetingsServer(t *testing.T) (*Server, *httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	log := testutil.DiscardLogger()
	store := testutil.Storage(t)
	a := auth.NewService(pool, cfg, log, nil)
	ms := meetings.New(pool, cfg, store, log)
	s := New(Deps{Config: cfg, Log: log, DB: pool, Storage: store, Sessions: a, Auth: a,
		Users: users.New(pool, cfg, store, nil, log), Meetings: ms, FoldersTags: ms})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv, pool
}

func TestMeetingsHTTPFlow(t *testing.T) {
	s, srv, pool := newMeetingsServer(t)
	u := testutil.CreateUser(t, pool, 0)
	id := ids.New()

	st, body := call(t, srv, "POST", "/v1/meetings", u.Token, `{"id":"`+id+`","title":"Weekly","timezone":"Asia/Jakarta","started_at":"2026-11-06T09:00:00+07:00"}`)
	m, _ := body["meeting"].(map[string]any)
	if st != 201 || m["id"] != id || m["started_at"] != "2026-11-06T02:00:00.000Z" || m["metadata_revision"] != float64(1) {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "POST", "/v1/meetings", u.Token, `{"id":"`+id+`"}`); st != 200 {
		t.Fatal("replay", st, body)
	}
	if st, body = call(t, srv, "POST", "/v1/meetings", u.Token, `{"title":"x"}`); st != 422 || errCode(body) != "validation_failed" {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "GET", "/v1/meetings/"+id, u.Token, ""); st != 200 {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "GET", "/v1/meetings/not-a-uuid", u.Token, ""); st != 404 {
		t.Fatal(st, body)
	}
	st, body = call(t, srv, "PATCH", "/v1/meetings/"+id, u.Token, `{"expected_metadata_revision":1,"title":"Weekly 2"}`)
	if st != 200 || body["meeting"].(map[string]any)["metadata_revision"] != float64(2) {
		t.Fatal(st, body)
	}
	st, body = call(t, srv, "PATCH", "/v1/meetings/"+id, u.Token, `{"expected_metadata_revision":1,"title":"Weekly 3"}`)
	if st != 409 || errCode(body) != "metadata_conflict" || body["error"].(map[string]any)["details"].(map[string]any)["current_metadata_revision"] != float64(2) {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "PATCH", "/v1/meetings/"+id, u.Token, `{"title":`); st != 400 || errCode(body) != "invalid_json" {
		t.Fatal(st, body)
	}

	st, body = call(t, srv, "POST", "/v1/folders", u.Token, `{"name":"Klien A"}`)
	folder, _ := body["folder"].(map[string]any)
	if st != 201 || folder["name"] != "Klien A" || folder["meeting_count"] != float64(0) {
		t.Fatal(st, body)
	}
	fid := folder["id"].(string)
	if st, body = call(t, srv, "POST", "/v1/folders", u.Token, `{"name":"klien a"}`); st != 409 || errCode(body) != "name_taken" {
		t.Fatal(st, body)
	}
	st, body = call(t, srv, "POST", "/v1/tags", u.Token, `{"id":"`+ids.New()+`","name":"Budget"}`)
	tid := body["tag"].(map[string]any)["id"].(string)
	if st != 201 {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "PATCH", "/v1/meetings/"+id, u.Token, `{"folder_id":"`+fid+`","tag_ids":["`+tid+`"]}`); st != 200 {
		t.Fatal(st, body)
	}
	st, body = call(t, srv, "GET", "/v1/folders", u.Token, "")
	if items := body["items"].([]any); st != 200 || len(items) != 1 || items[0].(map[string]any)["meeting_count"] != float64(1) {
		t.Fatal(st, body)
	}
	if st, body = call(t, srv, "PATCH", "/v1/tags/"+tid, u.Token, `{"name":"Budget Q1"}`); st != 200 || body["tag"].(map[string]any)["name"] != "Budget Q1" {
		t.Fatal(st, body)
	}

	for range 3 {
		call(t, srv, "POST", "/v1/meetings", u.Token, `{"timezone":"UTC"}`)
	}
	var all []string
	cursor := ""
	for {
		path := "/v1/meetings?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		st, body = call(t, srv, "GET", path, u.Token, "")
		if st != 200 {
			t.Fatal(st, body)
		}
		for _, it := range body["items"].([]any) {
			all = append(all, it.(map[string]any)["id"].(string))
		}
		if body["next_cursor"] == nil {
			break
		}
		cursor = body["next_cursor"].(string)
	}
	if len(all) != 4 {
		t.Fatal(all)
	}
	st, body = call(t, srv, "GET", "/v1/meetings?q=budget+q1&folder_id="+fid+"&tag_id="+tid, u.Token, "")
	if items := body["items"].([]any); st != 200 || len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Fatal(st, body)
	}
	for _, q := range []string{"limit=0", "limit=x", "cursor=!!!", "folder_id=bad", "q=" + strings.Repeat("a", 201)} {
		if st, body = call(t, srv, "GET", "/v1/meetings?"+q, u.Token, ""); st != 422 {
			t.Fatal(q, st, body)
		}
	}

	if st, body = call(t, srv, "DELETE", "/v1/meetings/"+id+"/audio", u.Token, ""); st != 409 || errCode(body) != "audio_not_ready" {
		t.Fatal(st, body)
	}
	if st, _ = call(t, srv, "DELETE", "/v1/tags/"+tid, u.Token, ""); st != 204 {
		t.Fatal(st)
	}
	if st, _ = call(t, srv, "DELETE", "/v1/folders/"+fid, u.Token, ""); st != 204 {
		t.Fatal(st)
	}
	st, body = call(t, srv, "DELETE", "/v1/meetings/"+id, u.Token, "")
	if _, ok := body["purge_scheduled_at"].(string); st != 202 || !ok {
		t.Fatal(st, body)
	}
	if st, _ = call(t, srv, "DELETE", "/v1/meetings/"+id, u.Token, ""); st != 404 {
		t.Fatal("repeat delete", st)
	}
	if st, body = call(t, srv, "POST", "/v1/meetings", u.Token, `{"id":"`+id+`","timezone":"UTC"}`); st != 409 || errCode(body) != "meeting_id_conflict" {
		t.Fatal(st, body)
	}

	req := httptest.NewRequest("DELETE", "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+u.Token)
	rec := serve(s, Route{Method: "DELETE", Path: "/v1/me", Handle: s.deleteMe}, req)
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"purge_scheduled_at"`) {
		t.Fatal(rec.Code, rec.Body)
	}
	if st, _ = call(t, srv, "GET", "/v1/meetings", u.Token, ""); st != 401 {
		t.Fatal("old token after account delete", st)
	}
}

func snapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var out strings.Builder
	for _, tbl := range []string{"meetings", "deleted_meetings", "folders", "tags", "meeting_tags", "jobs", "audio_assets",
		"upload_sessions", "quota_reservations"} {
		var s *string
		if err := pool.QueryRow(context.Background(),
			`SELECT string_agg(t::text, '|' ORDER BY t::text) FROM `+tbl+` t`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if s != nil {
			out.WriteString(tbl + ":" + *s + "\n")
		}
	}
	return out.String()
}

func TestMeetingsCrossAccount(t *testing.T) {
	s, srv, pool := newMeetingsServer(t)
	a := testutil.CreateUser(t, pool, 0)
	b := testutil.CreateUser(t, pool, 0)
	_, fb := call(t, srv, "POST", "/v1/folders", a.Token, `{"name":"A folder"}`)
	_, tb := call(t, srv, "POST", "/v1/tags", a.Token, `{"name":"A tag"}`)
	af := fb["folder"].(map[string]any)["id"].(string)
	at := tb["tag"].(map[string]any)["id"].(string)
	am := ids.New()
	if st, body := call(t, srv, "POST", "/v1/meetings", a.Token, `{"id":"`+am+`","title":"Secret A","timezone":"UTC","folder_id":"`+af+`","tag_ids":["`+at+`"]}`); st != 201 {
		t.Fatal(st, body)
	}
	_, mb := call(t, srv, "POST", "/v1/meetings", b.Token, `{"title":"B own","timezone":"UTC"}`)
	bm := mb["meeting"].(map[string]any)["id"].(string)
	before := snapshot(t, pool)

	type tc struct {
		method, path, body string
		status             int
		code, field        string
	}
	cases := map[string][]tc{
		"POST /v1/meetings": {
			{"POST", "/v1/meetings", `{"id":"` + am + `","timezone":"UTC"}`, 409, "meeting_id_conflict", ""},
			{"POST", "/v1/meetings", `{"timezone":"UTC","folder_id":"` + af + `"}`, 422, "validation_failed", "folder_id"},
			{"POST", "/v1/meetings", `{"timezone":"UTC","tag_ids":["` + at + `"]}`, 422, "validation_failed", "tag_ids"},
		},
		"GET /v1/meetings": {
			{"GET", "/v1/meetings?folder_id=" + af, "", 422, "validation_failed", "folder_id"},
			{"GET", "/v1/meetings?tag_id=" + at, "", 422, "validation_failed", "tag_id"},
		},
		"GET /v1/meetings/{id}":          {{"GET", "/v1/meetings/" + am, "", 404, "not_found", ""}},
		"PATCH /v1/meetings/{id}":        {{"PATCH", "/v1/meetings/" + am, `{"title":"pwn"}`, 404, "not_found", ""}, {"PATCH", "/v1/meetings/" + bm, `{"folder_id":"` + af + `"}`, 422, "validation_failed", "folder_id"}, {"PATCH", "/v1/meetings/" + bm, `{"tag_ids":["` + at + `"]}`, 422, "validation_failed", "tag_ids"}},
		"DELETE /v1/meetings/{id}":       {{"DELETE", "/v1/meetings/" + am, "", 404, "not_found", ""}},
		"DELETE /v1/meetings/{id}/audio": {{"DELETE", "/v1/meetings/" + am + "/audio", "", 404, "not_found", ""}},
		"GET /v1/folders":                {},
		"POST /v1/folders":               {{"POST", "/v1/folders", `{"id":"` + af + `","name":"x"}`, 409, "id_conflict", ""}},
		"PATCH /v1/folders/{id}":         {{"PATCH", "/v1/folders/" + af, `{"name":"x"}`, 404, "not_found", ""}},
		"DELETE /v1/folders/{id}":        {{"DELETE", "/v1/folders/" + af, "", 404, "not_found", ""}},
		"GET /v1/tags":                   {},
		"POST /v1/tags":                  {{"POST", "/v1/tags", `{"id":"` + at + `","name":"x"}`, 409, "id_conflict", ""}},
		"PATCH /v1/tags/{id}":            {{"PATCH", "/v1/tags/" + at, `{"name":"x"}`, 404, "not_found", ""}},
		"DELETE /v1/tags/{id}":           {{"DELETE", "/v1/tags/" + at, "", 404, "not_found", ""}},
	}
	for _, rt := range append(s.routesMeetings(), s.routesFoldersTags()...) {
		if _, ok := cases[rt.Pattern()]; !ok {
			t.Errorf("no cross-account case for %s", rt.Pattern())
		}
	}
	for route, list := range cases {
		for _, c := range list {
			st, body := call(t, srv, c.method, c.path, b.Token, c.body)
			if st != c.status || errCode(body) != c.code {
				t.Errorf("%s %s: %d %v", route, c.path, st, body)
				continue
			}
			if c.field != "" {
				f := body["error"].(map[string]any)["details"].(map[string]any)["fields"].([]any)[0].(map[string]any)
				if f["field"] != c.field || f["reason"] != "not_found" {
					t.Errorf("%s: %v", route, f)
				}
			}
		}
	}
	if after := snapshot(t, pool); after != before {
		t.Fatalf("user B's requests wrote data:\nbefore %s\nafter %s", before, after)
	}

	for path, n := range map[string]int{"/v1/meetings": 1, "/v1/meetings?q=secret": 0, "/v1/folders": 0, "/v1/tags": 0} {
		_, body := call(t, srv, "GET", path, b.Token, "")
		if items := body["items"].([]any); len(items) != n {
			t.Errorf("%s leaked: %v", path, items)
		}
	}
}
