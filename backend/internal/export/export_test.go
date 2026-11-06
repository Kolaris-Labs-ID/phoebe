package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"phoebe/internal/documents"
)

var update = flag.Bool("update", false, "rewrite golden files")

var createdAt = time.Date(2026, 11, 6, 3, 4, 5, 0, time.UTC)

var pins = documents.Pins{
	Segments: map[string]documents.Span{
		"seg_zz1": {StartMS: 62000, EndMS: 65000},
		"seg_zz2": {StartMS: 340500, EndMS: 342000},
		"seg_zz3": {StartMS: 3725000, EndMS: 3730000},
	},
	NoteIDs: map[string]bool{"note_qq7": true},
}

const momFixture = `{
  "schema_version": 1, "language": "id",
  "meeting_info": {
    "title": "Weekly Sync: Rilis Q1 / Café", "started_at": "2026-11-06T02:00:00.000Z", "timezone": "Asia/Jakarta",
    "participants": ["Budi", "Sari"],
    "agenda": [
      {"id": "it_a1", "text": "Budget Q1", "origin": "ai", "edited": false, "sources": []},
      {"id": "it_a2", "text": "Rencana rilis", "origin": "human", "edited": false, "sources": []}
    ]
  },
  "sections": [
    {"id": "summary", "type": "summary", "heading": "Ringkasan", "style": "paragraph", "items": [
      {"id": "it_s1", "text": "Tim membahas budget *marketing* dan <rilis>.", "origin": "ai", "edited": false,
       "sources": [{"type": "transcript", "segment_id": "seg_zz1"}]},
      {"id": "it_s2", "text": "# bukan judul\n2. bukan daftar", "origin": "ai", "edited": true, "sources": []}
    ]},
    {"id": "discussion", "type": "discussion", "heading": "Pembahasan", "style": "bullet", "items": [
      {"id": "it_t1", "title": "Rencana rilis", "origin": "ai", "edited": false,
       "sources": [{"type": "transcript", "segment_id": "seg_zz2"}],
       "points": [
         {"id": "it_p1", "text": "QA selesai Kamis", "origin": "ai", "edited": false, "sources": [{"type": "transcript", "segment_id": "seg_zz2"}]},
         {"id": "it_p2", "text": "Butuh 2 tester | tambahan_baru", "origin": "human", "edited": false, "sources": []}
       ]}
    ]},
    {"id": "decisions", "type": "decisions", "heading": "Keputusan", "style": "numbered", "items": [
      {"id": "it_d1", "text": "Budget marketing naik 10% menjadi Rp60 juta.", "origin": "ai", "edited": false,
       "sources": [{"type": "transcript", "segment_id": "seg_zz1"}, {"type": "transcript", "segment_id": "seg_zz3"}]},
      {"id": "it_d2", "text": "Rilis hari Jumat.", "origin": "ai", "edited": false,
       "sources": [{"type": "transcript", "segment_id": "seg_zz2"}, {"type": "user_notes", "note_id": "note_qq7"}]}
    ]},
    {"id": "action_items", "type": "action_items", "heading": "Action items", "style": null, "items": [
      {"id": "it_x1", "description": "Siapkan laporan vendor", "pic": "Budi", "deadline": "2026-11-13", "deadline_phrase": "minggu depan",
       "origin": "ai", "edited": false, "sources": [{"type": "transcript", "segment_id": "seg_zz3"}]},
      {"id": "it_x2", "description": "Kirim notulen\nke semua", "pic": null, "deadline": null, "deadline_phrase": "akhir bulan",
       "origin": "ai", "edited": false, "sources": [{"type": "user_notes", "note_id": "note_qq7"}]},
      {"id": "h_x3", "description": "Cek kontrak", "pic": "Sari", "deadline": null, "deadline_phrase": null,
       "origin": "human", "edited": false, "sources": []}
    ]},
    {"id": "open_issues", "type": "open_issues", "heading": "Hal terbuka", "style": "bullet", "items": []},
    {"id": "follow_ups", "type": "follow_ups", "heading": "Tindak lanjut", "style": "bullet", "items": [
      {"id": "it_f1", "text": "Jadwalkan demo", "origin": "ai", "edited": false, "sources": [{"type": "user_notes", "note_id": "note_qq7"}]}
    ]},
    {"id": "h_c1", "type": "custom", "heading": "Catatan lain", "style": "numbered", "items": [
      {"id": "h_c2", "text": "Satu", "origin": "human", "edited": false, "sources": []},
      {"id": "h_c3", "text": "Dua", "origin": "human", "edited": false, "sources": []}
    ]}
  ]
}`

const emptyMoMFixture = `{
  "schema_version": 1, "language": "en",
  "meeting_info": {"title": "", "started_at": null, "timezone": "Asia/Jakarta", "participants": [], "agenda": []},
  "sections": [
    {"id": "summary", "type": "summary", "heading": "Summary", "style": "paragraph", "items": []},
    {"id": "decisions", "type": "decisions", "heading": "Decisions", "style": "numbered", "items": []},
    {"id": "action_items", "type": "action_items", "heading": "Action items", "style": null, "items": []}
  ]
}`

const aiNotesFixture = `{"schema_version": 1, "language": "en", "sections": [
  {"id": "sec_1", "heading": "Budget", "style": "bullet", "items": [
    {"id": "it_1", "text": "Marketing budget up 10%.", "origin": "ai", "edited": false, "sources": [{"type": "transcript", "segment_id": "seg_zz1"}]},
    {"id": "it_2", "text": "Vendor shortlist", "origin": "ai", "edited": false, "sources": [{"type": "user_notes", "note_id": "note_qq7"}]}
  ]},
  {"id": "sec_2", "heading": "Release", "style": "numbered", "items": [
    {"id": "it_3", "text": "Friday release", "origin": "ai", "edited": false, "sources": [{"type": "transcript", "segment_id": "seg_zz3"}]}
  ]},
  {"id": "sec_3", "heading": "Empty", "style": "bullet", "items": []},
  {"id": "sec_4", "heading": "Notes", "style": "paragraph", "items": [
    {"id": "it_4", "text": "Line one\nline two", "origin": "human", "edited": false, "sources": []}
  ]}
]}`

const manualNotesFixture = `{"schema_version": 1, "items": [
  {"id": "n1", "text": "Bahas budget Q1", "at_ms": 125000},
  {"id": "n2", "text": "- follow up vendor", "at_ms": null}
]}`

var approxPins = documents.Pins{Segments: pins.Segments, NoteIDs: pins.NoteIDs, Approximate: true}

type fixture struct {
	name string
	in   Input
}

func mustContent(t testing.TB, kind documents.Kind, raw string) documents.Content {
	t.Helper()
	return mustContentPins(t, kind, raw, pins)
}

func mustContentPins(t testing.TB, kind documents.Kind, raw string, p documents.Pins) documents.Content {
	t.Helper()
	c, err := documents.ValidateContent(kind, []byte(raw), p)
	if err != nil {
		t.Fatalf("fixture %s invalid: %v", kind, err)
	}
	return c
}

func fixtures(t testing.TB) []fixture {
	started := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)
	notesHeader := Header{Title: "Rapat Vendor", StartedAt: &started, Timezone: "Asia/Jakarta", Participants: []string{"ignored"}}
	return []fixture{
		{"mom", Input{Content: mustContent(t, documents.KindMoM, momFixture), Language: "en", IsDraft: true, CreatedAt: createdAt}},
		{"mom_empty", Input{Content: mustContent(t, documents.KindMoM, emptyMoMFixture), IsDraft: false, CreatedAt: createdAt}},
		{"ai_notes", Input{Content: mustContent(t, documents.KindAINotes, aiNotesFixture), Header: notesHeader, Language: "id", IsDraft: false, CreatedAt: createdAt}},
		{"manual_notes", Input{Content: mustContent(t, documents.KindManualNotes, manualNotesFixture), Header: Header{Timezone: "Asia/Jakarta"}, Language: "id", IsDraft: true, CreatedAt: createdAt}},
		{"mom_approx", Input{Content: mustContentPins(t, documents.KindMoM, momFixture, approxPins), IsDraft: false, CreatedAt: createdAt}},
		{"ai_notes_approx", Input{Content: mustContentPins(t, documents.KindAINotes, aiNotesFixture, approxPins), Header: notesHeader, Language: "id", IsDraft: true, CreatedAt: createdAt}},
	}
}

func render(t *testing.T, in Input, f Format) []byte {
	t.Helper()
	b, err := Render(in, f)
	if err != nil {
		t.Fatalf("render %s: %v", f, err)
	}
	return b
}

func TestGolden(t *testing.T) {
	for _, fx := range fixtures(t) {
		for _, f := range []Format{MD, WhatsApp} {
			t.Run(fx.name+"."+string(f), func(t *testing.T) {
				got := render(t, fx.in, f)
				path := filepath.Join("testdata", fx.name+"."+string(f)+".golden")
				if *update {
					if err := os.MkdirAll("testdata", 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, got, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run with -update)", err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("golden mismatch %s\n--- got\n%s\n--- want\n%s", path, got, want)
				}
			})
		}
	}
}

func TestDeterministic(t *testing.T) {
	for _, fx := range fixtures(t) {
		for _, f := range []Format{MD, WhatsApp, DOCX, PDF} {
			a, b := render(t, fx.in, f), render(t, fx.in, f)
			if !bytes.Equal(a, b) {
				t.Errorf("%s %s: two renders differ", fx.name, f)
			}
		}
	}
}

// texts returns the human-visible text of every format.
func texts(t *testing.T, in Input) map[Format]string {
	t.Helper()
	return map[Format]string{
		MD:       mdUnescape(string(render(t, in, MD))),
		WhatsApp: string(render(t, in, WhatsApp)),
		DOCX:     docxText(t, render(t, in, DOCX)),
		PDF:      pdfToText(t, render(t, in, PDF)),
	}
}

func TestDraftLabelIffUnreviewed(t *testing.T) {
	for _, fx := range fixtures(t) {
		for _, draft := range []bool{true, false} {
			in := fx.in
			in.IsDraft = draft
			for f, s := range texts(t, in) {
				if got := strings.Contains(s, "DRAFT"); got != draft {
					t.Errorf("%s %s draft=%v: contains DRAFT = %v", fx.name, f, draft, got)
				}
			}
			if strings.HasSuffix(Filename(in, PDF), "-draft.pdf") != draft {
				t.Errorf("%s filename draft suffix wrong: %s", fx.name, Filename(in, PDF))
			}
		}
	}
	id := texts(t, fixtures(t)[0].in)
	if !strings.Contains(id[MD], "DRAFT — belum ditinjau") || !strings.Contains(id[PDF], "DRAFT — belum ditinjau") {
		t.Error("indonesian MoM must use the content language label")
	}
	if !strings.Contains(texts(t, fixtures(t)[3].in)[DOCX], "DRAFT — belum ditinjau") {
		t.Error("manual notes label uses the user language")
	}
}

func TestExportConsistency(t *testing.T) {
	in := fixtures(t)[0].in
	all := texts(t, in)
	same := []string{
		"Budget marketing naik 10% menjadi Rp60 juta. [01:02, 1:02:05]",
		"Rilis hari Jumat. [05:40] (dari catatan pengguna)",
		"Siapkan laporan vendor", "Budi", "2026-11-13 (minggu depan)", "[1:02:05]",
		"Kirim notulen", "Belum ditentukan", "akhir bulan", "(dari catatan pengguna)",
		"Cek kontrak", "Sari",
		"Weekly Sync: Rilis Q1 / Café", "2026-11-06 09:00 WIB", "Peserta: Budi, Sari",
		"Tim membahas budget *marketing* dan <rilis>. [01:02]",
		"Butuh 2 tester | tambahan_baru",
	}
	for _, f := range []Format{MD, DOCX, PDF} {
		norm := strings.Join(strings.Fields(all[f]), " ")
		for _, s := range same {
			if !strings.Contains(norm, s) {
				t.Errorf("%s missing %q\n%s", f, s, norm)
			}
		}
		for _, absent := range []string{"Hal terbuka"} {
			if strings.Contains(norm, absent) {
				t.Errorf("%s: empty section %q must be omitted", f, absent)
			}
		}
	}
	wa := all[WhatsApp]
	for _, s := range []string{"*Ringkasan*", "*Keputusan*", "1. Budget marketing naik 10% menjadi Rp60 juta.", "*Action items*",
		"   PIC: Budi · Deadline: 2026-11-13 (minggu depan)", "   PIC: Belum ditentukan · Deadline: akhir bulan"} {
		if !strings.Contains(wa, s) {
			t.Errorf("whatsapp missing %q", s)
		}
	}
	for _, s := range []string{"Pembahasan", "Tindak lanjut", "Agenda", "[01:02]", "catatan pengguna"} {
		if strings.Contains(wa, s) {
			t.Errorf("whatsapp must not contain %q", s)
		}
	}

	empty := texts(t, fixtures(t)[1].in)
	for f, s := range empty {
		for _, want := range []string{"Untitled meeting", "Date not set", "Decisions", "Action items", "None."} {
			if !strings.Contains(s, want) {
				t.Errorf("empty MoM %s missing %q", f, want)
			}
		}
		if strings.Contains(s, "Summary") {
			t.Errorf("empty MoM %s: empty summary must be omitted", f)
		}
	}
}

func TestApproximateRefs(t *testing.T) {
	fx := fixtures(t)
	exact, momApprox, notesApprox := texts(t, fx[0].in), texts(t, fx[4].in), texts(t, fx[5].in)
	for f, s := range exact {
		if strings.Contains(s, "~") {
			t.Errorf("exact %s must not mark times or print the legend:\n%s", f, s)
		}
	}
	for _, c := range []struct {
		all    map[Format]string
		legend string
		refs   []string
	}{
		{momApprox, "~ = perkiraan waktu", []string{"Budget marketing naik 10% menjadi Rp60 juta. [~01:02, ~1:02:05]",
			"Rilis hari Jumat. [~05:40] (dari catatan pengguna)", "Rencana rilis [~05:40]", "[~1:02:05]"}},
		{notesApprox, "~ = approximate time", []string{"Marketing budget up 10%. [~01:02]", "Friday release [~1:02:05]", "Vendor shortlist (from user notes)"}},
	} {
		for _, f := range []Format{MD, DOCX, PDF} {
			norm := strings.Join(strings.Fields(c.all[f]), " ")
			for _, want := range c.refs {
				if !strings.Contains(norm, want) {
					t.Errorf("%s missing %q\n%s", f, want, norm)
				}
			}
			if n := strings.Count(c.all[f], c.legend); n != 1 {
				t.Errorf("%s legend %q count %d, want 1", f, c.legend, n)
			}
			if !strings.HasSuffix(strings.TrimSpace(c.all[f]), c.legend) && f != PDF {
				t.Errorf("%s legend is not the last line", f)
			}
		}
		if strings.Contains(c.all[WhatsApp], "~") {
			t.Error("whatsapp renders no refs, so no approximate marks or legend")
		}
	}
	if md := string(render(t, fx[4].in, MD)); !strings.Contains(md, `[\~01:02, \~1:02:05]`) || !strings.HasSuffix(md, "\n\n\\~ = perkiraan waktu\n") {
		t.Errorf("md must escape ~ in refs and legend:\n%s", md)
	}
	if doc := docxDocumentXML(t, render(t, fx[4].in, DOCX)); !strings.Contains(doc, "[~01:02, ~1:02:05]") || !strings.Contains(doc, ">~ = perkiraan waktu<") {
		t.Errorf("docx missing approximate refs or legend:\n%s", doc)
	}
}

func TestFontRuns(t *testing.T) {
	cases := map[string][]fontRun{
		"":                {},
		"Rapat ñ":         {{pdfFont, "Rapat ñ"}},
		"∑":               {{pdfFallback, "∑"}},
		"Total ∑ → biaya": {{pdfFont, "Total "}, {pdfFallback, "∑ → "}, {pdfFont, "biaya"}},
		"a\n∑ 中":          {{pdfFont, "a\n"}, {pdfFallback, "∑ 中"}},
	}
	for in, want := range cases {
		got := fontRuns(in, "")
		if len(got) != len(want) {
			t.Errorf("fontRuns(%q) = %q, want %q", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("fontRuns(%q) = %q, want %q", in, got, want)
			}
		}
	}
	for _, style := range []string{"", "B"} {
		if primaryCoverage[style].has('∑') || !fallbackCoverage[style].has('∑') || !primaryCoverage[style].has('ñ') || primaryCoverage[style].has(0x1F44D) {
			t.Errorf("coverage tables wrong for style %q", style)
		}
	}
}

var baseFonts = regexp.MustCompile(`/BaseFont /([A-Za-z0-9+_-]+)`)

func embeddedFonts(b []byte) string {
	var names []string
	for _, m := range baseFonts.FindAllSubmatch(b, -1) {
		names = append(names, string(m[1]))
	}
	return strings.Join(names, " ")
}

func TestPDFFontFallback(t *testing.T) {
	raw := `{"schema_version":1,"language":"id","sections":[
	  {"id":"sec_1","heading":"Anggaran → Rilis","style":"bullet","items":[
	    {"id":"it_1","text":"Keputusan: anggaran pemasaran naik ∑ 10% → Rp60 juta, disetujui oleh Ibu Siti.","origin":"human","edited":false,"sources":[]},
	    {"id":"it_2","text":"Pengujian selesai hari Kamis; rilis Jumat.","origin":"human","edited":false,"sources":[]}]}]}`
	in := Input{Content: mustContent(t, documents.KindAINotes, raw), Header: Header{Title: "Rapat Anggaran", Timezone: "Asia/Jakarta"}, CreatedAt: createdAt}
	b, err := Render(in, PDF)
	if err != nil {
		t.Fatal(err)
	}
	fonts := embeddedFonts(b)
	for _, want := range []string{"utf8notosans", "utf8notosansB", "utf8dejavusans", "utf8dejavusansB"} {
		if !slices.Contains(strings.Fields(fonts), want) {
			t.Errorf("font %s not embedded: %s", want, fonts)
		}
	}
	if n := strings.Count(string(b), "/FontFile2"); n != 4 {
		t.Errorf("embedded font files = %d, want 4", n)
	}
	text := strings.Join(strings.Fields(pdfToText(t, b)), " ")
	for _, want := range []string{"Anggaran → Rilis", "Keputusan: anggaran pemasaran naik ∑ 10% → Rp60 juta, disetujui oleh Ibu Siti.",
		"Pengujian selesai hari Kamis; rilis Jumat.", "Rapat Anggaran"} {
		if !strings.Contains(text, want) {
			t.Errorf("pdf text missing %q\n%s", want, text)
		}
	}
	if !bytes.Equal(b, render(t, in, PDF)) {
		t.Error("mixed-font pdf not deterministic")
	}

	var ws []string
	for i := 1; i <= 900; i++ {
		w := fmt.Sprintf("kata%04d", i)
		if i%7 == 0 {
			w += "→∑"
		}
		ws = append(ws, w)
	}
	long := `{"schema_version":1,"language":"id","sections":[{"id":"sec_1","heading":"Panjang","style":"bullet","items":[
	  {"id":"it_1","text":"` + strings.Join(ws, " ") + `","origin":"human","edited":false,"sources":[]}]}]}`
	lin := Input{Content: mustContent(t, documents.KindAINotes, long), Header: Header{Timezone: "UTC"}, CreatedAt: createdAt}
	lb := render(t, lin, PDF)
	if pageCount(t, lb) < 2 {
		t.Fatal("long mixed-font text should span pages")
	}
	ltext := pdfToText(t, lb)
	for _, w := range ws {
		if !strings.Contains(ltext, w) {
			t.Fatalf("word %s lost in mixed-font wrapping", w)
		}
	}

	plain := render(t, fixtures(t)[0].in, PDF)
	if fonts := embeddedFonts(plain); strings.Contains(fonts, "DejaVu") {
		t.Errorf("fallback embedded although unused: %s", fonts)
	}
}

func TestDOCXFonts(t *testing.T) {
	raw := `{"schema_version":1,"items":[{"id":"n1","text":"Total ∑ biaya","at_ms":null}]}`
	in := Input{Content: mustContent(t, documents.KindManualNotes, raw), Header: Header{Timezone: "UTC"}, Language: "id", CreatedAt: createdAt}
	b := render(t, in, DOCX)
	doc := docxDocumentXML(t, b)
	want := `<w:r><w:t xml:space="preserve">Total </w:t></w:r><w:r><w:rPr><w:rFonts w:ascii="DejaVu Sans" w:hAnsi="DejaVu Sans" w:eastAsia="DejaVu Sans" w:cs="DejaVu Sans"/></w:rPr><w:t xml:space="preserve">∑ </w:t></w:r><w:r><w:t xml:space="preserve">biaya</w:t></w:r>`
	if !strings.Contains(doc, want) {
		t.Errorf("document.xml missing fallback runs:\n%s", doc)
	}
	if err := wellFormed([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stylesXML, `<w:rFonts w:ascii="Noto Sans" w:hAnsi="Noto Sans" w:eastAsia="Noto Sans" w:cs="Noto Sans"/>`) {
		t.Error("styles default font is not Noto Sans")
	}
}

func docxDocumentXML(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			defer rc.Close()
			data, _ := io.ReadAll(rc)
			return string(data)
		}
	}
	t.Fatal("no document.xml")
	return ""
}

func TestNoIdsOrURLsInOutput(t *testing.T) {
	for _, fx := range fixtures(t) {
		for f, s := range texts(t, fx.in) {
			for _, leak := range []string{"seg_zz", "note_qq", "it_", "h_x", "h_c", "sec_", "http", "://", "n1", "n2"} {
				if strings.Contains(s, leak) {
					t.Errorf("%s %s leaks %q", fx.name, f, leak)
				}
			}
		}
	}
}

func TestMarkdownEscaping(t *testing.T) {
	cases := map[string]string{
		`a*b_c` + "`d`" + `[e]<f>|g\h`:  `a\*b\_c\` + "`d\\`" + `\[e\]\<f\>\|g\\h`,
		"# x\n+ y\n- z\n12. w\n  3.5 v": "\\# x\n\\+ y\n\\- z\n12\\. w\n  3\\.5 v",
		"10% naik":                      "10% naik",
	}
	for in, want := range cases {
		if got := mdEscape(in); got != want {
			t.Errorf("mdEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTimestamp(t *testing.T) {
	for ms, want := range map[int64]string{0: "00:00", 61999: "01:01", 3599999: "59:59", 3600000: "1:00:00", 36125000: "10:02:05"} {
		if got := timestamp(ms); got != want {
			t.Errorf("timestamp(%d) = %s, want %s", ms, got, want)
		}
	}
}

func TestFilename(t *testing.T) {
	fx := fixtures(t)
	started := time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		in   Input
		f    Format
		want string
	}{
		{fx[0].in, PDF, "weekly-sync-rilis-q1-cafe-20261106-mom-draft.pdf"},
		{fx[1].in, DOCX, "meeting-undated-mom.docx"},
		{fx[2].in, WhatsApp, "rapat-vendor-20261106-notes.txt"},
		{fx[3].in, MD, "meeting-undated-manual-notes-draft.md"},
		{Input{Content: fx[2].in.Content, Header: Header{Title: strings.Repeat("ab ", 40), StartedAt: &started, Timezone: "Asia/Jakarta"}}, MD,
			strings.TrimRight(strings.Repeat("ab-", 20), "-") + "-20261106-notes.md"},
		{Input{Content: fx[2].in.Content, Header: Header{Title: "日本語 !!", StartedAt: &started, Timezone: "UTC"}}, PDF, "meeting-20261105-notes.pdf"},
	}
	for _, tc := range cases {
		got := Filename(tc.in, tc.f)
		if got != tc.want {
			t.Errorf("Filename = %s, want %s", got, tc.want)
		}
		if len(got) > 120 {
			t.Errorf("filename too long: %s", got)
		}
	}
}

func TestDOCXStructure(t *testing.T) {
	b := render(t, fixtures(t)[0].in, DOCX)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"[Content_Types].xml", "_rels/.rels", "docProps/core.xml", "word/document.xml", "word/styles.xml", "word/numbering.xml", "word/_rels/document.xml.rels"}
	if len(zr.File) != len(wantOrder) {
		t.Fatalf("entries %d", len(zr.File))
	}
	parts := map[string]string{}
	for i, f := range zr.File {
		if f.Name != wantOrder[i] {
			t.Errorf("entry %d = %s, want %s", i, f.Name, wantOrder[i])
		}
		if !f.Modified.Equal(createdAt) {
			t.Errorf("%s modified %v", f.Name, f.Modified)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		if err := wellFormed(data); err != nil {
			t.Errorf("%s not well-formed: %v", f.Name, err)
		}
		parts[f.Name] = string(data)
	}
	doc := parts["word/document.xml"]
	for _, want := range []string{
		`<w:pStyle w:val="Title"/></w:pPr><w:r><w:t xml:space="preserve">Weekly Sync: Rilis Q1 / Café</w:t>`,
		`<w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t xml:space="preserve">Keputusan</w:t>`,
		`<w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t xml:space="preserve">Rencana rilis [05:40]</w:t>`,
		`<w:rPr><w:b/></w:rPr><w:t xml:space="preserve">DRAFT — belum ditinjau</w:t>`,
		`dan &lt;rilis&gt;.`,
		`<w:t xml:space="preserve">Kirim notulen</w:t><w:br/><w:t xml:space="preserve">ke semua</w:t>`,
		`<w:trPr><w:tblHeader/></w:trPr>`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("document.xml missing %s", want)
		}
	}
	if n := strings.Count(doc, `<w:gridCol `); n != 5 {
		t.Errorf("gridCol count %d", n)
	}
	if n := strings.Count(doc, `<w:tr>`); n != 4 {
		t.Errorf("table rows %d, want header + 3", n)
	}
	for _, h := range []string{"No", "Tugas", "PIC", "Deadline", "Sumber"} {
		if !strings.Contains(doc, `<w:rPr><w:b/></w:rPr><w:t xml:space="preserve">`+h+`</w:t>`) {
			t.Errorf("header cell %s missing", h)
		}
	}

	// agenda, decisions and the custom section are separate numbered lists; each must restart.
	numIDs := regexp.MustCompile(`<w:numId w:val="(\d+)"/>`).FindAllStringSubmatch(doc, -1)
	seen := map[string]int{}
	for _, m := range numIDs {
		seen[m[1]]++
	}
	if seen["1"] != 3 {
		t.Errorf("bullet paragraphs = %v, want 3 (2 points + 1 follow-up)", seen)
	}
	numbering := parts["word/numbering.xml"]
	for _, id := range []string{"2", "3", "4"} {
		if seen[id] == 0 {
			t.Errorf("numbered list numId %s unused: %v", id, seen)
		}
		if !strings.Contains(numbering, `<w:num w:numId="`+id+`"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>`) {
			t.Errorf("numId %s does not restart", id)
		}
	}
	if !strings.Contains(numbering, `<w:numFmt w:val="decimal"/>`) || !strings.Contains(numbering, `<w:numFmt w:val="bullet"/>`) {
		t.Error("numbering formats missing")
	}
	for _, st := range []string{"Title", "Heading1", "Heading2", "Heading3"} {
		if !strings.Contains(parts["word/styles.xml"], `w:styleId="`+st+`"`) {
			t.Errorf("style %s missing", st)
		}
	}
	if !strings.Contains(parts["docProps/core.xml"], `<dcterms:created xsi:type="dcterms:W3CDTF">2026-11-06T03:04:05Z</dcterms:created>`) {
		t.Error("core.xml created date")
	}
	if !strings.Contains(parts["word/_rels/document.xml.rels"], `Target="numbering.xml"`) || !strings.Contains(parts["[Content_Types].xml"], `/word/numbering.xml`) {
		t.Error("numbering part not wired")
	}
}

func TestPDF(t *testing.T) {
	b := render(t, fixtures(t)[0].in, PDF)
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatal("not a pdf")
	}
	s := string(b)
	for _, want := range []string{"/CreationDate (D:20261106030405", "/ModDate (D:20261106030405", "/FontFile2", "/ToUnicode"} {
		if !strings.Contains(s, want) {
			t.Errorf("pdf missing %s", want)
		}
	}
	if n := pageCount(t, b); n != 1 {
		t.Errorf("short MoM pages = %d", n)
	}
	text := pdfToText(t, b)
	if !strings.Contains(text, "1/1") {
		t.Error("footer n/N missing")
	}
}

func TestPDFLongDocumentWrapsAcrossPages(t *testing.T) {
	var items []string
	word := 0
	for i := 0; i < 60; i++ {
		var ws []string
		n := 40
		if i == 30 {
			n = 900
		}
		for j := 0; j < n; j++ {
			word++
			ws = append(ws, fmt.Sprintf("kata%05d", word))
		}
		items = append(items, fmt.Sprintf(`{"id":"it_%d","text":%q,"origin":"human","edited":false,"sources":[]}`, i, strings.Join(ws, " ")))
	}
	raw := `{"schema_version":1,"language":"id","sections":[` +
		`{"id":"sec_1","heading":"Satu","style":"bullet","items":[` + strings.Join(items[:20], ",") + `]},` +
		`{"id":"sec_2","heading":"Dua","style":"paragraph","items":[` + strings.Join(items[20:40], ",") + `]},` +
		`{"id":"sec_3","heading":"Tiga","style":"numbered","items":[` + strings.Join(items[40:], ",") + `]}]}`
	in := Input{Content: mustContent(t, documents.KindAINotes, raw), Header: Header{Title: "Panjang", Timezone: "UTC"}, IsDraft: true, CreatedAt: createdAt}
	b := render(t, in, PDF)
	pages := pageCount(t, b)
	if pages < 3 {
		t.Fatalf("pages = %d, want several", pages)
	}
	text := pdfToText(t, b)
	for i := 1; i <= word; i++ {
		if w := fmt.Sprintf("kata%05d", i); !strings.Contains(text, w) {
			t.Fatalf("word %s lost (clipped across a page break?)", w)
		}
	}
	nb := strconv.Itoa(pages)
	if !strings.Contains(text, "1/"+nb) || !strings.Contains(text, nb+"/"+nb) {
		t.Errorf("footer n/N missing for %d pages", pages)
	}
	if got := strings.Count(text, "DRAFT"); got < pages {
		t.Errorf("DRAFT page header on %d of %d pages", got, pages)
	}
	if !bytes.Equal(b, render(t, in, PDF)) {
		t.Error("long pdf not deterministic")
	}
}

func TestPDFOutsideBMP(t *testing.T) {
	raw := `{"schema_version":1,"items":[{"id":"n1","text":"Siap 👍 ✓ — “kutip” ñ ü 中文","at_ms":null}]}`
	in := Input{Content: mustContent(t, documents.KindManualNotes, raw), Header: Header{Timezone: "UTC"}, Language: "en", CreatedAt: createdAt}
	if text := pdfToText(t, render(t, in, PDF)); !strings.Contains(text, "Siap") || !strings.Contains(text, "ñ ü") {
		t.Errorf("unexpected text %q", text)
	}
}

func wellFormed(b []byte) error {
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		if _, err := d.Token(); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
	}
}

func docxText(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, _ := f.Open()
		defer rc.Close()
		var out strings.Builder
		d := xml.NewDecoder(rc)
		for {
			tok, err := d.Token()
			if err == io.EOF {
				return out.String()
			}
			if err != nil {
				t.Fatal(err)
			}
			switch tok := tok.(type) {
			case xml.CharData:
				out.Write(tok)
			case xml.EndElement:
				if tok.Name.Local == "p" || tok.Name.Local == "tc" {
					out.WriteString("\n")
				}
			case xml.StartElement:
				if tok.Name.Local == "br" {
					out.WriteString("\n")
				}
			}
		}
	}
	t.Fatal("no document.xml")
	return ""
}

func pdfToText(t *testing.T, b []byte) string {
	t.Helper()
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Fatal("pdftotext (poppler) required on PATH: brew install poppler")
	}
	path := filepath.Join(t.TempDir(), "out.pdf")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-enc", "UTF-8", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	return string(out)
}

var pagesCount = regexp.MustCompile(`/Type /Pages\s*/Kids \[[^\]]*\]\s*/Count (\d+)`)

func pageCount(t *testing.T, b []byte) int {
	t.Helper()
	m := pagesCount.FindSubmatch(b)
	if m == nil {
		t.Fatal("no /Pages object")
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

var mdEscapes = regexp.MustCompile(`\\([\\*_` + "`" + `\[\]<>|#+\-.~])`)

func mdUnescape(s string) string { return mdEscapes.ReplaceAllString(s, "$1") }
