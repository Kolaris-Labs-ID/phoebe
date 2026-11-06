package meetings

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
	"phoebe/internal/testutil"
	"phoebe/internal/usage"
)

func TestCreateIdempotentByID(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	b := testutil.CreateUser(t, e.pool, 0)
	id := strings.ToUpper(ids.New())

	m := e.create(t, a.ID, CreateInput{ID: &id, Title: ptr("  Weekly sync "), Source: ptr("import"),
		Participants: []string{" Budi ", "Sari"}, Language: ptr("id")})
	if m.ID != strings.ToLower(id) || m.Title != "Weekly sync" || m.Source != "import" || m.Language != "id" ||
		!slices.Equal(m.Participants, []string{"Budi", "Sari"}) || m.MetadataRevision != 1 ||
		m.PipelineState != StateAwaitingAudio || m.StartedAt != nil || len(m.TagIDs) != 0 {
		t.Fatalf("%+v", m)
	}

	again, created, err := e.svc.Create(ctx, a.ID, CreateInput{ID: &id, Title: ptr("other"), Timezone: ptr("bogus")})
	if err != nil || created || again.ID != m.ID || again.Title != "Weekly sync" {
		t.Fatalf("replay: %+v %v %v", again, created, err)
	}
	if _, _, err := e.svc.Create(ctx, b.ID, CreateInput{ID: &id, Timezone: ptr("Asia/Jakarta")}); code(err) != apperr.MeetingIDConflict {
		t.Fatalf("other owner: %v", err)
	}
	if e.count(t, `SELECT count(*) FROM meetings`) != 1 {
		t.Fatal("duplicate meeting")
	}

	// concurrent first creates with one id → one row, all succeed
	cid := ids.New()
	var wg sync.WaitGroup
	results := make([]bool, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i], errs[i] = e.svc.Create(ctx, a.ID, CreateInput{ID: &cid, Timezone: ptr("Asia/Jakarta")})
		}()
	}
	wg.Wait()
	createdN := 0
	for i := range 8 {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if results[i] {
			createdN++
		}
	}
	if createdN != 1 || e.count(t, `SELECT count(*) FROM meetings WHERE id = $1`, cid) != 1 {
		t.Fatalf("concurrent create: %d created", createdN)
	}

	gen, created, err := e.svc.Create(ctx, a.ID, CreateInput{Timezone: ptr("Asia/Makassar")})
	if err != nil || !created || !ids.Valid(gen.ID) || gen.Source != "recording" || gen.Language != "auto" {
		t.Fatalf("server id: %+v %v", gen, err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	b := testutil.CreateUser(t, e.pool, 0)
	bFolder := e.label(t, Folders, b.ID, "B folder")
	bTag := e.label(t, Tags, b.ID, "B tag")
	many := make([]string, 21)
	for i := range many {
		many[i] = ids.New()
	}
	cases := []struct {
		in   CreateInput
		want map[string]string
	}{
		{CreateInput{}, map[string]string{"timezone": "required"}},
		{CreateInput{Timezone: ptr("Mars/Olympus")}, map[string]string{"timezone": "invalid"}},
		{CreateInput{Timezone: ptr("Local")}, map[string]string{"timezone": "invalid"}},
		{CreateInput{Timezone: ptr("UTC"), Title: ptr(strings.Repeat("é", 201))}, map[string]string{"title": "too_long"}},
		{CreateInput{Timezone: ptr("UTC"), Language: ptr("fr"), Source: ptr("upload")}, map[string]string{"language": "invalid", "source": "invalid"}},
		{CreateInput{Timezone: ptr("UTC"), Participants: []string{"Budi", "  "}}, map[string]string{"participants": "too_short"}},
		{CreateInput{Timezone: ptr("UTC"), Participants: []string{strings.Repeat("x", 101)}}, map[string]string{"participants": "too_long"}},
		{CreateInput{Timezone: ptr("UTC"), Participants: make([]string, 101)}, map[string]string{"participants": "too_long"}},
		{CreateInput{Timezone: ptr("UTC"), TagIDs: many}, map[string]string{"tag_ids": "too_long"}},
		{CreateInput{Timezone: ptr("UTC"), TagIDs: []string{"nope"}, FolderID: ptr("nope")}, map[string]string{"tag_ids": "invalid", "folder_id": "invalid"}},
		{CreateInput{Timezone: ptr("UTC"), FolderID: &bFolder, TagIDs: []string{bTag}}, map[string]string{"folder_id": "not_found", "tag_ids": "not_found"}},
		{CreateInput{ID: ptr("not-a-uuid"), Timezone: ptr("UTC")}, map[string]string{"id": "invalid"}},
	}
	for i, c := range cases {
		_, _, err := e.svc.Create(ctx, a.ID, c.in)
		if got := fields(err); !mapsEqual(got, c.want) {
			t.Errorf("case %d: %v, want %v (%v)", i, got, c.want, err)
		}
	}
	if e.count(t, `SELECT count(*) FROM meetings`) != 0 {
		t.Fatal("rejected create wrote a meeting")
	}

	// 20 tags with duplicates is fine; duplicates collapse
	tagA := e.label(t, Tags, a.ID, "a")
	m := e.create(t, a.ID, CreateInput{TagIDs: []string{tagA, strings.ToUpper(tagA)}})
	if !slices.Equal(m.TagIDs, []string{tagA}) {
		t.Fatal(m.TagIDs)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestPatchMetadataConflictAndLWW(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	m := e.create(t, a.ID, CreateInput{Title: ptr("v1")})
	f1 := e.label(t, Folders, a.ID, "F1")
	f2 := e.label(t, Folders, a.ID, "F2")
	tag := e.label(t, Tags, a.ID, "T")

	got, err := e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"expected_metadata_revision":1,"title":"v2","participants":["Budi"]}`))
	if err != nil || got.Title != "v2" || got.MetadataRevision != 2 || !slices.Equal(got.Participants, []string{"Budi"}) {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"expected_metadata_revision":1,"title":"v3"}`))
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.MetadataConflict || ae.Details["current_metadata_revision"] != 2 {
		t.Fatalf("stale patch: %v", err)
	}
	cur, _ := e.svc.Get(ctx, a.ID, m.ID)
	if cur.Title != "v2" {
		t.Fatal("conflicting patch applied")
	}

	// folder/tags are LWW: no expected revision needed, a stale one is ignored, revision unchanged
	got, err = e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"folder_id":"`+f1+`","tag_ids":["`+tag+`"]}`))
	if err != nil || *got.FolderID != f1 || !slices.Equal(got.TagIDs, []string{tag}) || got.MetadataRevision != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"expected_metadata_revision":1,"folder_id":"`+f2+`"}`))
	if err != nil || *got.FolderID != f2 || got.MetadataRevision != 2 {
		t.Fatalf("folder move with stale expected: %+v %v", got, err)
	}
	got, err = e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"folder_id":null,"tag_ids":[]}`))
	if err != nil || got.FolderID != nil || len(got.TagIDs) != 0 {
		t.Fatalf("%+v %v", got, err)
	}

	// unchanged values do not bump metadata_revision; started_at set and cleared
	got, _ = e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"expected_metadata_revision":2,"title":"v2"}`))
	if got.MetadataRevision != 2 {
		t.Fatal("no-op bumped revision")
	}
	got, _ = e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"started_at":"2026-11-06T09:00:00+07:00","timezone":"Asia/Makassar","language":"en"}`))
	if got.MetadataRevision != 3 || got.StartedAt == nil || jsonx.FormatTime(got.StartedAt.Time) != "2026-11-06T02:00:00.000Z" ||
		got.Timezone != "Asia/Makassar" || got.Language != "en" {
		t.Fatalf("%+v", got)
	}
	got, _ = e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"started_at":null}`))
	if got.StartedAt != nil || got.MetadataRevision != 4 {
		t.Fatalf("%+v", got)
	}

	for body, want := range map[string]map[string]string{
		`{"title":null}`:                               {"title": "invalid"},
		`{"timezone":null}`:                            {"timezone": "invalid"},
		`{"language":"xx"}`:                            {"language": "invalid"},
		`{"participants":null}`:                        {"participants": "invalid"},
		`{"tag_ids":null}`:                             {"tag_ids": "invalid"},
		`{"folder_id":"` + ids.New() + `"}`:            {"folder_id": "not_found"},
		`{"tag_ids":["` + ids.New() + `"]}`:            {"tag_ids": "not_found"},
		`{"title":"` + strings.Repeat("x", 201) + `"}`: {"title": "too_long"},
	} {
		if _, err := e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, body)); !mapsEqual(fields(err), want) {
			t.Errorf("%s: %v", body, err)
		}
	}
	if _, err := e.svc.Update(ctx, a.ID, ids.New(), decodePatch(t, `{"title":"x"}`)); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestConcurrentMetadataPatches(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	m := e.create(t, a.ID, CreateInput{})
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, `{"expected_metadata_revision":1,"title":"t`+string(rune('a'+i))+`"}`))
		}()
	}
	wg.Wait()
	ok, conflicts := 0, 0
	for _, err := range errs {
		switch code(err) {
		case "":
			ok++
		case apperr.MetadataConflict:
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 5 {
		t.Fatalf("ok=%d conflicts=%d", ok, conflicts)
	}
}

func TestChangeDetection(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	m := e.create(t, a.ID, CreateInput{})
	f := e.label(t, Folders, a.ID, "F")
	tg := e.label(t, Tags, a.ID, "T")

	step := func(name string, fn func() error) {
		t.Helper()
		e.ageMeeting(t, m.ID)
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !e.bumped(t, m.ID) {
			t.Errorf("%s did not bump updated_at", name)
		}
	}
	patch := func(body string) func() error {
		return func() error { _, err := e.svc.Update(ctx, a.ID, m.ID, decodePatch(t, body)); return err }
	}
	step("metadata patch", patch(`{"title":"x"}`))
	step("folder assign", patch(`{"folder_id":"`+f+`"}`))
	step("tag assign", patch(`{"tag_ids":["`+tg+`"]}`))
	step("folder delete", func() error { return e.svc.DeleteLabel(ctx, Folders, a.ID, f) })
	step("tag delete", func() error { return e.svc.DeleteLabel(ctx, Tags, a.ID, tg) })
	step("pipeline transition", func() error {
		changed, err := Transition(ctx, e.pool, m.ID, UploadCreated)
		if !changed {
			t.Error("transition not applied")
		}
		return err
	})
	e.audio(t, a.ID, m.ID, 2000)
	step("audio delete", func() error { return e.svc.DeleteAudio(ctx, a.ID, m.ID) })
	step("meeting delete", func() error { _, err := e.svc.Delete(ctx, a.ID, m.ID); return err })

	// no-op patch writes nothing
	m2 := e.create(t, a.ID, CreateInput{Title: ptr("same")})
	e.ageMeeting(t, m2.ID)
	if _, err := e.svc.Update(ctx, a.ID, m2.ID, decodePatch(t, `{"title":"same","tag_ids":[]}`)); err != nil || e.bumped(t, m2.ID) {
		t.Fatalf("no-op patch bumped: %v", err)
	}
}

func TestTransitions(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	m := e.create(t, a.ID, CreateInput{})
	state := func() string {
		var s string
		_ = e.pool.QueryRow(ctx, `SELECT pipeline_state FROM meetings WHERE id = $1`, m.ID).Scan(&s)
		return s
	}
	steps := []struct {
		ev      Event
		changed bool
		want    string
	}{
		{UploadVerified, false, StateAwaitingAudio},
		{UploadCreated, true, StateUploading},
		{UploadCreated, false, StateUploading},
		{UploadVerified, true, StateUploaded},
		{TranscriptWritten, false, StateUploaded},
		{TranscriptionStarted, true, StateTranscribing},
		{TranscriptionFailed, true, StateTranscriptionFailed},
		{TranscriptionStarted, true, StateTranscribing},
		{TranscriptWritten, true, StateGeneratingNotes},
		{NotesRequested, false, StateGeneratingNotes},
		{NotesFailed, true, StateNotesFailed},
		{NotesRequested, true, StateGeneratingNotes},
		{NotesWritten, true, StateNotesReady},
		{NotesRequested, true, StateGeneratingNotes},
	}
	for i, s := range steps {
		changed, err := Transition(ctx, e.pool, m.ID, s.ev)
		if err != nil || changed != s.changed || state() != s.want {
			t.Fatalf("step %d: changed=%v err=%v state=%s, want %v %s", i, changed, err, state(), s.changed, s.want)
		}
	}

	m2 := e.create(t, a.ID, CreateInput{})
	Transition(ctx, e.pool, m2.ID, UploadCreated)
	sid := e.openUpload(t, a.ID, m2.ID)
	if changed, _ := Transition(ctx, e.pool, m2.ID, UploadAbandoned); changed {
		t.Fatal("abandoned while a live session exists")
	}
	e.exec(t, `UPDATE upload_sessions SET status = 'expired', writer_lease_id = NULL, writer_lease_until = NULL WHERE id = $1`, sid)
	if changed, _ := Transition(ctx, e.pool, m2.ID, UploadAbandoned); !changed {
		t.Fatal("abandon not applied")
	}
}

func TestGetMeetingObject(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	m := e.create(t, a.ID, CreateInput{})

	b, _ := json.Marshal(m)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	for _, k := range []string{"audio", "upload", "transcript", "started_at", "folder_id"} {
		if v, ok := raw[k]; !ok || v != nil {
			t.Errorf("%s must be present and null: %s", k, b)
		}
	}
	docs := raw["documents"].(map[string]any)
	jb := raw["jobs"].(map[string]any)
	for _, k := range []string{"manual_notes", "ai_notes", "mom"} {
		if v, ok := docs[k]; !ok || v != nil {
			t.Errorf("documents.%s: %s", k, b)
		}
	}
	for _, k := range []string{"transcribe", "generate_notes", "generate_mom"} {
		if v, ok := jb[k]; !ok || v != nil {
			t.Errorf("jobs.%s: %s", k, b)
		}
	}
	if !strings.Contains(string(b), `"tag_ids":[]`) || !strings.Contains(string(b), `"participants":[]`) {
		t.Fatal(string(b))
	}

	sid := e.openUpload(t, a.ID, m.ID)
	got, _ := e.svc.Get(ctx, a.ID, m.ID)
	if got.Upload == nil || got.Upload.ID != sid || got.Upload.Status != "open" || got.Upload.ChunkMaxBytes != e.svc.cfg.UploadChunkMaxBytes ||
		got.Upload.AutoTranscription || got.Upload.DurationMS != 5000 {
		t.Fatalf("upload: %+v", got.Upload)
	}
	e.exec(t, `UPDATE upload_sessions SET status = 'canceled', writer_lease_id = NULL, writer_lease_until = NULL WHERE id = $1`, sid)

	assetID, _ := e.audio(t, a.ID, m.ID, 3000)
	t1 := e.transcript(t, a.ID, m.ID, 1, nil)
	manual1 := e.docRev(t, m.ID, "manual_notes", srcs{})
	ai1 := e.docRev(t, m.ID, "ai_notes", srcs{transcript: &t1, manual: &manual1})
	e.docRev(t, m.ID, "mom", srcs{transcript: &t1, manual: &manual1, ai: &ai1})
	notesJob := e.job(t, a.ID, m.ID, jobs.TypeGenerateNotes)

	got, err := e.svc.Get(ctx, a.ID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Upload != nil || got.Audio == nil || got.Audio.ID != assetID || !got.Audio.Available || got.Audio.DurationMS != 3000 ||
		got.Audio.ContentType != "audio/wav" || got.Audio.DeletedReason != nil || got.Audio.RetainUntil != nil {
		t.Fatalf("audio: %+v upload %+v", got.Audio, got.Upload)
	}
	if got.Transcript == nil || got.Transcript.CurrentRevisionID != t1 || got.Transcript.CurrentRevisionNumber != 1 {
		t.Fatalf("transcript: %+v", got.Transcript)
	}
	if got.Jobs.Transcribe == nil || got.Jobs.Transcribe.Status != "succeeded" || string(got.Jobs.Transcribe.Result) != "{}" ||
		got.Jobs.GenerateNotes == nil || got.Jobs.GenerateNotes.ID != notesJob.ID || got.Jobs.GenerateNotes.Status != "queued" ||
		got.Jobs.GenerateMoM != nil {
		t.Fatalf("jobs: %+v", got.Jobs)
	}
	for _, d := range []*DocumentSummary{got.Documents.ManualNotes, got.Documents.AINotes, got.Documents.MoM} {
		if d == nil || d.IsStale || len(d.StaleReasons) != 0 || d.ReviewStatus != "draft" || d.CurrentRevisionNumber != 1 {
			t.Fatalf("fresh docs: %+v", got.Documents)
		}
	}

	t2 := e.transcript(t, a.ID, m.ID, 2, &t1)
	manual2 := e.docRev(t, m.ID, "manual_notes", srcs{})
	e.docRev(t, m.ID, "ai_notes", srcs{transcript: &t2, manual: &manual2})
	e.exec(t, `UPDATE document_revisions SET reviewed_at = now() WHERE source_ai_notes_revision_id = $1`, ai1)
	got, _ = e.svc.Get(ctx, a.ID, m.ID)
	mom := got.Documents.MoM
	if !mom.IsStale || !slices.Equal(mom.StaleReasons, []string{"transcript", "manual_notes", "ai_notes"}) || mom.ReviewStatus != "reviewed" {
		t.Fatalf("mom: %+v", mom)
	}
	if an := got.Documents.AINotes; an.IsStale || an.CurrentRevisionNumber != 2 {
		t.Fatalf("ai notes: %+v", an)
	}
	if mn := got.Documents.ManualNotes; mn.IsStale || mn.CurrentRevisionID != manual2 {
		t.Fatalf("manual: %+v", mn)
	}
	if got.Transcript.CurrentRevisionNumber != 2 {
		t.Fatal(got.Transcript)
	}

	// manually created MoM (no sources) is never stale
	m2 := e.create(t, a.ID, CreateInput{})
	e.transcript(t, a.ID, m2.ID, 1, nil)
	e.docRev(t, m2.ID, "mom", srcs{})
	got, _ = e.svc.Get(ctx, a.ID, m2.ID)
	if got.Documents.MoM.IsStale {
		t.Fatal("source-less MoM stale")
	}

	b2 := testutil.CreateUser(t, e.pool, 0)
	if _, err := e.svc.Get(ctx, b2.ID, m.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatal("other owner read meeting")
	}
}

func TestListSearchFilterPagination(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	b := testutil.CreateUser(t, e.pool, 0)
	f := e.label(t, Folders, a.ID, "Klien A")
	tg := e.label(t, Tags, a.ID, "Budget")
	base := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	at := func(d int) *jsonx.Time { v := jsonx.NewTime(base.AddDate(0, 0, d)); return &v }

	m1 := e.create(t, a.ID, CreateInput{Title: ptr("Weekly sync"), StartedAt: at(1), FolderID: &f})
	m2 := e.create(t, a.ID, CreateInput{Title: ptr("Vendor call"), StartedAt: at(2), TagIDs: []string{tg}})
	m3 := e.create(t, a.ID, CreateInput{Title: ptr("Retro 100%"), StartedAt: at(3)})
	m4 := e.create(t, a.ID, CreateInput{Title: ptr("Planning_Q1"), StartedAt: at(4), FolderID: &f})
	m5 := e.create(t, a.ID, CreateInput{Title: ptr("")}) // no started_at: sorted by created_at (now)
	e.create(t, b.ID, CreateInput{Title: ptr("Weekly sync of B")})
	deleted := e.create(t, a.ID, CreateInput{Title: ptr("Weekly deleted")})
	if _, err := e.svc.Delete(ctx, a.ID, deleted.ID); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO search_documents (meeting_id, source, body) VALUES ($1, 'transcript', 'kita sepakat RILIS hari jumat'),
		($2, 'mom', 'Budi akan menyiapkan laporan'), ($3, 'manual_notes', 'ide: diskon 50')`, m3.ID, m1.ID, m4.ID)

	list := func(lq ListQuery) []string {
		t.Helper()
		if lq.Limit == 0 {
			lq.Limit = 50
		}
		items, _, err := e.svc.List(ctx, a.ID, lq)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.ID)
		}
		return out
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
	eq("all", list(ListQuery{}), m5.ID, m4.ID, m3.ID, m2.ID, m1.ID)
	eq("title", list(ListQuery{Q: "WEEKLY"}), m1.ID)
	eq("tag name", list(ListQuery{Q: "budg"}), m2.ID)
	eq("transcript text", list(ListQuery{Q: "rilis"}), m3.ID)
	eq("mom text", list(ListQuery{Q: "laporan"}), m1.ID)
	eq("manual notes text", list(ListQuery{Q: "diskon"}), m4.ID)
	eq("percent literal", list(ListQuery{Q: "100%"}), m3.ID)
	eq("percent alone", list(ListQuery{Q: "%"}), m3.ID)
	eq("underscore literal", list(ListQuery{Q: "g_q"}), m4.ID)
	eq("folder", list(ListQuery{FolderID: f}), m4.ID, m1.ID)
	eq("unfiled", list(ListQuery{FolderID: "none"}), m5.ID, m3.ID, m2.ID)
	eq("tag", list(ListQuery{TagID: tg}), m2.ID)
	eq("and", list(ListQuery{FolderID: f, Q: "planning"}), m4.ID)
	eq("no match", list(ListQuery{Q: "zzz"}))

	var got []string
	var cur *Cursor
	for {
		items, next, err := e.svc.List(ctx, a.ID, ListQuery{Limit: 2, After: cur})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 2 {
			t.Fatal("page too large")
		}
		for _, it := range items {
			got = append(got, it.ID)
		}
		if next == nil {
			break
		}
		// insert between pages must not shift the keyset
		e.create(t, a.ID, CreateInput{StartedAt: at(-30)})
		cur = next
	}
	if len(got) < 5 || !slices.Equal(got[:5], []string{m5.ID, m4.ID, m3.ID, m2.ID, m1.ID}) {
		t.Fatalf("paged: %v", got)
	}

	bFolder := e.label(t, Folders, b.ID, "B")
	bTag := e.label(t, Tags, b.ID, "B")
	for _, c := range []struct {
		lq   ListQuery
		want map[string]string
	}{
		{ListQuery{FolderID: bFolder}, map[string]string{"folder_id": "not_found"}},
		{ListQuery{TagID: bTag}, map[string]string{"tag_id": "not_found"}},
		{ListQuery{FolderID: "x"}, map[string]string{"folder_id": "invalid"}},
		{ListQuery{TagID: "x"}, map[string]string{"tag_id": "invalid"}},
		{ListQuery{Q: strings.Repeat("q", 201)}, map[string]string{"q": "too_long"}},
		{ListQuery{After: &Cursor{ID: "x"}}, map[string]string{"cursor": "invalid"}},
	} {
		c.lq.Limit = 50
		if _, _, err := e.svc.List(ctx, a.ID, c.lq); !mapsEqual(fields(err), c.want) {
			t.Errorf("%+v: %v", c.lq, err)
		}
	}
	items, _, _ := e.svc.List(ctx, b.ID, ListQuery{Limit: 50, Q: "weekly"})
	if len(items) != 1 || items[0].Title != "Weekly sync of B" {
		t.Fatalf("B search: %+v", items)
	}
}

func TestListItemDerivedFields(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	m := e.create(t, a.ID, CreateInput{})
	item := func() ListItem {
		t.Helper()
		items, _, err := e.svc.List(ctx, a.ID, ListQuery{Limit: 50})
		if err != nil || len(items) != 1 {
			t.Fatal(items, err)
		}
		return items[0]
	}
	it := item()
	if it.DurationMS != nil || it.AudioAvailable || it.MoMReviewStatus != nil || len(it.ActiveJobTypes) != 0 ||
		it.FailedStage != nil || it.TranscriptionErrorCategory != nil {
		t.Fatalf("%+v", it)
	}
	b, _ := json.Marshal(it)
	for _, k := range []string{`"duration_ms":null`, `"mom_review_status":null`, `"active_job_types":[]`, `"failed_stage":null`,
		`"transcription_error_category":null`, `"tag_ids":[]`, `"audio_available":false`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s in %s", k, b)
		}
	}

	e.audio(t, a.ID, m.ID, 4500)
	tj := e.job(t, a.ID, m.ID, jobs.TypeTranscribe)
	e.finishJob(t, tj.ID, jobs.StatusFailed, jobs.CatNoSpeech)
	e.exec(t, `UPDATE meetings SET pipeline_state = 'transcription_failed' WHERE id = $1`, m.ID)
	mj := e.job(t, a.ID, m.ID, jobs.TypeGenerateMoM)
	e.job(t, a.ID, m.ID, jobs.TypeGenerateNotes)
	e.docRev(t, m.ID, "mom", srcs{})
	it = item()
	if *it.DurationMS != 4500 || !it.AudioAvailable || *it.MoMReviewStatus != "draft" ||
		!slices.Equal(it.ActiveJobTypes, []string{"generate_notes", "generate_mom"}) ||
		*it.FailedStage != "transcription" || *it.TranscriptionErrorCategory != "no_speech" {
		t.Fatalf("%+v", it)
	}

	e.finishJob(t, mj.ID, jobs.StatusFailed, jobs.CatInvalidOutput)
	e.exec(t, `UPDATE meetings SET pipeline_state = 'notes_ready' WHERE id = $1`, m.ID)
	e.exec(t, `UPDATE document_revisions SET reviewed_at = now()`)
	e.exec(t, `UPDATE audio_assets SET deleted_at = now(), deleted_reason = 'user'`)
	it = item()
	if *it.FailedStage != "mom" || *it.MoMReviewStatus != "reviewed" || it.AudioAvailable || *it.DurationMS != 4500 ||
		!slices.Equal(it.ActiveJobTypes, []string{"generate_notes"}) {
		t.Fatalf("%+v", it)
	}
	e.exec(t, `UPDATE meetings SET pipeline_state = 'notes_failed' WHERE id = $1`, m.ID)
	if *item().FailedStage != "notes" {
		t.Fatal("notes_failed")
	}
}

func TestDeleteDuringActiveJob(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 3600)
	m := e.create(t, a.ID, CreateInput{})
	other := e.create(t, a.ID, CreateInput{})
	assetID, key := e.audio(t, a.ID, m.ID, 5000)
	sid := e.openUpload(t, a.ID, m.ID)
	if _, err := e.store.Put(ctx, "u/"+a.ID+"/m/"+m.ID+"/uploads/"+sid+".part", strings.NewReader("partial")); err != nil {
		t.Fatal(err)
	}
	keep := "u/" + a.ID + "/m/" + other.ID + "/exports/x.pdf"
	if _, err := e.store.Put(ctx, keep, strings.NewReader("pdf")); err != nil {
		t.Fatal(err)
	}

	us := usage.New(e.pool, e.svc.cfg, testutil.DiscardLogger())
	resID := ids.New()
	var jobID string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		q, err := us.CheckQuota(ctx, tx, a.ID, 5000)
		if err != nil {
			return err
		}
		j, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Type: jobs.TypeTranscribe, UserID: a.ID, MeetingID: m.ID, Trigger: jobs.TriggerUser,
			IdempotencyKey: ids.New(), RequestFingerprint: strings.Repeat("a", 64), MaxAttempts: 3, CorrelationID: ids.New(),
			Payload: map[string]any{"reservation_id": resID}})
		if err != nil {
			return err
		}
		jobID = j.ID
		return usage.Reserve(ctx, tx, q, usage.Reservation{ID: resID, MeetingID: m.ID, AudioAssetID: assetID, JobID: j.ID})
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := jobs.Claim(ctx, e.pool, "w/0", time.Minute)
	if err != nil || claimed == nil || claimed.ID != jobID {
		t.Fatal(claimed, err)
	}
	queuedNotes := e.job(t, a.ID, m.ID, jobs.TypeGenerateNotes)

	before := time.Now()
	purgeAt, err := e.svc.Delete(ctx, a.ID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d := purgeAt.Sub(before); d < e.svc.cfg.PurgeDelay-time.Second || d > e.svc.cfg.PurgeDelay+time.Minute {
		t.Fatalf("purge at %v (delay %v)", d, e.svc.cfg.PurgeDelay)
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE id IN ($1, $2) AND status = 'canceled' AND finished_at IS NOT NULL AND lease_owner IS NULL`,
		jobID, queuedNotes.ID) != 2 {
		t.Fatal("jobs not canceled")
	}
	if e.count(t, `SELECT count(*) FROM upload_sessions WHERE id = $1 AND status = 'canceled' AND writer_lease_id IS NULL`, sid) != 1 {
		t.Fatal("upload not canceled")
	}
	if e.count(t, `SELECT count(*) FROM quota_reservations WHERE id = $1 AND status = 'released'`, resID) != 1 {
		t.Fatal("reservation not released")
	}
	if e.count(t, `SELECT count(*) FROM deleted_meetings WHERE id = $1 AND user_id = $2`, m.ID, a.ID) != 1 {
		t.Fatal("no permanent tombstone")
	}
	pj, err := jobs.GetByKey(ctx, e.pool, "", "sys:purge_meeting:"+m.ID)
	if err != nil || pj.Status != jobs.StatusQueued || pj.MaxAttempts != jobs.PurgeMaxAttempts || pj.UserID != nil || pj.MeetingID != nil {
		t.Fatalf("purge job: %+v %v", pj, err)
	}

	// the in-flight attempt finds the tombstone and writes nothing
	err = jobs.Complete(ctx, e.pool, claimed, map[string]string{"transcript_revision_id": ids.New()}, func(tx pgx.Tx) error {
		t.Fatal("stage ran on a tombstoned meeting")
		return nil
	})
	if !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatal(err)
	}
	if ok, _ := jobs.Heartbeat(ctx, e.pool, claimed.Token(), time.Minute); ok {
		t.Fatal("heartbeat survived cancel")
	}

	if _, err := e.svc.Get(ctx, a.ID, m.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatal("tombstoned meeting readable")
	}
	if _, err := e.svc.Delete(ctx, a.ID, m.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatal("repeat delete:", err)
	}
	if _, _, err := e.svc.Create(ctx, a.ID, CreateInput{ID: &m.ID, Timezone: ptr("UTC")}); code(err) != apperr.MeetingIDConflict {
		t.Fatal("recreate tombstoned:", err)
	}

	runner := jobs.NewRunner(e.pool, testutil.DiscardLogger(), jobs.RunnerConfig{WorkerID: "test", Lease: time.Minute, RetryBase: time.Second})
	runner.Register(jobs.TypePurgeMeeting, e.svc.PurgeMeetingSpec())
	if ran, _ := runner.RunOnce(ctx); ran {
		t.Fatal("purge ran before PURGE_DELAY")
	}
	e.exec(t, `UPDATE jobs SET run_after = now() WHERE id = $1`, pj.ID)
	if ran, err := runner.RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	pj, _ = jobs.Get(ctx, e.pool, pj.ID)
	if pj.Status != jobs.StatusSucceeded {
		t.Fatalf("purge job %s %v", pj.Status, pj.LastErrorCategory)
	}
	if e.count(t, `SELECT count(*) FROM meetings WHERE id = $1`, m.ID) != 0 ||
		e.count(t, `SELECT count(*) FROM jobs WHERE meeting_id = $1`, m.ID) != 0 ||
		e.count(t, `SELECT count(*) FROM upload_sessions WHERE meeting_id = $1`, m.ID) != 0 ||
		e.count(t, `SELECT count(*) FROM audio_assets WHERE meeting_id = $1`, m.ID) != 0 {
		t.Fatal("rows survived purge")
	}
	if e.count(t, `SELECT count(*) FROM quota_reservations WHERE id = $1 AND meeting_id IS NULL AND audio_asset_id IS NULL AND job_id IS NULL`, resID) != 1 {
		t.Fatal("reservation ledger row not kept with null refs")
	}
	for _, k := range []string{key, "u/" + a.ID + "/m/" + m.ID + "/uploads/" + sid + ".part"} {
		if _, ok, _ := e.store.Stat(ctx, k); ok {
			t.Fatal("file survived purge:", k)
		}
	}
	if _, ok, _ := e.store.Stat(ctx, keep); !ok {
		t.Fatal("purge removed another meeting's file")
	}
	if e.count(t, `SELECT count(*) FROM meetings WHERE id = $1`, other.ID) != 1 {
		t.Fatal("other meeting purged")
	}
	if _, _, err := e.svc.Create(ctx, a.ID, CreateInput{ID: &m.ID, Timezone: ptr("UTC")}); code(err) != apperr.MeetingIDConflict {
		t.Fatal("recreate after purge:", err)
	}

	// purge is idempotent: a rerun on gone rows succeeds
	pj2, _ := jobs.Get(ctx, e.pool, pj.ID)
	e.exec(t, `UPDATE jobs SET status = 'queued', finished_at = NULL, result = NULL, run_after = now() WHERE id = $1`, pj2.ID)
	if ran, err := runner.RunOnce(ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if pj2, _ = jobs.Get(ctx, e.pool, pj.ID); pj2.Status != jobs.StatusSucceeded {
		t.Fatal("rerun purge", pj2.Status)
	}
}

func TestDeleteAudioKeepsNotes(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	m := e.create(t, a.ID, CreateInput{})
	if err := e.svc.DeleteAudio(ctx, a.ID, m.ID); code(err) != apperr.AudioNotReady {
		t.Fatal(err)
	}
	assetID, key := e.audio(t, a.ID, m.ID, 2000)
	tr := e.transcript(t, a.ID, m.ID, 1, nil)
	e.docRev(t, m.ID, "ai_notes", srcs{transcript: &tr})

	tj := e.job(t, a.ID, m.ID, jobs.TypeTranscribe)
	err := e.svc.DeleteAudio(ctx, a.ID, m.ID)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.JobInProgress || ae.Details["job_id"] != tj.ID {
		t.Fatal("active transcribe:", err)
	}
	e.finishJob(t, tj.ID, jobs.StatusFailed, jobs.CatTimeout)

	if err := e.svc.DeleteAudio(ctx, a.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM audio_assets WHERE id = $1 AND deleted_reason = 'user' AND deleted_at IS NOT NULL
		AND bytes_purged_at IS NOT NULL`, assetID) != 1 {
		t.Fatal("asset not marked deleted + purged")
	}
	if _, ok, _ := e.store.Stat(ctx, key); ok {
		t.Fatal("bytes kept")
	}
	got, _ := e.svc.Get(ctx, a.ID, m.ID)
	if got.Audio == nil || got.Audio.Available || *got.Audio.DeletedReason != "user" || got.Transcript == nil || got.Documents.AINotes == nil {
		t.Fatalf("%+v", got)
	}
	if err := e.svc.DeleteAudio(ctx, a.ID, m.ID); err != nil {
		t.Fatal("repeat delete must be 204:", err)
	}
	b := testutil.CreateUser(t, e.pool, 0)
	if err := e.svc.DeleteAudio(ctx, b.ID, m.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestFoldersAndTags(t *testing.T) {
	for _, k := range []LabelKind{Folders, Tags} {
		t.Run(k.table, func(t *testing.T) {
			e := newEnv(t)
			a := testutil.CreateUser(t, e.pool, 0)
			b := testutil.CreateUser(t, e.pool, 0)
			id := ids.New()
			l, created, err := e.svc.CreateLabel(ctx, k, a.ID, LabelInput{ID: &id, Name: ptr("  Klien A ")})
			if err != nil || !created || l.ID != id || l.Name != "Klien A" || l.MeetingCount != 0 {
				t.Fatalf("%+v %v", l, err)
			}
			l2, created, err := e.svc.CreateLabel(ctx, k, a.ID, LabelInput{ID: &id, Name: ptr("ignored")})
			if err != nil || created || l2.Name != "Klien A" {
				t.Fatalf("replay: %+v %v", l2, err)
			}
			if _, _, err := e.svc.CreateLabel(ctx, k, b.ID, LabelInput{ID: &id, Name: ptr("x")}); code(err) != apperr.IDConflict {
				t.Fatal(err)
			}
			if _, _, err := e.svc.CreateLabel(ctx, k, a.ID, LabelInput{Name: ptr("klien a")}); code(err) != apperr.NameTaken {
				t.Fatal("case-insensitive name:", err)
			}
			if _, _, err := e.svc.CreateLabel(ctx, k, b.ID, LabelInput{Name: ptr("Klien A")}); err != nil {
				t.Fatal("names are per user:", err)
			}
			for name, reason := range map[string]string{"   ": "too_short", strings.Repeat("n", 61): "too_long"} {
				if _, _, err := e.svc.CreateLabel(ctx, k, a.ID, LabelInput{Name: &name}); fields(err)["name"] != reason {
					t.Fatal(name, err)
				}
			}
			if _, _, err := e.svc.CreateLabel(ctx, k, a.ID, LabelInput{}); fields(err)["name"] != "required" {
				t.Fatal(err)
			}
			zID := e.label(t, k, a.ID, "alpha")
			e.label(t, k, a.ID, "Zeta")

			var m1, m2, gone *Meeting
			if k == Folders {
				m1 = e.create(t, a.ID, CreateInput{FolderID: &id})
				m2 = e.create(t, a.ID, CreateInput{FolderID: &id})
				gone = e.create(t, a.ID, CreateInput{FolderID: &id})
			} else {
				m1 = e.create(t, a.ID, CreateInput{TagIDs: []string{id}})
				m2 = e.create(t, a.ID, CreateInput{TagIDs: []string{id, zID}})
				gone = e.create(t, a.ID, CreateInput{TagIDs: []string{id}})
			}
			e.svc.Delete(ctx, a.ID, gone.ID)
			list, err := e.svc.ListLabels(ctx, k, a.ID)
			if err != nil || len(list) != 3 || list[0].Name != "alpha" || list[1].Name != "Klien A" || list[2].Name != "Zeta" ||
				list[1].MeetingCount != 2 {
				t.Fatalf("%+v %v", list, err)
			}

			if _, err := e.svc.RenameLabel(ctx, k, a.ID, id, ptr("ALPHA")); code(err) != apperr.NameTaken {
				t.Fatal(err)
			}
			r, err := e.svc.RenameLabel(ctx, k, a.ID, id, ptr("klien a"))
			if err != nil || r.Name != "klien a" || r.MeetingCount != 2 {
				t.Fatalf("case-only rename: %+v %v", r, err)
			}
			if _, err := e.svc.RenameLabel(ctx, k, b.ID, id, ptr("mine")); !errors.Is(err, apperr.ErrNotFound) {
				t.Fatal(err)
			}
			if err := e.svc.DeleteLabel(ctx, k, b.ID, id); !errors.Is(err, apperr.ErrNotFound) {
				t.Fatal(err)
			}

			if err := e.svc.DeleteLabel(ctx, k, a.ID, id); err != nil {
				t.Fatal(err)
			}
			g1, _ := e.svc.Get(ctx, a.ID, m1.ID)
			g2, _ := e.svc.Get(ctx, a.ID, m2.ID)
			if k == Folders && (g1.FolderID != nil || g2.FolderID != nil) {
				t.Fatal("meetings still filed")
			}
			if k == Tags && (len(g1.TagIDs) != 0 || !slices.Equal(g2.TagIDs, []string{zID})) {
				t.Fatal("tag not removed", g1.TagIDs, g2.TagIDs)
			}
			if err := e.svc.DeleteLabel(ctx, k, a.ID, id); !errors.Is(err, apperr.ErrNotFound) {
				t.Fatal("repeat delete", err)
			}
		})
	}
}
