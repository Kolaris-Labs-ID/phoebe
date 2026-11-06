package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"phoebe/internal/ai"
)

const maxResponseBytes = 16 << 20

type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature float64
	Timeout     time.Duration
	Client      *http.Client
}

type LLM struct {
	cfg Config
}

func New(cfg Config) *LLM {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &LLM{cfg: cfg}
}

func (l *LLM) Name() (string, string) { return "openaicompat", l.cfg.Model }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model          string            `json:"model"`
	Messages       []message         `json:"messages"`
	Temperature    float64           `json:"temperature"`
	ResponseFormat map[string]string `json:"response_format"`
}

type response struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Complete returns the result together with an invalid_output error when finish_reason is
// "length", so the caller can still record the billed tokens.
func (l *LLM) Complete(ctx context.Context, system, user string) (ai.LLMResult, error) {
	if l.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.cfg.Timeout)
		defer cancel()
	}
	payload, err := json.Marshal(request{
		Model:          l.cfg.Model,
		Messages:       []message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature:    l.cfg.Temperature,
		ResponseFormat: map[string]string{"type": "json_object"},
	})
	if err != nil {
		return ai.LLMResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return ai.LLMResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+l.cfg.APIKey)

	resp, err := l.cfg.Client.Do(req)
	if err != nil {
		return ai.LLMResult{}, ai.TransportError(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ai.LLMResult{}, ai.TransportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ai.LLMResult{}, ai.StatusError(resp.StatusCode, resp.Header, time.Now())
	}

	var v response
	if err := json.Unmarshal(body, &v); err != nil {
		return ai.LLMResult{}, ai.InvalidOutput(resp.StatusCode, "undecodable response")
	}
	res := ai.LLMResult{InputTokens: v.Usage.PromptTokens, OutputTokens: v.Usage.CompletionTokens}
	if len(v.Choices) == 0 {
		return res, ai.InvalidOutput(resp.StatusCode, "no choices")
	}
	c := v.Choices[0]
	if c.Message.Content != nil {
		res.Content = *c.Message.Content
	}
	res.FinishReason = c.FinishReason
	if c.FinishReason == "length" {
		return res, ai.InvalidOutput(resp.StatusCode, "finish_reason length")
	}
	return res, nil
}
