package openai

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"phoebe/internal/ai"
	"phoebe/internal/audio"
	"phoebe/internal/jobs"
)

type section struct {
	ms   int
	tone bool
}

func toneWAV(sections ...section) ([]byte, int64) {
	var pcm []byte
	n, total := 0, 0
	for _, s := range sections {
		total += s.ms
		for i := 0; i < s.ms*16; i++ {
			var v int16
			if s.tone {
				v = int16(0.3 * 32767 * math.Sin(2*math.Pi*440*float64(n)/16000))
			}
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v))
			n++
		}
	}
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], 16000)
	binary.LittleEndian.PutUint32(h[28:], 32000)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return append(h, pcm...), int64(total)
}

type captured struct {
	fields      map[string][]string
	fileName    string
	fileType    string
	fileBody    []byte
	auth        string
	path        string
	contentType string
}

func (c captured) field(k string) (string, bool) {
	v, ok := c.fields[k]
	if !ok || len(v) != 1 {
		return "", false
	}
	return v[0], true
}

type reply struct {
	status int
	header map[string]string
	body   string
}

type recorder struct {
	mu   sync.Mutex
	reqs []captured
}

func (r *recorder) all() []captured {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]captured(nil), r.reqs...)
}

func scripted(t *testing.T, replies ...reply) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := captured{auth: r.Header.Get("Authorization"), path: r.URL.Path, contentType: r.Header.Get("Content-Type"),
			fields: map[string][]string{}}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Errorf("multipart: %v", err)
			return
		}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("part: %v", err)
				return
			}
			b, _ := io.ReadAll(p)
			if p.FormName() == "file" {
				got.fileName, got.fileType, got.fileBody = p.FileName(), p.Header.Get("Content-Type"), b
			} else {
				got.fields[p.FormName()] = append(got.fields[p.FormName()], string(b))
			}
		}
		rec.mu.Lock()
		i := len(rec.reqs)
		rec.reqs = append(rec.reqs, got)
		rec.mu.Unlock()
		if i >= len(replies) {
			t.Errorf("unexpected request %d", i)
			w.WriteHeader(500)
			return
		}
		for k, v := range replies[i].header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(replies[i].status)
		io.WriteString(w, replies[i].body)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func ffmpegPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("ffmpeg not on PATH (brew install ffmpeg)")
	}
	return p
}

func testConfig(t *testing.T, url, model string) Config {
	return Config{
		BaseURL: url + "/", APIKey: "sk-test", Model: model, Timeout: 5 * time.Second,
		FFmpegPath:   ffmpegPath(t),
		Silence:      audio.SilenceParams{NoiseDB: -35, MinDuration: 500 * time.Millisecond},
		MaxFileBytes: 24_000_000, ChunkSeconds: 600,
		Windows:          audio.WindowPlan{TargetMS: 30000, MinMS: 10000, MaxMS: 45000},
		DropSilenceRatio: 0.8,
		MeanLogprobMin:   -0.7, AvgLogprobMin: -1.0, NoSpeechProbMax: 0.6, CompressionRatioMax: 2.4,
	}
}

func newSTT(t *testing.T, c Config) *STT {
	t.Helper()
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func wavInput(b []byte, durationMS int64, lang string) ai.STTInput {
	return ai.STTInput{Reader: bytes.NewReader(b), Filename: "s1.wav", ContentType: "audio/wav", Language: lang, DurationMS: durationMS}
}

func jsonReply(text string, logprobs ...float64) reply {
	lp := []map[string]any{}
	for _, v := range logprobs {
		lp = append(lp, map[string]any{"token": "t", "logprob": v, "bytes": []int{116}})
	}
	b, _ := json.Marshal(map[string]any{"text": text, "logprobs": lp, "usage": map[string]any{"type": "tokens", "total_tokens": 10}})
	return reply{status: 200, body: string(b)}
}

func near(a, b, tol int64) bool { return a-b <= tol && b-a <= tol }

func probeChunk(t *testing.T, c captured) int64 {
	t.Helper()
	ct, d, err := audio.Probe(bytes.NewReader(c.fileBody), int64(len(c.fileBody)))
	if err != nil || ct != audio.ChunkContentType || c.fileType != audio.ChunkContentType {
		t.Fatalf("chunk %q: %s %s %v", c.fileName, ct, c.fileType, err)
	}
	return d
}

func TestModelCapabilities(t *testing.T) {
	for model, timing := range map[string]string{
		"whisper-1": ai.TimingSegment, "gpt-4o-mini-transcribe": ai.TimingChunk, "gpt-4o-transcribe": ai.TimingChunk,
	} {
		s, err := New(Config{Model: model})
		if err != nil || s.timing != timing {
			t.Errorf("%s: %v %v", model, s, err)
		}
	}
	if _, err := New(Config{Model: "gpt-4o-diarize"}); err == nil {
		t.Error("unknown model accepted")
	}
}

func TestChunkModeJSONLogprobsAndPromptContinuity(t *testing.T) {
	wav, dur := toneWAV(section{25000, true}, section{2000, false}, section{28000, true}, section{2000, false}, section{23000, true})
	long := strings.TrimSpace(strings.Repeat("rapat ", 50))
	srv, rec := scripted(t,
		jsonReply(long, -0.1, -0.2),
		jsonReply("Kita sepakat rilis Jumat.", -1.0, -1.4),
		jsonReply("  ", -0.1),
	)
	s := newSTT(t, testConfig(t, srv.URL, "gpt-4o-mini-transcribe"))
	res, err := s.Transcribe(context.Background(), wavInput(wav, dur, "id"))
	if err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	if len(reqs) != 3 {
		t.Fatalf("requests %d, want 3", len(reqs))
	}
	windows := [][2]int64{{0, 26000}, {26000, 56000}, {56000, 80000}}
	for i, r := range reqs {
		if r.path != "/audio/transcriptions" || r.auth != "Bearer sk-test" || !strings.HasPrefix(r.contentType, "multipart/form-data") {
			t.Errorf("req %d: %+v", i, r)
		}
		for k, v := range map[string]string{"model": "gpt-4o-mini-transcribe", "response_format": "json", "include[]": "logprobs",
			"temperature": "0", "language": "id"} {
			if got, ok := r.field(k); !ok || got != v {
				t.Errorf("req %d field %s = %v", i, k, r.fields[k])
			}
		}
		if _, ok := r.fields["timestamp_granularities[]"]; ok {
			t.Errorf("req %d: timestamp_granularities is whisper-1 only", i)
		}
		if d, want := probeChunk(t, r), windows[i][1]-windows[i][0]; !near(d, want, 200) {
			t.Errorf("req %d chunk %d ms, want ~%d", i, d, want)
		}
	}
	if _, ok := reqs[0].fields["prompt"]; ok {
		t.Error("first chunk must not carry a prompt")
	}
	p1, _ := reqs[1].field("prompt")
	if len([]rune(p1)) > promptTailRunes || !strings.HasSuffix(long, p1) || !strings.HasPrefix(p1, "rapat") || len(p1) < 190 {
		t.Errorf("prompt 2 = %q", p1)
	}
	if p2, _ := reqs[2].field("prompt"); p2 != "Kita sepakat rilis Jumat." {
		t.Errorf("prompt 3 = %q", p2)
	}
	if res.Timing != ai.TimingChunk || res.Language != "id" || !near(res.AudioMS, dur, 5) || len(res.Segments) != 2 {
		t.Fatalf("result %+v", res)
	}
	s0, s1 := res.Segments[0], res.Segments[1]
	if s0.StartMS != 0 || !near(s0.EndMS, 26000, 100) || s0.Text != long || s0.Uncertain || s0.UncertainReason != "" {
		t.Errorf("seg0 %+v", s0)
	}
	if s1.StartMS != s0.EndMS || !near(s1.EndMS, 56000, 100) || s1.Text != "Kita sepakat rilis Jumat." ||
		!s1.Uncertain || s1.UncertainReason != ai.UncertainLowConfidence {
		t.Errorf("seg1 %+v", s1)
	}
}

func TestChunkModeSilentWindowSkipped(t *testing.T) {
	wav, dur := toneWAV(section{25000, true}, section{2000, false}, section{300, true}, section{32700, false}, section{20000, true})
	srv, rec := scripted(t, jsonReply("Pembukaan.", -0.1), jsonReply("Penutupan.", -0.1))
	res, err := newSTT(t, testConfig(t, srv.URL, "gpt-4o-transcribe")).Transcribe(context.Background(), wavInput(wav, dur, "auto"))
	if err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	if len(reqs) != 2 {
		t.Fatalf("requests %d, want 2 (silent window skipped)", len(reqs))
	}
	if _, ok := reqs[0].fields["language"]; ok {
		t.Error("language must not be sent for auto")
	}
	if p, _ := reqs[1].field("prompt"); p != "Pembukaan." {
		t.Errorf("prompt after skipped window = %q", p)
	}
	if len(res.Segments) != 2 || res.Language != "auto" {
		t.Fatalf("result %+v", res)
	}
	s0, s1 := res.Segments[0], res.Segments[1]
	if !near(s0.EndMS, 26000, 100) || !near(s1.StartMS, 43650, 100) || s1.EndMS != dur {
		t.Errorf("segments %+v", res.Segments)
	}
	if want := s0.EndMS + (s1.EndMS - s1.StartMS); res.AudioMS != want {
		t.Errorf("audio ms %d, want %d (skipped window not billed)", res.AudioMS, want)
	}
}

// Window-level silence must not hide or drop real speech; low confidence only flags it for review.
func TestChunkModeShortSpeechInSilentWindowKept(t *testing.T) {
	cases := []struct {
		name    string
		audio   []section
		replies []reply
		want    []string
	}{
		{"short reply in long pause", []section{{20200, true}, {29800, false}, {1400, true}, {58600, false}},
			[]reply{jsonReply("Pembukaan rapat.", -0.1), jsonReply("Ya, setuju, deal.", -1.2)},
			[]string{"Pembukaan rapat.", "Ya, setuju, deal."}},
		{"low confidence speech in mostly silent window", []section{{6000, true}, {48000, false}},
			[]reply{jsonReply("Anggarannya enam puluh juta.", -0.9)},
			[]string{"Anggarannya enam puluh juta."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, rec := scripted(t, c.replies...)
			wav, dur := toneWAV(c.audio...)
			res, err := newSTT(t, testConfig(t, srv.URL, "gpt-4o-mini-transcribe")).Transcribe(context.Background(), wavInput(wav, dur, "id"))
			if err != nil {
				t.Fatal(err)
			}
			if n := len(rec.all()); n != len(c.replies) {
				t.Fatalf("requests %d, want %d", n, len(c.replies))
			}
			if len(res.Segments) != len(c.want) {
				t.Fatalf("segments %+v", res.Segments)
			}
			last := res.Segments[len(res.Segments)-1]
			if last.Text != c.want[len(c.want)-1] || !last.Uncertain || last.UncertainReason != ai.UncertainLowConfidence {
				t.Errorf("low-confidence speech must be kept and flagged: %+v", last)
			}
		})
	}
}

func TestChunkModeUncertaintyAndDropRule(t *testing.T) {
	mostlySilent := []section{{1500, true}, {8500, false}}
	speech := []section{{10000, true}}
	cases := []struct {
		name      string
		audio     []section
		reply     *reply
		keep      bool
		uncertain bool
	}{
		{"low confidence + 85% silence kept uncertain", mostlySilent, ptr(jsonReply("Terima kasih telah menonton.", -2, -1.5)), true, true},
		{"high confidence + 85% silence kept", mostlySilent, ptr(jsonReply("Ya.", -0.05)), true, false},
		{"low confidence on speech kept uncertain", speech, ptr(jsonReply("Anggaran enam puluh juta.", -1.1, -0.9)), true, true},
		{"no logprobs kept", speech, ptr(jsonReply("Baik.")), true, false},
		{"empty text dropped", speech, ptr(jsonReply(" ", -0.1)), false, false},
		{"97% silent window: no call", []section{{300, true}, {9700, false}}, nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var replies []reply
			if c.reply != nil {
				replies = append(replies, *c.reply)
			}
			srv, rec := scripted(t, replies...)
			wav, dur := toneWAV(c.audio...)
			res, err := newSTT(t, testConfig(t, srv.URL, "gpt-4o-mini-transcribe")).Transcribe(context.Background(), wavInput(wav, dur, "id"))
			if err != nil {
				t.Fatal(err)
			}
			if n := len(rec.all()); n != len(replies) {
				t.Fatalf("requests %d, want %d", n, len(replies))
			}
			if res.Segments == nil || len(res.Segments) != map[bool]int{true: 1, false: 0}[c.keep] {
				t.Fatalf("segments %+v", res.Segments)
			}
			if c.keep {
				s := res.Segments[0]
				if s.StartMS != 0 || s.EndMS != dur || s.Uncertain != c.uncertain || (s.UncertainReason != "") != c.uncertain {
					t.Errorf("segment %+v", s)
				}
			}
			if c.reply == nil && res.AudioMS != 0 {
				t.Errorf("audio ms %d for a skipped window", res.AudioMS)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestChunkModeErrors(t *testing.T) {
	wav, dur := toneWAV(section{10000, true})
	cases := []struct {
		name      string
		r         reply
		category  string
		retryable bool
		status    int
	}{
		{"rate limited", reply{429, map[string]string{"Retry-After": "20"}, `{"error":{"message":"secret"}}`}, jobs.CatProviderRateLimited, true, 429},
		{"server error", reply{500, nil, `{"error":{"message":"secret"}}`}, jobs.CatProviderUnavailable, true, 500},
		{"rejected", reply{401, nil, `{"error":{"message":"secret"}}`}, jobs.CatProviderRejected, false, 401},
		{"undecodable audio", reply{400, nil, `{"error":{"message":"Audio file might be corrupted or unsupported"}}`}, jobs.CatAudioUnreadable, false, 400},
		{"invalid json", reply{200, nil, `{"text": secret`}, jobs.CatInvalidOutput, true, 200},
		{"missing text", reply{200, nil, `{"logprobs":[]}`}, jobs.CatInvalidOutput, true, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := scripted(t, c.r)
			_, err := newSTT(t, testConfig(t, srv.URL, "gpt-4o-mini-transcribe")).Transcribe(context.Background(), wavInput(wav, dur, "id"))
			e := wantErr(t, err, c.category, c.retryable, c.status)
			if c.status == 429 && e.RetryAfter != 20*time.Second {
				t.Errorf("retry after %v", e.RetryAfter)
			}
		})
	}
}

func TestChunkModeLaterChunkFailureFailsCall(t *testing.T) {
	wav, dur := toneWAV(section{25000, true}, section{2000, false}, section{23000, true})
	srv, rec := scripted(t, jsonReply("Satu.", -0.1), reply{status: 503})
	_, err := newSTT(t, testConfig(t, srv.URL, "gpt-4o-mini-transcribe")).Transcribe(context.Background(), wavInput(wav, dur, "id"))
	wantErr(t, err, jobs.CatProviderUnavailable, true, 503)
	if n := len(rec.all()); n != 2 {
		t.Errorf("requests %d: no internal retry expected", n)
	}
}

const verbose = `{"task":"transcribe","language":"indonesian","duration":12.345,"text":"…",
 "segments":[
  {"id":0,"start":0.0,"end":4.2,"text":" Halo semua.","avg_logprob":-0.25,"compression_ratio":1.3,"no_speech_prob":0.01},
  {"id":1,"start":4.2,"end":9.85,"text":" Terima kasih telah menonton","avg_logprob":-1.5,"compression_ratio":2.6,"no_speech_prob":0.9},
  {"id":2,"start":4.5,"end":9.0,"text":" Hmm.","avg_logprob":-0.2,"compression_ratio":1.0,"no_speech_prob":0.1},
  {"id":3,"start":9.85,"end":12.3,"text":" Kita sepakat."},
  {"id":4,"start":10.0,"end":12.3,"text":" Mungkin Jumat.","avg_logprob":-1.4,"compression_ratio":1.1,"no_speech_prob":0.1},
  {"id":5,"start":10.0,"end":12.3,"text":" ulang ulang ulang","avg_logprob":-0.3,"compression_ratio":2.6,"no_speech_prob":0.1},
  {"id":6,"start":12.3,"end":12.345,"text":"   "}
 ]}`

func TestSegmentModeVerboseJSON(t *testing.T) {
	wav, dur := toneWAV(section{4200, true}, section{5800, false}, section{2345, true})
	srv, rec := scripted(t, reply{200, map[string]string{"Content-Type": "application/json"}, verbose})
	s := newSTT(t, testConfig(t, srv.URL, "whisper-1"))
	if p, m := s.Name(); p != "openai" || m != "whisper-1" {
		t.Fatalf("name %s %s", p, m)
	}
	res, err := s.Transcribe(context.Background(), wavInput(wav, dur, "id"))
	if err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	if len(reqs) != 1 {
		t.Fatalf("requests %d", len(reqs))
	}
	r := reqs[0]
	for k, v := range map[string]string{"model": "whisper-1", "response_format": "verbose_json", "timestamp_granularities[]": "segment",
		"temperature": "0", "language": "id"} {
		if got, ok := r.field(k); !ok || got != v {
			t.Errorf("field %s = %v", k, r.fields[k])
		}
	}
	if _, ok := r.fields["include[]"]; ok {
		t.Error("whisper-1 request must not ask for logprobs")
	}
	if r.fileName != "s1.wav" || r.fileType != "audio/wav" || !bytes.Equal(r.fileBody, wav) {
		t.Errorf("file part: %q %q %d bytes", r.fileName, r.fileType, len(r.fileBody))
	}
	if res.Timing != ai.TimingSegment || res.Language != "id" || res.AudioMS != 12345 {
		t.Fatalf("result %+v", res)
	}
	type want struct {
		start, end int64
		text       string
		uncertain  bool
	}
	wants := []want{
		{0, 4200, "Halo semua.", false},
		{4500, 9000, "Hmm.", false},
		{9850, 12300, "Kita sepakat.", false},
		{10000, 12300, "Mungkin Jumat.", true},
		{10000, 12300, "ulang ulang ulang", true},
	}
	if len(res.Segments) != len(wants) {
		t.Fatalf("segments %+v", res.Segments)
	}
	for i, w := range wants {
		s := res.Segments[i]
		reason := ""
		if w.uncertain {
			reason = ai.UncertainLowConfidence
		}
		if s.StartMS != w.start || s.EndMS != w.end || s.Text != w.text || s.Uncertain != w.uncertain || s.UncertainReason != reason {
			t.Errorf("seg %d = %+v, want %+v", i, s, w)
		}
	}
}

func TestSegmentModeLargeFileSplitOffsets(t *testing.T) {
	wav, dur := toneWAV(section{12000, true})
	seg := func(d float64, text string) reply {
		return reply{status: 200, body: `{"language":"english","duration":` + strconv.FormatFloat(d, 'f', -1, 64) +
			`,"segments":[{"start":0.5,"end":2.0,"text":"` + text + `"}]}`}
	}
	srv, rec := scripted(t, seg(5, "a"), seg(5, "b"), seg(2, "c"))
	c := testConfig(t, srv.URL, "whisper-1")
	c.MaxFileBytes, c.ChunkSeconds = 1000, 5
	res, err := newSTT(t, c).Transcribe(context.Background(), wavInput(wav, dur, "auto"))
	if err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	if len(reqs) != 3 {
		t.Fatalf("requests %d", len(reqs))
	}
	for i, want := range []int64{5000, 5000, 2000} {
		if reqs[i].fileName != []string{"chunk_000.m4a", "chunk_001.m4a", "chunk_002.m4a"}[i] {
			t.Errorf("chunk %d name %q", i, reqs[i].fileName)
		}
		if d := probeChunk(t, reqs[i]); !near(d, want, 150) {
			t.Errorf("chunk %d %d ms, want ~%d", i, d, want)
		}
	}
	if res.Language != "en" || res.AudioMS != 12000 || len(res.Segments) != 3 {
		t.Fatalf("result %+v", res)
	}
	for i, s := range res.Segments {
		if s.StartMS != int64(i)*5000+500 || s.EndMS != int64(i)*5000+2000 {
			t.Errorf("seg %d offsets %+v", i, s)
		}
	}
}

func TestSegmentModeBadTimes(t *testing.T) {
	wav, dur := toneWAV(section{3000, true})
	srv, _ := scripted(t, reply{200, nil, `{"duration":3,"segments":[{"start":2,"end":1,"text":"mundur"}]}`})
	_, err := newSTT(t, testConfig(t, srv.URL, "whisper-1")).Transcribe(context.Background(), wavInput(wav, dur, "id"))
	wantErr(t, err, jobs.CatInvalidOutput, true, 200)
}

func wantErr(t *testing.T, err error, category string, retryable bool, status int) *ai.Error {
	t.Helper()
	var e *ai.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *ai.Error, got %v", err)
	}
	if e.Category != category || e.Retryable != retryable || e.HTTPStatus != status {
		t.Fatalf("got %+v, want %s retryable=%v status=%d", e, category, retryable, status)
	}
	if strings.Contains(e.Error(), "secret") || strings.Contains(e.Detail(), "secret") {
		t.Fatalf("provider body leaked: %q", e.Error())
	}
	return e
}

func TestSegmentModeErrors(t *testing.T) {
	wav, dur := toneWAV(section{3000, true})
	cases := []struct {
		name      string
		r         reply
		category  string
		retryable bool
	}{
		{"rate limited", reply{429, map[string]string{"Retry-After": "20"}, `{"error":{"message":"secret rate"}}`}, jobs.CatProviderRateLimited, true},
		{"server error", reply{500, nil, `{"error":{"message":"secret"}}`}, jobs.CatProviderUnavailable, true},
		{"request timeout", reply{408, nil, ``}, jobs.CatProviderUnavailable, true},
		{"too large", reply{413, nil, ``}, jobs.CatProviderRejected, false},
		{"invalid format", reply{400, nil, `{"error":{"message":"Invalid file format. Supported formats: ['m4a']"}}`}, jobs.CatAudioUnreadable, false},
		{"invalid json", reply{200, nil, `{"segments": [ secret`}, jobs.CatInvalidOutput, true},
		{"missing segments", reply{200, nil, `{"text":"secret only text"}`}, jobs.CatProviderRejected, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := scripted(t, c.r)
			_, err := newSTT(t, testConfig(t, srv.URL, "whisper-1")).Transcribe(context.Background(), wavInput(wav, dur, "id"))
			e := wantErr(t, err, c.category, c.retryable, c.r.status)
			if c.r.status == 429 && e.RetryAfter != 20*time.Second {
				t.Errorf("retry after %v", e.RetryAfter)
			}
		})
	}
}

func TestTranscribeTimeoutPerRequest(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	c := testConfig(t, srv.URL, "gpt-4o-mini-transcribe")
	c.Timeout = 200 * time.Millisecond
	wav, dur := toneWAV(section{3000, true})
	_, err := newSTT(t, c).Transcribe(context.Background(), wavInput(wav, dur, "id"))
	wantErr(t, err, jobs.CatProviderTimeout, true, 0)
}

func TestTranscribeNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	wav, dur := toneWAV(section{3000, true})
	_, err := newSTT(t, testConfig(t, url, "whisper-1")).Transcribe(context.Background(), wavInput(wav, dur, "id"))
	wantErr(t, err, jobs.CatProviderUnavailable, true, 0)
}

func TestTranscribeUnreadableAudioBeforeAnyCall(t *testing.T) {
	speech, err := os.ReadFile("testdata/speech_id.m4a")
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string][]byte{"garbage": []byte("not audio"), "truncated m4a": speech[:20000]}
	for _, model := range []string{"gpt-4o-mini-transcribe", "whisper-1"} {
		for name, b := range inputs {
			t.Run(model+"/"+name, func(t *testing.T) {
				srv, rec := scripted(t)
				in := ai.STTInput{Reader: bytes.NewReader(b), Filename: "x.m4a", ContentType: "audio/mp4", Language: "id", DurationMS: 5000}
				_, err := newSTT(t, testConfig(t, srv.URL, model)).Transcribe(context.Background(), in)
				wantErr(t, err, jobs.CatAudioUnreadable, false, 0)
				if n := len(rec.all()); n != 0 {
					t.Errorf("requests %d", n)
				}
			})
		}
	}
}

// A missing ffmpeg or a deadline kill must not fail the meeting permanently as audio_unreadable.
func TestTranscribeFFmpegFailuresNotUnreadable(t *testing.T) {
	wav, dur := toneWAV(section{3000, true})
	stub := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	signaled := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(signaled, []byte("#!/bin/sh\necho 'Exiting normally, received signal 15.' >&2\nexit 255\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, ffmpeg string
		timeout      time.Duration
		category     string
	}{
		{"missing binary", filepath.Join(t.TempDir(), "no-ffmpeg"), time.Minute, ""},
		{"ffmpeg caught SIGTERM", signaled, time.Minute, ""},
		{"deadline mid-run", stub, 300 * time.Millisecond, jobs.CatTimeout},
		{"deadline already passed", ffmpegPath(t), -time.Second, jobs.CatTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, rec := scripted(t)
			cfg := testConfig(t, srv.URL, "gpt-4o-mini-transcribe")
			cfg.FFmpegPath = c.ffmpeg
			ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
			defer cancel()
			_, err := newSTT(t, cfg).Transcribe(ctx, wavInput(wav, dur, "id"))
			if err == nil {
				t.Fatal("no error")
			}
			if got := ai.CategoryOf(err); got == jobs.CatAudioUnreadable {
				t.Fatalf("classified audio_unreadable: %v", err)
			}
			if c.category != "" {
				wantErr(t, err, c.category, true, 0)
			} else if _, ok := errors.AsType[*ai.Error](err); ok {
				t.Fatalf("missing binary classified: %v", err)
			}
			if n := len(rec.all()); n != 0 {
				t.Errorf("requests %d", n)
			}
		})
	}
}

func TestLanguageReported(t *testing.T) {
	wav, dur := toneWAV(section{3000, true})
	cases := []struct {
		model, body, req, want string
	}{
		{"gpt-4o-mini-transcribe", `{"text":"Halo."}`, "", "auto"},
		{"gpt-4o-mini-transcribe", `{"text":"Halo."}`, "auto", "auto"},
		{"gpt-4o-mini-transcribe", `{"text":"Halo."}`, "en", "en"},
		{"whisper-1", `{"duration":3,"segments":[]}`, "", "auto"},
		{"whisper-1", `{"duration":3,"segments":[]}`, "id", "id"},
		{"whisper-1", `{"language":"english","duration":3,"segments":[]}`, "", "en"},
	}
	for _, c := range cases {
		srv, _ := scripted(t, reply{200, nil, c.body})
		res, err := newSTT(t, testConfig(t, srv.URL, c.model)).Transcribe(context.Background(), wavInput(wav, dur, c.req))
		if err != nil || res.Language != c.want {
			t.Errorf("%s req %q: language %q, want %q (%v)", c.model, c.req, res.Language, c.want, err)
		}
	}
}

func TestTail(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"pendek", 200, "pendek"},
		{"satu dua tiga", 8, "dua tiga"},
		{"satu dua tiga", 7, "tiga"},
		{"satu dua tiga", 9, "dua tiga"},
		{"satu dua tiga", 4, "tiga"},
		{"abcdefghij", 4, "ghij"},
		{"rapat dengan Ibu Sri Ayu", 7, "Sri Ayu"},
		{"keputusan déjà vu", 7, "déjà vu"},
	} {
		if got := tail(c.in, c.n); got != c.want {
			t.Errorf("tail(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestChunkModeSpeechFixtureCutsAtPause(t *testing.T) {
	b, err := os.ReadFile("testdata/speech_id.m4a")
	if err != nil {
		t.Fatal(err)
	}
	ct, dur, err := audio.Probe(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	srv, rec := scripted(t, jsonReply("Selamat pagi semua.", -0.1), jsonReply("Kita sepakat rilis.", -0.1))
	res, err := newSTT(t, testConfig(t, srv.URL, "gpt-4o-mini-transcribe")).Transcribe(context.Background(),
		ai.STTInput{Reader: bytes.NewReader(b), Filename: "speech_id.m4a", ContentType: ct, Language: "id", DurationMS: dur})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.all()); n != 2 || len(res.Segments) != 2 {
		t.Fatalf("requests %d, segments %+v", n, res.Segments)
	}
	// The fixture has a 3.3 s pause at 28.1–31.4 s; the cut lands at its midpoint.
	if cut := res.Segments[0].EndMS; !near(cut, 29725, 150) || res.Segments[1].StartMS != cut || res.Segments[1].EndMS != dur {
		t.Errorf("segments %+v", res.Segments)
	}
}
