package documents

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"phoebe/internal/apperr"
)

const momJSON = `{
  "schema_version": 1, "language": "id", "extra": "ignored",
  "meeting_info": {
    "title": "  Weekly sync ", "started_at": "2026-11-06T09:00:00+07:00", "timezone": "Asia/Jakarta",
    "participants": ["Budi", " Sari "],
    "agenda": [{"id": "it_1", "text": "Budget", "origin": "ai", "edited": false, "sources": []}]
  },
  "sections": [
    {"id": "summary", "type": "summary", "heading": "Ringkasan", "style": "paragraph",
     "items": [{"id": "it_2", "text": "Ringkas.", "origin": "ai", "edited": false,
                "sources": [{"type": "transcript", "segment_id": "s1", "start_ms": 999, "end_ms": 999}]}]},
    {"id": "discussion", "type": "discussion", "heading": "Pembahasan", "style": "bullet",
     "items": [{"id": "it_3", "title": "Rilis", "origin": "ai", "edited": true, "sources": [],
                "points": [{"id": "it_4", "text": "Jumat", "origin": "human", "edited": false, "sources": []}]}]},
    {"id": "decisions", "type": "decisions", "heading": "Keputusan", "style": "numbered",
     "items": [{"id": "it_5", "text": "Rilis Jumat.", "origin": "ai", "edited": false,
                "sources": [{"type": "transcript", "segment_id": "s2"}]}]},
    {"id": "action_items", "type": "action_items", "heading": "Action items", "style": null,
     "items": [{"id": "it_6", "description": "Siapkan laporan", "pic": "Budi", "deadline": "2026-11-13",
                "deadline_phrase": "minggu depan", "origin": "ai", "edited": false,
                "sources": [{"type": "transcript", "segment_id": "s2"}, {"type": "user_notes", "note_id": "n1"}]},
               {"id": "h_x", "description": "Kirim notulen", "pic": null, "deadline": null, "deadline_phrase": null,
                "origin": "human", "edited": false, "sources": []}]},
    {"id": "h_1", "type": "custom", "heading": "Lain", "style": "bullet", "items": []}
  ]
}`

var testPins = Pins{
	Segments: map[string]Span{"s1": {StartMS: 0, EndMS: 4200}, "s2": {StartMS: 61000, EndMS: 66000}},
	NoteIDs:  map[string]bool{"n1": true},
}

func mutate(t *testing.T, src string, fn func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(src), &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func get(m map[string]any, path ...any) map[string]any {
	var cur any = m
	for _, p := range path {
		switch p := p.(type) {
		case string:
			cur = cur.(map[string]any)[p]
		case int:
			cur = cur.([]any)[p]
		}
	}
	return cur.(map[string]any)
}

func TestValidateMoMNormalizes(t *testing.T) {
	c, err := ValidateContent(KindMoM, []byte(momJSON), testPins)
	if err != nil {
		t.Fatal(err)
	}
	m := c.(*MoM)
	if m.MeetingInfo.Title != "Weekly sync" || m.MeetingInfo.Participants[1] != "Sari" {
		t.Fatalf("not trimmed: %+v", m.MeetingInfo)
	}
	if got := m.MeetingInfo.StartedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-11-06T02:00:00Z" {
		t.Fatalf("started_at %s", got)
	}
	ref := m.Sections[0].Items[0].Sources[0]
	if ref.StartMS != 0 || ref.EndMS != 4200 {
		t.Fatalf("ref times not overwritten from pins: %+v", ref)
	}
	if m.Sections[2].Items[0].Sources[0].StartMS != 61000 {
		t.Fatal("missing start_ms not filled from pins")
	}
	if len(m.Sections[1].Topics[0].Points) != 1 || len(m.Sections[3].Actions) != 2 {
		t.Fatal("typed items lost")
	}

	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`"style":null`, `"pic":null`, `"deadline_phrase":null`, `"items":[]`,
		`{"type":"transcript","segment_id":"s1","start_ms":0,"end_ms":4200}`,
		`{"type":"user_notes","note_id":"n1"}`, `"started_at":"2026-11-06T02:00:00.000Z"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled content missing %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "extra") {
		t.Error("unknown field kept")
	}

	back, err := DecodeContent(KindMoM, out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, c) {
		t.Fatalf("decode round trip differs:\n%+v\n%+v", back, c)
	}
	again, err := ValidateContent(KindMoM, out, testPins)
	if err != nil || !reflect.DeepEqual(again, c) {
		t.Fatalf("revalidate stored content: %v", err)
	}
}

func TestValidateMoMErrors(t *testing.T) {
	long := strings.Repeat("a", 201)
	cases := []struct {
		name, path, reason string
		pins               *Pins
		fn                 func(m map[string]any)
	}{
		{"schema version", "/schema_version", apperr.ReasonInvalid, nil, func(m map[string]any) { m["schema_version"] = 2 }},
		{"language", "/language", apperr.ReasonInvalid, nil, func(m map[string]any) { m["language"] = "fr" }},
		{"missing started_at", "/meeting_info/started_at", apperr.ReasonRequired, nil, func(m map[string]any) {
			delete(get(m, "meeting_info"), "started_at")
		}},
		{"bad started_at", "/meeting_info/started_at", apperr.ReasonInvalid, nil, func(m map[string]any) {
			get(m, "meeting_info")["started_at"] = "yesterday"
		}},
		{"bad timezone", "/meeting_info/timezone", apperr.ReasonInvalid, nil, func(m map[string]any) {
			get(m, "meeting_info")["timezone"] = "Mars/Base"
		}},
		{"empty participant", "/meeting_info/participants/0", apperr.ReasonTooShort, nil, func(m map[string]any) {
			get(m, "meeting_info")["participants"] = []any{"  "}
		}},
		{"empty text", "/sections/0/items/0/text", apperr.ReasonTooShort, nil, func(m map[string]any) {
			get(m, "sections", 0, "items", 0)["text"] = "   "
		}},
		{"heading too long", "/sections/0/heading", apperr.ReasonTooLong, nil, func(m map[string]any) {
			get(m, "sections", 0)["heading"] = long
		}},
		{"duplicate id", "/sections/2/items/0/id", apperr.ReasonDuplicate, nil, func(m map[string]any) {
			get(m, "sections", 2, "items", 0)["id"] = "it_2"
		}},
		{"bad id", "/sections/0/items/0/id", apperr.ReasonInvalid, nil, func(m map[string]any) {
			get(m, "sections", 0, "items", 0)["id"] = "a b"
		}},
		{"duplicate type", "/sections/4/type", apperr.ReasonDuplicate, nil, func(m map[string]any) {
			get(m, "sections", 4)["type"] = "summary"
		}},
		{"action style", "/sections/3/style", apperr.ReasonInvalid, nil, func(m map[string]any) {
			get(m, "sections", 3)["style"] = "bullet"
		}},
		{"missing action style", "/sections/3/style", apperr.ReasonRequired, nil, func(m map[string]any) {
			delete(get(m, "sections", 3), "style")
		}},
		{"missing pic", "/sections/3/items/1/pic", apperr.ReasonRequired, nil, func(m map[string]any) {
			delete(get(m, "sections", 3, "items", 1), "pic")
		}},
		{"empty pic", "/sections/3/items/1/pic", apperr.ReasonTooShort, nil, func(m map[string]any) {
			get(m, "sections", 3, "items", 1)["pic"] = ""
		}},
		{"bad deadline", "/sections/3/items/0/deadline", apperr.ReasonInvalid, nil, func(m map[string]any) {
			get(m, "sections", 3, "items", 0)["deadline"] = "2026-13-01"
		}},
		{"edited type", "/sections/0/items/0/edited", apperr.ReasonInvalid, nil, func(m map[string]any) {
			get(m, "sections", 0, "items", 0)["edited"] = "no"
		}},
		{"unknown segment", "/sections/0/items/0/sources/0/segment_id", apperr.ReasonNotFound, nil, func(m map[string]any) {
			get(m, "sections", 0, "items", 0, "sources", 0)["segment_id"] = "s99"
		}},
		{"no pinned transcript", "/sections/0/items/0/sources/0", apperr.ReasonNotFound, &Pins{NoteIDs: testPins.NoteIDs}, func(map[string]any) {}},
		{"unknown note", "/sections/3/items/0/sources/1/note_id", apperr.ReasonNotFound, nil, func(m map[string]any) {
			get(m, "sections", 3, "items", 0, "sources", 1)["note_id"] = "n9"
		}},
		{"no pinned notes", "/sections/3/items/0/sources/1", apperr.ReasonNotFound, &Pins{Segments: testPins.Segments}, func(map[string]any) {}},
		{"ref type", "/sections/0/items/0/sources/0/type", apperr.ReasonInvalid, nil, func(m map[string]any) {
			get(m, "sections", 0, "items", 0, "sources", 0)["type"] = "url"
		}},
		{"ai decision without source", "/sections/2/items/0/sources", apperr.ReasonRequired, nil, func(m map[string]any) {
			get(m, "sections", 2, "items", 0)["sources"] = []any{}
		}},
		{"ai action without source", "/sections/3/items/0/sources", apperr.ReasonRequired, nil, func(m map[string]any) {
			get(m, "sections", 3, "items", 0)["sources"] = []any{}
		}},
		{"too many sources", "/sections/0/items/0/sources", apperr.ReasonTooLong, nil, func(m map[string]any) {
			refs := make([]any, MaxSources+1)
			for i := range refs {
				refs[i] = map[string]any{"type": "transcript", "segment_id": "s1"}
			}
			get(m, "sections", 0, "items", 0)["sources"] = refs
		}},
		{"too many sections", "/sections", apperr.ReasonTooLong, nil, func(m map[string]any) {
			secs := make([]any, MaxSections+1)
			for i := range secs {
				secs[i] = map[string]any{}
			}
			m["sections"] = secs
		}},
		{"too large", "", apperr.ReasonTooLong, nil, func(m map[string]any) {
			m["pad"] = strings.Repeat("x", MaxContentBytes)
		}},
		{"not an object", "/meeting_info", apperr.ReasonInvalid, nil, func(m map[string]any) { m["meeting_info"] = []any{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pins := testPins
			if tc.pins != nil {
				pins = *tc.pins
			}
			_, err := ValidateContent(KindMoM, mutate(t, momJSON, tc.fn), pins)
			assertContentErr(t, err, tc.path, tc.reason)
		})
	}
}

func TestValidateMoMAllowsHumanItemsWithoutSourcesAndEmptyTitle(t *testing.T) {
	b := mutate(t, momJSON, func(m map[string]any) {
		get(m, "meeting_info")["title"] = ""
		get(m, "meeting_info")["started_at"] = nil
		d := get(m, "sections", 2, "items", 0)
		d["origin"], d["sources"] = "human", []any{}
	})
	c, err := ValidateContent(KindMoM, b, Pins{})
	if err == nil {
		t.Fatal("refs against no pins must fail")
	}
	c, err = ValidateContent(KindMoM, b, testPins)
	if err != nil {
		t.Fatal(err)
	}
	if c.(*MoM).MeetingInfo.StartedAt != nil {
		t.Fatal("null started_at")
	}
}

func TestValidateManualNotes(t *testing.T) {
	c, err := ValidateContent(KindManualNotes, []byte(`{"schema_version":1,"items":[{"id":"n1","text":" Bahas budget ","at_ms":125000},{"id":"n2","text":"x","at_ms":null}]}`), Pins{})
	if err != nil {
		t.Fatal(err)
	}
	mn := c.(*ManualNotes)
	if mn.Items[0].Text != "Bahas budget" || *mn.Items[0].AtMS != 125000 || mn.Items[1].AtMS != nil {
		t.Fatalf("%+v", mn)
	}
	cases := []struct{ name, body, path, reason string }{
		{"missing at_ms", `{"schema_version":1,"items":[{"id":"n1","text":"a"}]}`, "/items/0/at_ms", apperr.ReasonRequired},
		{"negative at_ms", `{"schema_version":1,"items":[{"id":"n1","text":"a","at_ms":-1}]}`, "/items/0/at_ms", apperr.ReasonOutOfRange},
		{"fractional at_ms", `{"schema_version":1,"items":[{"id":"n1","text":"a","at_ms":1.5}]}`, "/items/0/at_ms", apperr.ReasonInvalid},
		{"too long", `{"schema_version":1,"items":[{"id":"n1","text":"` + strings.Repeat("é", MaxManualNoteChars+1) + `","at_ms":null}]}`, "/items/0/text", apperr.ReasonTooLong},
		{"duplicate", `{"schema_version":1,"items":[{"id":"n1","text":"a","at_ms":null},{"id":"n1","text":"b","at_ms":null}]}`, "/items/1/id", apperr.ReasonDuplicate},
		{"missing items", `{"schema_version":1}`, "/items", apperr.ReasonRequired},
		{"missing version", `{"items":[]}`, "/schema_version", apperr.ReasonRequired},
		{"not json", `{`, "", apperr.ReasonInvalid},
		{"trailing", `{"schema_version":1,"items":[]} {}`, "", apperr.ReasonInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateContent(KindManualNotes, []byte(tc.body), Pins{})
			assertContentErr(t, err, tc.path, tc.reason)
		})
	}
}

func TestValidateAINotes(t *testing.T) {
	body := `{"schema_version":1,"language":"en","sections":[{"id":"sec_1","heading":"Budget","style":"bullet","items":[
	  {"id":"it_1","text":"Up 10%.","origin":"ai","edited":false,"sources":[{"type":"transcript","segment_id":"s2","start_ms":1,"end_ms":2}]}]}]}`
	c, err := ValidateContent(KindAINotes, []byte(body), testPins)
	if err != nil {
		t.Fatal(err)
	}
	if r := c.(*AINotes).Sections[0].Items[0].Sources[0]; r.StartMS != 61000 || r.EndMS != 66000 {
		t.Fatalf("%+v", r)
	}
	_, err = ValidateContent(KindAINotes, []byte(strings.Replace(body, `"bullet"`, `"fancy"`, 1)), testPins)
	assertContentErr(t, err, "/sections/0/style", apperr.ReasonInvalid)
	_, err = ValidateContent(KindAINotes, []byte(strings.Replace(body, `"style":"bullet",`, `"style":null,`, 1)), testPins)
	assertContentErr(t, err, "/sections/0/style", apperr.ReasonInvalid)
}

func assertContentErr(t *testing.T, err error, path, reason string) {
	t.Helper()
	e, ok := err.(*apperr.Error)
	if !ok || e.Code != apperr.InvalidContent {
		t.Fatalf("want invalid_content, got %v", err)
	}
	if e.Details["path"] != path || e.Details["reason"] != reason {
		t.Fatalf("want %s %s, got %v", path, reason, e.Details)
	}
}
