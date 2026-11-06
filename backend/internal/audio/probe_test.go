package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"phoebe/internal/apperr"
)

func probeBytes(b []byte) (string, int64, error) {
	return Probe(bytes.NewReader(b), int64(len(b)))
}

func wantCode(t *testing.T, err error, code apperr.Code) {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func wantOK(t *testing.T, b []byte, ct string, minMS, maxMS int64) {
	t.Helper()
	gotCT, d, err := probeBytes(b)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if gotCT != ct || d < minMS || d > maxMS {
		t.Fatalf("got %s %d ms, want %s in [%d,%d]", gotCT, d, ct, minMS, maxMS)
	}
}

func riffChunk(id string, body []byte) []byte {
	b := make([]byte, 8, 8+len(body)+1)
	copy(b, id)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	b = append(b, body...)
	if len(body)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func fmtChunk(format, channels uint16, rate uint32, bits uint16) []byte {
	body := make([]byte, 16)
	align := channels * bits / 8
	binary.LittleEndian.PutUint16(body[0:], format)
	binary.LittleEndian.PutUint16(body[2:], channels)
	binary.LittleEndian.PutUint32(body[4:], rate)
	binary.LittleEndian.PutUint32(body[8:], rate*uint32(align))
	binary.LittleEndian.PutUint16(body[12:], align)
	binary.LittleEndian.PutUint16(body[14:], bits)
	return riffChunk("fmt ", body)
}

func riff(chunks ...[]byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WAVE")
	for _, c := range chunks {
		b = append(b, c...)
	}
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

// pcm16 is 16-bit mono 8 kHz: 16 bytes per ms.
func pcm16(ms int) []byte {
	return riff(fmtChunk(1, 1, 8000, 16), riffChunk("data", make([]byte, ms*16)))
}

func TestProbeWAV(t *testing.T) {
	wantOK(t, pcm16(2000), WAV, 2000, 2000)
	wantOK(t, riff(fmtChunk(3, 2, 48000, 32), riffChunk("data", make([]byte, 48000*8*3/2))), WAV, 1500, 1500)
	wantOK(t, riff(fmtChunk(0xFFFE, 1, 8000, 16), riffChunk("data", make([]byte, 32000))), WAV, 2000, 2000)
	wantOK(t, riff(fmtChunk(1, 1, 8000, 16), riffChunk("fact", []byte{1, 2, 3}), riffChunk("LIST", make([]byte, 10)), riffChunk("data", make([]byte, 32000))), WAV, 2000, 2000)
	wantOK(t, riff(fmtChunk(1, 1, 8000, 16), riffChunk("data", make([]byte, 32000)), riffChunk("LIST", make([]byte, 21)), riffChunk("id3 ", make([]byte, 7))), WAV, 2000, 2000)
	wantOK(t, riff(fmtChunk(1, 1, 1000, 8), riffChunk("data", make([]byte, 1501))), WAV, 1501, 1501)

	_, _, err := probeBytes(pcm16(999))
	wantCode(t, err, apperr.AudioEmpty)

	b := pcm16(2000)
	binary.LittleEndian.PutUint32(b[40:], 64000)
	_, _, err = probeBytes(b)
	wantCode(t, err, apperr.AudioCorrupt)

	_, _, err = probeBytes(pcm16(2000)[:44+16000])
	wantCode(t, err, apperr.AudioCorrupt)

	_, _, err = probeBytes(append(pcm16(2000), make([]byte, 100)...))
	wantCode(t, err, apperr.AudioCorrupt)

	_, _, err = probeBytes(append(pcm16(2000), riffChunk("junk", make([]byte, 4))...))
	wantCode(t, err, apperr.AudioCorrupt)

	b = pcm16(2000)
	binary.LittleEndian.PutUint32(b[28:], 0)
	_, _, err = probeBytes(b)
	wantCode(t, err, apperr.AudioCorrupt)

	b = pcm16(2000)
	binary.LittleEndian.PutUint32(b[28:], 1000)
	_, _, err = probeBytes(b)
	wantCode(t, err, apperr.AudioCorrupt)

	_, _, err = probeBytes(riff(fmtChunk(2, 1, 8000, 4), riffChunk("data", make([]byte, 8000))))
	wantCode(t, err, apperr.AudioUnsupported)

	_, _, err = probeBytes(riff(fmtChunk(1, 1, 8000, 16)))
	wantCode(t, err, apperr.AudioCorrupt)

	_, _, err = probeBytes(riff(riffChunk("data", make([]byte, 32000))))
	wantCode(t, err, apperr.AudioCorrupt)
}

func TestProbeDurationLimitBoundary(t *testing.T) {
	const max = 7_200_000
	for _, tc := range []struct {
		ms      int
		tooLong bool
	}{{max, false}, {max + 1, true}} {
		_, d, err := probeBytes(riff(fmtChunk(1, 1, 1000, 8), riffChunk("data", make([]byte, tc.ms))))
		if err != nil || d != int64(tc.ms) {
			t.Fatalf("probe %d: %d %v", tc.ms, d, err)
		}
		err = CheckDuration(d, max)
		if !tc.tooLong {
			if err != nil {
				t.Fatalf("%d ms: %v", tc.ms, err)
			}
			continue
		}
		wantCode(t, err, apperr.AudioTooLong)
		var ae *apperr.Error
		errors.As(err, &ae)
		if ae.Details["duration_ms"] != int64(max+1) || ae.Details["max_duration_ms"] != int64(max) {
			t.Fatalf("details = %v", ae.Details)
		}
	}
}

// MPEG-1 Layer III, 128 kbps, 48 kHz, mono: 384-byte frames of 24 ms.
func mp3Frame() []byte {
	f := make([]byte, 384)
	copy(f, []byte{0xFF, 0xFB, 0x94, 0xC0})
	return f
}

func mp3Frames(n int) []byte {
	var b []byte
	for range n {
		b = append(b, mp3Frame()...)
	}
	return b
}

func xingFrame(frames uint32) []byte {
	f := mp3Frame()
	copy(f[4+17:], "Xing")
	binary.BigEndian.PutUint32(f[4+17+4:], 1)
	binary.BigEndian.PutUint32(f[4+17+8:], frames)
	return f
}

func id3v2(body int) []byte {
	h := []byte("ID3\x04\x00\x00")
	h = append(h, byte(body>>21&0x7F), byte(body>>14&0x7F), byte(body>>7&0x7F), byte(body&0x7F))
	return append(h, make([]byte, body)...)
}

func TestProbeMP3(t *testing.T) {
	wantOK(t, mp3Frames(84), MP3, 2016, 2016)

	tag := append([]byte("TAG"), make([]byte, 125)...)
	b := append(id3v2(300), mp3Frames(84)...)
	wantOK(t, append(b, tag...), MP3, 2016, 2016)

	b = append(mp3Frames(40), []byte("garbage\xff\x00junk")...)
	wantOK(t, append(b, mp3Frames(44)...), MP3, 2016, 2016)

	wantOK(t, append(mp3Frames(84), mp3Frame()[:100]...), MP3, 2016, 2016)

	wantOK(t, append(xingFrame(84), mp3Frames(84)...), MP3, 2016, 2016)

	_, _, err := probeBytes(append(xingFrame(200), mp3Frames(84)...))
	wantCode(t, err, apperr.AudioCorrupt)

	_, _, err = probeBytes(mp3Frames(10))
	wantCode(t, err, apperr.AudioEmpty)

	_, _, err = probeBytes(append(mp3Frame(), make([]byte, 2000)...))
	wantCode(t, err, apperr.AudioCorrupt)

	_, _, err = probeBytes(append(id3v2(50), make([]byte, 2000)...))
	wantCode(t, err, apperr.AudioCorrupt)

	b = id3v2(50)
	b[6] = 0x80
	_, _, err = probeBytes(append(b, mp3Frames(84)...))
	wantCode(t, err, apperr.AudioCorrupt)

	b = mp3Frames(84)
	b[2] = 0xF4
	_, _, err = probeBytes(b)
	wantCode(t, err, apperr.AudioUnsupported)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProbeM4A(t *testing.T) {
	wantOK(t, fixture(t, "tone.m4a"), M4A, 3000, 3100)
	wantOK(t, fixture(t, "tone_moov_end.m4a"), M4A, 3000, 3100)

	for name, code := range map[string]apperr.Code{
		"tone_truncated.m4a": apperr.AudioCorrupt,
		"tone_bad_mdhd.m4a":  apperr.AudioCorrupt,
		"video.mp4":          apperr.AudioUnsupported,
	} {
		_, _, err := probeBytes(fixture(t, name))
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		wantCode(t, err, code)
	}

	b := fixture(t, "tone.m4a")
	i := bytes.Index(b, []byte("mdhd")) + 4 + 4 + 8
	binary.BigEndian.PutUint32(b[i:], binary.BigEndian.Uint32(b[i:])*100)
	_, _, err := probeBytes(b)
	wantCode(t, err, apperr.AudioCorrupt)

	b = fixture(t, "tone.m4a")
	i = bytes.Index(b, []byte("mp4a"))
	copy(b[i:], "alac")
	_, _, err = probeBytes(b)
	wantCode(t, err, apperr.AudioUnsupported)

	b = fixture(t, "tone.m4a")
	i = bytes.Index(b, []byte("stsz")) + 4 + 8
	binary.BigEndian.PutUint32(b[i:], binary.BigEndian.Uint32(b[i:])+1)
	_, _, err = probeBytes(b)
	wantCode(t, err, apperr.AudioCorrupt)

	b = fixture(t, "tone.m4a")
	_, _, err = probeBytes(append(b, 1, 2, 3))
	wantCode(t, err, apperr.AudioCorrupt)
}

func TestProbeRealMP3(t *testing.T) {
	wantOK(t, fixture(t, "tone.mp3"), MP3, 3000, 3100)
	b := fixture(t, "tone.mp3")
	_, _, err := probeBytes(b[:len(b)/2])
	wantCode(t, err, apperr.AudioCorrupt)
}

func TestProbeUnsupported(t *testing.T) {
	for _, b := range [][]byte{
		[]byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00"),
		[]byte("fLaC\x00\x00\x00\x22"),
		[]byte("hello"),
		{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10},
		make([]byte, 4096),
	} {
		_, _, err := probeBytes(b)
		wantCode(t, err, apperr.AudioUnsupported)
	}
	_, _, err := probeBytes(nil)
	wantCode(t, err, apperr.AudioEmpty)
}

func TestNormalizeAndExt(t *testing.T) {
	for in, want := range map[string]string{
		"audio/mp4": M4A, "audio/m4a": M4A, "AUDIO/X-M4A": M4A,
		"audio/mpeg": MP3, "audio/wav": WAV, "audio/x-wav": WAV, "audio/wave; codecs=1": WAV,
	} {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "audio/ogg", "video/mp4", "audio/"} {
		if _, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) ok", in)
		}
	}
	if Ext(M4A) != "m4a" || Ext(MP3) != "mp3" || Ext(WAV) != "wav" || Ext("x") != "" {
		t.Error("Ext")
	}
}
