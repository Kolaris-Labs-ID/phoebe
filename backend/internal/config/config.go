package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Config mirrors backend.md §16. Load fails fast on missing or invalid values.
type Config struct {
	DatabaseURL   string
	HTTPAddr      string
	PublicBaseURL string
	SigningSecret []byte `secret:"true"`
	SubjectPepper []byte `secret:"true"`
	SignedURLTTL  time.Duration
	StorageDir    string

	AppleBundleID   string
	AppleJWKSURL    string
	AppleTokenURL   string
	AppleRevokeURL  string
	AppleTeamID     string
	AppleKeyID      string
	ApplePrivateKey string `secret:"true"`
	TokenEncKey     []byte `secret:"true"`

	DevLoginEnabled   bool
	AccessTokenTTL    time.Duration
	SessionMaxAge     time.Duration
	RefreshReuseGrace time.Duration

	MaxAudioSeconds     int64
	MaxUploadBytes      int64
	UploadChunkMaxBytes int64
	UploadSessionTTL    time.Duration
	UploadWriterLease   time.Duration
	AudioRetentionDays  int
	TrialQuotaMinutes   int64
	TrialPeriodDays     int
	AIGenerationLimit   int
	AIGenerationWindow  time.Duration
	PurgeDelay          time.Duration

	STTProvider       string
	STTBaseURL        string
	STTAPIKey         string `secret:"true"`
	STTModel          string
	STTRequestTimeout time.Duration
	STTMaxFileBytes   int64
	STTChunkSeconds   int
	STTPricePerMin    float64
	FFmpegPath        string

	STTChunkTargetSeconds        int
	STTChunkMinSeconds           int
	STTChunkMaxSeconds           int
	STTSilenceNoiseDB            float64
	STTSilenceMinDuration        time.Duration
	STTDropSilenceRatio          float64
	STTUncertainMeanLogprob      float64
	STTUncertainAvgLogprob       float64
	STTUncertainNoSpeechProb     float64
	STTUncertainCompressionRatio float64

	LLMProvider           string
	LLMBaseURL            string
	LLMAPIKey             string `secret:"true"`
	LLMModel              string
	LLMTemperature        float64
	LLMRequestTimeout     time.Duration
	LLMMaxTranscriptChars int
	LLMPriceInPerMTok     float64
	LLMPriceOutPerMTok    float64
	LLMThinkingNotes      bool
	LLMThinkingMoM        bool

	WorkerID                string
	WorkerConcurrency       int
	WorkerPollInterval      time.Duration
	WorkerShutdownGrace     time.Duration
	JobLease                time.Duration
	JobTimeoutTranscribeMax time.Duration
	JobTimeoutGenerateMax   time.Duration
	JobRetryBase            time.Duration
	SweepInterval           time.Duration
	AlertQueueAge           time.Duration
	AlertDailyCostUSD       float64

	LogLevel slog.Level
}

func Load() (*Config, error) { return LoadFrom(os.Getenv) }

func LoadFrom(getenv func(string) string) (*Config, error) {
	p := parser{getenv: getenv}
	c := &Config{
		DatabaseURL:   p.required("DATABASE_URL"),
		HTTPAddr:      p.str("HTTP_ADDR", ":8080"),
		PublicBaseURL: strings.TrimRight(p.required("PUBLIC_BASE_URL"), "/"),
		SigningSecret: p.secret("SIGNING_SECRET", true, 32, false),
		SubjectPepper: p.secret("SUBJECT_PEPPER", true, 32, false),
		SignedURLTTL:  p.dur("SIGNED_URL_TTL", "15m"),
		StorageDir:    p.str("STORAGE_DIR", "./data/storage"),

		AppleBundleID:   p.str("APPLE_BUNDLE_ID", "com.kolarislabs.phoebe"),
		AppleJWKSURL:    p.str("APPLE_JWKS_URL", "https://appleid.apple.com/auth/keys"),
		AppleTokenURL:   p.str("APPLE_TOKEN_URL", "https://appleid.apple.com/auth/token"),
		AppleRevokeURL:  p.str("APPLE_REVOKE_URL", "https://appleid.apple.com/auth/revoke"),
		AppleTeamID:     p.str("APPLE_TEAM_ID", ""),
		AppleKeyID:      p.str("APPLE_KEY_ID", ""),
		ApplePrivateKey: p.str("APPLE_PRIVATE_KEY", ""),

		DevLoginEnabled:   p.boolean("DEV_LOGIN_ENABLED", false),
		AccessTokenTTL:    p.dur("ACCESS_TOKEN_TTL", "15m"),
		SessionMaxAge:     p.dur("SESSION_MAX_AGE", "4320h"),
		RefreshReuseGrace: p.dur("REFRESH_REUSE_GRACE", "30s"),

		MaxAudioSeconds:     p.int64("MAX_AUDIO_SECONDS", 7200),
		MaxUploadBytes:      p.int64("MAX_UPLOAD_BYTES", 262144000),
		UploadChunkMaxBytes: p.int64("UPLOAD_CHUNK_MAX_BYTES", 33554432),
		UploadSessionTTL:    p.dur("UPLOAD_SESSION_TTL", "72h"),
		UploadWriterLease:   p.dur("UPLOAD_WRITER_LEASE", "10m"),
		AudioRetentionDays:  int(p.int64("AUDIO_RETENTION_DAYS", 30)),
		TrialQuotaMinutes:   p.int64("TRIAL_QUOTA_MINUTES", 120),
		TrialPeriodDays:     int(p.int64("TRIAL_PERIOD_DAYS", 30)),
		AIGenerationLimit:   int(p.int64("AI_GENERATION_LIMIT", 30)),
		AIGenerationWindow:  p.dur("AI_GENERATION_WINDOW", "24h"),
		PurgeDelay:          p.dur("PURGE_DELAY", "90m"),

		STTProvider:       p.str("STT_PROVIDER", "fake"),
		STTBaseURL:        strings.TrimRight(p.str("STT_BASE_URL", "https://api.openai.com/v1"), "/"),
		STTAPIKey:         p.str("STT_API_KEY", ""),
		STTModel:          p.str("STT_MODEL", "gpt-4o-mini-transcribe"),
		STTRequestTimeout: p.dur("STT_REQUEST_TIMEOUT", "5m"),
		STTMaxFileBytes:   p.int64("STT_MAX_FILE_BYTES", 24000000),
		STTChunkSeconds:   int(p.int64("STT_CHUNK_SECONDS", 600)),
		STTPricePerMin:    p.float("STT_PRICE_PER_MIN", 0),
		FFmpegPath:        p.str("FFMPEG_PATH", "ffmpeg"),

		STTChunkTargetSeconds:        int(p.int64("STT_CHUNK_TARGET_SECONDS", 30)),
		STTChunkMinSeconds:           int(p.int64("STT_CHUNK_MIN_SECONDS", 10)),
		STTChunkMaxSeconds:           int(p.int64("STT_CHUNK_MAX_SECONDS", 45)),
		STTSilenceNoiseDB:            p.float("STT_SILENCE_NOISE_DB", -35),
		STTSilenceMinDuration:        p.dur("STT_SILENCE_MIN_DURATION", "500ms"),
		STTDropSilenceRatio:          p.float("STT_DROP_SILENCE_RATIO", 0.8),
		STTUncertainMeanLogprob:      p.float("STT_UNCERTAIN_MEAN_LOGPROB", -0.7),
		STTUncertainAvgLogprob:       p.float("STT_UNCERTAIN_AVG_LOGPROB", -1.0),
		STTUncertainNoSpeechProb:     p.float("STT_UNCERTAIN_NO_SPEECH_PROB", 0.6),
		STTUncertainCompressionRatio: p.float("STT_UNCERTAIN_COMPRESSION_RATIO", 2.4),

		LLMProvider:           p.str("LLM_PROVIDER", "fake"),
		LLMBaseURL:            strings.TrimRight(p.str("LLM_BASE_URL", ""), "/"),
		LLMAPIKey:             p.str("LLM_API_KEY", ""),
		LLMModel:              p.str("LLM_MODEL", "deepseek-flash"),
		LLMTemperature:        p.float("LLM_TEMPERATURE", 0.2),
		LLMRequestTimeout:     p.dur("LLM_REQUEST_TIMEOUT", "180s"),
		LLMMaxTranscriptChars: int(p.int64("LLM_MAX_TRANSCRIPT_CHARS", 120000)),
		LLMPriceInPerMTok:     p.float("LLM_PRICE_IN_PER_MTOK", 0),
		LLMPriceOutPerMTok:    p.float("LLM_PRICE_OUT_PER_MTOK", 0),
		LLMThinkingNotes:      p.boolean("LLM_THINKING_NOTES", false),
		LLMThinkingMoM:        p.boolean("LLM_THINKING_MOM", false),

		WorkerID:                p.str("WORKER_ID", defaultWorkerID()),
		WorkerConcurrency:       int(p.int64("WORKER_CONCURRENCY", 10)),
		WorkerPollInterval:      p.dur("WORKER_POLL_INTERVAL", "1s"),
		WorkerShutdownGrace:     p.dur("WORKER_SHUTDOWN_GRACE", "30s"),
		JobLease:                p.dur("JOB_LEASE", "2m"),
		JobTimeoutTranscribeMax: p.dur("JOB_TIMEOUT_TRANSCRIBE_MAX", "70m"),
		JobTimeoutGenerateMax:   p.dur("JOB_TIMEOUT_GENERATE_MAX", "30m"),
		JobRetryBase:            p.dur("JOB_RETRY_BASE", "10s"),
		SweepInterval:           p.dur("SWEEP_INTERVAL", "5m"),
		AlertQueueAge:           p.dur("ALERT_QUEUE_AGE", "10m"),
		AlertDailyCostUSD:       p.float("ALERT_DAILY_COST_USD", 0),

		LogLevel: p.level("LOG_LEVEL", "info"),
	}
	c.TokenEncKey = p.secret("TOKEN_ENC_KEY", c.AppleKeyID != "", 32, true)
	if c.LLMProvider == "deepseek" && c.LLMBaseURL == "" {
		c.LLMBaseURL = "https://api.deepseek.com"
	}
	c.validate(&p)
	if len(p.errs) > 0 {
		return nil, errors.Join(p.errs...)
	}
	return c, nil
}

// AppleConfigured reports whether Apple token exchange + revocation is enabled.
func (c *Config) AppleConfigured() bool {
	return c.AppleTeamID != "" && c.AppleKeyID != "" && c.ApplePrivateKey != ""
}

func (c *Config) validate(p *parser) {
	if c.PublicBaseURL != "" {
		u, err := url.Parse(c.PublicBaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			p.fail("PUBLIC_BASE_URL", "must be an absolute http(s) URL")
		}
	}
	n := 0
	for _, v := range []string{c.AppleTeamID, c.AppleKeyID, c.ApplePrivateKey} {
		if v != "" {
			n++
		}
	}
	if n != 0 && n != 3 {
		p.fail("APPLE_TEAM_ID/APPLE_KEY_ID/APPLE_PRIVATE_KEY", "must be set together")
	}
	switch c.STTProvider {
	case "fake":
	case "openai":
		if c.STTAPIKey == "" {
			p.fail("STT_API_KEY", "required unless STT_PROVIDER=fake")
		}
	default:
		p.fail("STT_PROVIDER", "must be fake|openai")
	}
	switch c.LLMProvider {
	case "fake":
	case "deepseek", "openaicompat":
		if c.LLMAPIKey == "" {
			p.fail("LLM_API_KEY", "required unless LLM_PROVIDER=fake")
		}
		if c.LLMBaseURL == "" {
			p.fail("LLM_BASE_URL", "required when LLM_PROVIDER=openaicompat")
		}
	default:
		p.fail("LLM_PROVIDER", "must be fake|deepseek|openaicompat")
	}
	// max >= 2*min: a cut must leave both the window and the remainder at least min long.
	if c.STTChunkMinSeconds <= 0 || c.STTChunkMinSeconds > c.STTChunkTargetSeconds || c.STTChunkTargetSeconds > c.STTChunkMaxSeconds ||
		c.STTChunkMaxSeconds < 2*c.STTChunkMinSeconds {
		p.fail("STT_CHUNK_MIN_SECONDS/STT_CHUNK_TARGET_SECONDS/STT_CHUNK_MAX_SECONDS", "must satisfy 0 < min <= target <= max and max >= 2*min")
	}
	if c.STTSilenceNoiseDB >= 0 {
		p.fail("STT_SILENCE_NOISE_DB", "must be < 0")
	}
	if c.STTDropSilenceRatio <= 0 || c.STTDropSilenceRatio > 1 {
		p.fail("STT_DROP_SILENCE_RATIO", "must be in (0, 1]")
	}
	if c.AIGenerationWindow < time.Hour || c.AIGenerationWindow%time.Hour != 0 {
		p.fail("AI_GENERATION_WINDOW", "must be a whole number of hours >= 1h")
	}
	if c.PurgeDelay <= c.JobTimeoutTranscribeMax || c.PurgeDelay <= c.JobTimeoutGenerateMax {
		p.fail("PURGE_DELAY", "must exceed JOB_TIMEOUT_TRANSCRIBE_MAX and JOB_TIMEOUT_GENERATE_MAX")
	}
	positive := map[string]float64{
		"MAX_AUDIO_SECONDS":          float64(c.MaxAudioSeconds),
		"MAX_UPLOAD_BYTES":           float64(c.MaxUploadBytes),
		"UPLOAD_CHUNK_MAX_BYTES":     float64(c.UploadChunkMaxBytes),
		"AUDIO_RETENTION_DAYS":       float64(c.AudioRetentionDays),
		"TRIAL_PERIOD_DAYS":          float64(c.TrialPeriodDays),
		"AI_GENERATION_LIMIT":        float64(c.AIGenerationLimit),
		"STT_MAX_FILE_BYTES":         float64(c.STTMaxFileBytes),
		"STT_CHUNK_SECONDS":          float64(c.STTChunkSeconds),
		"LLM_MAX_TRANSCRIPT_CHARS":   float64(c.LLMMaxTranscriptChars),
		"WORKER_CONCURRENCY":         float64(c.WorkerConcurrency),
		"SIGNED_URL_TTL":             float64(c.SignedURLTTL),
		"ACCESS_TOKEN_TTL":           float64(c.AccessTokenTTL),
		"SESSION_MAX_AGE":            float64(c.SessionMaxAge),
		"UPLOAD_SESSION_TTL":         float64(c.UploadSessionTTL),
		"UPLOAD_WRITER_LEASE":        float64(c.UploadWriterLease),
		"STT_REQUEST_TIMEOUT":        float64(c.STTRequestTimeout),
		"STT_SILENCE_MIN_DURATION":   float64(c.STTSilenceMinDuration),
		"LLM_REQUEST_TIMEOUT":        float64(c.LLMRequestTimeout),
		"WORKER_POLL_INTERVAL":       float64(c.WorkerPollInterval),
		"JOB_LEASE":                  float64(c.JobLease),
		"JOB_TIMEOUT_TRANSCRIBE_MAX": float64(c.JobTimeoutTranscribeMax),
		"JOB_TIMEOUT_GENERATE_MAX":   float64(c.JobTimeoutGenerateMax),
		"JOB_RETRY_BASE":             float64(c.JobRetryBase),
		"SWEEP_INTERVAL":             float64(c.SweepInterval),
	}
	for k, v := range positive {
		if v <= 0 {
			p.fail(k, "must be > 0")
		}
	}
	nonNegative := map[string]float64{
		"TRIAL_QUOTA_MINUTES":    float64(c.TrialQuotaMinutes),
		"STT_PRICE_PER_MIN":      c.STTPricePerMin,
		"LLM_PRICE_IN_PER_MTOK":  c.LLMPriceInPerMTok,
		"LLM_PRICE_OUT_PER_MTOK": c.LLMPriceOutPerMTok,
		"ALERT_DAILY_COST_USD":   c.AlertDailyCostUSD,
		"WORKER_SHUTDOWN_GRACE":  float64(c.WorkerShutdownGrace),
		"REFRESH_REUSE_GRACE":    float64(c.RefreshReuseGrace),
	}
	for k, v := range nonNegative {
		if v < 0 {
			p.fail(k, "must be >= 0")
		}
	}
}

// CheckAPIStartup enforces backend.md §12.1: https PUBLIC_BASE_URL unless dev login is on.
func (c *Config) CheckAPIStartup() error {
	if !c.DevLoginEnabled && !strings.HasPrefix(c.PublicBaseURL, "https://") {
		return errors.New("config: PUBLIC_BASE_URL must be https:// unless DEV_LOGIN_ENABLED=true")
	}
	return nil
}

// String renders all settings with secrets redacted; safe to log.
func (c *Config) String() string {
	var b strings.Builder
	v := reflect.ValueOf(c).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		val := v.Field(i)
		b.WriteString(f.Name)
		b.WriteByte('=')
		switch {
		case f.Tag.Get("secret") == "true":
			if val.Len() > 0 {
				b.WriteString("[redacted]")
			}
		case f.Name == "DatabaseURL":
			b.WriteString(redactURL(c.DatabaseURL))
		default:
			fmt.Fprint(&b, val.Interface())
		}
		if i < t.NumField()-1 {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func redactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "[redacted]"
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "redacted")
	}
	return u.String()
}

func defaultWorkerID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		h = "worker"
	}
	return h + "-" + strconv.Itoa(os.Getpid())
}

type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) fail(key, msg string) {
	p.errs = append(p.errs, fmt.Errorf("config: %s %s", key, msg))
}

func (p *parser) str(key, def string) string {
	if v := strings.TrimSpace(p.getenv(key)); v != "" {
		return v
	}
	return def
}

func (p *parser) required(key string) string {
	v := p.str(key, "")
	if v == "" {
		p.fail(key, "is required")
	}
	return v
}

func (p *parser) dur(key, def string) time.Duration {
	s := p.str(key, def)
	d, err := time.ParseDuration(s)
	if err != nil {
		p.fail(key, "must be a Go duration (e.g. 15m)")
	}
	return d
}

func (p *parser) int64(key string, def int64) int64 {
	s := p.str(key, "")
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		p.fail(key, "must be an integer")
	}
	return n
}

func (p *parser) float(key string, def float64) float64 {
	s := p.str(key, "")
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		p.fail(key, "must be a number")
	}
	return f
}

func (p *parser) boolean(key string, def bool) bool {
	s := p.str(key, "")
	if s == "" {
		return def
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		p.fail(key, "must be true|false")
	}
	return b
}

func (p *parser) level(key, def string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(p.str(key, def))); err != nil {
		p.fail(key, "must be debug|info|warn|error")
	}
	return l
}

// secret decodes hex or base64 (std/url, padded or not). exact=true requires len==n.
func (p *parser) secret(key string, required bool, n int, exact bool) []byte {
	s := p.str(key, "")
	if s == "" {
		if required {
			p.fail(key, "is required")
		}
		return nil
	}
	b, ok := decodeKey(s)
	switch {
	case !ok:
		p.fail(key, "must be hex or base64")
	case exact && len(b) != n:
		p.fail(key, fmt.Sprintf("must decode to exactly %d bytes", n))
	case !exact && len(b) < n:
		p.fail(key, fmt.Sprintf("must decode to at least %d bytes", n))
	}
	return b
}

func decodeKey(s string) ([]byte, bool) {
	if b, err := hex.DecodeString(s); err == nil {
		return b, true
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}
