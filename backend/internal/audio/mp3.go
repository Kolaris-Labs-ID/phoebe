package audio

import (
	"bufio"
	"encoding/binary"
	"io"
)

type mpegFrame struct {
	mpeg1      bool
	version    byte
	sampleRate int64
	samples    int64
	length     int
	mono       bool
}

var (
	l3BitratesV1 = [16]int64{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	l3BitratesV2 = [16]int64{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	sampleRates  = map[byte][3]int64{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}
)

// parseFrame accepts MPEG-1/2/2.5 Layer III headers only; free-format and reserved values are invalid.
func parseFrame(b []byte) (mpegFrame, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return mpegFrame{}, false
	}
	version := b[1] >> 3 & 3
	layer := b[1] >> 1 & 3
	bri := b[2] >> 4
	sri := b[2] >> 2 & 3
	if version == 1 || layer != 1 || bri == 0 || bri == 15 || sri == 3 || b[3]&3 == 2 {
		return mpegFrame{}, false
	}
	f := mpegFrame{mpeg1: version == 3, version: version, sampleRate: sampleRates[version][sri], mono: b[3]>>6 == 3}
	pad := int64(b[2] >> 1 & 1)
	if f.mpeg1 {
		f.samples = 1152
		f.length = int(144000*l3BitratesV1[bri]/f.sampleRate + pad)
	} else {
		f.samples = 576
		f.length = int(72000*l3BitratesV2[bri]/f.sampleRate + pad)
	}
	return f, true
}

func (f mpegFrame) same(g mpegFrame) bool {
	return f.version == g.version && f.sampleRate == g.sampleRate
}

func looksMP3(head []byte) bool {
	if len(head) >= 3 && string(head[:3]) == "ID3" {
		return true
	}
	_, ok := parseFrame(head)
	return ok
}

// infoFrames reports whether the first frame is a Xing/Info/VBRI header (no audio) and its declared frame count (0 = none).
func infoFrames(f mpegFrame, frame []byte) (isInfo bool, frames int64) {
	side := 32
	switch {
	case f.mpeg1 && f.mono, !f.mpeg1 && !f.mono:
		side = 17
	case !f.mpeg1 && f.mono:
		side = 9
	}
	if x := 4 + side; len(frame) >= x+12 && (string(frame[x:x+4]) == "Xing" || string(frame[x:x+4]) == "Info") {
		if binary.BigEndian.Uint32(frame[x+4:x+8])&1 != 0 {
			frames = int64(binary.BigEndian.Uint32(frame[x+8 : x+12]))
		}
		return true, frames
	}
	if len(frame) >= 36+18 && string(frame[36:40]) == "VBRI" {
		return true, int64(binary.BigEndian.Uint32(frame[50:54]))
	}
	return false, 0
}

func probeMP3(rs io.ReadSeeker, size int64) (durationMS, payload int64, err error) {
	ra := readerAt{rs}
	pos := int64(0)
	h := make([]byte, 10)
	for size-pos >= 10 {
		if err := readFull(ra, h, pos); err != nil {
			return 0, 0, err
		}
		if string(h[:3]) != "ID3" {
			break
		}
		var tag int64
		for _, c := range h[6:10] {
			if c >= 0x80 {
				return 0, 0, corrupt()
			}
			tag = tag<<7 | int64(c)
		}
		pos += 10 + tag
		if h[5]&0x10 != 0 {
			pos += 10
		}
		if pos > size {
			return 0, 0, corrupt()
		}
	}
	if _, err := rs.Seek(pos, io.SeekStart); err != nil {
		return 0, 0, err
	}
	br := bufio.NewReaderSize(io.LimitReader(rs, size-pos), 64<<10)

	peek := func(n int) ([]byte, error) {
		b, err := br.Peek(n)
		if err != nil && !isEOF(err) {
			return nil, err
		}
		return b, nil
	}
	b, err := peek(4)
	if err != nil {
		return 0, 0, err
	}
	first, ok := parseFrame(b)
	if !ok {
		return 0, 0, corrupt()
	}
	if b, err = peek(first.length + 4); err != nil {
		return 0, 0, err
	}
	if len(b) < first.length+4 {
		return 0, 0, corrupt()
	}
	if second, ok := parseFrame(b[first.length:]); !ok || !second.same(first) {
		return 0, 0, corrupt()
	}
	isInfo, declared := infoFrames(first, b[:first.length])

	var frames, samples int64
	for {
		b, err := peek(4)
		if err != nil {
			return 0, 0, err
		}
		if len(b) >= 3 && string(b[:3]) == "TAG" || len(b) < 4 {
			break
		}
		f, ok := parseFrame(b)
		if !ok || !f.same(first) {
			if _, err := br.Discard(1); err != nil {
				return 0, 0, err
			}
			continue
		}
		if n, err := br.Discard(f.length); n < f.length {
			if err != nil && !isEOF(err) {
				return 0, 0, err
			}
			break
		}
		payload += int64(f.length)
		if isInfo {
			isInfo = false
			continue
		}
		frames++
		samples += f.samples
	}
	if declared > 0 && absDiff(uint64(frames), uint64(declared)) > max(1, uint64(declared)/100) {
		return 0, 0, corrupt()
	}
	return samples * 1000 / first.sampleRate, payload, nil
}
