package documents

import (
	"cmp"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"phoebe/internal/jsonx"
)

// LLM output shapes (backend.md §6.2).

type llmItem struct {
	Text     string   `json:"text"`
	Refs     []string `json:"refs"`
	NoteRefs []string `json:"note_refs"`
}

type llmSection struct {
	Heading string    `json:"heading"`
	Items   []llmItem `json:"items"`
}

type llmNotes struct {
	Sections []llmSection `json:"sections"`
}

type llmTopic struct {
	Title    string    `json:"title"`
	Refs     []string  `json:"refs"`
	NoteRefs []string  `json:"note_refs"`
	Points   []llmItem `json:"points"`
}

type llmAction struct {
	Description    string   `json:"description"`
	PIC            *string  `json:"pic"`
	Deadline       *string  `json:"deadline"`
	DeadlinePhrase *string  `json:"deadline_phrase"`
	Refs           []string `json:"refs"`
	NoteRefs       []string `json:"note_refs"`
}

type llmMoM struct {
	Agenda      []llmItem   `json:"agenda"`
	Summary     []llmItem   `json:"summary"`
	Discussion  []llmTopic  `json:"discussion"`
	Decisions   []llmItem   `json:"decisions"`
	ActionItems []llmAction `json:"action_items"`
	OpenIssues  []llmItem   `json:"open_issues"`
	FollowUps   []llmItem   `json:"follow_ups"`
}

// errEmptyOutput: nothing usable survived post-processing (backend.md §6.3 step 8).
var errEmptyOutput = errors.New("documents: empty output")

func decodeNotes(content string) (llmNotes, error) {
	var n llmNotes
	err := jsonx.Decode([]byte(content), &n)
	return n, err
}

func decodeMoM(content string) (llmMoM, error) {
	var m llmMoM
	err := jsonx.Decode([]byte(content), &m)
	return m, err
}

// §6.4 chunk merge.

var spaceRe = regexp.MustCompile(`\s+`)

func normKey(s string) string {
	s = strings.ToLower(spaceRe.ReplaceAllString(strings.TrimSpace(s), " "))
	return strings.TrimSpace(strings.TrimFunc(s, unicode.IsPunct))
}

func union(a, b []string) []string {
	for _, x := range b {
		if !slices.Contains(a, x) {
			a = append(a, x)
		}
	}
	return a
}

func mergeItems(dst, src []llmItem) []llmItem {
	for _, it := range src {
		k := normKey(it.Text)
		i := slices.IndexFunc(dst, func(d llmItem) bool { return k != "" && normKey(d.Text) == k })
		if i < 0 {
			dst = append(dst, it)
			continue
		}
		dst[i].Refs = union(dst[i].Refs, it.Refs)
		dst[i].NoteRefs = union(dst[i].NoteRefs, it.NoteRefs)
	}
	return dst
}

func mergeNotes(parts []llmNotes) llmNotes {
	var out llmNotes
	for _, p := range parts {
		for _, s := range p.Sections {
			k := normKey(s.Heading)
			i := slices.IndexFunc(out.Sections, func(d llmSection) bool { return normKey(d.Heading) == k })
			if i < 0 {
				out.Sections = append(out.Sections, llmSection{Heading: s.Heading})
				i = len(out.Sections) - 1
			}
			out.Sections[i].Items = mergeItems(out.Sections[i].Items, s.Items)
		}
	}
	return out
}

func mergeMoM(parts []llmMoM) llmMoM {
	var out llmMoM
	for _, p := range parts {
		out.Agenda = mergeItems(out.Agenda, p.Agenda)
		out.Summary = mergeItems(out.Summary, p.Summary)
		out.Decisions = mergeItems(out.Decisions, p.Decisions)
		out.OpenIssues = mergeItems(out.OpenIssues, p.OpenIssues)
		out.FollowUps = mergeItems(out.FollowUps, p.FollowUps)
		for _, t := range p.Discussion {
			k := normKey(t.Title)
			i := slices.IndexFunc(out.Discussion, func(d llmTopic) bool { return k != "" && normKey(d.Title) == k })
			if i < 0 {
				t.Points = mergeItems(nil, t.Points)
				out.Discussion = append(out.Discussion, t)
				continue
			}
			d := &out.Discussion[i]
			d.Refs, d.NoteRefs = union(d.Refs, t.Refs), union(d.NoteRefs, t.NoteRefs)
			d.Points = mergeItems(d.Points, t.Points)
		}
		for _, a := range p.ActionItems {
			k := normKey(a.Description)
			i := slices.IndexFunc(out.ActionItems, func(d llmAction) bool { return k != "" && normKey(d.Description) == k })
			if i < 0 {
				out.ActionItems = append(out.ActionItems, a)
				continue
			}
			d := &out.ActionItems[i]
			d.Refs, d.NoteRefs = union(d.Refs, a.Refs), union(d.NoteRefs, a.NoteRefs)
			if d.PIC == nil {
				d.PIC = a.PIC
			}
			if d.Deadline == nil && d.DeadlinePhrase == nil {
				d.Deadline, d.DeadlinePhrase = a.Deadline, a.DeadlinePhrase
			}
		}
	}
	return out
}

// §6.3 post-processing.

type segment struct {
	ID      string `json:"id"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

// sources is everything a generated item may be checked against: the pinned transcript
// segments and manual notes, plus the meeting metadata.
type sources struct {
	segIndex     map[string]int
	segments     []segment
	notes        map[string]string // nil = manual notes not pinned
	participants []string
	meetingDate  *time.Time
	language     string
}

func newSources(segs []segment, notes *ManualNotes, participants []string, meetingDate *time.Time, language string) *sources {
	s := &sources{segIndex: make(map[string]int, len(segs)), segments: segs, participants: participants,
		meetingDate: meetingDate, language: language}
	for i, g := range segs {
		s.segIndex[g.ID] = i
	}
	if notes != nil {
		s.notes = make(map[string]string, len(notes.Items))
		for _, n := range notes.Items {
			s.notes[n.ID] = n.Text
		}
	}
	return s
}

func (s *sources) pins() Pins {
	p := Pins{Segments: make(map[string]Span, len(s.segments))}
	for _, g := range s.segments {
		p.Segments[g.ID] = Span{g.StartMS, g.EndMS}
	}
	if s.notes != nil {
		p.NoteIDs = make(map[string]bool, len(s.notes))
		for id := range s.notes {
			p.NoteIDs[id] = true
		}
	}
	return p
}

// resolved holds an item's refs after resolution and its source text.
type resolved struct {
	refs       []SourceRef
	transcript int
	text       string
}

// resolve drops unknown ids, dedupes, orders transcript refs by start_ms then user-notes
// refs, and builds the newline-joined source text (§6.3 steps 3 and "source text").
func (s *sources) resolve(refs, noteRefs []string) resolved {
	var idx []int
	for _, id := range refs {
		if i, ok := s.segIndex[strings.TrimSpace(id)]; ok && !slices.Contains(idx, i) {
			idx = append(idx, i)
		}
	}
	slices.SortFunc(idx, func(a, b int) int {
		return cmp.Or(cmp.Compare(s.segments[a].StartMS, s.segments[b].StartMS), cmp.Compare(a, b))
	})
	var out resolved
	var texts []string
	for _, i := range idx {
		g := s.segments[i]
		out.refs = append(out.refs, SourceRef{Type: RefTranscript, SegmentID: g.ID, StartMS: g.StartMS, EndMS: g.EndMS})
		texts = append(texts, g.Text)
	}
	out.transcript = len(out.refs)
	var seen []string
	for _, id := range noteRefs {
		id = strings.TrimSpace(id)
		t, ok := s.notes[id]
		if !ok || slices.Contains(seen, id) {
			continue
		}
		seen = append(seen, id)
		out.refs = append(out.refs, SourceRef{Type: RefUserNotes, NoteID: id})
		texts = append(texts, t)
	}
	if len(out.refs) > MaxSources {
		out.refs = out.refs[:MaxSources]
	}
	if out.refs == nil {
		out.refs = []SourceRef{}
	}
	out.text = strings.Join(texts, "\n")
	return out
}

var digitRun = regexp.MustCompile(`[0-9]+`)

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// normalizeDigits removes '.', ',' and ' ' sitting between two digits ("60.000" → "60000").
func normalizeDigits(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if (c == '.' || c == ',' || c == ' ') && i > 0 && i+1 < len(r) && isDigit(r[i-1]) && isDigit(r[i+1]) {
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// numbersSupported is the §6.3 step 5 number guard.
func numbersSupported(item, source string) bool {
	src := normalizeDigits(source)
	for _, n := range digitRun.FindAllString(normalizeDigits(item), -1) {
		if !strings.Contains(src, n) {
			return false
		}
	}
	return true
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func trimmedPtr(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return nil
	}
	return &s
}

// guardPIC is §6.3 step 6.
func (s *sources) guardPIC(pic *string, source string) *string {
	p := trimmedPtr(pic)
	if p == nil || utf8.RuneCountInString(*p) > MaxPICChars || !containsFold(source, *p) {
		return nil
	}
	for _, name := range s.participants {
		if strings.EqualFold(name, *p) {
			return &name
		}
	}
	return p
}

// guardDeadline is §6.3 step 7.
func (s *sources) guardDeadline(deadline, phrase *string, source string) (*string, *string) {
	ph := trimmedPtr(phrase)
	if ph == nil || utf8.RuneCountInString(*ph) > MaxPhraseChars || !containsFold(source, *ph) {
		return nil, nil
	}
	d := trimmedPtr(deadline)
	if d == nil || s.meetingDate == nil {
		return nil, ph
	}
	t, err := time.Parse(time.DateOnly, *d)
	if err != nil || t.Before(*s.meetingDate) || t.After(s.meetingDate.AddDate(0, 0, 366)) {
		return nil, ph
	}
	return d, ph
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:max]))
}

type idGen struct{ sec, it int }

func (g *idGen) section() string { g.sec++; return "sec_" + strconv.Itoa(g.sec) }
func (g *idGen) item() string    { g.it++; return "it_" + strconv.Itoa(g.it) }

func (g *idGen) textItems(items []TextItem) {
	for i := range items {
		items[i].ID = g.item()
	}
}

// assignMoMIDs numbers items in display order: agenda, then sections, topics before their points.
func assignMoMIDs(m *MoM) {
	g := &idGen{}
	g.textItems(m.MeetingInfo.Agenda)
	for i := range m.Sections {
		sec := &m.Sections[i]
		g.textItems(sec.Items)
		for j := range sec.Topics {
			sec.Topics[j].ID = g.item()
			g.textItems(sec.Topics[j].Points)
		}
		for j := range sec.Actions {
			sec.Actions[j].ID = g.item()
		}
	}
}

func (s *sources) textItems(in []llmItem) []TextItem {
	out := []TextItem{}
	for _, it := range in {
		text := clip(it.Text, MaxTextChars)
		if text == "" {
			continue
		}
		r := s.resolve(it.Refs, it.NoteRefs)
		out = append(out, TextItem{Text: text, Origin: OriginAI, Sources: r.refs})
	}
	return out
}

var fallbackNotesHeading = map[string]string{"id": "Catatan", "en": "Notes"}

// buildNotes post-processes merged notes output into stored AI notes content.
func (s *sources) buildNotes(n llmNotes) (*AINotes, error) {
	g := &idGen{}
	out := &AINotes{SchemaVersion: SchemaVersion, Language: s.language, Sections: []NotesSection{}}
	for _, sec := range n.Sections {
		items := s.textItems(sec.Items)
		if len(items) == 0 {
			continue
		}
		h := clip(sec.Heading, MaxHeadingChars)
		if h == "" {
			h = fallbackNotesHeading[s.language]
		}
		g.textItems(items)
		out.Sections = append(out.Sections, NotesSection{ID: g.section(), Heading: h, Style: StyleBullet, Items: items})
	}
	if len(out.Sections) == 0 {
		return nil, errEmptyOutput
	}
	return out, nil
}

// buildMoM post-processes merged MoM output; dropped counts AI decisions, action items and
// follow-ups removed for lacking a valid source or for unsupported numbers.
func (s *sources) buildMoM(m llmMoM, info MeetingInfo) (*MoM, int, error) {
	dropped := 0
	info.Agenda = s.textItems(m.Agenda)
	summary := s.textItems(m.Summary)

	topics := []TopicItem{}
	for _, t := range m.Discussion {
		title := clip(t.Title, MaxHeadingChars)
		points := s.textItems(t.Points)
		if title == "" {
			continue
		}
		r := s.resolve(t.Refs, t.NoteRefs)
		topics = append(topics, TopicItem{Title: title, Origin: OriginAI, Sources: r.refs, Points: points})
	}

	decisions, moved := []TextItem{}, []TextItem{}
	for _, it := range m.Decisions {
		text := clip(it.Text, MaxTextChars)
		if text == "" {
			continue
		}
		r := s.resolve(it.Refs, it.NoteRefs)
		switch {
		case r.transcript == 0 && len(r.refs) > 0:
			moved = append(moved, TextItem{Text: text, Origin: OriginAI, Sources: r.refs})
		case r.transcript == 0 || !numbersSupported(text, r.text):
			dropped++
		default:
			decisions = append(decisions, TextItem{Text: text, Origin: OriginAI, Sources: r.refs})
		}
	}

	actions := []ActionItem{}
	for _, a := range m.ActionItems {
		desc := clip(a.Description, MaxTextChars)
		if desc == "" {
			continue
		}
		r := s.resolve(a.Refs, a.NoteRefs)
		if len(r.refs) == 0 || !numbersSupported(desc, r.text) {
			dropped++
			continue
		}
		deadline, phrase := s.guardDeadline(a.Deadline, a.DeadlinePhrase, r.text)
		actions = append(actions, ActionItem{Description: desc, PIC: s.guardPIC(a.PIC, r.text),
			Deadline: deadline, DeadlinePhrase: phrase, Origin: OriginAI, Sources: r.refs})
	}

	openIssues := append(s.textItems(m.OpenIssues), moved...)

	followUps := []TextItem{}
	for _, it := range s.textItems(m.FollowUps) {
		if len(it.Sources) == 0 {
			dropped++
			continue
		}
		followUps = append(followUps, it)
	}

	if len(info.Agenda)+len(summary)+len(topics)+len(decisions)+len(actions)+len(openIssues)+len(followUps) == 0 {
		return nil, dropped, errEmptyOutput
	}
	sec := func(typ string) MoMSection {
		return MoMSection{ID: typ, Type: typ, Heading: DefaultHeading(s.language, typ), Style: DefaultStyle(typ)}
	}
	sum, disc, dec, act, open, fol := sec(SectionSummary), sec(SectionDiscussion), sec(SectionDecisions),
		sec(SectionActionItems), sec(SectionOpenIssues), sec(SectionFollowUps)
	sum.Items, disc.Topics, dec.Items, act.Actions, open.Items, fol.Items = summary, topics, decisions, actions, openIssues, followUps
	out := &MoM{SchemaVersion: SchemaVersion, Language: s.language, MeetingInfo: info,
		Sections: []MoMSection{sum, disc, dec, act, open, fol}}
	assignMoMIDs(out)
	return out, dropped, nil
}

// finalizeContent re-validates generated content exactly like a client write, so stored
// AI content always satisfies api.md §3.
func finalizeContent(kind Kind, c Content, pins Pins) ([]byte, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	v, err := ValidateContent(kind, raw, pins)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
