package audio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const ChunkContentType = M4A

// Chunker splits audio above the STT provider's file size limit with ffmpeg (backend.md §5.1).
type Chunker struct {
	FFmpegPath   string
	ChunkSeconds int
}

type Chunk struct {
	Path     string
	Filename string
	OffsetMS int64
}

// Chunks owns a temp dir; Close removes it.
type Chunks struct {
	Dir   string
	Items []Chunk
}

func (c *Chunks) Close() error { return os.RemoveAll(c.Dir) }

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

// Split copies src (of extension ext) into a temp dir and cuts mono 16 kHz AAC 32 kbps pieces of
// ChunkSeconds; chunk k starts at k × ChunkSeconds × 1000 ms, the last one runs to the end.
func (c Chunker) Split(ctx context.Context, src io.Reader, ext string, durationMS int64) (*Chunks, error) {
	if c.ChunkSeconds <= 0 {
		return nil, fmt.Errorf("audio: chunk seconds must be > 0")
	}
	if ext != "m4a" && ext != "mp3" && ext != "wav" {
		return nil, fmt.Errorf("audio: unsupported extension %q", ext)
	}
	dir, err := os.MkdirTemp("", "phoebe-chunks-")
	if err != nil {
		return nil, err
	}
	out := &Chunks{Dir: dir}
	if err := c.split(ctx, out, src, ext, durationMS); err != nil {
		out.Close()
		return nil, err
	}
	return out, nil
}

func (c Chunker) split(ctx context.Context, out *Chunks, src io.Reader, ext string, durationMS int64) error {
	in := filepath.Join(out.Dir, "source."+ext)
	f, err := os.OpenFile(in, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}

	n := c.Count(durationMS)
	for k := 0; k < n; k++ {
		name := fmt.Sprintf("chunk_%03d.m4a", k)
		path := filepath.Join(out.Dir, name)
		args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
			"-ss", strconv.Itoa(k * c.ChunkSeconds)}
		if k < n-1 {
			args = append(args, "-t", strconv.Itoa(c.ChunkSeconds))
		}
		args = append(args, "-i", in, "-map", "0:a:0", "-vn", "-sn", "-dn", "-map_metadata", "-1",
			"-ac", "1", "-ar", "16000", "-c:a", "aac", "-b:a", "32k", path)
		cmd := exec.CommandContext(ctx, c.FFmpegPath, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ffmpeg chunk %d: %w: %s", k, err, lastLine(stderr.Bytes()))
		}
		out.Items = append(out.Items, Chunk{Path: path, Filename: name, OffsetMS: int64(k) * int64(c.ChunkSeconds) * 1000})
	}
	return nil
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}
