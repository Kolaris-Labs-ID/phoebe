package audio

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const ChunkContentType = M4A

// Chunker plans fixed-length pieces for audio above the STT provider's file size limit (whisper-1, backend.md §6.1).
type Chunker struct {
	ChunkSeconds int
}

func CheckFFmpeg(ctx context.Context, path string) error {
	p, err := exec.LookPath(path)
	if err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	if out, err := exec.CommandContext(ctx, p, "-version").CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg -version: %w: %s", err, lastLine(out))
	}
	return nil
}

// Count is the number of chunks for a duration. A remainder under MinDurationMS is folded into the
// previous chunk: providers reject sub-second files and it would cost a whole extra call.
func (c Chunker) Count(durationMS int64) int {
	step := int64(c.ChunkSeconds) * 1000
	n := (durationMS + step - 1) / step
	if n > 1 && durationMS%step != 0 && durationMS%step < MinDurationMS {
		n--
	}
	return int(max(n, 1))
}

func Extract(ctx context.Context, ffmpegPath, src, dst string, startMS, durMS int64) error {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-ss", msToSec(startMS)}
	if durMS > 0 {
		args = append(args, "-t", msToSec(durMS))
	}
	args = append(args, "-i", src, "-map", "0:a:0", "-vn", "-sn", "-dn", "-map_metadata", "-1",
		"-ac", "1", "-ar", "16000", "-c:a", "aac", "-b:a", "32k", dst)
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, lastLine(stderr.Bytes()))
	}
	return nil
}

func msToSec(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}
