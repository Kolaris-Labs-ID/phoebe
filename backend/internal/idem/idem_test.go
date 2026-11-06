package idem

import "testing"

func TestCanonicalJSON(t *testing.T) {
	a, err := CanonicalJSON([]byte(` { "b": 1.50, "a": {"z": null, "y": "<x>"} } `))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != `{"a":{"y":"<x>","z":null},"b":1.50}` {
		t.Fatalf("got %s", a)
	}
	e, _ := CanonicalJSON(nil)
	if string(e) != "{}" {
		t.Fatalf("empty body: %s", e)
	}
	if _, err := CanonicalJSON([]byte("{")); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestFingerprints(t *testing.T) {
	route := "POST /v1/meetings/{id}/transcription"
	f1, _ := RequestFingerprint(route, "m1", []byte(`{"language":"auto"}`))
	f2, _ := RequestFingerprint(route, "m1", []byte("{ \"language\" : \"auto\" }\n"))
	f3, _ := RequestFingerprint(route, "m2", []byte(`{"language":"auto"}`))
	f4, _ := RequestFingerprint(route, "m1", []byte(`{"language":"id"}`))
	if f1 != f2 || f1 == f3 || f1 == f4 || len(f1) != 64 {
		t.Fatal("request fingerprint semantics")
	}
	m1, _ := MutationFingerprint([]byte(`{"x":1,"y":2}`))
	m2, _ := MutationFingerprint([]byte(`{"y":2,"x":1}`))
	if m1 != m2 {
		t.Fatal("mutation fingerprint not key-order independent")
	}
}
