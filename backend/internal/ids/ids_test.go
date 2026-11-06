package ids

import "testing"

func TestNewIsV4AndParses(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := New()
		if p, ok := Parse(id); !ok || p != id {
			t.Fatalf("New not canonical: %s", id)
		}
		if id[14] != '4' || !(id[19] == '8' || id[19] == '9' || id[19] == 'a' || id[19] == 'b') {
			t.Fatalf("not v4: %s", id)
		}
		if seen[id] {
			t.Fatal("duplicate")
		}
		seen[id] = true
	}
}

func TestParse(t *testing.T) {
	if p, ok := Parse("8F0C1A2B-3C4D-4E5F-8A9B-0C1D2E3F4A5B"); !ok || p != "8f0c1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b" {
		t.Fatal("uppercase not normalized")
	}
	for _, bad := range []string{"", "x", "8f0c1a2b3c4d4e5f8a9b0c1d2e3f4a5b", "8f0c1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5g", "{8f0c1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5}"} {
		if Valid(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
