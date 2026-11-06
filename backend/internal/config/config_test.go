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
	if c.HTTPAddr != ":8080" || c.SignedURLTTL != 15*time.Minute || c.SessionMaxAge != 4320*time.Hour ||
		c.AccessTokenTTL != 15*time.Minute || c.RefreshReuseGrace != 30*time.Second ||
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
		"missing db":         {"DATABASE_URL": ""},
		"short secret":       {"SIGNING_SECRET": "abcd"},
		"not hex/base64":     {"SUBJECT_PEPPER": "!!!not-a-key!!!"},
		"bad duration":       {"JOB_LEASE": "soon"},
		"purge too short":    {"PURGE_DELAY": "60m"},
		"openai no key":      {"STT_PROVIDER": "openai"},
		"llm no key":         {"LLM_PROVIDER": "deepseek"},
		"llm bad provider":   {"LLM_PROVIDER": "gemini", "LLM_API_KEY": "k"},
		"chunk min>target":   {"STT_CHUNK_MIN_SECONDS": "40"},
		"chunk target>max":   {"STT_CHUNK_TARGET_SECONDS": "50"},
		"chunk max<2*min":    {"STT_CHUNK_MIN_SECONDS": "30", "STT_CHUNK_TARGET_SECONDS": "30", "STT_CHUNK_MAX_SECONDS": "45"},
		"compat no base url": {"LLM_PROVIDER": "openaicompat", "LLM_API_KEY": "k"},
		"noise db positive":  {"STT_SILENCE_NOISE_DB": "3"},
		"drop ratio zero":    {"STT_DROP_SILENCE_RATIO": "0"},
		"silence min zero":   {"STT_SILENCE_MIN_DURATION": "0s"},
		"bad thinking bool":  {"LLM_THINKING_MOM": "maybe"},
		"apple partial":      {"APPLE_KEY_ID": "K1"},
		"apple no enc key":   {"APPLE_KEY_ID": "K1", "APPLE_TEAM_ID": "T", "APPLE_PRIVATE_KEY": "pem"},
		"enc key wrong len":  {"TOKEN_ENC_KEY": strings.Repeat("ab", 16)},
		"bad level":          {"LOG_LEVEL": "loud"},
		"zero concurrency":   {"WORKER_CONCURRENCY": "0"},
		"zero ai limit":      {"AI_GENERATION_LIMIT": "0"},
		"ai window < 1h":     {"AI_GENERATION_WINDOW": "30m"},
		"ai window partial":  {"AI_GENERATION_WINDOW": "90m"},
	}
	for name, over := range cases {
		if _, err := LoadFrom(env(over)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestProductDefaults(t *testing.T) {
	c, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.TrialQuotaMinutes != 120 || c.TrialPeriodDays != 30 || c.AudioRetentionDays != 30 || c.SignedURLTTL != 15*time.Minute ||
		c.AIGenerationLimit != 30 || c.AIGenerationWindow != 24*time.Hour {
		t.Fatalf("product defaults: %s", c)
	}
	c, err = LoadFrom(env(map[string]string{"TRIAL_QUOTA_MINUTES": "60", "TRIAL_PERIOD_DAYS": "7", "AUDIO_RETENTION_DAYS": "10",
		"SIGNED_URL_TTL": "5m", "AI_GENERATION_LIMIT": "5", "AI_GENERATION_WINDOW": "12h"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.TrialQuotaMinutes != 60 || c.TrialPeriodDays != 7 || c.AudioRetentionDays != 10 || c.SignedURLTTL != 5*time.Minute ||
		c.AIGenerationLimit != 5 || c.AIGenerationWindow != 12*time.Hour {
		t.Fatalf("overrides: %s", c)
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

func TestProviderDefaults(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{"STT_PROVIDER": "openai", "STT_API_KEY": "k", "LLM_PROVIDER": "deepseek", "LLM_API_KEY": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.STTModel != "gpt-4o-mini-transcribe" || c.STTChunkTargetSeconds != 30 || c.STTChunkMinSeconds != 10 ||
		c.STTChunkMaxSeconds != 45 || c.STTSilenceNoiseDB != -35 || c.STTSilenceMinDuration != 500*time.Millisecond ||
		c.STTDropSilenceRatio != 0.8 || c.STTUncertainMeanLogprob != -0.7 ||
		c.STTUncertainAvgLogprob != -1 || c.STTUncertainNoSpeechProb != 0.6 || c.STTUncertainCompressionRatio != 2.4 {
		t.Fatalf("stt defaults: %s", c)
	}
	if c.LLMBaseURL != "https://api.deepseek.com" || c.LLMModel != "deepseek-flash" || c.LLMThinkingNotes || c.LLMThinkingMoM ||
		c.LLMPriceInPerMTok != 0 || c.LLMPriceOutPerMTok != 0 {
		t.Fatalf("llm defaults: %s", c)
	}
	c, err = LoadFrom(env(map[string]string{"LLM_THINKING_NOTES": "true", "LLM_THINKING_MOM": "true"}))
	if err != nil || !c.LLMThinkingNotes || !c.LLMThinkingMoM {
		t.Fatalf("thinking overrides: %v %v", c, err)
	}
}

func TestLLMBaseURLDefaultOnlyForDeepSeek(t *testing.T) {
	_, err := LoadFrom(env(map[string]string{"LLM_PROVIDER": "openaicompat", "LLM_API_KEY": "k"}))
	if err == nil || !strings.Contains(err.Error(), "LLM_BASE_URL") {
		t.Fatalf("openaicompat without LLM_BASE_URL: %v", err)
	}
	c, err := LoadFrom(env(map[string]string{"LLM_PROVIDER": "openaicompat", "LLM_API_KEY": "k",
		"LLM_BASE_URL": "https://generativelanguage.googleapis.com/v1beta/openai/"}))
	if err != nil || c.LLMBaseURL != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatalf("openaicompat explicit base url: %v %v", c, err)
	}
	c, err = LoadFrom(env(map[string]string{"LLM_PROVIDER": "deepseek", "LLM_API_KEY": "k"}))
	if err != nil || c.LLMBaseURL != "https://api.deepseek.com" {
		t.Fatalf("deepseek default: %v %v", c, err)
	}
	c, err = LoadFrom(env(nil))
	if err != nil || c.LLMBaseURL != "" {
		t.Fatalf("fake must not get a vendor base url: %v %v", c, err)
	}
}

func TestChunkWindowBounds(t *testing.T) {
	for _, over := range []map[string]string{
		nil,
		{"STT_CHUNK_MIN_SECONDS": "20", "STT_CHUNK_TARGET_SECONDS": "20", "STT_CHUNK_MAX_SECONDS": "40"},
	} {
		if _, err := LoadFrom(env(over)); err != nil {
			t.Errorf("%v: %v", over, err)
		}
	}
	_, err := LoadFrom(env(map[string]string{"STT_CHUNK_MIN_SECONDS": "20", "STT_CHUNK_TARGET_SECONDS": "30", "STT_CHUNK_MAX_SECONDS": "39"}))
	if err == nil || !strings.Contains(err.Error(), "max >= 2*min") {
		t.Fatalf("max < 2*min accepted: %v", err)
	}
}
