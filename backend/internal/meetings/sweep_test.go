package meetings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
)

func TestRetentionSweep(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)

	untranscribed := e.create(t, a.ID, CreateInput{})
	uAsset, uKey := e.audio(t, a.ID, untranscribed.ID, 2000)
	e.exec(t, `UPDATE audio_assets SET created_at = now() - interval '400 days' WHERE id = $1`, uAsset)

	expired := e.create(t, a.ID, CreateInput{})
	xAsset, xKey := e.audio(t, a.ID, expired.ID, 2000)
	e.transcript(t, a.ID, expired.ID, 1, nil)
	e.exec(t, `UPDATE audio_assets SET retain_until = now() - interval '1 second' WHERE id = $1`, xAsset)

	busy := e.create(t, a.ID, CreateInput{})
	bAsset, bKey := e.audio(t, a.ID, busy.ID, 2000)
	e.exec(t, `UPDATE audio_assets SET retain_until = now() - interval '1 second' WHERE id = $1`, bAsset)
	e.job(t, a.ID, busy.ID, jobs.TypeTranscribe)

	future := e.create(t, a.ID, CreateInput{})
	fAsset, fKey := e.audio(t, a.ID, future.ID, 2000)
	e.exec(t, `UPDATE audio_assets SET retain_until = now() + interval '1 day' WHERE id = $1`, fAsset)

	var before time.Time
	_ = e.pool.QueryRow(ctx, `SELECT updated_at FROM meetings WHERE id = $1`, expired.ID).Scan(&before)
	n, err := e.svc.RetentionSweep(ctx)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if e.count(t, `SELECT count(*) FROM audio_assets WHERE id = $1 AND deleted_reason = 'expired' AND bytes_purged_at IS NOT NULL`, xAsset) != 1 {
		t.Fatal("expired asset not marked + purged")
	}
	if _, ok, _ := e.store.Stat(ctx, xKey); ok {
		t.Fatal("expired bytes kept")
	}
	var after time.Time
	_ = e.pool.QueryRow(ctx, `SELECT updated_at FROM meetings WHERE id = $1`, expired.ID).Scan(&after)
	if !after.After(before) {
		t.Fatal("updated_at not bumped")
	}
	for _, k := range []string{uKey, bKey, fKey} {
		if _, ok, _ := e.store.Stat(ctx, k); !ok {
			t.Fatal("live bytes deleted", k)
		}
	}
	if e.count(t, `SELECT count(*) FROM audio_assets WHERE deleted_at IS NOT NULL`) != 1 {
		t.Fatal("untranscribed, running or unexpired asset swept")
	}
	got, _ := e.svc.Get(ctx, a.ID, expired.ID)
	if got.Audio.Available || *got.Audio.DeletedReason != "expired" || got.Transcript == nil {
		t.Fatalf("%+v", got.Audio)
	}
	if n, err := e.svc.RetentionSweep(ctx); err != nil || n != 0 {
		t.Fatal("rerun", n, err)
	}
}

func TestBytePurgeSweepAfterCrash(t *testing.T) {
	e := newEnv(t)
	a := testutil.CreateUser(t, e.pool, 0)
	m := e.create(t, a.ID, CreateInput{})
	assetID, key := e.audio(t, a.ID, m.ID, 2000)

	crashed := New(e.pool, testutil.Config(t), failingStore{e.store}, testutil.DiscardLogger())
	if err := crashed.DeleteAudio(ctx, a.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.store.Stat(ctx, key); !ok {
		t.Fatal("setup: bytes should survive the failed delete")
	}
	if n, err := e.svc.BytePurgeSweep(ctx); err != nil || n != 0 {
		t.Fatal("must wait 1 minute after deleted_at", n, err)
	}
	e.exec(t, `UPDATE audio_assets SET deleted_at = now() - interval '2 minutes' WHERE id = $1`, assetID)
	if n, err := e.svc.BytePurgeSweep(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, ok, _ := e.store.Stat(ctx, key); ok {
		t.Fatal("bytes kept")
	}
	if e.count(t, `SELECT count(*) FROM audio_assets WHERE id = $1 AND bytes_purged_at IS NOT NULL`, assetID) != 1 {
		t.Fatal("bytes_purged_at not set")
	}
	if n, err := e.svc.BytePurgeSweep(ctx); err != nil || n != 0 {
		t.Fatal("rerun", n, err)
	}
}

type failingStore struct{ *storage.LocalFS }

func (failingStore) Delete(context.Context, string) error { return errors.New("disk gone") }

func TestOrphanSweep(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewLocalFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	pool := testutil.DB(t)
	e := &env{pool: pool, store: store, svc: New(pool, testutil.Config(t), store, testutil.DiscardLogger())}
	a := testutil.CreateUser(t, pool, 0)
	m := e.create(t, a.ID, CreateInput{})
	prefix := "u/" + a.ID + "/m/" + m.ID + "/"

	_, liveAudio := e.audio(t, a.ID, m.ID, 2000)
	openSession := e.openUpload(t, a.ID, m.ID)
	liveTemp := prefix + "uploads/" + openSession + ".part"
	exportID := ids.New()
	liveExport := prefix + "exports/" + exportID + ".md"
	e.exec(t, `INSERT INTO documents (meeting_id, kind, current_revision_number) VALUES ($1, 'manual_notes', 1)`, m.ID)
	var revID string
	if err := pool.QueryRow(ctx, `INSERT INTO document_revisions (document_id, revision_number, origin, schema_version, content, mutation_id, mutation_fingerprint)
		SELECT id, 1, 'user_edit', 1, '{}', gen_random_uuid(), repeat('a', 64) FROM documents WHERE meeting_id = $1 RETURNING id`, m.ID).Scan(&revID); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO exports (id, meeting_id, document_revision_id, format, is_draft, filename, content_type, size_bytes, sha256, storage_key)
		VALUES ($1, $2, $3, 'md', true, 'x.md', 'text/markdown; charset=utf-8', 1, repeat('a', 64), $4)`, exportID, m.ID, revID, liveExport)

	orphans := []string{
		prefix + "exports/" + ids.New() + ".pdf",
		prefix + "uploads/" + ids.New() + ".part",
		prefix + "audio/" + ids.New() + ".m4a",
		prefix + "exports/.tmp-0123456789abcdef",
	}
	fresh := prefix + "exports/" + ids.New() + ".docx"
	for _, k := range append([]string{liveTemp, liveExport, fresh}, orphans...) {
		if _, err := store.Put(ctx, k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-OrphanGrace - time.Hour)
	for _, k := range append([]string{liveAudio, liveTemp, liveExport}, orphans...) {
		if err := os.Chtimes(filepath.Join(dir, filepath.FromSlash(k)), old, old); err != nil {
			t.Fatal(err)
		}
	}

	n, err := e.svc.OrphanSweep(ctx)
	if err != nil || n != len(orphans) {
		t.Fatal(n, err)
	}
	for _, k := range orphans {
		if _, ok, _ := store.Stat(ctx, k); ok {
			t.Error("orphan kept", k)
		}
	}
	for _, k := range []string{liveAudio, liveTemp, liveExport, fresh} {
		if _, ok, _ := store.Stat(ctx, k); !ok {
			t.Error("live or fresh object deleted", k)
		}
	}

	e.exec(t, `UPDATE upload_sessions SET status = 'canceled', writer_lease_id = NULL, writer_lease_until = NULL WHERE id = $1`, openSession)
	if n, err := e.svc.OrphanSweep(ctx); err != nil || n != 1 {
		t.Fatal("canceled session temp", n, err)
	}
	if _, ok, _ := store.Stat(ctx, liveTemp); ok {
		t.Fatal("canceled session temp kept")
	}
}
