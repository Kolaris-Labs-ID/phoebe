package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type section struct {
	ms   int
	tone bool
}

func toneWAV(sections ...section) []byte {
	var pcm []byte
	n := 0
	for _, s := range sections {
		for i := 0; i < s.ms*16; i++ {
			var v int16
			if s.tone {
				v = int16(0.3 * 32767 * math.Sin(2*math.Pi*440*float64(n)/16000))
			}
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v))
			n++
		}
	}
	return riff(fmtChunk(1, 1, 16000, 16), riffChunk("data", pcm))
}

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func near(a, b, tol int64) bool { return a-b <= tol && b-a <= tol }

var defaultSilence = SilenceParams{NoiseDB: -35, MinDuration: 500 * time.Millisecond}

func TestDetectSilence(t *testing.T) {
	src := writeTemp(t, "a.wav", toneWAV(section{2000, true}, section{2000, false}, section{2000, true}, section{300, false},
		section{2000, true}, section{2000, false}))
	got, err := DetectSilence(context.Background(), ffmpegPath(t), src, 10300, defaultSilence)
	if err != nil {
		t.Fatal(err)
	}
	want := []Interval{{2000, 4000}, {8300, 10300}}
	if len(got) != len(want) {
		t.Fatalf("silences %v, want ~%v (300 ms gap under the min duration is not silence)", got, want)
	}
	for i := range want {
		if !near(got[i].StartMS, want[i].StartMS, 60) || !near(got[i].EndMS, want[i].EndMS, 60) {
			t.Errorf("silence %d = %v, want ~%v", i, got[i], want[i])
		}
	}
	if _, err := DetectSilence(context.Background(), ffmpegPath(t), writeTemp(t, "x.wav", []byte("nope")), 1000, defaultSilence); !errors.Is(err, ErrUndecodable) {
		t.Fatalf("garbage: %v, want ErrUndecodable", err)
	}
	if _, err := DetectSilence(context.Background(), filepath.Join(t.TempDir(), "no-ffmpeg"), src, 10300, defaultSilence); err == nil || errors.Is(err, ErrUndecodable) {
		t.Fatalf("missing binary: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DetectSilence(ctx, ffmpegPath(t), src, 10300, defaultSilence); !errors.Is(err, context.Canceled) || errors.Is(err, ErrUndecodable) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestDetectSilenceSignalExitNotUndecodable(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	wrapper := filepath.Join(dir, "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\necho $$ > '%s'\nexec '%s' -re \"$@\"\n", pidFile, ffmpegPath(t))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	src := writeTemp(t, "a.wav", toneWAV(section{30000, true}))
	errc := make(chan error, 1)
	go func() {
		_, err := DetectSilence(context.Background(), wrapper, src, 30000, defaultSilence)
		errc <- err
	}()
	pid := 0
	for deadline := time.Now().Add(10 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if pid == 0 {
		t.Fatal("ffmpeg did not start")
	}
	time.Sleep(time.Second)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := <-errc
	if err == nil || !strings.Contains(err.Error(), "exit status 255") || errors.Is(err, ErrUndecodable) {
		t.Fatalf("SIGTERM caught by ffmpeg: %v, want exit 255 that is not ErrUndecodable", err)
	}
}

func TestParseSilence(t *testing.T) {
	out := []byte(`[silencedetect @ 0x1] silence_end: 1.5 | silence_duration: 1.5
size=N/A time=00:00:03.00
[silencedetect @ 0x1] silence_start: 2.25
[silencedetect @ 0x1] silence_end: 3 | silence_duration: 0.75
[silencedetect @ 0x1] silence_start: -0.01
[silencedetect @ 0x1] silence_end: nan | silence_duration: nan
[silencedetect @ 0x1] silence_start: 9.5
`)
	got := parseSilence(out, 10000)
	want := []Interval{{0, 1500}, {2250, 3000}, {9500, 10000}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %v, want %v", i, got[i], want[i])
		}
	}
	if s := parseSilence([]byte("silence_start: 12\n"), 10000); len(s) != 0 {
		t.Errorf("silence past the end kept: %v", s)
	}
}

func TestCoverage(t *testing.T) {
	sil := []Interval{{1000, 2000}, {3000, 6000}}
	for _, c := range []struct {
		start, end int64
		want       float64
	}{
		{0, 1000, 0}, {0, 2000, 0.5}, {1500, 3500, 0.5}, {3000, 6000, 1}, {0, 10000, 0.4}, {1500, 1500, 1}, {2500, 2500, 0},
	} {
		if got := Coverage(sil, c.start, c.end); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Coverage(%d,%d) = %v, want %v", c.start, c.end, got, c.want)
		}
	}
	if Coverage(nil, 0, 1000) != 0 {
		t.Error("no silences")
	}
}

func TestPlanWindows(t *testing.T) {
	p := WindowPlan{TargetMS: 30000, MinMS: 10000, MaxMS: 45000}
	sil := func(mids ...int64) []Interval {
		var out []Interval
		for _, m := range mids {
			out = append(out, Interval{m - 400, m + 400})
		}
		return out
	}
	cases := []struct {
		name     string
		duration int64
		silences []Interval
		cuts     []int64
	}{
		{"zero", 0, nil, nil},
		{"short single window", 45000, sil(20000), []int64{45000}},
		{"closest midpoint to target", 80000, sil(12000, 26000, 41000, 56000), []int64{26000, 56000, 80000}},
		{"midpoint below min ignored", 60000, sil(5000), []int64{45000, 60000}},
		{"midpoint leaving short remainder ignored", 52000, sil(44000), []int64{42000, 52000}},
		{"no silence hard cuts", 100000, nil, []int64{45000, 90000, 100000}},
		{"hard cut pulled back for remainder", 95000, nil, []int64{45000, 85000, 95000}},
		{"two hours", 7_200_000, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PlanWindows(c.duration, c.silences, p)
			if c.duration == 0 {
				if got != nil {
					t.Fatalf("got %v", got)
				}
				return
			}
			var prev int64
			for i, w := range got {
				if w.StartMS != prev || w.EndMS <= w.StartMS || w.DurationMS() > p.MaxMS {
					t.Fatalf("window %d %v not contiguous/bounded: %v", i, w, got)
				}
				if w.DurationMS() < p.MinMS && c.duration >= p.MinMS {
					t.Fatalf("window %d %v under min: %v", i, w, got)
				}
				prev = w.EndMS
			}
			if prev != c.duration {
				t.Fatalf("windows end at %d, want %d", prev, c.duration)
			}
			if c.cuts == nil {
				return
			}
			if len(got) != len(c.cuts) {
				t.Fatalf("got %v, want ends %v", got, c.cuts)
			}
			for i, end := range c.cuts {
				if got[i].EndMS != end {
					t.Errorf("window %d ends %d, want %d", i, got[i].EndMS, end)
				}
			}
		})
	}
}

func TestPlanWindowsNeverUnderMin(t *testing.T) {
	plans := []WindowPlan{
		{TargetMS: 30000, MinMS: 10000, MaxMS: 45000},
		{TargetMS: 20000, MinMS: 20000, MaxMS: 40000},
		{TargetMS: 30000, MinMS: 30000, MaxMS: 60000},
		{TargetMS: 45000, MinMS: 15000, MaxMS: 45000},
		{TargetMS: 1000, MinMS: 1000, MaxMS: 2000},
	}
	rng := rand.New(rand.NewPCG(1, 2))
	layouts := map[string]func(dur int64) []Interval{
		"none": func(int64) []Interval { return nil },
		"every 7s": func(dur int64) []Interval {
			var out []Interval
			for m := int64(7000); m < dur; m += 7000 {
				out = append(out, Interval{m - 300, min(m+300, dur)})
			}
			return out
		},
		"one long pause": func(dur int64) []Interval { return []Interval{{dur / 3, dur / 3 * 2}} },
		"random": func(dur int64) []Interval {
			var out []Interval
			for c := rng.Int64N(5000); c < dur; {
				end := min(c+500+rng.Int64N(8000), dur)
				out = append(out, Interval{c, end})
				c = end + rng.Int64N(25000) + 1
			}
			return out
		},
	}
	for _, p := range plans {
		durs := []int64{1, p.MinMS - 1, p.MinMS, p.MaxMS, p.MaxMS + 1, p.MaxMS + p.MinMS - 1, p.MaxMS + p.MinMS, 2*p.MaxMS + 1, 7_200_000}
		for d := int64(500); d <= 400_000; d += 997 {
			durs = append(durs, d)
		}
		for _, dur := range durs {
			for name, layout := range layouts {
				sil := layout(dur)
				got := PlanWindows(dur, sil, p)
				ctx := fmt.Sprintf("plan %+v dur %d %s", p, dur, name)
				var prev int64
				for i, w := range got {
					if w.StartMS != prev || w.DurationMS() > p.MaxMS {
						t.Fatalf("%s: window %d %v not contiguous/bounded: %v", ctx, i, w, got)
					}
					if w.DurationMS() < p.MinMS && !(len(got) == 1 && dur < p.MinMS) {
						t.Fatalf("%s: window %d %v under min: %v (silences %v)", ctx, i, w, got, sil)
					}
					prev = w.EndMS
				}
				if prev != dur {
					t.Fatalf("%s: windows end at %d: %v", ctx, prev, got)
				}
			}
		}
	}
}

func TestExtract(t *testing.T) {
	src := writeTemp(t, "a.wav", toneWAV(section{6000, true}))
	dir := t.TempDir()
	for _, c := range []struct {
		start, dur, want int64
	}{{2000, 3000, 3000}, {4500, 0, 1500}} {
		dst := filepath.Join(dir, "c.m4a")
		if err := Extract(context.Background(), ffmpegPath(t), src, dst, c.start, c.dur); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		ct, d, err := probeBytes(b)
		if err != nil || ct != ChunkContentType || !near(d, c.want, 150) {
			t.Errorf("extract %d+%d: %s %d ms %v, want ~%d", c.start, c.dur, ct, d, err, c.want)
		}
	}
}
