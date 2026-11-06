package providers

import (
	"testing"
	"time"

	"phoebe/internal/ai/fake"
	"phoebe/internal/ai/openai"
	"phoebe/internal/ai/openaicompat"
	"phoebe/internal/config"
)

func TestSelectByConfig(t *testing.T) {
	cfg := &config.Config{STTProvider: "fake", LLMProvider: "fake"}
	if s, err := STT(cfg); err != nil || s.(*fake.STT) == nil {
		t.Fatalf("fake stt %T %v", s, err)
	}
	if l, err := LLM(cfg); err != nil || l.(*fake.LLM) == nil {
		t.Fatalf("fake llm %T %v", l, err)
	}

	cfg = &config.Config{
		STTProvider: "openai", STTBaseURL: "https://api.openai.com/v1", STTAPIKey: "k", STTModel: "whisper-1", STTRequestTimeout: time.Minute,
		LLMProvider: "openaicompat", LLMBaseURL: "https://api.deepseek.com", LLMAPIKey: "k", LLMModel: "deepseek-chat", LLMRequestTimeout: time.Minute,
	}
	s, err := STT(cfg)
	if _, ok := s.(*openai.STT); err != nil || !ok {
		t.Fatalf("openai %T %v", s, err)
	}
	if p, m := s.Name(); p != "openai" || m != "whisper-1" {
		t.Errorf("stt name %s %s", p, m)
	}
	l, err := LLM(cfg)
	if _, ok := l.(*openaicompat.LLM); err != nil || !ok {
		t.Fatalf("openaicompat %T %v", l, err)
	}
	if p, m := l.Name(); p != "openaicompat" || m != "deepseek-chat" {
		t.Errorf("llm name %s %s", p, m)
	}

	if _, err := STT(&config.Config{STTProvider: "openai", STTModel: "gpt-5-transcribe"}); err == nil {
		t.Error("model outside the capability table accepted")
	}
	cfg.LLMProvider, cfg.LLMModel, cfg.LLMThinkingMoM = "deepseek", "deepseek-flash", true
	if l, err = LLM(cfg); err != nil {
		t.Fatal(err)
	}
	if p, m := l.Name(); p != "deepseek" || m != "deepseek-flash" {
		t.Errorf("deepseek name %s %s", p, m)
	}
	if _, err := STT(&config.Config{STTProvider: "whisperx"}); err == nil {
		t.Error("unknown stt accepted")
	}
	if _, err := LLM(&config.Config{LLMProvider: "gemini"}); err == nil {
		t.Error("unknown llm accepted")
	}
}
