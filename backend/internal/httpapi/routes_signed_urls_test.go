package httpapi

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"phoebe/internal/auth"
	"phoebe/internal/export"
	"phoebe/internal/storage"
	"phoebe/internal/testutil"
)

func TestSignedURLTTLAndOwnership(t *testing.T) {
	pool := testutil.DB(t)
	cfg := testutil.Config(t)
	cfg.SignedURLTTL = 7 * time.Minute
	log := testutil.DiscardLogger()
	store := testutil.Storage(t)
	signer := storage.NewSigner(cfg.SigningSecret, cfg.PublicBaseURL, cfg.SignedURLTTL)
	a := auth.NewService(pool, cfg, log, nil)
	srv := httptest.NewServer(New(Deps{Config: cfg, Log: log, DB: pool, Storage: store, Signer: signer, Sessions: a, Auth: a,
		Exports: export.NewService(pool, store, signer, log), Files: export.NewFiles(pool, store, signer)}))
	t.Cleanup(srv.Close)

	owner := testutil.CreateUser(t, pool, 0)
	stranger := testutil.CreateUser(t, pool, 0)
	mid := testutil.CreateMeeting(t, pool, owner.ID)
	rev := insertRevision(t, pool, mid)
	insertAudioAsset(t, pool, store, owner.ID, mid, testutil.WAV(2000))

	checkTTL := func(what, rawURL, expiresAt string) {
		t.Helper()
		exp, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			t.Fatalf("%s expires_at %q: %v", what, expiresAt, err)
		}
		if d := time.Until(exp); d < cfg.SignedURLTTL-5*time.Second || d > cfg.SignedURLTTL+time.Second {
			t.Fatalf("%s ttl %v, want %v", what, d, cfg.SignedURLTTL)
		}
		u, _ := url.Parse(rawURL)
		if q, _ := strconv.ParseInt(u.Query().Get("exp"), 10, 64); q != exp.Unix() {
			t.Fatalf("%s exp param %d vs expires_at %v", what, q, exp)
		}
	}

	st, body := call(t, srv, "POST", "/v1/exports", owner.Token, `{"document_revision_id":"`+rev+`","format":"md"}`)
	if st != 201 {
		t.Fatal(st, body)
	}
	checkTTL("export create", body["download_url"].(string), body["download_expires_at"].(string))
	eid := body["export"].(map[string]any)["id"].(string)
	st, body = call(t, srv, "GET", "/v1/exports/"+eid, owner.Token, "")
	if st != 200 {
		t.Fatal(st, body)
	}
	checkTTL("export get", body["download_url"].(string), body["download_expires_at"].(string))
	st, body = call(t, srv, "GET", "/v1/meetings/"+mid+"/audio/url", owner.Token, "")
	if st != 200 {
		t.Fatal(st, body)
	}
	checkTTL("audio", body["url"].(string), body["expires_at"].(string))

	for _, p := range []string{"/v1/exports/" + eid, "/v1/meetings/" + mid + "/audio/url"} {
		st, body := call(t, srv, "GET", p, stranger.Token, "")
		if st != 404 || errCode(body) != "not_found" || body["url"] != nil || body["download_url"] != nil {
			t.Fatalf("%s as stranger: %d %v", p, st, body)
		}
	}
}
