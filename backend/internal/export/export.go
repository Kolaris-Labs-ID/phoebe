package export

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"phoebe/internal/documents"
)

type Format string

const (
	MD       Format = "md"
	WhatsApp Format = "whatsapp"
	DOCX     Format = "docx"
	PDF      Format = "pdf"
)

func ParseFormat(s string) (Format, bool) {
	switch f := Format(s); f {
	case MD, WhatsApp, DOCX, PDF:
		return f, true
	}
	return "", false
}

func ContentType(f Format) string {
	switch f {
	case MD:
		return "text/markdown; charset=utf-8"
	case WhatsApp:
		return "text/plain; charset=utf-8"
	case DOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case PDF:
		return "application/pdf"
	}
	return ""
}

func Extension(f Format) string {
	if f == WhatsApp {
		return "txt"
	}
	return string(f)
}

// Header is the meeting row at export time; renderers use it for notes kinds only
// (MoM renders its own meeting_info snapshot).
type Header struct {
	Title        string
	StartedAt    *time.Time
	Timezone     string
	Participants []string
}

type Input struct {
	Content documents.Content
	Header  Header
	// Language is the user's language; it labels manual notes, which carry none.
	Language  string
	IsDraft   bool
	CreatedAt time.Time
}

// Render is a pure function of its input (api.md §5): no clock, no randomness.
func Render(in Input, f Format) ([]byte, error) {
	d, err := build(in)
	if err != nil {
		return nil, err
	}
	switch f {
	case MD:
		return renderMarkdown(d), nil
	case WhatsApp:
		return renderWhatsApp(d), nil
	case DOCX:
		return renderDOCX(d)
	case PDF:
		return renderPDF(d, true)
	}
	return nil, fmt.Errorf("export: unknown format %q", f)
}

func Filename(in Input, f Format) string {
	h := header(in)
	var b strings.Builder
	b.WriteString(slug(h.Title))
	b.WriteByte('-')
	if h.StartedAt != nil {
		b.WriteString(h.StartedAt.In(location(h.Timezone)).Format("20060102"))
	} else {
		b.WriteString("undated")
	}
	switch in.Content.Kind() {
	case documents.KindMoM:
		b.WriteString("-mom")
	case documents.KindAINotes:
		b.WriteString("-notes")
	default:
		b.WriteString("-manual-notes")
	}
	if in.IsDraft {
		b.WriteString("-draft")
	}
	b.WriteByte('.')
	b.WriteString(Extension(f))
	return b.String()
}

func slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFKD.String(strings.ToLower(title)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		case unicode.Is(unicode.Mn, r):
		default:
			dash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	s := strings.TrimRight(b.String()[:min(b.Len(), 60)], "-")
	if s == "" {
		return "meeting"
	}
	return s
}

func location(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil && tz != "" && tz != "Local" {
		return loc
	}
	return time.UTC
}

func header(in Input) Header {
	if m, ok := in.Content.(*documents.MoM); ok {
		h := Header{Title: m.MeetingInfo.Title, Timezone: m.MeetingInfo.Timezone, Participants: m.MeetingInfo.Participants}
		if m.MeetingInfo.StartedAt != nil {
			t := m.MeetingInfo.StartedAt.Time
			h.StartedAt = &t
		}
		return h
	}
	return Header{Title: in.Header.Title, StartedAt: in.Header.StartedAt, Timezone: in.Header.Timezone}
}

type labels struct {
	untitled, noDate, draft, participants, none, undetermined, fromNotes string
	no, task, pic, deadline, source                                      string
	approxLegend                                                         string
}

var labelSets = map[string]labels{
	"id": {"Rapat tanpa judul", "Tanggal belum ditentukan", "DRAFT — belum ditinjau", "Peserta", "Tidak ada.", "Belum ditentukan", "(dari catatan pengguna)",
		"No", "Tugas", "PIC", "Deadline", "Sumber", "~ = perkiraan waktu"},
	"en": {"Untitled meeting", "Date not set", "DRAFT — not reviewed", "Participants", "None.", "Not determined", "(from user notes)",
		"No", "Task", "PIC", "Deadline", "Source", "~ = approximate time"},
}

// doc is the format-independent view every renderer walks, so all formats carry
// textually identical decisions, PICs, deadlines and refs.
type doc struct {
	kind         documents.Kind
	l            labels
	title        string
	date         string
	participants string
	draft        bool
	createdAt    time.Time
	sections     []section
	approximate  bool
}

type section struct {
	typ     string
	heading string
	style   string
	items   []item
	topics  []topic
	actions []action
	none    bool
}

type item struct{ text, refs string }

type topic struct {
	title, refs string
	points      []item
}

type action struct{ desc, pic, deadline, refs string }

func build(in Input) (*doc, error) {
	if in.Content == nil {
		return nil, fmt.Errorf("export: nil content")
	}
	lang := in.Language
	switch c := in.Content.(type) {
	case *documents.AINotes:
		lang = c.Language
	case *documents.MoM:
		lang = c.Language
	}
	l, ok := labelSets[lang]
	if !ok {
		l = labelSets["id"]
	}
	h := header(in)
	d := &doc{kind: in.Content.Kind(), l: l, draft: in.IsDraft, createdAt: in.CreatedAt.UTC()}
	d.title = oneLine(h.Title)
	if d.title == "" {
		d.title = l.untitled
	}
	d.date = l.noDate
	if h.StartedAt != nil {
		d.date = h.StartedAt.In(location(h.Timezone)).Format("2006-01-02 15:04 MST")
	}

	switch c := in.Content.(type) {
	case *documents.ManualNotes:
		s := section{style: documents.StyleBullet}
		for _, n := range c.Items {
			it := item{text: clean(n.Text)}
			if n.AtMS != nil {
				it.refs = "[" + timestamp(*n.AtMS) + "]"
			}
			s.items = append(s.items, it)
		}
		d.addSection(s)
	case *documents.AINotes:
		for _, cs := range c.Sections {
			d.addSection(section{heading: oneLine(cs.Heading), style: cs.Style, items: d.textItems(cs.Items)})
		}
	case *documents.MoM:
		if len(c.MeetingInfo.Participants) > 0 {
			ps := make([]string, len(c.MeetingInfo.Participants))
			for i, p := range c.MeetingInfo.Participants {
				ps[i] = oneLine(p)
			}
			d.participants = l.participants + ": " + strings.Join(ps, ", ")
		}
		d.addSection(section{typ: "agenda", heading: documents.DefaultHeading(lang, "agenda"), style: documents.StyleNumbered,
			items: d.textItems(c.MeetingInfo.Agenda)})
		for _, cs := range c.Sections {
			s := section{typ: cs.Type, heading: oneLine(cs.Heading)}
			if cs.Style != nil {
				s.style = *cs.Style
			}
			switch cs.Type {
			case documents.SectionDiscussion:
				for _, t := range cs.Topics {
					s.topics = append(s.topics, topic{title: oneLine(t.Title), refs: d.refs(t.Sources), points: d.textItems(t.Points)})
				}
			case documents.SectionActionItems:
				for _, a := range cs.Actions {
					s.actions = append(s.actions, action{desc: clean(a.Description), pic: d.pic(a.PIC), deadline: d.deadline(a), refs: d.refs(a.Sources)})
				}
			default:
				s.items = d.textItems(cs.Items)
			}
			s.none = cs.Type == documents.SectionDecisions || cs.Type == documents.SectionActionItems
			d.addSection(s)
		}
	default:
		return nil, fmt.Errorf("export: unsupported content %T", in.Content)
	}
	return d, nil
}

func (d *doc) addSection(s section) {
	if len(s.items) == 0 && len(s.topics) == 0 && len(s.actions) == 0 {
		if !s.none {
			return
		}
	} else {
		s.none = false
	}
	d.sections = append(d.sections, s)
}

func (d *doc) textItems(items []documents.TextItem) []item {
	out := make([]item, 0, len(items))
	for _, it := range items {
		out = append(out, item{text: clean(it.Text), refs: d.refs(it.Sources)})
	}
	return out
}

func (d *doc) pic(p *string) string {
	if p == nil {
		return d.l.undetermined
	}
	return oneLine(*p)
}

func (d *doc) deadline(a documents.ActionItem) string {
	switch {
	case a.Deadline != nil && a.DeadlinePhrase != nil:
		return *a.Deadline + " (" + oneLine(*a.DeadlinePhrase) + ")"
	case a.Deadline != nil:
		return *a.Deadline
	case a.DeadlinePhrase != nil:
		return oneLine(*a.DeadlinePhrase)
	}
	return d.l.undetermined
}

// refs renders transcript refs as "[01:02, 05:40]" ("~01:02" when approximate) and
// user-notes refs as one label; ids are never printed.
func (d *doc) refs(rs []documents.SourceRef) string {
	var times []string
	seen := map[string]bool{}
	notes := false
	for _, r := range rs {
		switch r.Type {
		case documents.RefTranscript:
			ts := timestamp(r.StartMS)
			if r.Approximate {
				ts = "~" + ts
				d.approximate = true
			}
			if !seen[ts] {
				seen[ts] = true
				times = append(times, ts)
			}
		case documents.RefUserNotes:
			notes = true
		}
	}
	var parts []string
	if len(times) > 0 {
		parts = append(parts, "["+strings.Join(times, ", ")+"]")
	}
	if notes {
		parts = append(parts, d.l.fromNotes)
	}
	return strings.Join(parts, " ")
}

func timestamp(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// clean normalizes line endings and drops control characters (invalid in XML and PDF text).
func clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.Map(func(r rune) rune {
		if r != '\n' && (unicode.IsControl(r) || r == '￾' || r == '￿') {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(clean(s)), " ")
}

func withRefs(text, refs string) string {
	if refs == "" {
		return text
	}
	return text + " " + refs
}
