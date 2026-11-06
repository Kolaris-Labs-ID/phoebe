package jsonx

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTime(t *testing.T) {
	loc := time.FixedZone("WIB", 7*3600)
	v := NewTime(time.Date(2026, 11, 6, 9, 0, 0, 123456789, loc))
	b, _ := json.Marshal(v)
	if string(b) != `"2026-11-06T02:00:00.123Z"` {
		t.Fatalf("got %s", b)
	}
	z, _ := json.Marshal(NewTime(time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)))
	if string(z) != `"2026-11-06T02:00:00.000Z"` {
		t.Fatalf("fraction not always present: %s", z)
	}
	var in Time
	if err := json.Unmarshal([]byte(`"2026-11-06T09:00:00.5+07:00"`), &in); err != nil {
		t.Fatal(err)
	}
	if !in.Equal(time.Date(2026, 11, 6, 2, 0, 0, 5e8, time.UTC)) || in.Location() != time.UTC {
		t.Fatalf("got %v", in.Time)
	}
	if json.Unmarshal([]byte(`"yesterday"`), &in) == nil {
		t.Fatal("bad time accepted")
	}
	var p struct {
		A *Time `json:"a"`
	}
	b, _ = json.Marshal(p)
	if string(b) != `{"a":null}` || TimePtr(nil) != nil {
		t.Fatal("nullable time")
	}
}

func TestOpt(t *testing.T) {
	var v struct {
		A Opt[string] `json:"a"`
		B Opt[string] `json:"b"`
		C Opt[string] `json:"c"`
	}
	if err := Decode([]byte(`{"a":"x","b":null,"unknown":1}`), &v); err != nil {
		t.Fatal(err)
	}
	if !v.A.Set || v.A.Null || v.A.Value != "x" {
		t.Fatal("A")
	}
	if !v.B.Set || !v.B.Null {
		t.Fatal("B")
	}
	if v.C.Set {
		t.Fatal("C absent but Set")
	}
	var bad struct {
		A Opt[int] `json:"a"`
	}
	if Decode([]byte(`{"a":"str"}`), &bad) == nil {
		t.Fatal("type mismatch accepted")
	}
}

func TestDecodeTrailing(t *testing.T) {
	var v map[string]any
	if Decode([]byte(`{} {}`), &v) == nil || Decode([]byte(`{}]`), &v) == nil {
		t.Fatal("trailing data accepted")
	}
	if Decode([]byte("{}\n "), &v) != nil {
		t.Fatal("trailing whitespace rejected")
	}
}
