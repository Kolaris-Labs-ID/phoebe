package transcripts

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"phoebe/internal/ai"
	"phoebe/internal/ai/fake"
	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/meetings"
	"phoebe/internal/testutil"
	"phoebe/internal/usage"

	"github.com/jackc/pgx/v5"
)

func strp(s string) *string { return &s }

func TestNormalize(t *testing.T) {
	seg := func(start, end int64, text string) ai.Segment {
		return ai.Segment{StartMS: start, EndMS: end, Text: text}
	}
	low := func(start, end int64, text string) ai.Segment {
		return ai.Segment{StartMS: start, EndMS: end, Text: text, Uncertain: true, UncertainReason: ai.UncertainLowConfidence}
	}
	raw := []ai.Segment{
		seg(5000, 6001, "  dua  "),
		seg(0, 1234, "satu"),
		seg(7000, 8000, "   "),
		low(8000, 9000, "ragu"),
		seg(9500, 10700, "akhir"),
	}
	got, err := normalize(raw, 10000)
	if err != nil {
		t.Fatal(err)
	}
	reason := ai.UncertainLowConfidence
	want := []Segment{{"s1", 0, 1234, "satu", false, nil}, {"s2", 5000, 6001, "dua", false, nil},
		{"s3", 8000, 9000, "ragu", true, &reason}, {"s4", 9500, 10000, "akhir", false, nil}}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ID != w.ID || g.StartMS != w.StartMS || g.EndMS != w.EndMS || g.Text != w.Text || g.Uncertain != w.Uncertain ||
			(g.UncertainReason == nil) != (w.UncertainReason == nil) || (g.UncertainReason != nil && *g.UncertainReason != *w.UncertainReason) {
			t.Fatalf("%d: %+v want %+v", i, g, w)
		}
	}

	category := func(err error) (string, bool) {
		var je *jobs.Error
		if !errors.As(err, &je) {
			t.Fatalf("not a job error: %v", err)
		}
		return je.Category, je.Permanent
	}
	if _, err := normalize([]ai.Segment{seg(0, 1000, ""), seg(1000, 2000, " \n ")}, 10000); err == nil {
		t.Fatal("expected no_speech")
	} else if c, p := category(err); c != jobs.CatNoSpeech || !p {
		t.Fatal(c, p)
	}
	if _, err := normalize([]ai.Segment{seg(11001, 12000, "lewat")}, 10000); err == nil {
		t.Fatal("expected overrun")
	} else if c, p := category(err); c != jobs.CatAudioUnreadable || !p {
		t.Fatal(c, p)
	}
	if got, err := normalize([]ai.Segment{seg(10900, 11500, "pas")}, 10000); err != nil || got[0].StartMS != 10000 || got[0].EndMS != 10000 {
		t.Fatal(got, err)
	}
	for _, bad := range []ai.Segment{seg(3000, 2000, "mundur"), seg(-1000, 2000, "negatif")} {
		if _, err := normalize([]ai.Segment{bad}, 10000); err == nil {
			t.Fatal("expected invalid_output")
		} else if c, p := category(err); c != jobs.CatInvalidOutput || p {
			t.Fatal(c, p)
		}
	}
}

func TestTranscribeSuccess(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, assetID := e.meeting(t, 3600, 60_000)
	manualID := ids.New()
	docID := ids.New()
	e.exec(t, `INSERT INTO documents (id, meeting_id, kind, current_revision_number) VALUES ($1, $2, 'manual_notes', 1)`, docID, mid)
	e.exec(t, `INSERT INTO document_revisions (id, document_id, revision_number, origin, schema_version, content, mutation_id, mutation_fingerprint)
		VALUES ($1, $2, 1, 'user_edit', 1, '{}', $3, $4)`, manualID, docID, ids.New(), strings.Repeat("a", 64))

	before := e.updatedAt(t, mid)
	jobID := e.start(t, uid, mid)
	if e.state(t, mid) != meetings.StateTranscribing || !e.updatedAt(t, mid).After(before) {
		t.Fatal("start did not move state/updated_at")
	}
	if n := e.count(t, `SELECT count(*) FROM quota_reservations WHERE job_id = $1 AND status = 'reserved' AND seconds = 60`, jobID); n != 1 {
		t.Fatal("reservation", n)
	}

	e.run(t)
	j := e.job(t, jobID)
	if j.Status != jobs.StatusSucceeded || j.Attempts != 1 {
		t.Fatalf("%+v", j)
	}
	var res transcribeResult
	if err := json.Unmarshal(j.Result, &res); err != nil {
		t.Fatal(err)
	}
	rev, err := e.svc.Current(ctx, uid, mid)
	if err != nil {
		t.Fatal(err)
	}
	if rev.ID != res.TranscriptRevisionID || rev.RevisionNumber != 1 || rev.Kind != KindOriginal || rev.Language != "id" || len(rev.Segments) != 4 {
		t.Fatalf("%+v", rev)
	}
	for i, s := range rev.Segments {
		if s.ID != "s"+string(rune('1'+i)) || s.StartMS != int64(i)*15000 || s.EndMS != int64(i+1)*15000 || s.Text == "" {
			t.Fatalf("segment %d: %+v", i, s)
		}
	}
	if e.count(t, `SELECT count(*) FROM audio_assets WHERE id = $1
		AND retain_until BETWEEN now() + make_interval(days => $2) - interval '1 minute' AND now() + make_interval(days => $2)`,
		assetID, e.cfg.AudioRetentionDays) != 1 || e.cfg.AudioRetentionDays != 30 {
		t.Fatal("retain_until must be AUDIO_RETENTION_DAYS after transcription success")
	}
	if !strings.Contains(e.str(t, `SELECT body FROM search_documents WHERE meeting_id = $1 AND source = 'transcript'`, mid), "Kita sepakat rilis hari Jumat.") {
		t.Fatal("search body")
	}
	if e.count(t, `SELECT count(*) FROM quota_reservations WHERE job_id = $1 AND status = 'consumed'`, jobID) != 1 {
		t.Fatal("not consumed")
	}
	notes, err := jobs.GetByKey(ctx, e.pool, uid, "sys:notes:"+rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	var np NotesPayload
	if err := notes.DecodePayload(&np); err != nil {
		t.Fatal(err)
	}
	if notes.Type != jobs.TypeGenerateNotes || notes.Trigger != jobs.TriggerSystem || notes.Status != jobs.StatusQueued ||
		notes.CorrelationID != j.CorrelationID || np.TranscriptRevisionID != rev.ID || np.ManualNotesRevisionID == nil ||
		*np.ManualNotesRevisionID != manualID || np.Language != "id" {
		t.Fatalf("%+v %+v", notes, np)
	}
	if e.state(t, mid) != meetings.StateGeneratingNotes {
		t.Fatal(e.state(t, mid))
	}
	if e.count(t, `SELECT count(*) FROM events WHERE user_id = $1 AND event_id = $2 AND name = 'transcription_completed' AND meeting_id = $3`, uid, rev.ID, mid) != 1 {
		t.Fatal("event")
	}
	if e.count(t, `SELECT count(*) FROM ai_calls WHERE job_id = $1 AND capability = 'stt' AND outcome = 'ok' AND audio_ms = 60000 AND attempt = 1`, jobID) != 1 {
		t.Fatal("ai_calls")
	}
}

func TestStartIdempotency(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, _ := e.meeting(t, 3600, 60_000)
	key := ids.New()
	body := []byte(`{"language":"id"}`)
	j1, created, err := e.svc.Start(ctx, uid, mid, key, StartInput{Language: strp("id")}, body)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	j2, created, err := e.svc.Start(ctx, uid, mid, key, StartInput{Language: strp("id")}, []byte(` { "language" : "id" } `))
	if err != nil || created || j2.ID != j1.ID {
		t.Fatal("replay", err, created)
	}
	if _, _, err := e.svc.Start(ctx, uid, mid, key, StartInput{Language: strp("en")}, []byte(`{"language":"en"}`)); code(err) != apperr.IdempotencyKeyReused {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{}, nil); code(err) != apperr.JobInProgress || details(err)["job_id"] != j1.ID {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{Language: strp("fr")}, []byte(`{"language":"fr"}`)); code(err) != apperr.ValidationFailed {
		t.Fatal(err)
	}
	nuid, nmid, _ := e.meeting(t, 3600, 60_000)
	nkey := ids.New()
	jn, created, err := e.svc.Start(ctx, nuid, nmid, nkey, StartInput{}, []byte(`null`))
	if err != nil || !created {
		t.Fatal("null body behaves like {}", err, created)
	}
	for _, b := range [][]byte{nil, []byte(`{}`), []byte(` null `)} {
		if j, created, err := e.svc.Start(ctx, nuid, nmid, nkey, StartInput{}, b); err != nil || created || j.ID != jn.ID {
			t.Fatalf("%q must replay the null-body start: %v %v", b, err, created)
		}
	}
	var p payload
	if err := e.job(t, j1.ID).DecodePayload(&p); err != nil || p.Language != "id" || p.DurationMS != 60_000 {
		t.Fatal(p, err)
	}

	uid2, mid2, _ := e.meeting(t, 3600, 60_000)
	key2 := ids.New()
	var wg sync.WaitGroup
	results := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Go(func() {
			j, _, err := e.svc.Start(ctx, uid2, mid2, key2, StartInput{}, nil)
			errs[i] = err
			if j != nil {
				results[i] = j.ID
			}
		})
	}
	wg.Wait()
	for i := range 8 {
		if errs[i] != nil || results[i] != results[0] {
			t.Fatal(i, errs[i], results)
		}
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE meeting_id = $1`, mid2) != 1 ||
		e.count(t, `SELECT count(*) FROM quota_reservations WHERE meeting_id = $1`, mid2) != 1 {
		t.Fatal("duplicates")
	}

	// Concurrent different keys: exactly one job; the others see it in progress.
	uid3, mid3, _ := e.meeting(t, 3600, 60_000)
	var okCount int
	var mu sync.Mutex
	for range 6 {
		wg.Go(func() {
			_, _, err := e.svc.Start(ctx, uid3, mid3, ids.New(), StartInput{}, nil)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				okCount++
			} else if code(err) != apperr.JobInProgress {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if okCount != 1 || e.count(t, `SELECT count(*) FROM quota_reservations WHERE meeting_id = $1`, mid3) != 1 {
		t.Fatal(okCount)
	}
}

func TestStartStageChecks(t *testing.T) {
	e := newEnv(t, nil, nil)
	u := testutil.CreateUser(t, e.pool, 3600)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	if _, _, err := e.svc.Start(ctx, u.ID, mid, ids.New(), StartInput{}, nil); code(err) != apperr.AudioNotReady {
		t.Fatal(err)
	}
	other := testutil.CreateUser(t, e.pool, 3600)
	if _, _, err := e.svc.Start(ctx, other.ID, mid, ids.New(), StartInput{}, nil); code(err) != apperr.NotFound {
		t.Fatal(err)
	}
	e.audio(t, u.ID, mid, 5000)
	e.exec(t, `UPDATE audio_assets SET deleted_at = now(), deleted_reason = 'user' WHERE meeting_id = $1`, mid)
	if _, _, err := e.svc.Start(ctx, u.ID, mid, ids.New(), StartInput{}, nil); code(err) != apperr.AudioUnavailable {
		t.Fatal(err)
	}

	uid, mid2, _ := e.meeting(t, 3600, 5000)
	e.start(t, uid, mid2)
	e.run(t)
	if _, _, err := e.svc.Start(ctx, uid, mid2, ids.New(), StartInput{}, nil); code(err) != apperr.AlreadyTranscribed {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE meeting_id = $1 AND type = 'transcribe'`, mid2) != 1 {
		t.Fatal("extra job")
	}
}

func TestDeleteAudioThenStart(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, _ := e.meeting(t, 3600, 5000)
	ms := meetings.New(e.pool, e.cfg, e.store, testutil.DiscardLogger())
	if err := ms.DeleteAudio(ctx, uid, mid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{}, nil); code(err) != apperr.AudioUnavailable {
		t.Fatal(err)
	}
}

func TestStartQuota(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, _ := e.meeting(t, 10, 60_000)
	_, _, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{}, nil)
	if code(err) != apperr.QuotaExceeded || details(err)["required_seconds"] != int64(60) || details(err)["remaining_seconds"] != int64(10) {
		t.Fatal(err, details(err))
	}
	e.exec(t, `UPDATE entitlements SET quota_seconds = 3600, period_end = now() - interval '1 second', period_start = now() - interval '2 days' WHERE user_id = $1`, uid)
	if _, _, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{}, nil); code(err) != apperr.EntitlementInactive {
		t.Fatal(err)
	}
	e.exec(t, `UPDATE entitlements SET period_end = now() + interval '1 day', status = 'inactive' WHERE user_id = $1`, uid)
	if _, _, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{}, nil); code(err) != apperr.EntitlementInactive {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE meeting_id = $1`, mid) != 0 || e.count(t, `SELECT count(*) FROM quota_reservations`) != 0 {
		t.Fatal("402 wrote rows")
	}
	if e.state(t, mid) != meetings.StateUploaded {
		t.Fatal(e.state(t, mid))
	}
}

func TestStartInTx(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, _ := e.meeting(t, 0, 60_000)
	key := ids.New()
	err := db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, mid, uid); err != nil {
			return err
		}
		if _, _, err := e.svc.StartInTx(ctx, tx, uid, mid, key, "auto", NotesAuto); code(err) != apperr.QuotaExceeded {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `UPDATE meetings SET title = 'tx still usable' WHERE id = $1`, mid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM jobs`) != 0 || e.str(t, `SELECT title FROM meetings WHERE id = $1`, mid) != "tx still usable" {
		t.Fatal("savepoint")
	}

	e.exec(t, `UPDATE entitlements SET quota_seconds = 3600 WHERE user_id = $1`, uid)
	var auto *jobs.Job
	err = db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, mid, uid); err != nil {
			return err
		}
		j, created, err := e.svc.StartInTx(ctx, tx, uid, mid, key, "auto", NotesAuto)
		if err != nil || !created {
			t.Fatal(err, created)
		}
		auto = j
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	j, created, err := e.svc.Start(ctx, uid, mid, key, StartInput{Language: strp("auto")}, []byte(`{"language": "auto"}`))
	if err != nil || created || j.ID != auto.ID {
		t.Fatal("explicit confirm", err, created)
	}
	j, created, err = e.svc.Start(ctx, uid, mid, key, StartInput{Language: strp("auto"), Notes: strp(NotesAuto)}, []byte(`{"notes":"auto","language": "auto"}`))
	if err != nil || created || j.ID != auto.ID {
		t.Fatal("explicit confirm with notes", err, created)
	}
	if _, _, err := e.svc.Start(ctx, uid, mid, key, StartInput{Language: strp("auto"), Notes: strp(NotesSkip)}, []byte(`{"notes":"skip","language": "auto"}`)); code(err) != apperr.IdempotencyKeyReused {
		t.Fatal("skip with the auto key", err)
	}
	if _, _, err := e.svc.Start(ctx, uid, mid, key, StartInput{}, nil); code(err) != apperr.IdempotencyKeyReused {
		t.Fatal(err)
	}
	if e.state(t, mid) != meetings.StateTranscribing {
		t.Fatal(e.state(t, mid))
	}
}

func assertFailed(t *testing.T, e *env, jobID, mid, category string, attempts int) {
	t.Helper()
	j := e.job(t, jobID)
	if j.Status != jobs.StatusFailed || j.Attempts != attempts || j.LastErrorCategory == nil || *j.LastErrorCategory != category {
		t.Fatalf("%+v %v", j, *j.LastErrorCategory)
	}
	if e.count(t, `SELECT count(*) FROM quota_reservations WHERE job_id = $1 AND status = 'released'`, jobID) != 1 {
		t.Fatal("reservation not released")
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE meeting_id = $1 AND type = 'generate_notes'`, mid) != 0 ||
		e.count(t, `SELECT count(*) FROM transcript_revisions WHERE meeting_id = $1`, mid) != 0 {
		t.Fatal("wrote outputs")
	}
	if e.state(t, mid) != meetings.StateTranscriptionFailed {
		t.Fatal(e.state(t, mid))
	}
}

func TestNoSpeech(t *testing.T) {
	cases := map[string][]ai.Segment{
		"zero segments": nil,
		"empty text":    {{StartMS: 0, EndMS: 4000, Text: "   "}},
	}
	for name, segs := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, &fake.STT{Results: []ai.STTResult{{Language: "id", Segments: segs, AudioMS: 5000}}}, nil)
			uid, mid, _ := e.meeting(t, 3600, 5000)
			jobID := e.start(t, uid, mid)
			e.run(t)
			assertFailed(t, e, jobID, mid, jobs.CatNoSpeech, 1)
			var remaining int64
			if err := e.pool.QueryRow(ctx, `SELECT quota_seconds - COALESCE((SELECT sum(seconds) FROM quota_reservations
				WHERE user_id = $1 AND status IN ('reserved', 'consumed')), 0) FROM entitlements WHERE user_id = $1`, uid).Scan(&remaining); err != nil || remaining != 3600 {
				t.Fatal(remaining, err)
			}
		})
	}
}

func TestProviderOverrun(t *testing.T) {
	e := newEnv(t, &fake.STT{Results: []ai.STTResult{{Segments: []ai.Segment{
		{StartMS: 0, EndMS: 3000, Text: "halo"}, {StartMS: 6500, EndMS: 9000, Text: "lewat akhir"},
	}}}}, nil)
	uid, mid, _ := e.meeting(t, 3600, 5000)
	jobID := e.start(t, uid, mid)
	e.run(t)
	assertFailed(t, e, jobID, mid, jobs.CatAudioUnreadable, 1)
	if e.stt.Calls() != 1 {
		t.Fatal(e.stt.Calls())
	}
}

// The adapter times out each provider request itself; only the whole-attempt deadline is the runner's timeout.
func TestProviderTimeout(t *testing.T) {
	e := newEnv(t, &fake.STT{Block: true}, func(c *config.Config) { c.JobTimeoutTranscribeMax = 50 * time.Millisecond })
	uid, mid, _ := e.meeting(t, 3600, 5000)
	jobID := e.start(t, uid, mid)
	if d := e.svc.deadline(e.job(t, jobID)); d != 50*time.Millisecond {
		t.Fatal("deadline", d)
	}
	for i := 1; i <= 2; i++ {
		e.run(t)
		j := e.job(t, jobID)
		if j.Status != jobs.StatusQueued || j.Attempts != i || *j.LastErrorCategory != jobs.CatTimeout || !j.RunAfter.After(j.UpdatedAt.Add(-time.Second)) {
			t.Fatalf("attempt %d: %+v", i, j)
		}
		if e.state(t, mid) != meetings.StateTranscribing ||
			e.count(t, `SELECT count(*) FROM quota_reservations WHERE job_id = $1 AND status = 'reserved'`, jobID) != 1 ||
			e.count(t, `SELECT count(*) FROM ai_generation_slots WHERE transcribe_job_id = $1 AND status = 'reserved'`, jobID) != 1 {
			t.Fatal("retry must keep reservation, slot and state")
		}
	}
	e.run(t)
	assertFailed(t, e, jobID, mid, jobs.CatTimeout, 3)
	if e.count(t, `SELECT count(*) FROM ai_calls WHERE job_id = $1 AND outcome = 'error' AND error_category = 'provider_timeout'`, jobID) != 3 {
		t.Fatal("ai_calls")
	}
}

func TestInvalidOutputRetried(t *testing.T) {
	e := newEnv(t, &fake.STT{Results: []ai.STTResult{{Segments: []ai.Segment{{StartMS: 3000, EndMS: 1000, Text: "mundur"}}}}}, nil)
	uid, mid, _ := e.meeting(t, 3600, 5000)
	jobID := e.start(t, uid, mid)
	e.run(t)
	if j := e.job(t, jobID); j.Status != jobs.StatusQueued || *j.LastErrorCategory != jobs.CatInvalidOutput {
		t.Fatalf("%+v", j)
	}
	e.run(t)
	if j := e.job(t, jobID); j.Status != jobs.StatusSucceeded || j.Attempts != 2 {
		t.Fatalf("%+v", j)
	}
	if e.count(t, `SELECT count(*) FROM quota_reservations WHERE job_id = $1 AND status = 'consumed'`, jobID) != 1 {
		t.Fatal("consume")
	}
}

func TestLeaseExpiryRerun(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, _ := e.meeting(t, 3600, 30_000)
	jobID := e.start(t, uid, mid)
	stale, err := jobs.Claim(ctx, e.pool, "crashed/0", time.Minute)
	if err != nil || stale == nil || stale.ID != jobID {
		t.Fatal(stale, err)
	}
	e.exec(t, `UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id = $1`, jobID)
	e.run(t)
	if j := e.job(t, jobID); j.Status != jobs.StatusSucceeded || j.Attempts != 2 {
		t.Fatalf("%+v", j)
	}
	if err := e.svc.Transcribe(ctx, stale); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatal(err)
	}
	for sql, want := range map[string]int{
		`SELECT count(*) FROM transcript_revisions WHERE meeting_id = $1`:                        1,
		`SELECT count(*) FROM quota_reservations WHERE meeting_id = $1 AND status = 'consumed'`:  1,
		`SELECT count(*) FROM quota_reservations WHERE meeting_id = $1`:                          1,
		`SELECT count(*) FROM events WHERE meeting_id = $1 AND name = 'transcription_completed'`: 1,
		`SELECT count(*) FROM jobs WHERE meeting_id = $1 AND type = 'generate_notes'`:            1,
		`SELECT count(*) FROM ai_calls WHERE meeting_id = $1`:                                    2,
	} {
		if n := e.count(t, sql, mid); n != want {
			t.Errorf("%s = %d, want %d", sql, n, want)
		}
	}
}

func TestDeleteDuringJob(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, _ := e.meeting(t, 3600, 30_000)
	jobID := e.start(t, uid, mid)
	claimed, err := jobs.Claim(ctx, e.pool, "w/0", time.Minute)
	if err != nil || claimed.ID != jobID {
		t.Fatal(claimed, err)
	}
	ms := meetings.New(e.pool, e.cfg, e.store, testutil.DiscardLogger())
	if _, err := ms.Delete(ctx, uid, mid); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Transcribe(ctx, claimed); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM transcript_revisions WHERE meeting_id = $1`, mid) != 0 ||
		e.count(t, `SELECT count(*) FROM quota_reservations WHERE job_id = $1 AND status = 'released'`, jobID) != 1 ||
		e.str(t, `SELECT status FROM jobs WHERE id = $1`, jobID) != jobs.StatusCanceled {
		t.Fatal("finalize wrote after delete")
	}
}

func insertDoc(t *testing.T, e *env, mid, kind, transcriptRevID string, aiNotesRevID *string) string {
	t.Helper()
	docID, revID := ids.New(), ids.New()
	e.exec(t, `INSERT INTO documents (id, meeting_id, kind, current_revision_number) VALUES ($1, $2, $3, 1)`, docID, mid, kind)
	e.exec(t, `INSERT INTO document_revisions (id, document_id, revision_number, origin, schema_version, content,
		source_transcript_revision_id, source_ai_notes_revision_id, mutation_id, mutation_fingerprint)
		VALUES ($1, $2, 1, 'user_edit', 1, '{}', $3, $4, $5, $6)`, revID, docID, transcriptRevID, aiNotesRevID, ids.New(), strings.Repeat("b", 64))
	return revID
}

func correction(expected int, mutationID string, edits ...Edit) (CorrectionInput, []byte) {
	in := CorrectionInput{ExpectedRevision: &expected, MutationID: &mutationID, Edits: edits}
	b, _ := json.Marshal(in)
	return in, b
}

func TestCorrection(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, _ := e.meeting(t, 3600, 60_000)
	in, body := correction(1, ids.New(), Edit{SegmentID: strp("s1"), Text: strp("x")})
	if _, _, err := e.svc.Correct(ctx, uid, mid, in, body); code(err) != apperr.TranscriptNotReady {
		t.Fatal(err)
	}
	e.start(t, uid, mid)
	e.run(t)
	orig, _ := e.svc.Current(ctx, uid, mid)
	aiNotes := insertDoc(t, e, mid, "ai_notes", orig.ID, nil)
	insertDoc(t, e, mid, "mom", orig.ID, &aiNotes)
	docs, _ := meetings.LoadDocumentSummaries(ctx, e.pool, mid)
	if docs.AINotes.IsStale || docs.MoM.IsStale {
		t.Fatal("stale before correction")
	}

	before := e.updatedAt(t, mid)
	mut := ids.New()
	in, body = correction(1, mut, Edit{SegmentID: strp("s2"), Text: strp("Budget naik 10 persen.")})
	rev, created, err := e.svc.Correct(ctx, uid, mid, in, body)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	if rev.RevisionNumber != 2 || rev.Kind != KindCorrection || rev.Language != orig.Language || len(rev.Segments) != 4 ||
		rev.Segments[1].Text != "Budget naik 10 persen." || rev.Segments[1].StartMS != orig.Segments[1].StartMS ||
		rev.Segments[0] != orig.Segments[0] || rev.Segments[3] != orig.Segments[3] {
		t.Fatalf("%+v", rev)
	}
	if e.str(t, `SELECT parent_revision_id::text FROM transcript_revisions WHERE id = $1`, rev.ID) != orig.ID {
		t.Fatal("parent")
	}
	if r1, _ := e.svc.GetRevision(ctx, uid, mid, 1); r1.Segments[1].Text != orig.Segments[1].Text {
		t.Fatal("original changed")
	}
	if !e.updatedAt(t, mid).After(before) {
		t.Fatal("updated_at")
	}
	if !strings.Contains(e.str(t, `SELECT body FROM search_documents WHERE meeting_id = $1 AND source = 'transcript'`, mid), "Budget naik") {
		t.Fatal("search")
	}
	docs, _ = meetings.LoadDocumentSummaries(ctx, e.pool, mid)
	for _, d := range []*meetings.DocumentSummary{docs.AINotes, docs.MoM} {
		if !d.IsStale || len(d.StaleReasons) != 1 || d.StaleReasons[0] != "transcript" {
			t.Fatalf("%+v", d)
		}
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE meeting_id = $1 AND type IN ('generate_notes', 'generate_mom')`, mid) != 1 {
		t.Fatal("correction enqueued generation")
	}
	if e.count(t, `SELECT count(*) FROM quota_reservations WHERE meeting_id = $1 AND status = 'consumed'`, mid) != 1 {
		t.Fatal("correction charged quota")
	}

	if r, created, err := e.svc.Correct(ctx, uid, mid, in, body); err != nil || created || r.ID != rev.ID {
		t.Fatal("replay", err, created)
	}
	in2, body2 := correction(1, mut, Edit{SegmentID: strp("s2"), Text: strp("lain")})
	if _, _, err := e.svc.Correct(ctx, uid, mid, in2, body2); code(err) != apperr.IdempotencyKeyReused {
		t.Fatal(err)
	}
	in3, body3 := correction(1, ids.New(), Edit{SegmentID: strp("s3"), Text: strp("y")})
	_, _, err = e.svc.Correct(ctx, uid, mid, in3, body3)
	if code(err) != apperr.RevisionConflict || details(err)["current_revision_number"] != 2 || details(err)["current_revision_id"] != rev.ID {
		t.Fatal(err, details(err))
	}

	for name, c := range map[string]struct {
		edits     []Edit
		path, why string
	}{
		"unknown":   {[]Edit{{SegmentID: strp("s99"), Text: strp("x")}}, "/edits/0/segment_id", apperr.ReasonNotFound},
		"duplicate": {[]Edit{{SegmentID: strp("s1"), Text: strp("a")}, {SegmentID: strp("s1"), Text: strp("b")}}, "/edits/1/segment_id", apperr.ReasonDuplicate},
		"too long":  {[]Edit{{SegmentID: strp("s1"), Text: strp(strings.Repeat("é", MaxSegmentText+1))}}, "/edits/0/text", apperr.ReasonTooLong},
		"no text":   {[]Edit{{SegmentID: strp("s1")}}, "/edits/0/text", apperr.ReasonRequired},
		"no edits":  {nil, "/edits", apperr.ReasonTooShort},
	} {
		in, body := correction(2, ids.New(), c.edits...)
		_, _, err := e.svc.Correct(ctx, uid, mid, in, body)
		if code(err) != apperr.InvalidContent || details(err)["path"] != c.path || details(err)["reason"] != c.why {
			t.Errorf("%s: %v %v", name, err, details(err))
		}
	}
	in4, body4 := correction(2, ids.New(), Edit{SegmentID: strp("s1"), Text: strp("")})
	if r, _, err := e.svc.Correct(ctx, uid, mid, in4, body4); err != nil || r.RevisionNumber != 3 || r.Segments[0].Text != "" {
		t.Fatal("empty text allowed", err)
	}
	if _, _, err := e.svc.Correct(ctx, uid, mid, CorrectionInput{}, []byte(`{}`)); code(err) != apperr.ValidationFailed {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM transcript_revisions WHERE meeting_id = $1`, mid); n != 3 {
		t.Fatal(n)
	}
}

func TestChangeDetection(t *testing.T) {
	e := newEnv(t, nil, nil)
	uid, mid, _ := e.meeting(t, 3600, 5000)
	last := e.updatedAt(t, mid)
	step := func(name string) {
		t.Helper()
		at := e.updatedAt(t, mid)
		if !at.After(last) {
			t.Fatalf("%s: updated_at did not move", name)
		}
		last = at
	}
	jobID := e.start(t, uid, mid)
	step("start")
	j, err := jobs.Claim(ctx, e.pool, "w/0", time.Minute)
	if err != nil || j.ID != jobID {
		t.Fatal(err)
	}
	step("claim")
	if err := e.svc.Transcribe(ctx, j); err != nil {
		t.Fatal(err)
	}
	step("transcript written")
	in, body := correction(1, ids.New(), Edit{SegmentID: strp("s1"), Text: strp("koreksi")})
	if _, _, err := e.svc.Correct(ctx, uid, mid, in, body); err != nil {
		t.Fatal(err)
	}
	step("correction")
}

func (e *env) fillSlots(t *testing.T, uid string, n int) {
	t.Helper()
	e.exec(t, `INSERT INTO ai_generation_slots (user_id, status, consumed_at) SELECT $1, 'consumed', now() FROM generate_series(1, $2)`, uid, n)
}

func TestStartAutoNotesSlot(t *testing.T) {
	e := newEnv(t, nil, func(c *config.Config) { c.AIGenerationLimit = 2 })
	uid, mid, _ := e.meeting(t, 3600, 30_000)
	jobID := e.start(t, uid, mid)
	if e.count(t, `SELECT count(*) FROM ai_generation_slots WHERE transcribe_job_id = $1 AND meeting_id = $2 AND job_id IS NULL AND status = 'reserved'`, jobID, mid) != 1 {
		t.Fatal("auto notes slot not reserved with the transcription")
	}
	e.run(t)
	notes := e.str(t, `SELECT id::text FROM jobs WHERE meeting_id = $1 AND type = 'generate_notes'`, mid)
	if e.count(t, `SELECT count(*) FROM ai_generation_slots WHERE transcribe_job_id = $1 AND job_id = $2 AND status = 'reserved'`, jobID, notes) != 1 ||
		e.count(t, `SELECT count(*) FROM ai_generation_slots WHERE user_id = $1`, uid) != 1 {
		t.Fatal("slot not handed to the notes job")
	}
}

func TestStartGenerationLimit(t *testing.T) {
	e := newEnv(t, nil, func(c *config.Config) { c.AIGenerationLimit = 3 })
	uid, mid, _ := e.meeting(t, 3600, 30_000)
	e.fillSlots(t, uid, 3)

	key := ids.New()
	_, _, err := e.svc.Start(ctx, uid, mid, key, StartInput{}, nil)
	d := details(err)
	if code(err) != apperr.AIGenerationLimit || d["limit"] != 3 || d["remaining"] != 0 || d["next_slot_at"] == nil {
		t.Fatal(err, d)
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE meeting_id = $1`, mid) != 0 ||
		e.count(t, `SELECT count(*) FROM quota_reservations WHERE user_id = $1`, uid) != 0 ||
		e.count(t, `SELECT count(*) FROM ai_generation_slots WHERE user_id = $1`, uid) != 3 || e.state(t, mid) != meetings.StateUploaded {
		t.Fatal("429 reserved something")
	}
	// The 429 stored nothing for the key: the same request may be retried.
	if _, _, err := e.svc.Start(ctx, uid, mid, key, StartInput{}, nil); code(err) != apperr.AIGenerationLimit {
		t.Fatal(err)
	}

	// Transcription only: a new intent with a new key.
	j, created, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{Notes: strp(NotesSkip)}, []byte(`{"notes":"skip"}`))
	if err != nil || !created {
		t.Fatal(err, created)
	}
	if e.count(t, `SELECT count(*) FROM quota_reservations WHERE job_id = $1 AND status = 'reserved' AND seconds = 30`, j.ID) != 1 ||
		e.count(t, `SELECT count(*) FROM ai_generation_slots WHERE user_id = $1`, uid) != 3 {
		t.Fatal("skip must reserve quota and no slot")
	}
	e.run(t)
	if e.job(t, j.ID).Status != jobs.StatusSucceeded || e.state(t, mid) != meetings.StateTranscriptReady ||
		e.count(t, `SELECT count(*) FROM jobs WHERE meeting_id = $1 AND type = 'generate_notes'`, mid) != 0 ||
		e.count(t, `SELECT count(*) FROM quota_reservations WHERE job_id = $1 AND status = 'consumed'`, j.ID) != 1 {
		t.Fatal("skip: transcript_ready without notes")
	}
	if _, _, err := e.svc.Start(ctx, uid, mid, ids.New(), StartInput{Notes: strp("later")}, []byte(`{"notes":"later"}`)); code(err) != apperr.ValidationFailed {
		t.Fatal(err)
	}
}

func TestNoSpeechReleasesSlot(t *testing.T) {
	e := newEnv(t, &fake.STT{Results: []ai.STTResult{{Language: "id", AudioMS: 5000}}}, nil)
	uid, mid, _ := e.meeting(t, 3600, 5000)
	jobID := e.start(t, uid, mid)
	e.run(t)
	assertFailed(t, e, jobID, mid, jobs.CatNoSpeech, 1)
	if e.count(t, `SELECT count(*) FROM ai_generation_slots WHERE transcribe_job_id = $1 AND status = 'released'`, jobID) != 1 {
		t.Fatal("slot not released")
	}
	u, err := usage.New(e.pool, e.cfg, testutil.DiscardLogger()).Get(ctx, uid)
	if err != nil || u.AIGenerations.Remaining != e.cfg.AIGenerationLimit {
		t.Fatal(u.AIGenerations, err)
	}
}

func TestTimingAndUncertainPersisted(t *testing.T) {
	for _, tm := range []string{ai.TimingChunk, ai.TimingSegment} {
		t.Run(tm, func(t *testing.T) {
			e := newEnv(t, &fake.STT{Timing: tm, Uncertain: []int{1, 3}}, nil)
			uid, mid, _ := e.meeting(t, 3600, 60_000)
			e.start(t, uid, mid)
			e.run(t)
			rev, err := e.svc.Current(ctx, uid, mid)
			if err != nil || rev.Timing != tm {
				t.Fatal(rev, err)
			}
			for i, s := range rev.Segments {
				want := i == 1 || i == 3
				if s.Uncertain != want || (want != (s.UncertainReason != nil)) || (want && *s.UncertainReason != ai.UncertainLowConfidence) {
					t.Fatalf("segment %d: %+v", i, s)
				}
			}
			b, _ := json.Marshal(rev)
			if !strings.Contains(string(b), `"timing":"`+tm+`"`) || !strings.Contains(string(b), `"uncertain":false,"uncertain_reason":null`) ||
				!strings.Contains(string(b), `"uncertain":true,"uncertain_reason":"low_confidence"`) {
				t.Fatal(string(b))
			}

			// s2 text changed, s4 confirmed as-is, s1 resent unchanged: only s2 and s4 resolve.
			in, body := correction(1, ids.New(), Edit{SegmentID: strp("s2"), Text: strp("Agenda dikoreksi.")},
				Edit{SegmentID: strp("s1"), Text: strp(rev.Segments[0].Text)})
			in.ResolvedUncertainSegmentIDs = []string{"s4"}
			body, _ = json.Marshal(in)
			cor, _, err := e.svc.Correct(ctx, uid, mid, in, body)
			if err != nil {
				t.Fatal(err)
			}
			if cor.Timing != tm {
				t.Fatal("correction must inherit timing", cor.Timing)
			}
			for i, s := range cor.Segments {
				if s.Uncertain || s.UncertainReason != nil {
					t.Fatalf("segment %d still uncertain: %+v", i, s)
				}
			}
			if r1, _ := e.svc.GetRevision(ctx, uid, mid, 1); !r1.Segments[1].Uncertain || !r1.Segments[3].Uncertain {
				t.Fatal("original changed")
			}
			list, _, err := e.svc.ListRevisions(ctx, uid, mid, 10, nil)
			if err != nil || list[0].Timing != tm || list[1].Timing != tm {
				t.Fatal(list, err)
			}
		})
	}
}

func TestCorrectionResolveOnly(t *testing.T) {
	e := newEnv(t, &fake.STT{Uncertain: []int{0}}, nil)
	uid, mid, _ := e.meeting(t, 3600, 30_000)
	e.start(t, uid, mid)
	e.run(t)
	in := CorrectionInput{ExpectedRevision: new(int), MutationID: strp(ids.New()), ResolvedUncertainSegmentIDs: []string{"s1"}}
	*in.ExpectedRevision = 1
	body, _ := json.Marshal(in)
	rev, created, err := e.svc.Correct(ctx, uid, mid, in, body)
	if err != nil || !created || rev.Segments[0].Uncertain {
		t.Fatal(rev, err)
	}
	for name, c := range map[string]struct {
		ids  []string
		path string
		why  string
	}{
		"unknown":   {[]string{"s9"}, "/resolved_uncertain_segment_ids/0", apperr.ReasonNotFound},
		"duplicate": {[]string{"s1", "s1"}, "/resolved_uncertain_segment_ids/1", apperr.ReasonDuplicate},
	} {
		in := CorrectionInput{ExpectedRevision: new(int), MutationID: strp(ids.New()), ResolvedUncertainSegmentIDs: c.ids}
		*in.ExpectedRevision = 2
		body, _ := json.Marshal(in)
		_, _, err := e.svc.Correct(ctx, uid, mid, in, body)
		if code(err) != apperr.InvalidContent || details(err)["path"] != c.path || details(err)["reason"] != c.why {
			t.Errorf("%s: %v %v", name, err, details(err))
		}
	}
}
