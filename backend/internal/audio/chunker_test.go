package audio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func ffmpegPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("ffmpeg not on PATH (brew install ffmpeg)")
	}
	return p
}

func TestChunkerCount(t *testing.T) {
	c := Chunker{ChunkSeconds: 600}
	for ms, want := range map[int64]int{
		0: 1, 1000: 1, 600_000: 1, 600_999: 1, 601_000: 2, 1_500_000: 3, 1_800_000: 3, 7_200_000: 12, 7_200_500: 12,
	} {
		if got := c.Count(ms); got != want {
			t.Errorf("Count(%d) = %d, want %d", ms, got, want)
		}
	}
}

func TestCheckFFmpeg(t *testing.T) {
	if err := CheckFFmpeg(context.Background(), ffmpegPath(t)); err != nil {
		t.Fatal(err)
	}
	if err := CheckFFmpeg(context.Background(), "/nonexistent/ffmpeg"); err == nil {
		t.Fatal("missing ffmpeg accepted")
	}
}

func TestExtractSourceFormats(t *testing.T) {
	ff := ffmpegPath(t)
	for _, tc := range []struct {
		name   string
		src    []byte
		ext    string
		lastMS int64
	}{
		{"wav", pcm16(5500), "wav", 3500},
		{"m4a", fixture(t, "tone.m4a"), "m4a", 1065},
		{"mp3", fixture(t, "tone.mp3"), "mp3", 1030},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "source."+tc.ext)
			if err := os.WriteFile(in, tc.src, 0o600); err != nil {
				t.Fatal(err)
			}
			for k, want := range []int64{2000, tc.lastMS} {
				out := filepath.Join(dir, fmt.Sprintf("piece_%d.m4a", k))
				dur := int64(2000)
				if k == 1 {
					dur = 0
				}
				if err := Extract(context.Background(), ff, in, out, int64(k)*2000, dur); err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				ct, cd, err := probeBytes(b)
				if err != nil {
					t.Fatalf("piece %d: %v", k, err)
				}
				if ct != ChunkContentType || cd < want-150 || cd > want+150 {
					t.Errorf("piece %d: %s %d ms, want ~%d", k, ct, cd, want)
				}
			}
		})
	}
	if err := Extract(context.Background(), ff, filepath.Join(t.TempDir(), "missing.wav"), filepath.Join(t.TempDir(), "x.m4a"), 0, 0); err == nil {
		t.Fatal("extract of a missing file succeeded")
	}
}
