package documents

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
	"phoebe/internal/testutil"
)

func TestRevisionConflictAndMutationReplay(t *testing.T) {
	e := newDEnv(t, nil, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid, _ := e.seedMeeting(t, u.ID, seedSegments)

	m1 := ids.New()
	body1 := manualNotesBody(0, m1, "Bahas budget Q1")
	v1 := e.mustPut(t, u.ID, mid, KindManualNotes, body1)
	if v1.Revision.RevisionNumber != 1 || v1.Revision.Origin != OriginUserEdit || v1.Document.CurrentRevisionNumber != 1 ||
		v1.Document.ReviewStatus != "draft" || v1.Document.IsStale {
		t.Fatalf("%+v %+v", v1.Document, v1.Revision)
	}

	v, created, err := e.put(t, u.ID, mid, KindManualNotes, body1)
	if err != nil || created || v.Revision.ID != v1.Revision.ID {
		t.Fatal("replay", err, created)
	}
	if _, _, err := e.put(t, u.ID, mid, KindManualNotes, manualNotesBody(0, m1, "Lain")); code(err) != apperr.IdempotencyKeyReused {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			_, _, errs[i] = e.put(t, u.ID, mid, KindManualNotes, manualNotesBody(1, ids.New(), "Versi "+itoaTest(i)))
		})
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, err := range errs {
		switch code(err) {
		case "":
			ok++
		case apperr.RevisionConflict:
			conflict++
			d := details(err)
			if d["current_revision_number"] != 2 || d["current_revision_id"] == nil {
				t.Fatalf("conflict details %v", d)
			}
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
	if v, created, err = e.put(t, u.ID, mid, KindManualNotes, body1); err != nil || created || v.Revision.ID != v1.Revision.ID ||
		v.Document.CurrentRevisionNumber != 2 {
		t.Fatal("replay after advance", err, created)
	}
	if n := e.count(t, `SELECT count(*) FROM document_revisions`); n != 2 {
		t.Fatalf("revisions %d", n)
	}
	var body string
	if err := e.pool.QueryRow(bg, `SELECT body FROM search_documents WHERE meeting_id = $1 AND source = 'manual_notes'`, mid).Scan(&body); err != nil ||
		!strings.HasPrefix(body, "Versi ") {
		t.Fatal(body, err)
	}

	_, _, err = e.put(t, u.ID, mid, KindAINotes, putBody(3, -1, ids.New(), json.RawMessage(`{}`)))
	if d := details(err); code(err) != apperr.RevisionConflict || d["current_revision_number"] != 0 || d["current_revision_id"] != (*string)(nil) {
		t.Fatal(err, d)
	}

	for body, want := range map[string]string{
		`{"mutation_id":"` + ids.New() + `","content":{}}`:                                         "expected_revision",
		`{"expected_revision":2,"content":{}}`:                                                     "mutation_id",
		`{"expected_revision":2,"mutation_id":"x","content":{}}`:                                   "mutation_id",
		`{"expected_revision":2,"base_revision":3,"mutation_id":"` + ids.New() + `","content":{}}`: "base_revision",
		`{"expected_revision":2,"mutation_id":"` + ids.New() + `"}`:                                "content",
	} {
		_, _, err := e.put(t, u.ID, mid, KindManualNotes, body)
		f, _ := details(err)["fields"].([]apperr.FieldError)
		if code(err) != apperr.ValidationFailed || len(f) != 1 || f[0].Field != want {
			t.Errorf("%s: %v %v", body, err, f)
		}
	}
	_, _, err = e.put(t, u.ID, mid, KindManualNotes, manualNotesBody(2, ids.New(), ""))
	if code(err) != apperr.InvalidContent || details(err)["path"] != "/items/0/text" {
		t.Fatal(err, details(err))
	}
	other := testutil.CreateUser(t, e.pool, 0)
	if _, _, err := e.put(t, other.ID, mid, KindManualNotes, manualNotesBody(2, ids.New(), "x")); code(err) != apperr.NotFound {
		t.Fatal(err)
	}
}

// momWithRefs returns a valid MoM content citing segment s2 and note n1.
func momWithRefs(decision string) json.RawMessage {
	return json.RawMessage(`{"schema_version":1,"language":"id","meeting_info":{"title":"Weekly sync","started_at":null,
	"timezone":"Asia/Jakarta","participants":["Budi"],"agenda":[]},"sections":[
	{"id":"decisions","type":"decisions","heading":"Keputusan","style":"numbered","items":[
	 {"id":"it_1","text":"` + decision + `","origin":"ai","edited":true,"sources":[{"type":"transcript","segment_id":"s2"}]}]}]}`)
}

func TestKeepMineProvenanceAndStaleness(t *testing.T) {
	e := newDEnv(t, nil, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid, tr1 := e.seedMeeting(t, u.ID, seedSegments)
	e.generateNotes(t, u.ID, mid)
	e.generateMoM(t, u.ID, mid)

	rev2 := e.mustPut(t, u.ID, mid, KindMoM, putBody(1, -1, ids.New(), momWithRefs("Rilis Jumat (diedit).")))
	if rev2.Revision.RevisionNumber != 2 || *rev2.Revision.Source.TranscriptRevisionID != tr1 || rev2.Revision.Source.AINotesRevisionID == nil {
		t.Fatalf("user edit must copy base sources: %+v", rev2.Revision.Source)
	}
	var c MoM
	_ = json.Unmarshal(rev2.Revision.Content, &c)
	if s := c.Sections[0].Items[0].Sources[0]; s.StartMS != 15000 || s.EndMS != 19000 {
		t.Fatalf("ref times not overwritten from pinned transcript: %+v", s)
	}

	tr2 := e.correct(t, mid)
	docs, _ := meetings.LoadDocumentSummaries(bg, e.pool, mid)
	if !docs.MoM.IsStale || !reflect.DeepEqual(docs.MoM.StaleReasons, []string{"transcript"}) ||
		!docs.AINotes.IsStale || !reflect.DeepEqual(docs.AINotes.StaleReasons, []string{"transcript"}) {
		t.Fatalf("after correction: mom %+v notes %+v", docs.MoM, docs.AINotes)
	}
	if n := e.count(t, `SELECT count(*) FROM transcript_revisions WHERE id = $1`, tr1); n != 1 {
		t.Fatal("original transcript gone")
	}

	e.generateNotes(t, u.ID, mid)
	e.generateMoM(t, u.ID, mid)
	cur, err := e.svc.Get(bg, u.ID, mid, KindMoM)
	if err != nil || cur.Revision.RevisionNumber != 3 || cur.Revision.Origin != OriginAIGenerated ||
		*cur.Revision.Source.TranscriptRevisionID != tr2 || cur.Document.IsStale {
		t.Fatalf("regenerated: %v %+v %+v", err, cur.Document, cur.Revision)
	}

	if _, _, err := e.put(t, u.ID, mid, KindMoM, putBody(2, -1, ids.New(), momWithRefs("x"))); code(err) != apperr.RevisionConflict {
		t.Fatal("old expected_revision", err)
	}
	keep := e.mustPut(t, u.ID, mid, KindMoM, putBody(3, 2, ids.New(), momWithRefs("Rilis Jumat (punyaku).")))
	if keep.Revision.RevisionNumber != 4 || !reflect.DeepEqual(keep.Revision.Source, rev2.Revision.Source) {
		t.Fatalf("keep mine sources %+v want %+v", keep.Revision.Source, rev2.Revision.Source)
	}
	if !keep.Document.IsStale || !reflect.DeepEqual(keep.Document.StaleReasons, []string{"transcript", "ai_notes"}) {
		t.Fatalf("keep mine staleness %+v", keep.Document)
	}

	noSrc := e.mustPut(t, u.ID, mid, KindMoM, putBody(4, 0, ids.New(), json.RawMessage(
		`{"schema_version":1,"language":"id","meeting_info":{"title":"","started_at":null,"timezone":"UTC","participants":[],"agenda":[]},"sections":[]}`)))
	if noSrc.Revision.Source.TranscriptRevisionID != nil || noSrc.Document.IsStale {
		t.Fatalf("base 0 = no sources, never stale: %+v", noSrc)
	}
	if _, _, err := e.put(t, u.ID, mid, KindMoM, putBody(5, 0, ids.New(), momWithRefs("x"))); code(err) != apperr.InvalidContent {
		t.Fatal("ref with base 0 must be invalid", err)
	}
}

func TestRestoreAfterRegenerate(t *testing.T) {
	e := newDEnv(t, nil, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid, _ := e.seedMeeting(t, u.ID, seedSegments)
	e.generateNotes(t, u.ID, mid)
	e.generateMoM(t, u.ID, mid)
	edit := e.mustPut(t, u.ID, mid, KindMoM, putBody(1, -1, ids.New(), momWithRefs("Diedit.")))
	e.generateMoM(t, u.ID, mid)

	m := ids.New()
	body := `{"revision_number":2,"expected_revision":3,"mutation_id":"` + m + `"}`
	in := decode[RestoreInput](t, body)
	v, created, err := e.svc.Restore(bg, u.ID, mid, KindMoM, []byte(body), in)
	if err != nil || !created || v.Revision.RevisionNumber != 4 || v.Revision.Origin != OriginRestore ||
		*v.Revision.RestoredFromRevisionNumber != 2 || string(v.Revision.Content) != string(edit.Revision.Content) ||
		!reflect.DeepEqual(v.Revision.Source, edit.Revision.Source) {
		t.Fatalf("restore: %v %+v", err, v)
	}
	if v2, created, err := e.svc.Restore(bg, u.ID, mid, KindMoM, []byte(body), in); err != nil || created || v2.Revision.ID != v.Revision.ID {
		t.Fatal("restore replay", err)
	}
	reused := `{"revision_number":1,"expected_revision":3,"mutation_id":"` + m + `"}`
	if _, _, err := e.svc.Restore(bg, u.ID, mid, KindMoM, []byte(reused), decode[RestoreInput](t, reused)); code(err) != apperr.IdempotencyKeyReused {
		t.Fatal(err)
	}
	b := `{"revision_number":9,"expected_revision":4,"mutation_id":"` + ids.New() + `"}`
	if _, _, err := e.svc.Restore(bg, u.ID, mid, KindMoM, []byte(b), decode[RestoreInput](t, b)); code(err) != apperr.NotFound {
		t.Fatal(err)
	}
	b = `{"revision_number":1,"expected_revision":3,"mutation_id":"` + ids.New() + `"}`
	if _, _, err := e.svc.Restore(bg, u.ID, mid, KindMoM, []byte(b), decode[RestoreInput](t, b)); code(err) != apperr.RevisionConflict {
		t.Fatal(err)
	}
	revs, next, err := e.svc.ListRevisions(bg, u.ID, mid, KindMoM, 3, 0)
	if err != nil || len(revs) != 3 || revs[0].RevisionNumber != 4 || revs[0].Content != nil || next == nil || *next != 2 {
		t.Fatal(err, len(revs), next)
	}
	revs, next, _ = e.svc.ListRevisions(bg, u.ID, mid, KindMoM, 3, *next)
	if len(revs) != 1 || revs[0].RevisionNumber != 1 || next != nil {
		t.Fatal(len(revs), next)
	}
	r, err := e.svc.GetRevision(bg, u.ID, mid, KindMoM, 2)
	if err != nil || r.ID != edit.Revision.ID || len(r.Content) == 0 {
		t.Fatal(err)
	}
	if _, err := e.svc.GetRevision(bg, u.ID, mid, KindMoM, 99); code(err) != apperr.NotFound {
		t.Fatal(err)
	}
}

func TestReviewAndChangeDetection(t *testing.T) {
	e := newDEnv(t, nil, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid, _ := e.seedMeeting(t, u.ID, seedSegments)
	review := func(status string, expected int) error {
		body := `{"status":"` + status + `","expected_revision":` + itoaTest(expected) + `}`
		_, err := e.svc.Review(bg, u.ID, mid, KindMoM, decode[ReviewInput](t, body))
		return err
	}
	if err := review("reviewed", 1); code(err) != apperr.NotFound {
		t.Fatal(err)
	}
	t0 := e.updatedAt(t, mid)
	e.mustPut(t, u.ID, mid, KindMoM, putBody(0, -1, ids.New(), json.RawMessage(
		`{"schema_version":1,"language":"id","meeting_info":{"title":"","started_at":null,"timezone":"UTC","participants":[],"agenda":[]},"sections":[]}`)))
	t1 := e.updatedAt(t, mid)
	if !t1.After(t0) {
		t.Fatal("revision insert must bump meetings.updated_at")
	}
	var docUpdated1 string
	_ = e.pool.QueryRow(bg, `SELECT updated_at::text FROM documents WHERE meeting_id = $1 AND kind = 'mom'`, mid).Scan(&docUpdated1)

	sum, err := e.svc.Review(bg, u.ID, mid, KindMoM, decode[ReviewInput](t, `{"status":"reviewed","expected_revision":1}`))
	if err != nil || sum.ReviewStatus != "reviewed" {
		t.Fatal(err, sum)
	}
	t2 := e.updatedAt(t, mid)
	var docUpdated2 string
	_ = e.pool.QueryRow(bg, `SELECT updated_at::text FROM documents WHERE meeting_id = $1 AND kind = 'mom'`, mid).Scan(&docUpdated2)
	if !t2.After(t1) || docUpdated2 == docUpdated1 {
		t.Fatal("review set must bump meeting and document updated_at")
	}
	if err := review("reviewed", 1); err != nil || !e.updatedAt(t, mid).Equal(t2) {
		t.Fatal("repeat review is a no-op", err)
	}
	if err := review("reviewed", 2); code(err) != apperr.RevisionConflict {
		t.Fatal(err)
	}
	if err := review("done", 1); code(err) != apperr.ValidationFailed {
		t.Fatal(err)
	}

	v := e.mustPut(t, u.ID, mid, KindMoM, putBody(1, -1, ids.New(), json.RawMessage(
		`{"schema_version":1,"language":"id","meeting_info":{"title":"x","started_at":null,"timezone":"UTC","participants":[],"agenda":[]},"sections":[]}`)))
	if v.Document.ReviewStatus != "draft" || v.Revision.ReviewedAt != nil {
		t.Fatal("edit after reviewed must be draft")
	}
	r1, _ := e.svc.GetRevision(bg, u.ID, mid, KindMoM, 1)
	if r1.ReviewedAt == nil {
		t.Fatal("old revision keeps its review")
	}
	if err := review("reviewed", 2); err != nil {
		t.Fatal(err)
	}
	t3 := e.updatedAt(t, mid)
	sum, err = e.svc.Review(bg, u.ID, mid, KindMoM, decode[ReviewInput](t, `{"status":"draft","expected_revision":2}`))
	if err != nil || sum.ReviewStatus != "draft" || !e.updatedAt(t, mid).After(t3) {
		t.Fatal("review clear", err, sum)
	}
}

func TestGenerateEndpointRules(t *testing.T) {
	e := newDEnv(t, nil, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid, tr := e.seedMeeting(t, u.ID, seedSegments)
	empty := ids.New()
	if _, err := e.pool.Exec(bg, `INSERT INTO meetings (id, user_id, timezone) VALUES ($1, $2, 'UTC')`, empty, u.ID); err != nil {
		t.Fatal(err)
	}

	k0 := ids.New()
	if _, _, err := e.generate(t, u.ID, empty, KindAINotes, k0, `{}`); code(err) != apperr.TranscriptNotReady {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE idempotency_key = $1`, k0) != 0 {
		t.Fatal("rejected request stored its key")
	}
	if _, _, err := e.generate(t, u.ID, mid, KindAINotes, ids.New(), `{"language":"fr"}`); code(err) != apperr.ValidationFailed {
		t.Fatal(err)
	}
	if _, _, err := e.generate(t, u.ID, mid, KindMoM, ids.New(), `{}`); code(err) != apperr.AINotesNotReady {
		t.Fatal(err)
	}

	manual := e.mustPut(t, u.ID, mid, KindManualNotes, manualNotesBody(0, ids.New(), "Catatan"))
	k := ids.New()
	j, created, err := e.generate(t, u.ID, mid, KindAINotes, k, `{"language":null}`)
	if err != nil || !created || j.Status != jobs.StatusQueued || e.state(t, mid) != meetings.StateGeneratingNotes {
		t.Fatal(err, created, e.state(t, mid))
	}
	job := e.job(t, j.ID)
	var p Payload
	_ = job.DecodePayload(&p)
	if p.TranscriptRevisionID != tr || p.ManualNotesRevisionID == nil || *p.ManualNotesRevisionID != manual.Revision.ID ||
		p.Language != "id" || p.AINotesRevisionID != "" || job.Trigger != jobs.TriggerUser || job.MaxAttempts != 3 {
		t.Fatalf("payload %+v job %+v", p, job)
	}
	if !strings.Contains(string(job.Payload), `"manual_notes_revision_id"`) || strings.Contains(string(job.Payload), "ai_notes") {
		t.Fatal(string(job.Payload))
	}
	j2, created, err := e.generate(t, u.ID, mid, KindAINotes, k, `{"language": null}`)
	if err != nil || created || j2.ID != j.ID {
		t.Fatal("replay", err, created)
	}
	if _, _, err := e.generate(t, u.ID, mid, KindAINotes, k, `{"language":"en"}`); code(err) != apperr.IdempotencyKeyReused {
		t.Fatal(err)
	}
	if _, _, err := e.generate(t, u.ID, mid, KindMoM, k, `{"language": null}`); code(err) != apperr.IdempotencyKeyReused {
		t.Fatal("same key other route", err)
	}
	_, _, err = e.generate(t, u.ID, mid, KindAINotes, ids.New(), `{}`)
	if code(err) != apperr.JobInProgress || details(err)["job_id"] != j.ID {
		t.Fatal(err)
	}

	e.runAll(t)
	other := testutil.CreateUser(t, e.pool, 0)
	omid, _ := e.seedMeeting(t, other.ID, seedSegments)
	var foreign string
	if err := e.pool.QueryRow(bg, `SELECT r.id FROM document_revisions r JOIN documents d ON d.id = r.document_id
		WHERE d.meeting_id = $1 AND d.kind = 'ai_notes'`, mid).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.generate(t, other.ID, omid, KindMoM, ids.New(), `{"ai_notes_revision_id":"`+foreign+`"}`); code(err) != apperr.NotFound {
		t.Fatal("foreign ai notes revision", err)
	}
	if _, _, err := e.generate(t, other.ID, mid, KindAINotes, ids.New(), `{}`); code(err) != apperr.NotFound {
		t.Fatal("foreign meeting", err)
	}
	if _, _, err := e.generate(t, u.ID, mid, KindMoM, ids.New(), `{"ai_notes_revision_id":"nope"}`); code(err) != apperr.ValidationFailed {
		t.Fatal(err)
	}
	jm, created, err := e.generate(t, u.ID, mid, KindMoM, ids.New(), `{"ai_notes_revision_id":"`+strings.ToUpper(foreign)+`","language":"en"}`)
	if err != nil || !created {
		t.Fatal(err)
	}
	_ = e.job(t, jm.ID).DecodePayload(&p)
	if p.AINotesRevisionID != foreign || p.Language != "en" || e.state(t, mid) != meetings.StateNotesReady {
		t.Fatalf("mom payload %+v state %s", p, e.state(t, mid))
	}
}

func TestAIRateLimitRace(t *testing.T) {
	e := newDEnv(t, nil, func(c *config.Config) { c.AIUserJobsPerDay = 3 })
	u := testutil.CreateUser(t, e.pool, 0)
	for range 2 {
		m, _ := e.seedMeeting(t, u.ID, seedSegments)
		if _, err := e.pool.Exec(bg, `INSERT INTO jobs (type, user_id, meeting_id, trigger, status, idempotency_key,
			request_fingerprint, max_attempts, correlation_id, finished_at, last_error_category)
			VALUES ('generate_notes', $1, $2, 'user', 'failed', $3, repeat('c', 64), 3, gen_random_uuid(), now(), 'internal')`,
			u.ID, m, ids.New()); err != nil {
			t.Fatal(err)
		}
	}
	const n = 8
	var mids []string
	for range n {
		m, _ := e.seedMeeting(t, u.ID, seedSegments)
		mids = append(mids, m)
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() { _, _, errs[i] = e.generate(t, u.ID, mids[i], KindAINotes, ids.New(), `{}`) })
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch code(err) {
		case "":
			ok++
		case apperr.RateLimited:
			if s, _ := details(err)["retry_after_seconds"].(int64); s < 1 {
				t.Fatal(details(err))
			}
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d generates succeeded at limit-1", ok)
	}
}
