package openai

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"phoebe/internal/ai"
	"phoebe/internal/audio"
)

// Live tests call the real provider and cost money: they run only with PHOEBE_LIVE=1 plus the key.
func TestLiveOpenAISTT(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if os.Getenv("PHOEBE_LIVE") != "1" || key == "" {
		t.Skip("SKIPPED live OpenAI STT test: set PHOEBE_LIVE=1 and OPENAI_API_KEY to run it (see backend/scripts/live-check.sh)")
	}
	model := os.Getenv("PHOEBE_LIVE_STT_MODEL")
	if model == "" {
		model = "gpt-4o-mini-transcribe"
	}
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	b, err := os.ReadFile("testdata/speech_id.m4a")
	if err != nil {
		t.Fatal(err)
	}
	ct, dur, err := audio.Probe(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig(t, base, model)
	c.APIKey, c.Timeout = key, 2*time.Minute
	s := newSTT(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := s.Transcribe(ctx, ai.STTInput{Reader: bytes.NewReader(b), Filename: "speech_id.m4a", ContentType: ct,
		Language: "id", DurationMS: dur})
	if err != nil {
		t.Fatal(err)
	}
	if res.Timing != Models[model] || len(res.Segments) == 0 || res.AudioMS <= 0 {
		t.Fatalf("timing %q, %d segments, audio %d ms", res.Timing, len(res.Segments), res.AudioMS)
	}
	var prev int64
	uncertain := 0
	for i, sg := range res.Segments {
		if sg.Text == "" || sg.StartMS < prev || sg.EndMS <= sg.StartMS || sg.EndMS > dur {
			t.Errorf("segment %d: [%d, %d] text %d chars (duration %d)", i, sg.StartMS, sg.EndMS, len(sg.Text), dur)
		}
		if sg.Uncertain {
			uncertain++
		}
		prev = sg.EndMS
	}
	t.Logf("model %s timing %s: %d segments (%d uncertain), %d of %d ms sent", model, res.Timing, len(res.Segments), uncertain, res.AudioMS, dur)
}
