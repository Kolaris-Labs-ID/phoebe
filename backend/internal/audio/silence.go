package audio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Interval struct {
	StartMS, EndMS int64
}

func (i Interval) DurationMS() int64 { return i.EndMS - i.StartMS }

type SilenceParams struct {
	NoiseDB     float64
	MinDuration time.Duration
}

// DetectSilence writes no output, so a non-zero ffmpeg exit is the input's fault, except 255: ffmpeg's exit after it
// catches SIGTERM/SIGINT (systemd stop or Ctrl-C reaching the worker's process group). A signal kill, cancel or missing
// binary is not the input's fault either.
var ErrUndecodable = errors.New("audio: ffmpeg cannot decode input")

const ffmpegSignalExit = 255

func DetectSilence(ctx context.Context, ffmpegPath, path string, durationMS int64, p SilenceParams) ([]Interval, error) {
	filter := fmt.Sprintf("silencedetect=noise=%sdB:d=%s",
		strconv.FormatFloat(p.NoiseDB, 'f', -1, 64), strconv.FormatFloat(p.MinDuration.Seconds(), 'f', -1, 64))
	cmd := exec.CommandContext(ctx, ffmpegPath, "-nostdin", "-hide_banner", "-nostats", "-i", path,
		"-map", "0:a:0", "-vn", "-sn", "-dn", "-af", filter, "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() > 0 && ee.ExitCode() != ffmpegSignalExit {
			err = fmt.Errorf("%w: %w", ErrUndecodable, err)
		}
		return nil, fmt.Errorf("ffmpeg silencedetect: %w: %s", err, lastLine(stderr.Bytes()))
	}
	return parseSilence(stderr.Bytes(), durationMS), nil
}

func parseSilence(out []byte, durationMS int64) []Interval {
	var res []Interval
	var start int64
	open := false
	add := func(start, end int64) {
		start, end = max(start, 0), min(end, durationMS)
		if end > start {
			res = append(res, Interval{start, end})
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if v, ok := field(line, "silence_start:"); ok {
			start, open = v, true
		} else if v, ok := field(line, "silence_end:"); ok {
			if !open {
				start = 0
			}
			add(start, v)
			open = false
		}
	}
	if open {
		add(start, durationMS)
	}
	return res
}

func field(line, key string) (int64, bool) {
	i := strings.Index(line, key)
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(line[i+len(key):])
	if len(f) == 0 {
		return 0, false
	}
	s, err := strconv.ParseFloat(f[0], 64)
	if err != nil || math.IsNaN(s) || math.IsInf(s, 0) {
		return 0, false
	}
	return int64(math.Round(s * 1000)), true
}

func Coverage(silences []Interval, startMS, endMS int64) float64 {
	if endMS <= startMS {
		for _, s := range silences {
			if s.StartMS <= startMS && startMS <= s.EndMS {
				return 1
			}
		}
		return 0
	}
	var covered int64
	for _, s := range silences {
		lo, hi := max(s.StartMS, startMS), min(s.EndMS, endMS)
		if hi > lo {
			covered += hi - lo
		}
	}
	return float64(covered) / float64(endMS-startMS)
}

type WindowPlan struct {
	TargetMS, MinMS, MaxMS int64
}

func PlanWindows(durationMS int64, silences []Interval, p WindowPlan) []Interval {
	if durationMS <= 0 {
		return nil
	}
	var out []Interval
	cursor := int64(0)
	for durationMS-cursor > p.MaxMS {
		lo, hi := cursor+p.MinMS, min(cursor+p.MaxMS, durationMS-p.MinMS)
		cut, best := int64(-1), int64(math.MaxInt64)
		for _, s := range silences {
			m := (s.StartMS + s.EndMS) / 2
			if m < lo || m > hi {
				continue
			}
			if d := abs(m - (cursor + p.TargetMS)); d < best {
				cut, best = m, d
			}
		}
		if cut < 0 {
			cut = max(hi, cursor+1)
		}
		out = append(out, Interval{cursor, cut})
		cursor = cut
	}
	return append(out, Interval{cursor, durationMS})
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
