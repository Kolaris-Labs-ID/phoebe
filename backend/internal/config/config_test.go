package config

import (
	"strings"
	"testing"
	"time"
)

func env(over map[string]string) func(string) string {
	base := map[string]string{
		"DATABASE_URL":    "postgres://phoebe:pw-secret@localhost:5432/phoebe",
		"PUBLIC_BASE_URL": "https://api.example.com/",
		"SIGNING_SECRET":  strings.Repeat("ab", 32),
		"SUBJECT_PEPPER":  "cGVwcGVyLXBlcHBlci1wZXBwZXItcGVwcGVyLXBlcHBlcg==",
	}
	for k, v := range over {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestDefaults(t *testing.T) {
	c, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.SignedURLTTL != 15*time.Minute || c.SessionTTL != 4320*time.Hour ||
		c.MaxAudioSeconds != 7200 || c.MaxUploadBytes != 262144000 || c.UploadChunkMaxBytes != 33554432 ||
		c.PurgeDelay != 90*time.Minute || c.STTProvider != "fake" || c.LLMProvider != "fake" ||
		c.WorkerConcurrency != 10 || c.JobLease != 2*time.Minute || c.LLMTemperature != 0.2 {
		t.Fatalf("unexpected defaults: %s", c)
	}
	if c.PublicBaseURL != "https://api.example.com" {
		t.Fatalf("trailing slash not trimmed: %q", c.PublicBaseURL)
	}
	if len(c.SigningSecret) != 32 || len(c.SubjectPepper) < 32 {
		t.Fatal("secret decoding")
	}
	if c.WorkerID == "" {
		t.Fatal("worker id default")
	}
}

func TestRequiredAndInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"missing db":        {"DATABASE_URL": ""},
		"short secret":      {"SIGNING_SECRET": "abcd"},
		"not hex/base64":    {"SUBJECT_PEPPER": "!!!not-a-key!!!"},
		"bad duration":      {"JOB_LEASE": "soon"},
		"purge too short":   {"PURGE_DELAY": "60m"},
		"openai no key":     {"STT_PROVIDER": "openai"},
		"llm no base":       {"LLM_PROVIDER": "openaicompat", "LLM_API_KEY": "k", "LLM_MODEL": "m"},
		"apple partial":     {"APPLE_KEY_ID": "K1"},
		"apple no enc key":  {"APPLE_KEY_ID": "K1", "APPLE_TEAM_ID": "T", "APPLE_PRIVATE_KEY": "pem"},
		"enc key wrong len": {"TOKEN_ENC_KEY": strings.Repeat("ab", 16)},
		"bad level":         {"LOG_LEVEL": "loud"},
		"zero concurrency":  {"WORKER_CONCURRENCY": "0"},
	}
	for name, over := range cases {
		if _, err := LoadFrom(env(over)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestAppleConfigured(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{
		"APPLE_KEY_ID": "K1", "APPLE_TEAM_ID": "T", "APPLE_PRIVATE_KEY": "pem",
		"TOKEN_ENC_KEY": strings.Repeat("01", 32),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AppleConfigured() || len(c.TokenEncKey) != 32 {
		t.Fatal("apple config")
	}
}

func TestAPIStartupRequiresHTTPS(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{"PUBLIC_BASE_URL": "http://localhost:8080"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.CheckAPIStartup() == nil {
		t.Fatal("http allowed without dev login")
	}
	c.DevLoginEnabled = true
	if c.CheckAPIStartup() != nil {
		t.Fatal("http rejected with dev login")
	}
}

func TestStringRedacts(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{"STT_API_KEY": "sk-live-123", "STT_PROVIDER": "openai", "STT_MODEL": "whisper-1"}))
	if err != nil {
		t.Fatal(err)
	}
	s := c.String()
	for _, leak := range []string{"pw-secret", "sk-live-123", strings.Repeat("ab", 32), "cGVwcGVy"} {
		if strings.Contains(s, leak) {
			t.Fatalf("String leaks %q: %s", leak, s)
		}
	}
	if !strings.Contains(s, "STTModel=whisper-1") {
		t.Fatalf("String missing plain field: %s", s)
	}
}
