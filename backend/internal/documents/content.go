package documents

import (
	"encoding/json"
	"fmt"

	"phoebe/internal/jsonx"
)

type Kind string

const (
	KindManualNotes Kind = "manual_notes"
	KindAINotes     Kind = "ai_notes"
	KindMoM         Kind = "mom"
)

func ParseKind(s string) (Kind, bool) {
	switch k := Kind(s); k {
	case KindManualNotes, KindAINotes, KindMoM:
		return k, true
	}
	return "", false
}

const SchemaVersion = 1

const (
	RefTranscript = "transcript"
	RefUserNotes  = "user_notes"

	OriginAI    = "ai"
	OriginHuman = "human"

	StyleBullet    = "bullet"
	StyleNumbered  = "numbered"
	StyleParagraph = "paragraph"

	SectionSummary     = "summary"
	SectionDiscussion  = "discussion"
	SectionDecisions   = "decisions"
	SectionActionItems = "action_items"
	SectionOpenIssues  = "open_issues"
	SectionFollowUps   = "follow_ups"
	SectionCustom      = "custom"
)

// StandardSections is the stored order of the non-custom MoM sections.
var StandardSections = []string{SectionSummary, SectionDiscussion, SectionDecisions, SectionActionItems, SectionOpenIssues, SectionFollowUps}

var defaultHeadings = map[string]map[string]string{
	"id": {"agenda": "Agenda", SectionSummary: "Ringkasan", SectionDiscussion: "Pembahasan", SectionDecisions: "Keputusan",
		SectionActionItems: "Action items", SectionOpenIssues: "Hal terbuka", SectionFollowUps: "Tindak lanjut"},
	"en": {"agenda": "Agenda", SectionSummary: "Summary", SectionDiscussion: "Discussion", SectionDecisions: "Decisions",
		SectionActionItems: "Action items", SectionOpenIssues: "Open issues", SectionFollowUps: "Follow-ups"},
}

// DefaultHeading returns the localized default heading for a MoM section type or "agenda".
func DefaultHeading(language, sectionType string) string {
	h, ok := defaultHeadings[language]
	if !ok {
		h = defaultHeadings["id"]
	}
	return h[sectionType]
}

// DefaultStyle is the default style of a standard MoM section; nil for action_items.
func DefaultStyle(sectionType string) *string {
	var s string
	switch sectionType {
	case SectionActionItems:
		return nil
	case SectionSummary:
		s = StyleParagraph
	case SectionDecisions:
		s = StyleNumbered
	default:
		s = StyleBullet
	}
	return &s
}

type Content interface{ Kind() Kind }

type ManualNotes struct {
	SchemaVersion int          `json:"schema_version"`
	Items         []ManualNote `json:"items"`
}

type ManualNote struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	AtMS *int64 `json:"at_ms"`
}

type AINotes struct {
	SchemaVersion int            `json:"schema_version"`
	Language      string         `json:"language"`
	Sections      []NotesSection `json:"sections"`
}

type NotesSection struct {
	ID      string     `json:"id"`
	Heading string     `json:"heading"`
	Style   string     `json:"style"`
	Items   []TextItem `json:"items"`
}

type MoM struct {
	SchemaVersion int          `json:"schema_version"`
	Language      string       `json:"language"`
	MeetingInfo   MeetingInfo  `json:"meeting_info"`
	Sections      []MoMSection `json:"sections"`
}

type MeetingInfo struct {
	Title        string      `json:"title"`
	StartedAt    *jsonx.Time `json:"started_at"`
	Timezone     string      `json:"timezone"`
	Participants []string    `json:"participants"`
	Agenda       []TextItem  `json:"agenda"`
}

// MoMSection holds its items in the field matching Type: Topics for discussion,
// Actions for action_items, Items otherwise. On the wire all three are "items".
type MoMSection struct {
	ID      string
	Type    string
	Heading string
	Style   *string
	Items   []TextItem
	Topics  []TopicItem
	Actions []ActionItem
}

type TextItem struct {
	ID      string      `json:"id"`
	Text    string      `json:"text"`
	Origin  string      `json:"origin"`
	Edited  bool        `json:"edited"`
	Sources []SourceRef `json:"sources"`
}

type TopicItem struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	Origin  string      `json:"origin"`
	Edited  bool        `json:"edited"`
	Sources []SourceRef `json:"sources"`
	Points  []TextItem  `json:"points"`
}

type ActionItem struct {
	ID             string      `json:"id"`
	Description    string      `json:"description"`
	PIC            *string     `json:"pic"`
	Deadline       *string     `json:"deadline"`
	DeadlinePhrase *string     `json:"deadline_phrase"`
	Origin         string      `json:"origin"`
	Edited         bool        `json:"edited"`
	Sources        []SourceRef `json:"sources"`
}

// SourceRef is {type:"transcript", segment_id, start_ms, end_ms, approximate} or {type:"user_notes", note_id}.
// Approximate is server-set from the transcript revision's timing ("chunk"), never from LLM or client input.
type SourceRef struct {
	Type        string
	SegmentID   string
	StartMS     int64
	EndMS       int64
	Approximate bool
	NoteID      string
}

func (*ManualNotes) Kind() Kind { return KindManualNotes }
func (*AINotes) Kind() Kind     { return KindAINotes }
func (*MoM) Kind() Kind         { return KindMoM }

func (r SourceRef) MarshalJSON() ([]byte, error) {
	switch r.Type {
	case RefTranscript:
		return json.Marshal(struct {
			Type        string `json:"type"`
			SegmentID   string `json:"segment_id"`
			StartMS     int64  `json:"start_ms"`
			EndMS       int64  `json:"end_ms"`
			Approximate bool   `json:"approximate"`
		}{r.Type, r.SegmentID, r.StartMS, r.EndMS, r.Approximate})
	case RefUserNotes:
		return json.Marshal(struct {
			Type   string `json:"type"`
			NoteID string `json:"note_id"`
		}{r.Type, r.NoteID})
	}
	return nil, fmt.Errorf("documents: unknown source ref type %q", r.Type)
}

func (r *SourceRef) UnmarshalJSON(b []byte) error {
	var v struct {
		Type        string `json:"type"`
		SegmentID   string `json:"segment_id"`
		StartMS     int64  `json:"start_ms"`
		EndMS       int64  `json:"end_ms"`
		Approximate bool   `json:"approximate"`
		NoteID      string `json:"note_id"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*r = SourceRef(v)
	return nil
}

type momSectionWire struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Heading string          `json:"heading"`
	Style   *string         `json:"style"`
	Items   json.RawMessage `json:"items"`
}

func (s MoMSection) MarshalJSON() ([]byte, error) {
	var items any
	switch s.Type {
	case SectionDiscussion:
		items = nonNil(s.Topics)
	case SectionActionItems:
		items = nonNil(s.Actions)
	default:
		items = nonNil(s.Items)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return json.Marshal(momSectionWire{s.ID, s.Type, s.Heading, s.Style, raw})
}

func (s *MoMSection) UnmarshalJSON(b []byte) error {
	var w momSectionWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*s = MoMSection{ID: w.ID, Type: w.Type, Heading: w.Heading, Style: w.Style}
	if len(w.Items) == 0 {
		return nil
	}
	switch w.Type {
	case SectionDiscussion:
		return json.Unmarshal(w.Items, &s.Topics)
	case SectionActionItems:
		return json.Unmarshal(w.Items, &s.Actions)
	default:
		return json.Unmarshal(w.Items, &s.Items)
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// DecodeContent decodes stored (already validated) content.
func DecodeContent(kind Kind, raw []byte) (Content, error) {
	var c Content
	switch kind {
	case KindManualNotes:
		c = &ManualNotes{}
	case KindAINotes:
		c = &AINotes{}
	case KindMoM:
		c = &MoM{}
	default:
		return nil, fmt.Errorf("documents: unknown kind %q", kind)
	}
	if err := json.Unmarshal(raw, c); err != nil {
		return nil, err
	}
	return c, nil
}
