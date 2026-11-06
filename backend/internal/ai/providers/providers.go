package providers

import (
	"fmt"

	"phoebe/internal/ai"
	"phoebe/internal/ai/fake"
	"phoebe/internal/ai/openai"
	"phoebe/internal/ai/openaicompat"
	"phoebe/internal/config"
)

func STT(cfg *config.Config) (ai.STT, error) {
	switch cfg.STTProvider {
	case "fake":
		return &fake.STT{}, nil
	case "openai":
		return openai.New(openai.Config{
			BaseURL: cfg.STTBaseURL, APIKey: cfg.STTAPIKey, Model: cfg.STTModel, Timeout: cfg.STTRequestTimeout,
		}), nil
	}
	return nil, fmt.Errorf("ai: unknown STT_PROVIDER %q", cfg.STTProvider)
}

func LLM(cfg *config.Config) (ai.LLM, error) {
	switch cfg.LLMProvider {
	case "fake":
		return &fake.LLM{}, nil
	case "openaicompat":
		return openaicompat.New(openaicompat.Config{
			BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, Model: cfg.LLMModel,
			Temperature: cfg.LLMTemperature, Timeout: cfg.LLMRequestTimeout,
		}), nil
	}
	return nil, fmt.Errorf("ai: unknown LLM_PROVIDER %q", cfg.LLMProvider)
}
