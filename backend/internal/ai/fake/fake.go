package fake

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"

	"phoebe/internal/ai"
)

const segmentMS = 15000

var sentences = []string{
	"Selamat pagi semua, mari kita mulai rapatnya.",
	"Agenda hari ini adalah membahas progres proyek.",
	"Kita sepakat rilis hari Jumat.",
	"Budi akan menyiapkan laporan minggu depan.",
	"Masih ada pertanyaan soal anggaran yang belum terjawab.",
	"Baik, rapat kita tutup di sini.",
}

// STT is deterministic. Call i returns Errs[i] if non-nil, else Results[i] if present, else
// segments every 15 s of in.DurationMS. Block waits for ctx to end (provider timeout).
type STT struct {
	Results []ai.STTResult
	Errs    []error
	Block   bool

	mu    sync.Mutex
	calls int
}

func (f *STT) Name() (string, string) { return "fake", "fake-stt" }

func (f *STT) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *STT) Transcribe(ctx context.Context, in ai.STTInput) (ai.STTResult, error) {
	f.mu.Lock()
	i := f.calls
	f.calls++
	f.mu.Unlock()
	if f.Block {
		<-ctx.Done()
		return ai.STTResult{}, ai.TransportError(ctx, ctx.Err())
	}
	if i < len(f.Errs) && f.Errs[i] != nil {
		return ai.STTResult{}, f.Errs[i]
	}
	if i < len(f.Results) {
		return f.Results[i], nil
	}
	return transcript(in.DurationMS, in.Language), nil
}

func transcript(durationMS int64, language string) ai.STTResult {
	if language != "en" {
		language = "id"
	}
	res := ai.STTResult{Language: language, AudioMS: durationMS, Segments: []ai.RawSegment{}}
	for k, start := 0, int64(0); start < durationMS; k, start = k+1, start+segmentMS {
		end := min(start+segmentMS, durationMS)
		res.Segments = append(res.Segments, ai.RawSegment{
			StartSec: float64(start) / 1000,
			EndSec:   float64(end) / 1000,
			Text:     sentences[k%len(sentences)],
		})
	}
	return res
}

// LLM is deterministic: it parses the <meeting_data> JSON of the user message and answers with
// notes JSON, or MoM JSON when the input carries an "ai_notes" key. Scripting as for STT.
type LLM struct {
	Results []ai.LLMResult
	Errs    []error
	Block   bool

	mu    sync.Mutex
	calls int
}

func (f *LLM) Name() (string, string) { return "fake", "fake-llm" }

func (f *LLM) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *LLM) Complete(ctx context.Context, system, user string) (ai.LLMResult, error) {
	f.mu.Lock()
	i := f.calls
	f.calls++
	f.mu.Unlock()
	if f.Block {
		<-ctx.Done()
		return ai.LLMResult{}, ai.TransportError(ctx, ctx.Err())
	}
	if i < len(f.Errs) && f.Errs[i] != nil {
		return ai.LLMResult{}, f.Errs[i]
	}
	if i < len(f.Results) {
		r := f.Results[i]
		if r.FinishReason == "length" {
			return r, ai.InvalidOutput(0, "finish_reason length")
		}
		return r, nil
	}
	content, err := generate(user)
	if err != nil {
		return ai.LLMResult{}, err
	}
	return ai.LLMResult{
		Content:      content,
		FinishReason: "stop",
		InputTokens:  (len(system) + len(user) + 3) / 4,
		OutputTokens: (len(content) + 3) / 4,
	}, nil
}

type input struct {
	Meeting struct {
		Participants []string `json:"participants"`
	} `json:"meeting"`
	OutputLanguage string          `json:"output_language"`
	AINotes        json.RawMessage `json:"ai_notes"`
	Transcript     []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"transcript"`
}

type item struct {
	Text     string   `json:"text"`
	Refs     []string `json:"refs"`
	NoteRefs []string `json:"note_refs"`
}

type section struct {
	Heading string `json:"heading"`
	Items   []item `json:"items"`
}

type topic struct {
	Title    string   `json:"title"`
	Refs     []string `json:"refs"`
	NoteRefs []string `json:"note_refs"`
	Points   []item   `json:"points"`
}

type actionItem struct {
	Description    string   `json:"description"`
	PIC            *string  `json:"pic"`
	Deadline       *string  `json:"deadline"`
	DeadlinePhrase *string  `json:"deadline_phrase"`
	Refs           []string `json:"refs"`
	NoteRefs       []string `json:"note_refs"`
}

type mom struct {
	Agenda      []item       `json:"agenda"`
	Summary     []item       `json:"summary"`
	Discussion  []topic      `json:"discussion"`
	Decisions   []item       `json:"decisions"`
	ActionItems []actionItem `json:"action_items"`
	OpenIssues  []item       `json:"open_issues"`
	FollowUps   []item       `json:"follow_ups"`
}

var (
	decisionRe = regexp.MustCompile(`(?i)\b(sepakat|setuju|agree|decide)`)
	actionRe   = regexp.MustCompile(`(?i)\b(akan|will)\b`)
)

func generate(user string) (string, error) {
	data := user
	if a := strings.Index(user, "<meeting_data>"); a >= 0 {
		data = user[a+len("<meeting_data>"):]
		if b := strings.LastIndex(data, "</meeting_data>"); b >= 0 {
			data = data[:b]
		}
	}
	var in input
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &in); err != nil {
		return "", errors.New("fake llm: undecodable meeting_data")
	}

	var out any
	if in.AINotes == nil {
		heading := "Ringkasan"
		if in.OutputLanguage == "en" {
			heading = "Summary"
		}
		items := []item{}
		for _, s := range in.Transcript[:min(3, len(in.Transcript))] {
			items = append(items, ref(s.Text, s.ID))
		}
		sections := []section{}
		if len(items) > 0 {
			sections = append(sections, section{Heading: heading, Items: items})
		}
		out = map[string]any{"sections": sections}
	} else {
		m := mom{Agenda: []item{}, Summary: []item{}, Discussion: []topic{}, Decisions: []item{},
			ActionItems: []actionItem{}, OpenIssues: []item{}, FollowUps: []item{}}
		if len(in.Transcript) > 0 {
			m.Summary = append(m.Summary, ref(in.Transcript[0].Text, in.Transcript[0].ID))
		}
		for _, s := range in.Transcript {
			if decisionRe.MatchString(s.Text) {
				m.Decisions = append(m.Decisions, ref(s.Text, s.ID))
			}
			if actionRe.MatchString(s.Text) {
				m.ActionItems = append(m.ActionItems, actionItem{
					Description: s.Text, PIC: pic(s.Text, in.Meeting.Participants),
					Refs: []string{s.ID}, NoteRefs: []string{},
				})
			}
		}
		out = m
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func ref(text, id string) item {
	return item{Text: text, Refs: []string{id}, NoteRefs: []string{}}
}

func pic(text string, participants []string) *string {
	lt := strings.ToLower(text)
	for _, p := range participants {
		if p = strings.TrimSpace(p); p != "" && strings.Contains(lt, strings.ToLower(p)) {
			return &p
		}
	}
	return nil
}
