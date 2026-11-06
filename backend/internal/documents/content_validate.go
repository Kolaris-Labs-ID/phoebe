package documents

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"phoebe/internal/apperr"
	"phoebe/internal/jsonx"
)

const (
	MaxContentBytes     = 1 << 20
	MaxSections         = 50
	MaxSectionItems     = 500
	MaxTopicPoints      = 100
	MaxSources          = 50
	MaxManualNotes      = 1000
	MaxManualNoteChars  = 2000
	MaxTextChars        = 10000
	MaxHeadingChars     = 200
	MaxParticipants     = 100
	MaxParticipantChars = 100
	MaxPICChars         = 100
	MaxPhraseChars      = 200
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Span struct{ StartMS, EndMS int64 }

// Pins are the sources pinned by the base revision; refs are validated against them.
// A nil map means that source is not pinned, so any ref of that type is invalid.
type Pins struct {
	Segments map[string]Span
	NoteIDs  map[string]bool
}

// ValidateContent strictly validates client content (api.md §3) and returns it
// normalized: strings trimmed, unknown fields dropped, transcript ref times taken
// from the pinned transcript. Failures are *apperr.Error invalid_content.
func ValidateContent(kind Kind, raw []byte, pins Pins) (Content, error) {
	if len(raw) > MaxContentBytes {
		return nil, apperr.Content("", apperr.ReasonTooLong)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, apperr.Content("", apperr.ReasonInvalid)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, apperr.Content("", apperr.ReasonInvalid)
	}
	v := &validator{pins: pins, ids: map[string]bool{}}
	var c Content
	switch kind {
	case KindManualNotes:
		c = v.manualNotes(tree)
	case KindAINotes:
		c = v.aiNotes(tree)
	case KindMoM:
		c = v.mom(tree)
	default:
		return nil, apperr.Content("", apperr.ReasonInvalid)
	}
	if v.err != nil {
		return nil, v.err
	}
	return c, nil
}

// validator records the first error; every method is a no-op once err is set.
type validator struct {
	pins Pins
	ids  map[string]bool
	err  *apperr.Error
}

func (v *validator) fail(path, reason string) {
	if v.err == nil {
		v.err = apperr.Content(path, reason)
	}
}

func at(p string, k any) string {
	switch k := k.(type) {
	case int:
		return p + "/" + strconv.Itoa(k)
	default:
		return p + "/" + k.(string)
	}
}

func (v *validator) object(p string, x any) map[string]any {
	if v.err != nil {
		return nil
	}
	m, ok := x.(map[string]any)
	if !ok {
		v.fail(p, apperr.ReasonInvalid)
	}
	return m
}

func (v *validator) key(m map[string]any, p, k string) (any, bool) {
	if v.err != nil {
		return nil, false
	}
	x, ok := m[k]
	if !ok {
		v.fail(at(p, k), apperr.ReasonRequired)
	}
	return x, ok
}

func (v *validator) array(m map[string]any, p, k string, max int) []any {
	x, ok := v.key(m, p, k)
	if !ok {
		return nil
	}
	a, ok := x.([]any)
	if !ok {
		v.fail(at(p, k), apperr.ReasonInvalid)
		return nil
	}
	if len(a) > max {
		v.fail(at(p, k), apperr.ReasonTooLong)
		return nil
	}
	return a
}

func (v *validator) checkText(p string, x any, min, max int) string {
	s, ok := x.(string)
	if !ok {
		v.fail(p, apperr.ReasonInvalid)
		return ""
	}
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	switch {
	case n < min:
		v.fail(p, apperr.ReasonTooShort)
	case n > max:
		v.fail(p, apperr.ReasonTooLong)
	}
	return s
}

func (v *validator) text(m map[string]any, p, k string, min, max int) string {
	x, ok := v.key(m, p, k)
	if !ok {
		return ""
	}
	return v.checkText(at(p, k), x, min, max)
}

func (v *validator) nullableText(m map[string]any, p, k string, max int) *string {
	x, ok := v.key(m, p, k)
	if !ok || x == nil {
		return nil
	}
	s := v.checkText(at(p, k), x, 1, max)
	return &s
}

func (v *validator) enum(m map[string]any, p, k string, allowed ...string) string {
	x, ok := v.key(m, p, k)
	if !ok {
		return ""
	}
	s, _ := x.(string)
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	v.fail(at(p, k), apperr.ReasonInvalid)
	return ""
}

func (v *validator) boolean(m map[string]any, p, k string) bool {
	x, ok := v.key(m, p, k)
	if !ok {
		return false
	}
	b, ok := x.(bool)
	if !ok {
		v.fail(at(p, k), apperr.ReasonInvalid)
	}
	return b
}

func (v *validator) id(m map[string]any, p string) string {
	x, ok := v.key(m, p, "id")
	if !ok {
		return ""
	}
	s, _ := x.(string)
	switch {
	case !idPattern.MatchString(s):
		v.fail(at(p, "id"), apperr.ReasonInvalid)
	case v.ids[s]:
		v.fail(at(p, "id"), apperr.ReasonDuplicate)
	default:
		v.ids[s] = true
	}
	return s
}

func (v *validator) schemaVersion(m map[string]any) {
	x, ok := v.key(m, "", "schema_version")
	if !ok {
		return
	}
	if n, ok := x.(json.Number); !ok || n.String() != strconv.Itoa(SchemaVersion) {
		v.fail("/schema_version", apperr.ReasonInvalid)
	}
}

func (v *validator) manualNotes(tree any) *ManualNotes {
	m := v.object("", tree)
	v.schemaVersion(m)
	items := v.array(m, "", "items", MaxManualNotes)
	out := &ManualNotes{SchemaVersion: SchemaVersion, Items: make([]ManualNote, 0, len(items))}
	for i, x := range items {
		p := at("/items", i)
		im := v.object(p, x)
		n := ManualNote{ID: v.id(im, p), Text: v.text(im, p, "text", 1, MaxManualNoteChars)}
		if ax, ok := v.key(im, p, "at_ms"); ok && ax != nil {
			num, isNum := ax.(json.Number)
			ms, err := num.Int64()
			switch {
			case !isNum || err != nil:
				v.fail(at(p, "at_ms"), apperr.ReasonInvalid)
			case ms < 0:
				v.fail(at(p, "at_ms"), apperr.ReasonOutOfRange)
			default:
				n.AtMS = &ms
			}
		}
		out.Items = append(out.Items, n)
	}
	return out
}

func (v *validator) aiNotes(tree any) *AINotes {
	m := v.object("", tree)
	v.schemaVersion(m)
	out := &AINotes{SchemaVersion: SchemaVersion, Language: v.enum(m, "", "language", "id", "en")}
	secs := v.array(m, "", "sections", MaxSections)
	out.Sections = make([]NotesSection, 0, len(secs))
	for i, x := range secs {
		p := at("/sections", i)
		sm := v.object(p, x)
		s := NotesSection{
			ID:      v.id(sm, p),
			Heading: v.text(sm, p, "heading", 1, MaxHeadingChars),
			Style:   v.enum(sm, p, "style", StyleBullet, StyleNumbered, StyleParagraph),
		}
		s.Items = v.textItems(sm, p, "items", MaxSectionItems)
		out.Sections = append(out.Sections, s)
	}
	return out
}

func (v *validator) mom(tree any) *MoM {
	m := v.object("", tree)
	v.schemaVersion(m)
	out := &MoM{SchemaVersion: SchemaVersion, Language: v.enum(m, "", "language", "id", "en")}

	const ip = "/meeting_info"
	x, _ := v.key(m, "", "meeting_info")
	im := v.object(ip, x)
	// title mirrors meetings.title, which may be empty; exports render a fallback.
	out.MeetingInfo.Title = v.text(im, ip, "title", 0, MaxHeadingChars)
	if sx, ok := v.key(im, ip, "started_at"); ok && sx != nil {
		s, _ := sx.(string)
		if t, err := time.Parse(time.RFC3339Nano, s); err != nil {
			v.fail(ip+"/started_at", apperr.ReasonInvalid)
		} else {
			jt := jsonx.NewTime(t)
			out.MeetingInfo.StartedAt = &jt
		}
	}
	out.MeetingInfo.Timezone = v.text(im, ip, "timezone", 1, 64)
	if v.err == nil {
		if _, err := time.LoadLocation(out.MeetingInfo.Timezone); err != nil || out.MeetingInfo.Timezone == "Local" {
			v.fail(ip+"/timezone", apperr.ReasonInvalid)
		}
	}
	parts := v.array(im, ip, "participants", MaxParticipants)
	out.MeetingInfo.Participants = make([]string, 0, len(parts))
	for i, px := range parts {
		out.MeetingInfo.Participants = append(out.MeetingInfo.Participants, v.checkText(at(ip+"/participants", i), px, 1, MaxParticipantChars))
	}
	out.MeetingInfo.Agenda = v.textItems(im, ip, "agenda", MaxSectionItems)

	secs := v.array(m, "", "sections", MaxSections)
	out.Sections = make([]MoMSection, 0, len(secs))
	seen := map[string]bool{}
	for i, x := range secs {
		p := at("/sections", i)
		sm := v.object(p, x)
		s := MoMSection{ID: v.id(sm, p)}
		s.Type = v.enum(sm, p, "type", SectionSummary, SectionDiscussion, SectionDecisions, SectionActionItems, SectionOpenIssues, SectionFollowUps, SectionCustom)
		if v.err == nil && s.Type != SectionCustom {
			if seen[s.Type] {
				v.fail(p+"/type", apperr.ReasonDuplicate)
			}
			seen[s.Type] = true
		}
		s.Heading = v.text(sm, p, "heading", 1, MaxHeadingChars)
		if s.Type == SectionActionItems {
			if st, ok := v.key(sm, p, "style"); ok && st != nil {
				v.fail(p+"/style", apperr.ReasonInvalid)
			}
		} else {
			st := v.enum(sm, p, "style", StyleBullet, StyleNumbered, StyleParagraph)
			s.Style = &st
		}
		switch s.Type {
		case SectionDiscussion:
			s.Topics = v.topics(sm, p)
		case SectionActionItems:
			s.Actions = v.actions(sm, p)
		default:
			s.Items = v.textItems(sm, p, "items", MaxSectionItems)
			if s.Type == SectionDecisions {
				for j, it := range s.Items {
					v.requireAISource(at(at(p+"/items", j), "sources"), it.Origin, it.Sources)
				}
			}
		}
		out.Sections = append(out.Sections, s)
	}
	return out
}

// requireAISource rejects rather than drops: a client never gets success for content
// the server did not store.
func (v *validator) requireAISource(p, origin string, sources []SourceRef) {
	if origin == OriginAI && len(sources) == 0 {
		v.fail(p, apperr.ReasonRequired)
	}
}

func (v *validator) textItems(m map[string]any, p, k string, max int) []TextItem {
	a := v.array(m, p, k, max)
	out := make([]TextItem, 0, len(a))
	for i, x := range a {
		ip := at(at(p, k), i)
		im := v.object(ip, x)
		it := TextItem{ID: v.id(im, ip), Text: v.text(im, ip, "text", 1, MaxTextChars)}
		it.Origin, it.Edited, it.Sources = v.provenance(im, ip)
		out = append(out, it)
	}
	return out
}

func (v *validator) topics(m map[string]any, p string) []TopicItem {
	a := v.array(m, p, "items", MaxSectionItems)
	out := make([]TopicItem, 0, len(a))
	for i, x := range a {
		ip := at(p+"/items", i)
		im := v.object(ip, x)
		t := TopicItem{ID: v.id(im, ip), Title: v.text(im, ip, "title", 1, MaxHeadingChars)}
		t.Origin, t.Edited, t.Sources = v.provenance(im, ip)
		t.Points = v.textItems(im, ip, "points", MaxTopicPoints)
		out = append(out, t)
	}
	return out
}

func (v *validator) actions(m map[string]any, p string) []ActionItem {
	a := v.array(m, p, "items", MaxSectionItems)
	out := make([]ActionItem, 0, len(a))
	for i, x := range a {
		ip := at(p+"/items", i)
		im := v.object(ip, x)
		ai := ActionItem{
			ID:          v.id(im, ip),
			Description: v.text(im, ip, "description", 1, MaxTextChars),
			PIC:         v.nullableText(im, ip, "pic", MaxPICChars),
			Deadline:    v.nullableText(im, ip, "deadline", 10),
		}
		if ai.Deadline != nil && v.err == nil {
			if _, err := time.Parse(time.DateOnly, *ai.Deadline); err != nil {
				v.fail(ip+"/deadline", apperr.ReasonInvalid)
			}
		}
		ai.DeadlinePhrase = v.nullableText(im, ip, "deadline_phrase", MaxPhraseChars)
		ai.Origin, ai.Edited, ai.Sources = v.provenance(im, ip)
		v.requireAISource(ip+"/sources", ai.Origin, ai.Sources)
		out = append(out, ai)
	}
	return out
}

func (v *validator) provenance(m map[string]any, p string) (string, bool, []SourceRef) {
	origin := v.enum(m, p, "origin", OriginAI, OriginHuman)
	edited := v.boolean(m, p, "edited")
	a := v.array(m, p, "sources", MaxSources)
	refs := make([]SourceRef, 0, len(a))
	for i, x := range a {
		rp := at(p+"/sources", i)
		rm := v.object(rp, x)
		switch v.enum(rm, rp, "type", RefTranscript, RefUserNotes) {
		case RefTranscript:
			sid, _ := rm["segment_id"].(string)
			span, ok := v.pins.Segments[sid]
			switch {
			case v.pins.Segments == nil:
				v.fail(rp, apperr.ReasonNotFound)
			case sid == "":
				v.fail(rp+"/segment_id", apperr.ReasonRequired)
			case !ok:
				v.fail(rp+"/segment_id", apperr.ReasonNotFound)
			}
			refs = append(refs, SourceRef{Type: RefTranscript, SegmentID: sid, StartMS: span.StartMS, EndMS: span.EndMS})
		case RefUserNotes:
			nid, _ := rm["note_id"].(string)
			switch {
			case v.pins.NoteIDs == nil:
				v.fail(rp, apperr.ReasonNotFound)
			case nid == "":
				v.fail(rp+"/note_id", apperr.ReasonRequired)
			case !v.pins.NoteIDs[nid]:
				v.fail(rp+"/note_id", apperr.ReasonNotFound)
			}
			refs = append(refs, SourceRef{Type: RefUserNotes, NoteID: nid})
		}
	}
	return origin, edited, refs
}
