package openai

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"phoebe/internal/ai"
	"phoebe/internal/jobs"
)

const maxResponseBytes = 32 << 20

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	Client  *http.Client
}

type STT struct {
	cfg Config
}

func New(cfg Config) *STT {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &STT{cfg: cfg}
}

func (s *STT) Name() (string, string) { return "openai", s.cfg.Model }

type segment struct {
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
	AvgLogprob       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
}

type verboseJSON struct {
	Language string     `json:"language"`
	Duration float64    `json:"duration"`
	Segments *[]segment `json:"segments"`
}

func (s *STT) Transcribe(ctx context.Context, in ai.STTInput) (ai.STTResult, error) {
	if s.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
		defer cancel()
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pw.CloseWithError(writeForm(mw, s.cfg.Model, in))
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BaseURL+"/audio/transcriptions", pr)
	if err != nil {
		pr.Close()
		<-done
		return ai.STTResult{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := s.cfg.Client.Do(req)
	pr.Close()
	<-done
	if err != nil {
		return ai.STTResult{}, ai.TransportError(ctx, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ai.STTResult{}, ai.TransportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusBadRequest && undecodable(body) {
			return ai.STTResult{}, ai.NewError(jobs.CatAudioUnreadable, false, resp.StatusCode, "")
		}
		return ai.STTResult{}, ai.StatusError(resp.StatusCode, resp.Header, time.Now())
	}

	var v verboseJSON
	if err := json.Unmarshal(body, &v); err != nil {
		return ai.STTResult{}, ai.InvalidOutput(resp.StatusCode, "undecodable response")
	}
	if v.Segments == nil {
		return ai.STTResult{}, ai.NewError(jobs.CatProviderRejected, false, resp.StatusCode, "missing segments")
	}
	out := ai.STTResult{
		Language: language(v.Language, in.Language),
		AudioMS:  int64(v.Duration*1000 + 0.5),
		Segments: make([]ai.RawSegment, 0, len(*v.Segments)),
	}
	for _, sg := range *v.Segments {
		out.Segments = append(out.Segments, ai.RawSegment{
			StartSec: sg.Start, EndSec: sg.End, Text: sg.Text,
			NoSpeechProb: sg.NoSpeechProb, AvgLogprob: sg.AvgLogprob, CompressionRatio: sg.CompressionRatio,
		})
	}
	return out, nil
}

func writeForm(mw *multipart.Writer, model string, in ai.STTInput) error {
	fields := [][2]string{
		{"model", model},
		{"response_format", "verbose_json"},
		{"timestamp_granularities[]", "segment"},
		{"temperature", "0"},
	}
	if in.Language == "id" || in.Language == "en" {
		fields = append(fields, [2]string{"language", in.Language})
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return err
		}
	}
	ct := in.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+quoteEscaper.Replace(filename(in))+`"`)
	h.Set("Content-Type", ct)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, in.Reader); err != nil {
		return err
	}
	return mw.Close()
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// The provider detects the format from the file extension.
func filename(in ai.STTInput) string {
	if in.Filename != "" {
		return in.Filename
	}
	switch strings.ToLower(strings.TrimSpace(strings.Split(in.ContentType, ";")[0])) {
	case "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/aac":
		return "audio.m4a"
	case "audio/mpeg", "audio/mp3":
		return "audio.mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "audio.wav"
	}
	return "audio"
}

func undecodable(body []byte) bool {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return false
	}
	m := strings.ToLower(e.Error.Message)
	for _, k := range []string{"decod", "corrupt", "file format", "invalid file"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// verbose_json reports the detected language by name ("indonesian"); transcripts store ISO codes when known.
func language(detected, requested string) string {
	switch d := strings.ToLower(strings.TrimSpace(detected)); d {
	case "indonesian", "id":
		return "id"
	case "english", "en":
		return "en"
	case "":
		if requested == "id" || requested == "en" {
			return requested
		}
		return ""
	default:
		if len(d) > 16 {
			return d[:16]
		}
		return d
	}
}
