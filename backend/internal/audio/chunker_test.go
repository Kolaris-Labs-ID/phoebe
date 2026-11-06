package audio

import (
	"bytes"
	"context"
	"os"
	"os/exec"
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

func TestChunkerSplit(t *testing.T) {
	c := Chunker{FFmpegPath: ffmpegPath(t), ChunkSeconds: 2}
	for _, tc := range []struct {
		name    string
		src     []byte
		ext     string
		offsets []int64
		lastMS  int64
	}{
		{"wav", pcm16(5500), "wav", []int64{0, 2000, 4000}, 1500},
		{"wav remainder folded", pcm16(4500), "wav", []int64{0, 2000}, 2500},
		{"m4a", fixture(t, "tone.m4a"), "m4a", []int64{0, 2000}, 1065},
		{"mp3", fixture(t, "tone.mp3"), "mp3", []int64{0, 2000}, 1030},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d, err := probeBytes(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			chunks, err := c.Split(context.Background(), bytes.NewReader(tc.src), tc.ext, d)
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks.Items) != len(tc.offsets) {
				t.Fatalf("chunks = %d, want %d", len(chunks.Items), len(tc.offsets))
			}
			for k, ch := range chunks.Items {
				if ch.OffsetMS != tc.offsets[k] {
					t.Errorf("chunk %d offset %d, want %d", k, ch.OffsetMS, tc.offsets[k])
				}
				b, err := os.ReadFile(ch.Path)
				if err != nil {
					t.Fatal(err)
				}
				ct, cd, err := probeBytes(b)
				if err != nil {
					t.Fatalf("chunk %d: %v", k, err)
				}
				want := int64(2000)
				if k == len(chunks.Items)-1 {
					want = tc.lastMS
				}
				if ct != ChunkContentType || cd < want-150 || cd > want+150 {
					t.Errorf("chunk %d: %s %d ms, want ~%d", k, ct, cd, want)
				}
			}
			if err := chunks.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(chunks.Dir); !os.IsNotExist(err) {
				t.Fatalf("temp dir not removed: %v", err)
			}
		})
	}
}

func TestChunkerSplitFailureCleansUp(t *testing.T) {
	c := Chunker{FFmpegPath: ffmpegPath(t), ChunkSeconds: 2}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if _, err := c.Split(context.Background(), bytes.NewReader([]byte("not audio at all")), "wav", 5000); err == nil {
		t.Fatal("split of garbage succeeded")
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("temp dir left behind: %v", left)
	}
	if _, err := c.Split(context.Background(), bytes.NewReader(pcm16(3000)), "../x", 3000); err == nil {
		t.Fatal("bad ext accepted")
	}
}
