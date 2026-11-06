package events_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/events"
	"phoebe/internal/httpapi"
	"phoebe/internal/ids"
	"phoebe/internal/testutil"
)

var ctx = context.Background()

const at = "2026-11-06T09:00:00.123456+07:00"

func ev(name string, meetingID *string) events.ClientEvent {
	return events.ClientEvent{EventID: ids.New(), Name: name, OccurredAt: at, MeetingID: meetingID}
}

func count(t *testing.T, q db.Querier, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIngestDedupe(t *testing.T) {
	pool := testutil.DB(t)
	s := events.New(pool)
	u := testutil.CreateUser(t, pool, 0)

	a := ev(events.RecordingStarted, nil)
	b := ev(events.ShareSheetOpened, nil)
	upper := a
	upper.EventID = strings.ToUpper(a.EventID)
	n, err := s.Ingest(ctx, u.ID, []events.ClientEvent{a, b, a, upper})
	if err != nil || n != 2 {
		t.Fatalf("first batch accepted %d, err %v", n, err)
	}
	c := ev(events.QuotaBlocked, nil)
	n, err = s.Ingest(ctx, u.ID, []events.ClientEvent{a, b, c})
	if err != nil || n != 1 {
		t.Fatalf("resent batch accepted %d, err %v", n, err)
	}
	if got := count(t, pool, `SELECT count(*) FROM events WHERE user_id = $1 AND source = 'client'`, u.ID); got != 3 {
		t.Fatalf("rows %d", got)
	}
	var occurred string
	if err := pool.QueryRow(ctx, `SELECT to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US') FROM events WHERE event_id = $1`, a.EventID).Scan(&occurred); err != nil {
		t.Fatal(err)
	}
	if occurred != "2026-11-06T02:00:00.123000" {
		t.Fatalf("occurred_at %s, want UTC ms-truncated", occurred)
	}

	// Dedupe is per user: another user may use the same event_id.
	u2 := testutil.CreateUser(t, pool, 0)
	if n, err := s.Ingest(ctx, u2.ID, []events.ClientEvent{a}); err != nil || n != 1 {
		t.Fatalf("other user accepted %d, err %v", n, err)
	}
}

func TestIngestMeetingOwnership(t *testing.T) {
	pool := testutil.DB(t)
	s := events.New(pool)
	a := testutil.CreateUser(t, pool, 0)
	b := testutil.CreateUser(t, pool, 0)
	own := testutil.CreateMeeting(t, pool, a.ID)
	foreign := testutil.CreateMeeting(t, pool, b.ID)
	gone := testutil.CreateMeeting(t, pool, a.ID)
	if _, err := pool.Exec(ctx, `UPDATE meetings SET deleted_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	unknown := ids.New()
	e1, e2, e3, e4 := ev(events.RecordingSaved, &own), ev(events.RecordingSaved, &foreign), ev(events.RecordingSaved, &gone), ev(events.RecordingSaved, &unknown)
	if n, err := s.Ingest(ctx, a.ID, []events.ClientEvent{e1, e2, e3, e4}); err != nil || n != 4 {
		t.Fatalf("accepted %d, err %v", n, err)
	}
	var mid *string
	if err := pool.QueryRow(ctx, `SELECT meeting_id::text FROM events WHERE event_id = $1`, e1.EventID).Scan(&mid); err != nil || mid == nil || *mid != own {
		t.Fatalf("own meeting_id %v %v", mid, err)
	}
	if got := count(t, pool, `SELECT count(*) FROM events WHERE user_id = $1 AND meeting_id IS NULL`, a.ID); got != 3 {
		t.Fatalf("foreign/tombstoned/unknown meeting ids must be stored as null, got %d nulls", got)
	}
}

func TestIngestValidation(t *testing.T) {
	pool := testutil.DB(t)
	s := events.New(pool)
	u := testutil.CreateUser(t, pool, 0)
	bad := "nope"
	many := make([]events.ClientEvent, events.MaxBatch+1)
	for i := range many {
		many[i] = ev(events.RecordingStarted, nil)
	}
	for _, tc := range []struct {
		name  string
		batch []events.ClientEvent
		field string
	}{
		{"empty", nil, "events"},
		{"too many", many, "events"},
		{"server name upload_verified", []events.ClientEvent{ev(events.RecordingStarted, nil), ev(events.UploadVerified, nil)}, "events[1].name"},
		{"server name transcription_completed", []events.ClientEvent{ev(events.TranscriptionCompleted, nil)}, "events[0].name"},
		{"server name notes_ready", []events.ClientEvent{ev(events.NotesReady, nil)}, "events[0].name"},
		{"server name mom_generated", []events.ClientEvent{ev(events.MoMGenerated, nil)}, "events[0].name"},
		{"unknown name", []events.ClientEvent{ev("meeting_title_changed", nil)}, "events[0].name"},
		{"bad event_id", []events.ClientEvent{{EventID: "x", Name: events.RecordingStarted, OccurredAt: at}}, "events[0].event_id"},
		{"missing occurred_at", []events.ClientEvent{{EventID: ids.New(), Name: events.RecordingStarted}}, "events[0].occurred_at"},
		{"bad occurred_at", []events.ClientEvent{{EventID: ids.New(), Name: events.RecordingStarted, OccurredAt: "yesterday"}}, "events[0].occurred_at"},
		{"bad meeting_id", []events.ClientEvent{ev(events.RecordingStarted, &bad)}, "events[0].meeting_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Ingest(ctx, u.ID, tc.batch)
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Code != apperr.ValidationFailed {
				t.Fatalf("want validation_failed, got %v", err)
			}
			fields := ae.Details["fields"].([]apperr.FieldError)
			if len(fields) == 0 || fields[0].Field != tc.field {
				t.Fatalf("fields %+v, want %s", fields, tc.field)
			}
		})
	}
	if got := count(t, pool, `SELECT count(*) FROM events`); got != 0 {
		t.Fatalf("rejected batches must store nothing, got %d rows", got)
	}
}

func TestEmitOnceUnderRerun(t *testing.T) {
	pool := testutil.DB(t)
	u := testutil.CreateUser(t, pool, 0)
	m := testutil.CreateMeeting(t, pool, u.ID)
	revID := ids.New()
	for range 2 {
		if err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
			return events.Emit(ctx, tx, u.ID, revID, events.TranscriptionCompleted, m)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(t, pool, `SELECT count(*) FROM events WHERE user_id = $1 AND event_id = $2 AND source = 'server' AND name = 'transcription_completed' AND meeting_id = $3`, u.ID, revID, m); got != 1 {
		t.Fatalf("rows %d", got)
	}
	// Rolled-back finalize leaves no event.
	other := ids.New()
	_ = db.InTx(ctx, pool, func(tx pgx.Tx) error {
		if err := events.Emit(ctx, tx, u.ID, other, events.NotesReady, m); err != nil {
			return err
		}
		return errors.New("lease lost")
	})
	if got := count(t, pool, `SELECT count(*) FROM events WHERE event_id = $1`, other); got != 0 {
		t.Fatal("event must roll back with its tx")
	}
	if err := events.Emit(ctx, pool, u.ID, ids.New(), events.RecordingStarted, ""); err == nil {
		t.Fatal("client names must not be emitted by the server")
	}
}

type fakeSessions map[string]string

func (f fakeSessions) ResolveSession(_ context.Context, token string) (string, string, error) {
	if id, ok := f[token]; ok {
		return id, ids.New(), nil
	}
	return "", "", apperr.New(apperr.Unauthenticated)
}

func TestPostEventsHTTP(t *testing.T) {
	pool := testutil.DB(t)
	u := testutil.CreateUser(t, pool, 0)
	srv := httptest.NewServer(httpapi.New(httpapi.Deps{Config: testutil.Config(t), Log: testutil.DiscardLogger(), DB: pool,
		Sessions: fakeSessions{u.Token: u.ID}, Events: events.New(pool)}))
	defer srv.Close()

	post := func(body string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/events", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+u.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	id := ids.New()
	batch := fmt.Sprintf(`{"events":[{"event_id":%q,"name":"recording_started","occurred_at":"2026-11-06T02:00:00.000Z","meeting_id":null},
		{"event_id":%q,"name":"recording_started","occurred_at":"2026-11-06T02:00:00.000Z","meeting_id":null}]}`, id, id)
	if code, body := post(batch); code != http.StatusAccepted || body["accepted"] != 1.0 {
		t.Fatalf("first: %d %v", code, body)
	}
	if code, body := post(batch); code != http.StatusAccepted || body["accepted"] != 0.0 {
		t.Fatalf("replay: %d %v", code, body)
	}
	code, body := post(fmt.Sprintf(`{"events":[{"event_id":%q,"name":"notes_ready","occurred_at":"2026-11-06T02:00:00.000Z","meeting_id":null}]}`, ids.New()))
	if code != http.StatusUnprocessableEntity || body["error"].(map[string]any)["code"] != "validation_failed" {
		t.Fatalf("server-only name: %d %v", code, body)
	}
	if code, body := post(`{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing events: %d %v", code, body)
	}
	if code, body := post(`{"events":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("wrong type: %d %v", code, body)
	}
}
