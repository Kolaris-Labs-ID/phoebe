package users

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"phoebe/internal/apperr"
	"phoebe/internal/jsonx"
	"phoebe/internal/testutil"
)

func patch(t *testing.T, body string) Patch {
	t.Helper()
	var p Patch
	if err := jsonx.Decode([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func fieldReasons(err error) map[string]string {
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.ValidationFailed {
		return nil
	}
	out := map[string]string{}
	for _, f := range ae.Details["fields"].([]apperr.FieldError) {
		out[f.Field] = f.Reason
	}
	return out
}

func TestGetAndUpdate(t *testing.T) {
	pool := testutil.DB(t)
	s := New(pool, testutil.Config(t), nil, nil, testutil.DiscardLogger())
	ctx := context.Background()
	u := testutil.CreateUser(t, pool, 0)

	got, err := s.Get(ctx, u.ID)
	if err != nil || got.ID != u.ID || got.Language != "id" || got.Timezone != "Asia/Jakarta" || got.DisplayName != "" {
		t.Fatalf("%+v %v", got, err)
	}
	b, _ := json.Marshal(got)
	for _, k := range []string{`"id"`, `"display_name"`, `"language"`, `"timezone"`, `"created_at"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing %s in %s", k, b)
		}
	}

	got, err = s.Update(ctx, u.ID, patch(t, `{"display_name":" Ari ","language":"en","timezone":"Asia/Makassar"}`))
	if err != nil || got.DisplayName != "Ari" || got.Language != "en" || got.Timezone != "Asia/Makassar" {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = s.Update(ctx, u.ID, patch(t, `{"language":"id"}`))
	if err != nil || got.DisplayName != "Ari" || got.Language != "id" || got.Timezone != "Asia/Makassar" {
		t.Fatalf("partial patch: %+v %v", got, err)
	}
	got, err = s.Update(ctx, u.ID, patch(t, `{}`))
	if err != nil || got.Language != "id" {
		t.Fatalf("empty patch: %+v %v", got, err)
	}
	got, err = s.Update(ctx, u.ID, patch(t, `{"display_name":""}`))
	if err != nil || got.DisplayName != "" {
		t.Fatalf("clear name: %+v %v", got, err)
	}

	_, err = s.Update(ctx, u.ID, patch(t, `{"display_name":null,"language":"fr","timezone":"Mars/Olympus"}`))
	want := map[string]string{"display_name": "invalid", "language": "invalid", "timezone": "invalid"}
	if r := fieldReasons(err); len(r) != 3 || r["display_name"] != want["display_name"] || r["language"] != "invalid" || r["timezone"] != "invalid" {
		t.Fatalf("got %v", err)
	}
	for _, tz := range []string{"Local", "", "../etc/passwd", strings.Repeat("A", 65)} {
		b, _ := json.Marshal(map[string]string{"timezone": tz})
		if r := fieldReasons(func() error { _, err := s.Update(ctx, u.ID, patch(t, string(b))); return err }()); r["timezone"] != "invalid" {
			t.Errorf("timezone %q accepted", tz)
		}
	}
	b, _ = json.Marshal(map[string]string{"display_name": strings.Repeat("é", 101)})
	if _, err := s.Update(ctx, u.ID, patch(t, string(b))); fieldReasons(err)["display_name"] != "too_long" {
		t.Fatalf("long name: %v", err)
	}
	b, _ = json.Marshal(map[string]string{"display_name": strings.Repeat("é", 100)})
	if _, err := s.Update(ctx, u.ID, patch(t, string(b))); err != nil {
		t.Fatalf("100 chars: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE users SET deleted_at=now() WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, u.ID); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("tombstoned get: %v", err)
	}
	if _, err := s.Update(ctx, u.ID, patch(t, `{"language":"en"}`)); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("tombstoned update: %v", err)
	}
}
