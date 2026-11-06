package providers

import (
	"fmt"

	"phoebe/internal/ai"
	"phoebe/internal/ai/fake"
	"phoebe/internal/ai/openai"
	"phoebe/internal/ai/openaicompat"
	"phoebe/internal/audio"
	"phoebe/internal/config"
)

func STT(cfg *config.Config) (ai.STT, error) {
	switch cfg.STTProvider {
	case "fake":
		return &fake.STT{}, nil
	case "openai":
		s, err := openai.New(openai.Config{
			BaseURL: cfg.STTBaseURL, APIKey: cfg.STTAPIKey, Model: cfg.STTModel, Timeout: cfg.STTRequestTimeout,
			FFmpegPath:   cfg.FFmpegPath,
			Silence:      audio.SilenceParams{NoiseDB: cfg.STTSilenceNoiseDB, MinDuration: cfg.STTSilenceMinDuration},
			MaxFileBytes: cfg.STTMaxFileBytes, ChunkSeconds: cfg.STTChunkSeconds,
			Windows: audio.WindowPlan{TargetMS: int64(cfg.STTChunkTargetSeconds) * 1000,
				MinMS: int64(cfg.STTChunkMinSeconds) * 1000, MaxMS: int64(cfg.STTChunkMaxSeconds) * 1000},
			DropSilenceRatio: cfg.STTDropSilenceRatio,
			MeanLogprobMin:   cfg.STTUncertainMeanLogprob, AvgLogprobMin: cfg.STTUncertainAvgLogprob,
			NoSpeechProbMax: cfg.STTUncertainNoSpeechProb, CompressionRatioMax: cfg.STTUncertainCompressionRatio,
		})
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	return nil, fmt.Errorf("ai: unknown STT_PROVIDER %q", cfg.STTProvider)
}

func LLM(cfg *config.Config) (ai.LLM, error) {
	switch cfg.LLMProvider {
	case "fake":
		return &fake.LLM{}, nil
	case "deepseek", "openaicompat":
		c := openaicompat.Config{
			Provider: cfg.LLMProvider, BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, Model: cfg.LLMModel,
			Temperature: cfg.LLMTemperature, Timeout: cfg.LLMRequestTimeout,
		}
		if cfg.LLMProvider == "deepseek" {
			c.ThinkingParam = true
			c.Thinking = map[ai.Task]bool{ai.TaskNotes: cfg.LLMThinkingNotes, ai.TaskMoM: cfg.LLMThinkingMoM}
		}
		return openaicompat.New(c), nil
	}
	return nil, fmt.Errorf("ai: unknown LLM_PROVIDER %q", cfg.LLMProvider)
}
