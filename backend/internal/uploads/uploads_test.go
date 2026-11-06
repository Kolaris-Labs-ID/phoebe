package uploads

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai/fake"
	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/ids"
	"phoebe/internal/meetings"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
	"phoebe/internal/transcripts"
	"phoebe/internal/usage"
)

type env struct {
	t     *testing.T
	ctx   context.Context
	pool  *pgxpool.Pool
	cfg   *config.Config
	store storage.Storage
	svc   *Service
}

func newEnv(t *testing.T, tweak func(*config.Config)) *env {
	t.Helper()
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	if tweak != nil {
		tweak(cfg)
	}
	log := testutil.DiscardLogger()
	store := testutil.Storage(t)
	return &env{t: t, ctx: context.Background(), pool: pool, cfg: cfg, store: store,
		svc: New(pool, cfg, store, log, transcripts.New(pool, cfg, store, usage.New(pool, cfg, log), &fake.STT{}, log))}
}

func (e *env) withStore(st storage.Storage) {
	e.store = st
	e.svc = New(e.pool, e.cfg, st, testutil.DiscardLogger(), e.svc.starter)
}

func ptr[T any](v T) *T { return &v }

func input(b []byte, ct string, durationMS int64) CreateInput {
	return CreateInput{SizeBytes: ptr(int64(len(b))), SHA256: ptr(testutil.SHA256Hex(b)), ContentType: ptr(ct), DurationMS: ptr(durationMS)}
}

func withAuto(in CreateInput, key, lang string) CreateInput {
	in.Transcription = &TranscriptionInput{IdempotencyKey: &key, Language: &lang}
	return in
}

// wav1k is an 8-bit mono 1 kHz PCM WAV: 1 byte per ms, so durationMS is exact.
func wav1k(durationMS int64) []byte {
	buf := make([]byte, 44+durationMS)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+durationMS))
	copy(buf[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], 1)
	binary.LittleEndian.PutUint32(buf[24:], 1000)
	binary.LittleEndian.PutUint32(buf[28:], 1000)
	binary.LittleEndian.PutUint16(buf[32:], 1)
	binary.LittleEndian.PutUint16(buf[34:], 8)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(durationMS))
	for i := range durationMS {
		buf[44+i] = byte(128 + i%64)
	}
	return buf
}

func (e *env) create(uid, mid string, in CreateInput) *meetings.UploadSession {
	e.t.Helper()
	u, _, err := e.svc.Create(e.ctx, uid, mid, in)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) put(uid, id string, offset int64, b []byte) (*Result, error) {
	return e.svc.PutChunk(e.ctx, uid, id, offset, int64(len(b)), bytes.NewReader(b))
}

// upload sends b in chunks of the configured max and returns the last response.
func (e *env) upload(uid, id string, b []byte) (*Result, error) {
	var res *Result
	var err error
	for off := int64(0); off < int64(len(b)); {
		end := min(off+e.cfg.UploadChunkMaxBytes, int64(len(b)))
		if res, err = e.put(uid, id, off, b[off:end]); err != nil {
			return nil, err
		}
		off = end
	}
	return res, nil
}

func (e *env) scalar(sql string, args ...any) any {
	e.t.Helper()
	var v any
	if err := e.pool.QueryRow(e.ctx, sql, args...).Scan(&v); err != nil {
		e.t.Fatal(sql, err)
	}
	return v
}

func (e *env) count(sql string, args ...any) int64 { return e.scalar(sql, args...).(int64) }

func (e *env) state(mid string) string {
	return e.scalar(`SELECT pipeline_state FROM meetings WHERE id = $1`, mid).(string)
}

func (e *env) updatedAt(mid string) time.Time {
	return e.scalar(`SELECT updated_at FROM meetings WHERE id = $1`, mid).(time.Time)
}

func wantCode(t *testing.T, err error, code apperr.Code) *apperr.Error {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	return ae
}

func TestCreate(t *testing.T) {
	e := newEnv(t, nil)
	u := testutil.CreateUser(t, e.pool, 3600)
	other := testutil.CreateUser(t, e.pool, 3600)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	b := testutil.WAV(2000)

	for name, in := range map[string]CreateInput{
		"empty":       {},
		"bad sha":     {SizeBytes: ptr(int64(10)), SHA256: ptr("xyz"), ContentType: ptr("audio/wav"), DurationMS: ptr(int64(1))},
		"bad type":    {SizeBytes: ptr(int64(10)), SHA256: ptr(testutil.SHA256Hex(b)), ContentType: ptr("video/mp4"), DurationMS: ptr(int64(1))},
		"zero size":   {SizeBytes: ptr(int64(0)), SHA256: ptr(testutil.SHA256Hex(b)), ContentType: ptr("audio/wav"), DurationMS: ptr(int64(1))},
		"bad key":     withAuto(input(b, "audio/wav", 2000), "nope", "auto"),
		"bad lang":    withAuto(input(b, "audio/wav", 2000), ids.New(), "fr"),
		"no key/lang": {SizeBytes: ptr(int64(10)), SHA256: ptr(testutil.SHA256Hex(b)), ContentType: ptr("audio/wav"), DurationMS: ptr(int64(1)), Transcription: &TranscriptionInput{}},
	} {
		_, _, err := e.svc.Create(e.ctx, u.ID, mid, in)
		if apperr.CodeOf(err) != apperr.ValidationFailed {
			t.Errorf("%s: %v", name, err)
		}
	}
	big := input(b, "audio/wav", 2000)
	big.SizeBytes = ptr(e.cfg.MaxUploadBytes + 1)
	_, _, err := e.svc.Create(e.ctx, u.ID, mid, big)
	wantCode(t, err, apperr.UploadTooLarge)
	_, _, err = e.svc.Create(e.ctx, u.ID, mid, input(b, "audio/wav", 7_200_001))
	ae := wantCode(t, err, apperr.AudioTooLong)
	if ae.Details["duration_ms"] != int64(7_200_001) || ae.Details["max_duration_ms"] != int64(7_200_000) {
		t.Fatal(ae.Details)
	}
	_, _, err = e.svc.Create(e.ctx, other.ID, mid, input(b, "audio/wav", 2000))
	wantCode(t, err, apperr.NotFound)
	if n := e.count(`SELECT count(*) FROM upload_sessions`); n != 0 {
		t.Fatal("rejected creates stored sessions", n)
	}

	before := e.updatedAt(mid)
	in := input(b, "audio/x-wav", 2000)
	s1, created, err := e.svc.Create(e.ctx, u.ID, mid, in)
	if err != nil || !created || s1.ContentType != "audio/wav" || s1.Status != "open" || s1.AutoTranscription ||
		s1.ChunkMaxBytes != e.cfg.UploadChunkMaxBytes {
		t.Fatal(s1, created, err)
	}
	if e.state(mid) != meetings.StateUploading || !e.updatedAt(mid).After(before) {
		t.Fatal("pipeline/updated_at not moved")
	}
	key := ids.New()
	s2, created, err := e.svc.Create(e.ctx, u.ID, mid, withAuto(in, key, "id"))
	if err != nil || created || s2.ID != s1.ID || !s2.AutoTranscription {
		t.Fatal("resume", s2, created, err)
	}
	if k := e.scalar(`SELECT transcribe_idempotency_key::text FROM upload_sessions WHERE id = $1`, s1.ID); k != key {
		t.Fatal(k)
	}

	if _, err := e.put(u.ID, s1.ID, 0, b[:100]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET writer_lease_id = gen_random_uuid(), writer_lease_until = now() + interval '1 minute' WHERE id = $1`, s1.ID); err != nil {
		t.Fatal(err)
	}
	b2 := testutil.WAV(3000)
	_, _, err = e.svc.Create(e.ctx, u.ID, mid, input(b2, "audio/wav", 3000))
	wantCode(t, err, apperr.UploadBusy)
	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET writer_lease_until = now() - interval '1 second' WHERE id = $1`, s1.ID); err != nil {
		t.Fatal(err)
	}
	s3, created, err := e.svc.Create(e.ctx, u.ID, mid, input(b2, "audio/wav", 3000))
	if err != nil || !created || s3.ID == s1.ID {
		t.Fatal(s3, err)
	}
	if st := e.scalar(`SELECT status FROM upload_sessions WHERE id = $1`, s1.ID); st != "canceled" {
		t.Fatal(st)
	}
	if _, ok, _ := e.store.Stat(e.ctx, tempKey(u.ID, mid, s1.ID)); ok {
		t.Fatal("canceled session temp kept")
	}

	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, s3.ID); err != nil {
		t.Fatal(err)
	}
	s4, created, err := e.svc.Create(e.ctx, u.ID, mid, input(b2, "audio/wav", 3000))
	if err != nil || !created || s4.ID == s3.ID {
		t.Fatal("expired same-checksum session must be replaced", s4, err)
	}
	if st := e.scalar(`SELECT status FROM upload_sessions WHERE id = $1`, s3.ID); st != "expired" {
		t.Fatal(st)
	}

	res, err := e.upload(u.ID, s4.ID, b2)
	if err != nil || res.Audio == nil {
		t.Fatal(res, err)
	}
	_, _, err = e.svc.Create(e.ctx, u.ID, mid, input(b, "audio/wav", 2000))
	wantCode(t, err, apperr.AudioExists)
}

type failingReader struct {
	r io.Reader
	n int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	p = p[:min(len(p), f.n)]
	n, err := f.r.Read(p)
	f.n -= n
	return n, err
}

func TestResumeAndCompleteTwice(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.UploadChunkMaxBytes = 20000 })
	u := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	b := testutil.WAV(3000)
	s := e.create(u.ID, mid, input(b, "audio/mpeg", 3000))

	res, err := e.put(u.ID, s.ID, 0, b[:20000])
	if err != nil || res.Upload.ReceivedBytes != 20000 || res.Audio != nil || res.Job != nil {
		t.Fatal(res, err)
	}
	_, err = e.svc.PutChunk(e.ctx, u.ID, s.ID, 20000, 20000, &failingReader{bytes.NewReader(b[20000:40000]), 5000})
	if err == nil {
		t.Fatal("interrupted body must error")
	}
	g, err := e.svc.Get(e.ctx, u.ID, s.ID)
	if err != nil || g.ReceivedBytes != 25000 || g.Status != "open" {
		t.Fatal("persisted bytes of an interrupted chunk must count", g, err)
	}
	_, err = e.put(u.ID, s.ID, 20000, b[20000:40000])
	if ae := wantCode(t, err, apperr.UploadOffsetMismatch); ae.Details["received_bytes"] != int64(25000) {
		t.Fatal(ae.Details)
	}
	_, err = e.svc.Complete(e.ctx, u.ID, s.ID)
	if ae := wantCode(t, err, apperr.UploadIncomplete); ae.Details["received_bytes"] != int64(25000) || ae.Details["size_bytes"] != int64(len(b)) {
		t.Fatal(ae.Details)
	}
	if res, err = e.put(u.ID, s.ID, 25000, b[25000:45000]); err != nil || res.Audio != nil {
		t.Fatal(res, err)
	}
	before := e.updatedAt(mid)
	res, err = e.put(u.ID, s.ID, 45000, b[45000:])
	if err != nil || res.Upload.Status != "completed" || res.Audio == nil || res.Job != nil {
		t.Fatal(res, err)
	}
	a := res.Audio
	if a.ContentType != "audio/wav" || a.DurationMS != 3000 || a.SizeBytes != int64(len(b)) || !a.Available || a.RetainUntil != nil {
		t.Fatal("header-lying declared type must be replaced by the detected one", a)
	}
	if e.state(mid) != meetings.StateUploaded || !e.updatedAt(mid).After(before) {
		t.Fatal(e.state(mid))
	}
	again, err := e.svc.Complete(e.ctx, u.ID, s.ID)
	if err != nil || again.Audio.ID != a.ID || again.Upload.Status != "completed" || again.Job != nil {
		t.Fatal(again, err)
	}
	if n := e.count(`SELECT count(*) FROM audio_assets WHERE meeting_id = $1`, mid); n != 1 {
		t.Fatal(n)
	}
	if n := e.count(`SELECT count(*) FROM events WHERE name = 'upload_verified' AND event_id = $1`, a.ID); n != 1 {
		t.Fatal(n)
	}
	key := e.scalar(`SELECT storage_key FROM audio_assets WHERE id = $1`, a.ID).(string)
	if key != "u/"+u.ID+"/m/"+mid+"/audio/"+s.ID+".wav" {
		t.Fatal(key)
	}
	if _, ok, _ := e.store.Stat(e.ctx, key); !ok {
		t.Fatal("final missing")
	}
	if _, ok, _ := e.store.Stat(e.ctx, tempKey(u.ID, mid, s.ID)); ok {
		t.Fatal("temp kept")
	}
	_, err = e.put(u.ID, s.ID, int64(len(b)), nil)
	if ae := wantCode(t, err, apperr.UploadNotOpen); ae.Details["status"] != "completed" {
		t.Fatal(ae.Details)
	}
}

func TestAutoStart(t *testing.T) {
	e := newEnv(t, nil)
	u := testutil.CreateUser(t, e.pool, 60)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	b := testutil.WAV(5000)
	key := ids.New()
	s := e.create(u.ID, mid, withAuto(input(b, "audio/wav", 5000), key, "auto"))
	if !s.AutoTranscription {
		t.Fatal(s)
	}
	res, err := e.upload(u.ID, s.ID, b)
	if err != nil || res.Audio == nil || res.Job == nil || res.Job.Type != "transcribe" || res.Job.Status != "queued" {
		t.Fatal(res, err)
	}
	if e.state(mid) != meetings.StateTranscribing {
		t.Fatal(e.state(mid))
	}
	if n := e.count(`SELECT count(*) FROM quota_reservations WHERE meeting_id = $1 AND status = 'reserved' AND seconds = 5`, mid); n != 1 {
		t.Fatal(n)
	}
	again, err := e.svc.Complete(e.ctx, u.ID, s.ID)
	if err != nil || again.Job == nil || again.Job.ID != res.Job.ID {
		t.Fatal(again, err)
	}
	tx, err := e.pool.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	j, created, err := e.svc.starter.StartInTx(e.ctx, tx, u.ID, mid, key, "auto")
	_ = tx.Rollback(e.ctx)
	if err != nil || created || j.ID != res.Job.ID {
		t.Fatal("same key/body must replay the auto-started job", j, err)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE meeting_id = $1 AND type = 'transcribe'`, mid); n != 1 {
		t.Fatal(n)
	}

	broke := testutil.CreateUser(t, e.pool, 0)
	mid2 := testutil.CreateMeeting(t, e.pool, broke.ID)
	s2 := e.create(broke.ID, mid2, withAuto(input(b, "audio/wav", 5000), ids.New(), "id"))
	res, err = e.upload(broke.ID, s2.ID, b)
	if err != nil || res.Audio == nil || res.Job != nil || res.Upload.Status != "completed" {
		t.Fatal("quota exhausted must still verify, with job null", res, err)
	}
	if e.state(mid2) != meetings.StateUploaded {
		t.Fatal(e.state(mid2))
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE meeting_id = $1`, mid2) +
		e.count(`SELECT count(*) FROM quota_reservations WHERE meeting_id = $1`, mid2); n != 0 {
		t.Fatal("no job or reservation", n)
	}
}

type putResult struct {
	res *Result
	err error
}

func (e *env) putAsync(uid, id string, offset, length int64, body io.Reader) chan putResult {
	ch := make(chan putResult, 1)
	go func() {
		r, err := e.svc.PutChunk(e.ctx, uid, id, offset, length, body)
		ch <- putResult{r, err}
	}()
	return ch
}

func TestWriterFencing(t *testing.T) {
	e := newEnv(t, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	b := testutil.WAV(2000)
	s := e.create(u.ID, mid, input(b, "audio/wav", 2000))

	pr, pw := io.Pipe()
	a := e.putAsync(u.ID, s.ID, 0, 10000, pr)
	if _, err := pw.Write(bytes.Repeat([]byte{1}, 5000)); err != nil {
		t.Fatal(err)
	}
	_, err := e.put(u.ID, s.ID, 0, b[:10000])
	wantCode(t, err, apperr.UploadBusy)

	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET writer_lease_until = now() - interval '1 second' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.put(u.ID, s.ID, 0, b[:10000])
	wantCode(t, err, apperr.UploadBusy)
	if _, err := pw.Write(bytes.Repeat([]byte{1}, 5000)); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	r := <-a
	if ae := wantCode(t, r.err, apperr.UploadOffsetMismatch); ae.Details["received_bytes"] != int64(0) {
		t.Fatal("stale writer's commit must affect 0 rows", ae.Details)
	}
	g, _ := e.svc.Get(e.ctx, u.ID, s.ID)
	if g.ReceivedBytes != 0 {
		t.Fatal(g.ReceivedBytes)
	}
	res, err := e.upload(u.ID, s.ID, b)
	if err != nil || res.Audio == nil {
		t.Fatal("fresh writer truncates stale bytes and completes", res, err)
	}
}

func TestLeaseRenewalLost(t *testing.T) {
	e := newEnv(t, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	size := int64(6 << 20)
	sha := make([]byte, 32)
	_, _ = rand.Read(sha)
	in := input(nil, "audio/wav", 1000)
	in.SizeBytes = &size
	in.SHA256 = ptr(testutil.SHA256Hex(sha))
	s := e.create(u.ID, mid, in)

	pr, pw := io.Pipe()
	a := e.putAsync(u.ID, s.ID, 0, size, pr)
	if _, err := pw.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET writer_lease_id = gen_random_uuid() WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = pw.Write(make([]byte, size-(1<<20)))
		pw.Close()
	}()
	r := <-a
	pr.Close()
	wantCode(t, r.err, apperr.UploadBusy)
	g, _ := e.svc.Get(e.ctx, u.ID, s.ID)
	if g.ReceivedBytes != 0 {
		t.Fatal("aborted writer must not count bytes", g.ReceivedBytes)
	}
}

func TestPutErrors(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.UploadChunkMaxBytes = 1000 })
	u := testutil.CreateUser(t, e.pool, 0)
	other := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	b := wav1k(1500)
	s := e.create(u.ID, mid, input(b, "audio/wav", 1500))

	_, err := e.svc.PutChunk(e.ctx, u.ID, s.ID, 0, 1001, bytes.NewReader(make([]byte, 1001)))
	wantCode(t, err, apperr.ChunkTooLarge)
	_, err = e.put(other.ID, s.ID, 0, b[:10])
	wantCode(t, err, apperr.NotFound)
	_, err = e.put(u.ID, ids.New(), 0, b[:10])
	wantCode(t, err, apperr.NotFound)
	if _, err := e.put(u.ID, s.ID, 0, b[:1000]); err != nil {
		t.Fatal(err)
	}
	_, err = e.put(u.ID, s.ID, 1000, make([]byte, 1000))
	wantCode(t, err, apperr.UploadTooLarge)
	_, err = e.svc.PutChunk(e.ctx, u.ID, s.ID, 1000, 10, bytes.NewReader(make([]byte, 600)))
	wantCode(t, err, apperr.UploadTooLarge)
	_, err = e.put(u.ID, s.ID, 0, b[:10])
	wantCode(t, err, apperr.UploadOffsetMismatch)
	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.put(u.ID, s.ID, 1000, b[1000:])
	wantCode(t, err, apperr.UploadExpired)
	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET received_bytes = size_bytes WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Complete(e.ctx, u.ID, s.ID)
	wantCode(t, err, apperr.UploadExpired)

	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET status = 'canceled' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Complete(e.ctx, u.ID, s.ID)
	if ae := wantCode(t, err, apperr.UploadNotOpen); ae.Details["status"] != "canceled" {
		t.Fatal(ae.Details)
	}
	_, err = e.svc.Complete(e.ctx, other.ID, s.ID)
	wantCode(t, err, apperr.NotFound)
	_, err = e.svc.Get(e.ctx, other.ID, s.ID)
	wantCode(t, err, apperr.NotFound)
}

func TestVerifyRejections(t *testing.T) {
	e := newEnv(t, nil)
	u := testutil.CreateUser(t, e.pool, 100000)
	read := func(name string) []byte {
		b, err := os.ReadFile("../audio/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	garbage := make([]byte, 5000)
	_, _ = rand.Read(garbage)
	wav := testutil.WAV(2000)
	cases := []struct {
		name string
		b    []byte
		sha  string
		code apperr.Code
	}{
		{"checksum", wav, testutil.SHA256Hex([]byte("other")), apperr.ChecksumMismatch},
		{"wrong type", garbage, "", apperr.AudioUnsupported},
		{"video", read("video.mp4"), "", apperr.AudioUnsupported},
		{"empty", testutil.WAV(500), "", apperr.AudioEmpty},
		{"wav data size lies", wav[:len(wav)-100], "", apperr.AudioCorrupt},
		{"m4a mdhd != stts", read("tone_bad_mdhd.m4a"), "", apperr.AudioCorrupt},
		{"m4a truncated", read("tone_truncated.m4a"), "", apperr.AudioCorrupt},
		{"too long", wav1k(7_200_001), "", apperr.AudioTooLong},
	}
	for _, c := range cases {
		mid := testutil.CreateMeeting(t, e.pool, u.ID)
		in := withAuto(input(c.b, "audio/mp4", 1000), ids.New(), "auto")
		if c.sha != "" {
			in.SHA256 = &c.sha
		}
		s := e.create(u.ID, mid, in)
		_, err := e.upload(u.ID, s.ID, c.b)
		if apperr.CodeOf(err) != c.code {
			t.Errorf("%s: want %s, got %v", c.name, c.code, err)
			continue
		}
		if c.code == apperr.AudioTooLong {
			ae := wantCode(t, err, c.code)
			if ae.Details["duration_ms"] != int64(7_200_001) || ae.Details["max_duration_ms"] != int64(7_200_000) {
				t.Error(ae.Details)
			}
		}
		g, _ := e.svc.Get(e.ctx, u.ID, s.ID)
		if g.Status != "failed" || g.FailureReason == nil || *g.FailureReason != string(c.code) {
			t.Errorf("%s: session %+v", c.name, g)
		}
		if st := e.state(mid); st != meetings.StateAwaitingAudio {
			t.Errorf("%s: state %s", c.name, st)
		}
		if n := e.count(`SELECT count(*) FROM jobs WHERE meeting_id = $1`, mid) +
			e.count(`SELECT count(*) FROM audio_assets WHERE meeting_id = $1`, mid); n != 0 {
			t.Errorf("%s: job/asset created", c.name)
		}
		if _, ok, _ := e.store.Stat(e.ctx, tempKey(u.ID, mid, s.ID)); ok {
			t.Errorf("%s: temp kept", c.name)
		}
		_, err = e.svc.Complete(e.ctx, u.ID, s.ID)
		wantCode(t, err, apperr.UploadNotOpen)
		if _, _, err := e.svc.Create(e.ctx, u.ID, mid, input(wav, "audio/wav", 2000)); err != nil {
			t.Errorf("%s: new session after failure: %v", c.name, err)
		}
	}

	for _, c := range []struct {
		name, ct string
		b        []byte
		ms       int64
	}{
		{"exact max", "audio/wav", wav1k(7_200_000), 7_200_000},
		{"m4a", "audio/mp4", read("tone.m4a"), 0},
		{"m4a moov at end", "audio/mp4", read("tone_moov_end.m4a"), 0},
		{"mp3", "audio/mpeg", read("tone.mp3"), 0},
	} {
		mid := testutil.CreateMeeting(t, e.pool, u.ID)
		s := e.create(u.ID, mid, input(c.b, "audio/wav", 1000))
		res, err := e.upload(u.ID, s.ID, c.b)
		if err != nil || res.Audio == nil || res.Audio.ContentType != c.ct || (c.ms != 0 && res.Audio.DurationMS != c.ms) {
			t.Errorf("%s: %+v %v", c.name, res, err)
		}
	}
}

type hookStore struct {
	*storage.LocalFS
	onMove func()
}

func (h hookStore) Move(ctx context.Context, src, dst string) error {
	if err := h.LocalFS.Move(ctx, src, dst); err != nil {
		return err
	}
	h.onMove()
	return nil
}

func TestCompleteCrashRecovery(t *testing.T) {
	e := newEnv(t, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	b := testutil.WAV(2000)
	s := e.create(u.ID, mid, input(b, "audio/wav", 2000))
	if _, err := e.store.Put(e.ctx, tempKey(u.ID, mid, s.ID), bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET received_bytes = size_bytes, status = 'verifying',
		writer_lease_id = gen_random_uuid(), writer_lease_until = now() + interval '1 minute' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Complete(e.ctx, u.ID, s.ID)
	wantCode(t, err, apperr.UploadBusy)
	_, err = e.put(u.ID, s.ID, int64(len(b)), nil)
	wantCode(t, err, apperr.UploadBusy)

	final := "u/" + u.ID + "/m/" + mid + "/audio/" + s.ID + ".wav"
	if err := e.store.Move(e.ctx, tempKey(u.ID, mid, s.ID), final); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET writer_lease_until = now() - interval '1 second' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Complete(e.ctx, u.ID, s.ID)
	if err != nil || res.Audio == nil || res.Upload.Status != "completed" {
		t.Fatal(res, err)
	}
	again, err := e.svc.Complete(e.ctx, u.ID, s.ID)
	if err != nil || again.Audio.ID != res.Audio.ID {
		t.Fatal(again, err)
	}
	if n := e.count(`SELECT count(*) FROM audio_assets WHERE meeting_id = $1 AND storage_key = $2`, mid, final); n != 1 {
		t.Fatal(n)
	}

	mid2 := testutil.CreateMeeting(t, e.pool, u.ID)
	ms := meetings.New(e.pool, e.cfg, e.store, testutil.DiscardLogger())
	e.withStore(hookStore{e.store.(*storage.LocalFS), func() {
		if _, err := ms.Delete(e.ctx, u.ID, mid2); err != nil {
			t.Error(err)
		}
	}})
	s2 := e.create(u.ID, mid2, input(b, "audio/wav", 2000))
	_, err = e.upload(u.ID, s2.ID, b)
	wantCode(t, err, apperr.NotFound)
	if _, ok, _ := e.store.Stat(e.ctx, "u/"+u.ID+"/m/"+mid2+"/audio/"+s2.ID+".wav"); ok {
		t.Fatal("final kept after tombstone during verify")
	}
	if n := e.count(`SELECT count(*) FROM audio_assets WHERE meeting_id = $1`, mid2) +
		e.count(`SELECT count(*) FROM events WHERE meeting_id = $1`, mid2); n != 0 {
		t.Fatal("finalize wrote for a tombstoned meeting")
	}
}

func TestExpireSweep(t *testing.T) {
	e := newEnv(t, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	b := testutil.WAV(2000)
	mk := func(sql string) (string, string) {
		mid := testutil.CreateMeeting(t, e.pool, u.ID)
		s := e.create(u.ID, mid, input(b, "audio/wav", 2000))
		if _, err := e.put(u.ID, s.ID, 0, b[:100]); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(e.ctx, sql, s.ID); err != nil {
			t.Fatal(err)
		}
		return mid, s.ID
	}
	expMid, expID := mk(`UPDATE upload_sessions SET expires_at = now() - interval '1 second' WHERE id = $1`)
	leaseMid, _ := mk(`UPDATE upload_sessions SET expires_at = now() - interval '1 second',
		writer_lease_id = gen_random_uuid(), writer_lease_until = now() + interval '1 minute' WHERE id = $1`)
	verMid, _ := mk(`UPDATE upload_sessions SET expires_at = now() - interval '1 second', received_bytes = size_bytes,
		status = 'verifying' WHERE id = $1`)
	liveMid, _ := mk(`SELECT $1::uuid`)
	before := e.updatedAt(expMid)

	n, err := e.svc.ExpireSweep(e.ctx)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if st := e.scalar(`SELECT status FROM upload_sessions WHERE id = $1`, expID); st != "expired" {
		t.Fatal(st)
	}
	if e.state(expMid) != meetings.StateAwaitingAudio || !e.updatedAt(expMid).After(before) {
		t.Fatal(e.state(expMid))
	}
	if _, ok, _ := e.store.Stat(e.ctx, tempKey(u.ID, expMid, expID)); ok {
		t.Fatal("temp kept")
	}
	for _, mid := range []string{leaseMid, verMid, liveMid} {
		if e.state(mid) != meetings.StateUploading {
			t.Fatal("must not expire", mid)
		}
	}
	_, err = e.put(u.ID, expID, 100, b[100:])
	wantCode(t, err, apperr.UploadExpired)
}

func TestConcurrentComplete(t *testing.T) {
	e := newEnv(t, nil)
	u := testutil.CreateUser(t, e.pool, 0)
	mid := testutil.CreateMeeting(t, e.pool, u.ID)
	b := testutil.WAV(2000)
	s := e.create(u.ID, mid, input(b, "audio/wav", 2000))
	if _, err := e.store.Put(e.ctx, tempKey(u.ID, mid, s.ID), bytes.NewReader(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE upload_sessions SET received_bytes = size_bytes WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := e.svc.Complete(e.ctx, u.ID, s.ID)
			errs <- err
		}()
	}
	ok := 0
	for range 8 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case apperr.CodeOf(err) != apperr.UploadBusy:
			t.Fatal(err)
		}
	}
	if ok == 0 {
		t.Fatal("no complete succeeded")
	}
	if n := e.count(`SELECT count(*) FROM audio_assets WHERE meeting_id = $1`, mid) +
		e.count(`SELECT count(*) FROM events WHERE meeting_id = $1`, mid); n != 2 {
		t.Fatal(n)
	}
}
