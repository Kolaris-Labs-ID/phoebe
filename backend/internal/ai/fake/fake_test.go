package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"phoebe/internal/ai"
	"phoebe/internal/jobs"
)

func TestSTTDeterministicSegments(t *testing.T) {
	f := &STT{}
	res, err := f.Transcribe(context.Background(), ai.STTInput{DurationMS: 100_000, Language: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Language != "id" || res.AudioMS != 100_000 || len(res.Segments) != 7 {
		t.Fatalf("result %+v", res)
	}
	for k, s := range res.Segments {
		if s.StartSec != float64(k*15) {
			t.Errorf("seg %d start %v", k, s.StartSec)
		}
		if s.NoSpeechProb != 0 || s.AvgLogprob != 0 || s.CompressionRatio != 0 || s.Text == "" {
			t.Errorf("seg %d %+v", k, s)
		}
	}
	if last := res.Segments[6]; last.EndSec != 100 {
		t.Errorf("last end %v", last.EndSec)
	}
	texts := ""
	for _, s := range res.Segments {
		texts += s.Text + "\n"
	}
	if !strings.Contains(texts, "Kita sepakat rilis hari Jumat.") || !strings.Contains(texts, "Budi akan menyiapkan laporan minggu depan.") {
		t.Errorf("missing decision/action sentences:\n%s", texts)
	}
	again, _ := (&STT{}).Transcribe(context.Background(), ai.STTInput{DurationMS: 100_000, Language: "auto"})
	a, _ := json.Marshal(res)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Error("not deterministic")
	}
	if en, _ := f.Transcribe(context.Background(), ai.STTInput{DurationMS: 1, Language: "en"}); en.Language != "en" || len(en.Segments) != 1 {
		t.Errorf("en %+v", en)
	}
	if zero, _ := f.Transcribe(context.Background(), ai.STTInput{}); len(zero.Segments) != 0 {
		t.Errorf("zero duration %+v", zero)
	}
	if f.Calls() != 3 {
		t.Errorf("calls %d", f.Calls())
	}
}

func TestSTTScripted(t *testing.T) {
	boom := ai.NewError(jobs.CatProviderUnavailable, true, 503, "")
	f := &STT{Errs: []error{boom}, Results: []ai.STTResult{{}, {Segments: []ai.RawSegment{{Text: "x", NoSpeechProb: 0.9}}}}}
	if _, err := f.Transcribe(context.Background(), ai.STTInput{DurationMS: 30000}); !errors.Is(err, boom) {
		t.Errorf("call 0: %v", err)
	}
	if r, err := f.Transcribe(context.Background(), ai.STTInput{DurationMS: 30000}); err != nil || len(r.Segments) != 1 || r.Segments[0].NoSpeechProb != 0.9 {
		t.Errorf("call 1: %+v %v", r, err)
	}
	if r, _ := f.Transcribe(context.Background(), ai.STTInput{DurationMS: 30000}); len(r.Segments) != 2 {
		t.Errorf("call 2 default: %+v", r)
	}
}

func TestBlockReturnsProviderTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := (&STT{Block: true}).Transcribe(ctx, ai.STTInput{DurationMS: 1000})
	if ai.CategoryOf(err) != jobs.CatProviderTimeout {
		t.Errorf("stt %v", err)
	}
	_, err = (&LLM{Block: true}).Complete(ctx, "s", "u")
	if ai.CategoryOf(err) != jobs.CatProviderTimeout {
		t.Errorf("llm %v", err)
	}
}

type strictItem struct {
	Text     *string   `json:"text"`
	Refs     *[]string `json:"refs"`
	NoteRefs *[]string `json:"note_refs"`
}

type strictNotes struct {
	Sections *[]struct {
		Heading *string       `json:"heading"`
		Items   *[]strictItem `json:"items"`
	} `json:"sections"`
}

type strictMoM struct {
	Agenda     *[]strictItem `json:"agenda"`
	Summary    *[]strictItem `json:"summary"`
	Discussion *[]struct {
		Title    *string       `json:"title"`
		Refs     *[]string     `json:"refs"`
		NoteRefs *[]string     `json:"note_refs"`
		Points   *[]strictItem `json:"points"`
	} `json:"discussion"`
	Decisions   *[]strictItem `json:"decisions"`
	ActionItems *[]struct {
		Description    *string   `json:"description"`
		PIC            *string   `json:"pic"`
		Deadline       *string   `json:"deadline"`
		DeadlinePhrase *string   `json:"deadline_phrase"`
		Refs           *[]string `json:"refs"`
		NoteRefs       *[]string `json:"note_refs"`
	} `json:"action_items"`
	OpenIssues *[]strictItem `json:"open_issues"`
	FollowUps  *[]strictItem `json:"follow_ups"`
}

func strictDecode(t *testing.T, s string, v any) {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		t.Fatalf("schema: %v\n%s", err, s)
	}
}

func userMessage(t *testing.T, payload map[string]any) string {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return "<meeting_data>\n" + string(b) + "\n</meeting_data>"
}

func meetingPayload(withAINotes bool) map[string]any {
	transcript := []map[string]any{}
	for i, text := range []string{
		"Selamat pagi semua.",
		"Kita sepakat rilis hari Jumat.",
		"Budi akan kirim laporan.",
		"Sari setuju dengan anggaran.",
		"Tim akan review minggu depan.",
	} {
		transcript = append(transcript, map[string]any{"id": "s" + string(rune('1'+i)), "start_ms": i * 15000, "end_ms": (i + 1) * 15000, "text": text})
	}
	p := map[string]any{
		"meeting":         map[string]any{"title": "Rapat", "date": "2026-11-06", "timezone": "Asia/Jakarta", "participants": []string{"Budi", "Sari"}},
		"output_language": "id",
		"manual_notes":    []map[string]any{{"id": "n1", "text": "catatan", "at_ms": 1000}},
		"transcript":      transcript,
	}
	if withAINotes {
		p["ai_notes"] = map[string]any{"sections": []any{}}
	}
	return p
}

func TestLLMNotes(t *testing.T) {
	f := &LLM{}
	user := userMessage(t, meetingPayload(false))
	res, err := f.Complete(context.Background(), "system", user)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinishReason != "stop" || res.InputTokens <= 0 || res.OutputTokens <= 0 {
		t.Errorf("result %+v", res)
	}
	var n strictNotes
	strictDecode(t, res.Content, &n)
	if n.Sections == nil || len(*n.Sections) != 1 {
		t.Fatalf("sections %s", res.Content)
	}
	sec := (*n.Sections)[0]
	if sec.Heading == nil || *sec.Heading == "" || sec.Items == nil || len(*sec.Items) != 3 {
		t.Fatalf("section %s", res.Content)
	}
	for i, it := range *sec.Items {
		if it.Text == nil || it.Refs == nil || it.NoteRefs == nil || len(*it.Refs) != 1 || (*it.Refs)[0] != "s"+string(rune('1'+i)) {
			t.Errorf("item %d %s", i, res.Content)
		}
	}
	again, _ := (&LLM{}).Complete(context.Background(), "system", user)
	if again.Content != res.Content {
		t.Error("not deterministic")
	}
}

func TestLLMMoM(t *testing.T) {
	res, err := (&LLM{}).Complete(context.Background(), "system", userMessage(t, meetingPayload(true)))
	if err != nil {
		t.Fatal(err)
	}
	var m strictMoM
	strictDecode(t, res.Content, &m)
	if m.Agenda == nil || m.Summary == nil || m.Discussion == nil || m.Decisions == nil || m.ActionItems == nil || m.OpenIssues == nil || m.FollowUps == nil {
		t.Fatalf("missing lists: %s", res.Content)
	}
	if len(*m.Summary) != 1 || (*(*m.Summary)[0].Refs)[0] != "s1" {
		t.Errorf("summary %s", res.Content)
	}
	var decisionRefs []string
	for _, d := range *m.Decisions {
		decisionRefs = append(decisionRefs, (*d.Refs)...)
	}
	if strings.Join(decisionRefs, ",") != "s2,s4" {
		t.Errorf("decisions refs %v", decisionRefs)
	}
	acts := *m.ActionItems
	if len(acts) != 2 {
		t.Fatalf("actions %s", res.Content)
	}
	if (*acts[0].Refs)[0] != "s3" || acts[0].PIC == nil || *acts[0].PIC != "Budi" || acts[0].Deadline != nil || acts[0].DeadlinePhrase != nil {
		t.Errorf("action 0 %s", res.Content)
	}
	if (*acts[1].Refs)[0] != "s5" || acts[1].PIC != nil {
		t.Errorf("action 1 %s", res.Content)
	}
	if !strings.Contains(res.Content, `"pic":null`) || !strings.Contains(res.Content, `"deadline":null`) {
		t.Errorf("nullable keys must be present: %s", res.Content)
	}
}

func TestLLMEmptyTranscriptAndEnglish(t *testing.T) {
	p := meetingPayload(false)
	p["transcript"] = []any{}
	res, err := (&LLM{}).Complete(context.Background(), "s", userMessage(t, p))
	if err != nil || res.Content != `{"sections":[]}` {
		t.Errorf("empty: %q %v", res.Content, err)
	}
	p = meetingPayload(false)
	p["output_language"] = "en"
	res, _ = (&LLM{}).Complete(context.Background(), "s", userMessage(t, p))
	if !strings.Contains(res.Content, `"heading":"Summary"`) {
		t.Errorf("en heading: %s", res.Content)
	}
}

func TestLLMScripted(t *testing.T) {
	f := &LLM{Results: []ai.LLMResult{{Content: "not json", FinishReason: "stop"}, {Content: `{"sec`, FinishReason: "length", OutputTokens: 9}}}
	user := userMessage(t, meetingPayload(false))
	if r, err := f.Complete(context.Background(), "s", user); err != nil || r.Content != "not json" {
		t.Errorf("call 0 %+v %v", r, err)
	}
	r, err := f.Complete(context.Background(), "s", user)
	if ai.CategoryOf(err) != jobs.CatInvalidOutput || r.OutputTokens != 9 {
		t.Errorf("call 1 %+v %v", r, err)
	}
	if r, err := f.Complete(context.Background(), "s", user); err != nil || !strings.HasPrefix(r.Content, `{"sections":[{`) {
		t.Errorf("call 2 %+v %v", r, err)
	}
	if f.Calls() != 3 {
		t.Errorf("calls %d", f.Calls())
	}
	boom := errors.New("boom")
	if _, err := (&LLM{Errs: []error{boom}}).Complete(context.Background(), "s", user); !errors.Is(err, boom) {
		t.Errorf("errs %v", err)
	}
}
