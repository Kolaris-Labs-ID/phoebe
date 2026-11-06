package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"phoebe/internal/ai"
	"phoebe/internal/audio"
	"phoebe/internal/jobs"
)

const (
	maxResponseBytes = 32 << 20
	promptTailRunes  = 200
)

// Only whisper-1 returns segment timestamps; the gpt-4o transcribe models don't, so we transcribe silence-aligned chunks.
var Models = map[string]string{
	"whisper-1":              ai.TimingSegment,
	"gpt-4o-mini-transcribe": ai.TimingChunk,
	"gpt-4o-transcribe":      ai.TimingChunk,
}

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	Client  *http.Client

	FFmpegPath string
	Silence    audio.SilenceParams

	MaxFileBytes int64
	ChunkSeconds int

	Windows audio.WindowPlan

	DropSilenceRatio    float64
	MeanLogprobMin      float64
	AvgLogprobMin       float64
	NoSpeechProbMax     float64
	CompressionRatioMax float64
}

type STT struct {
	cfg    Config
	timing string
}

func New(cfg Config) (*STT, error) {
	timing, ok := Models[cfg.Model]
	if !ok {
		return nil, fmt.Errorf("openai: unsupported STT_MODEL %q", cfg.Model)
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	return &STT{cfg: cfg, timing: timing}, nil
}

func (s *STT) Name() (string, string) { return "openai", s.cfg.Model }

func (s *STT) Transcribe(ctx context.Context, in ai.STTInput) (ai.STTResult, error) {
	dir, err := os.MkdirTemp("", "phoebe-stt-")
	if err != nil {
		return ai.STTResult{}, err
	}
	defer os.RemoveAll(dir)
	name := filename(in)
	src := filepath.Join(dir, "source"+filepath.Ext(name))
	size, err := writeFile(src, in.Reader)
	if err != nil {
		return ai.STTResult{}, err
	}
	silences, err := audio.DetectSilence(ctx, s.cfg.FFmpegPath, src, in.DurationMS, s.cfg.Silence)
	if err != nil {
		return ai.STTResult{}, ffmpegError(ctx, err)
	}
	if s.timing == ai.TimingChunk {
		return s.chunked(ctx, in, dir, src, silences)
	}
	return s.segmented(ctx, in, dir, src, size, silences)
}

func (s *STT) chunked(ctx context.Context, in ai.STTInput, dir, src string, silences []audio.Interval) (ai.STTResult, error) {
	res := ai.STTResult{Language: reported(in.Language), Timing: ai.TimingChunk, Segments: []ai.Segment{}}
	prompt := ""
	for k, w := range audio.PlanWindows(in.DurationMS, silences, s.cfg.Windows) {
		// An absolute bound, not a ratio: a short "setuju" between long pauses must still be sent.
		nonSilent := int64(math.Round(float64(w.DurationMS()) * (1 - audio.Coverage(silences, w.StartMS, w.EndMS))))
		if nonSilent < s.cfg.Silence.MinDuration.Milliseconds() {
			continue
		}
		name := fmt.Sprintf("chunk_%03d.m4a", k)
		path := filepath.Join(dir, name)
		if err := audio.Extract(ctx, s.cfg.FFmpegPath, src, path, w.StartMS, w.DurationMS()); err != nil {
			return ai.STTResult{}, ffmpegError(ctx, err)
		}
		fields := [][2]string{{"response_format", "json"}, {"include[]", "logprobs"}}
		if prompt != "" {
			fields = append(fields, [2]string{"prompt", prompt})
		}
		body, status, err := s.post(ctx, path, name, audio.ChunkContentType, in.Language, fields)
		os.Remove(path)
		if err != nil {
			return ai.STTResult{}, err
		}
		res.AudioMS += w.DurationMS()
		var v struct {
			Text     *string `json:"text"`
			Logprobs []struct {
				Logprob float64 `json:"logprob"`
			} `json:"logprobs"`
		}
		if err := json.Unmarshal(body, &v); err != nil || v.Text == nil {
			return ai.STTResult{}, ai.InvalidOutput(status, "undecodable response")
		}
		text := strings.TrimSpace(*v.Text)
		low := false
		if len(v.Logprobs) > 0 {
			var sum float64
			for _, lp := range v.Logprobs {
				sum += lp.Logprob
			}
			low = sum/float64(len(v.Logprobs)) < s.cfg.MeanLogprobMin
		}
		// No silence-based drop: a whole window's silence ratio says nothing about where the text came from.
		if text == "" {
			continue
		}
		res.Segments = append(res.Segments, segment(w.StartMS, w.EndMS, text, low))
		prompt = tail(text, promptTailRunes)
	}
	return res, nil
}

type verboseSegment struct {
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
	AvgLogprob       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
}

func (s *STT) segmented(ctx context.Context, in ai.STTInput, dir, src string, size int64, silences []audio.Interval) (ai.STTResult, error) {
	res := ai.STTResult{Timing: ai.TimingSegment, Segments: []ai.Segment{}}
	step := int64(s.cfg.ChunkSeconds) * 1000
	n := 1
	if size > s.cfg.MaxFileBytes {
		n = audio.Chunker{ChunkSeconds: s.cfg.ChunkSeconds}.Count(in.DurationMS)
	}
	for k := 0; k < n; k++ {
		offset := int64(k) * step
		path, name, ct := src, filename(in), contentType(in)
		if n > 1 {
			var dur int64
			if k < n-1 {
				dur = step
			}
			name, ct = fmt.Sprintf("chunk_%03d.m4a", k), audio.ChunkContentType
			path = filepath.Join(dir, name)
			if err := audio.Extract(ctx, s.cfg.FFmpegPath, src, path, offset, dur); err != nil {
				return ai.STTResult{}, ffmpegError(ctx, err)
			}
		}
		body, status, err := s.post(ctx, path, name, ct, in.Language,
			[][2]string{{"response_format", "verbose_json"}, {"timestamp_granularities[]", "segment"}})
		if n > 1 {
			os.Remove(path)
		}
		if err != nil {
			return ai.STTResult{}, err
		}
		var v struct {
			Language string            `json:"language"`
			Duration float64           `json:"duration"`
			Segments *[]verboseSegment `json:"segments"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return ai.STTResult{}, ai.InvalidOutput(status, "undecodable response")
		}
		if v.Segments == nil {
			return ai.STTResult{}, ai.NewError(jobs.CatProviderRejected, false, status, "missing segments")
		}
		if res.Language == "" {
			res.Language = detected(v.Language)
		}
		res.AudioMS += secToMS(v.Duration)
		for _, sg := range *v.Segments {
			if sg.Start < 0 || sg.End < 0 || sg.Start > sg.End || math.IsNaN(sg.Start) || math.IsNaN(sg.End) {
				return ai.STTResult{}, ai.InvalidOutput(status, "segment times")
			}
			start, end := offset+secToMS(sg.Start), offset+secToMS(sg.End)
			text := strings.TrimSpace(sg.Text)
			low := sg.AvgLogprob < s.cfg.AvgLogprobMin || sg.NoSpeechProb > s.cfg.NoSpeechProbMax ||
				sg.CompressionRatio > s.cfg.CompressionRatioMax
			if !s.keep(text, low, audio.Coverage(silences, start, end)) {
				continue
			}
			res.Segments = append(res.Segments, segment(start, end, text, low))
		}
	}
	if res.Language == "" {
		res.Language = reported(in.Language)
	}
	return res, nil
}

// Never discard speech on confidence alone: low confidence must coincide with silence over the segment's own span.
func (s *STT) keep(text string, low bool, silence float64) bool {
	return text != "" && !(low && silence >= s.cfg.DropSilenceRatio)
}

func segment(start, end int64, text string, low bool) ai.Segment {
	sg := ai.Segment{StartMS: start, EndMS: end, Text: text}
	if low {
		sg.Uncertain, sg.UncertainReason = true, ai.UncertainLowConfidence
	}
	return sg
}

func (s *STT) post(ctx context.Context, path, name, ct, lang string, extra [][2]string) ([]byte, int, error) {
	if s.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
		defer cancel()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fields := append([][2]string{{"model", s.cfg.Model}, {"temperature", "0"}}, extra...)
	if l := requested(lang); l != "" {
		fields = append(fields, [2]string{"language", l})
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	done := make(chan struct{})
	go func() {
		defer close(done)
		pw.CloseWithError(writeForm(mw, fields, name, ct, f))
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BaseURL+"/audio/transcriptions", pr)
	if err != nil {
		pr.Close()
		<-done
		return nil, 0, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := s.cfg.Client.Do(req)
	pr.Close()
	<-done
	if err != nil {
		return nil, 0, ai.TransportError(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, ai.TransportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusBadRequest && undecodable(body) {
			return nil, 0, ai.NewError(jobs.CatAudioUnreadable, false, resp.StatusCode, "")
		}
		return nil, 0, ai.StatusError(resp.StatusCode, resp.Header, time.Now())
	}
	return body, resp.StatusCode, nil
}

func writeForm(mw *multipart.Writer, fields [][2]string, name, ct string, file io.Reader) error {
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return err
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+quoteEscaper.Replace(name)+`"`)
	h.Set("Content-Type", ct)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	return mw.Close()
}

func writeFile(path string, r io.Reader) (int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
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

func contentType(in ai.STTInput) string {
	if in.ContentType == "" {
		return "application/octet-stream"
	}
	return in.ContentType
}

func requested(lang string) string {
	if lang == "id" || lang == "en" {
		return lang
	}
	return ""
}

// The gpt-4o transcribe models never report a language.
func reported(lang string) string {
	if l := requested(lang); l != "" {
		return l
	}
	return "auto"
}

// Undecodable input is permanent like a provider 400; a missing binary or disk error must stay retryable.
func ffmpegError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ai.NewError(jobs.CatTimeout, true, 0, "ffmpeg")
	case errors.Is(err, audio.ErrUndecodable):
		return ai.NewError(jobs.CatAudioUnreadable, false, 0, "ffmpeg")
	}
	return err
}

func secToMS(sec float64) int64 { return int64(math.Floor(sec*1000 + 0.5)) }

func tail(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	r := []rune(text)
	t := string(r[len(r)-n:])
	if r[len(r)-n-1] == ' ' {
		return t
	}
	if i := strings.IndexByte(t, ' '); i >= 0 && i < len(t)-1 {
		t = t[i+1:]
	}
	return t
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
func detected(name string) string {
	switch d := strings.ToLower(strings.TrimSpace(name)); d {
	case "indonesian", "id":
		return "id"
	case "english", "en":
		return "en"
	default:
		if len(d) > 16 {
			return d[:16]
		}
		return d
	}
}
