package audio

import (
	"bufio"
	"encoding/binary"
	"io"
)

type box struct {
	typ       string
	off, size int64
}

func readBoxes(r io.ReaderAt, start, end int64) ([]box, error) {
	var out []box
	h := make([]byte, 16)
	for pos := start; pos < end; {
		if end-pos < 8 {
			return nil, corrupt()
		}
		if err := readFull(r, h[:8], pos); err != nil {
			return nil, err
		}
		size, hdr := int64(binary.BigEndian.Uint32(h[:4])), int64(8)
		switch size {
		case 0:
			size = end - pos
		case 1:
			if end-pos < 16 {
				return nil, corrupt()
			}
			if err := readFull(r, h[8:16], pos+8); err != nil {
				return nil, err
			}
			size, hdr = int64(binary.BigEndian.Uint64(h[8:16])), 16
		}
		if size < hdr || size > end-pos {
			return nil, corrupt()
		}
		out = append(out, box{string(h[4:8]), pos + hdr, size - hdr})
		pos += size
	}
	return out, nil
}

func findBox(bs []box, typ string) (box, bool) {
	for _, b := range bs {
		if b.typ == typ {
			return b, true
		}
	}
	return box{}, false
}

func childBoxes(r io.ReaderAt, parent box) ([]box, error) {
	return readBoxes(r, parent.off, parent.off+parent.size)
}

func boxBytes(r io.ReaderAt, b box, n int64) ([]byte, error) {
	if b.size < n {
		return nil, corrupt()
	}
	p := make([]byte, n)
	return p, readFull(r, p, b.off)
}

// mediaTimes parses mvhd/mdhd (full boxes, version 0 or 1) into timescale and duration.
func mediaTimes(r io.ReaderAt, b box) (timescale, duration uint64, err error) {
	p, err := boxBytes(r, b, 1)
	if err != nil {
		return 0, 0, err
	}
	if p[0] == 1 {
		if p, err = boxBytes(r, b, 32); err != nil {
			return 0, 0, err
		}
		return uint64(binary.BigEndian.Uint32(p[20:24])), binary.BigEndian.Uint64(p[24:32]), nil
	}
	if p, err = boxBytes(r, b, 20); err != nil {
		return 0, 0, err
	}
	return uint64(binary.BigEndian.Uint32(p[12:16])), uint64(binary.BigEndian.Uint32(p[16:20])), nil
}

type soundTrack struct {
	timescale, mdhdDuration uint64
	ticks, samples, payload uint64
}

func probeM4A(r io.ReaderAt, size int64) (durationMS, payload int64, err error) {
	top, err := readBoxes(r, 0, size)
	if err != nil {
		return 0, 0, err
	}
	if top[0].typ != "ftyp" {
		return 0, 0, corrupt()
	}
	moov, ok := findBox(top, "moov")
	if !ok {
		return 0, 0, corrupt()
	}
	kids, err := childBoxes(r, moov)
	if err != nil {
		return 0, 0, err
	}
	var mvhdScale, mvhdDuration uint64
	if mvhd, ok := findBox(kids, "mvhd"); ok {
		if mvhdScale, mvhdDuration, err = mediaTimes(r, mvhd); err != nil {
			return 0, 0, err
		}
	}

	var sound *soundTrack
	for _, k := range kids {
		if k.typ != "trak" {
			continue
		}
		handler, t, err := parseTrak(r, k)
		if err != nil {
			return 0, 0, err
		}
		if handler == "vide" {
			return 0, 0, unsupported()
		}
		if t != nil && sound == nil {
			sound = t
		}
	}
	if sound == nil {
		return 0, 0, unsupported()
	}

	t := sound
	if t.timescale == 0 {
		return 0, 0, corrupt()
	}
	if t.ticks == 0 {
		return 0, int64(t.payload), nil
	}
	ref := t.mdhdDuration
	if ref == 0 && mvhdScale > 0 {
		f := float64(mvhdDuration) * float64(t.timescale) / float64(mvhdScale)
		if f > maxSum {
			return 0, 0, corrupt()
		}
		ref = uint64(f)
	}
	if ref == 0 || absDiff(t.ticks, ref) > ref/100 || t.payload > uint64(size) || t.ticks/t.timescale > 1<<40 {
		return 0, 0, corrupt()
	}
	durationMS = int64(t.ticks/t.timescale*1000 + t.ticks%t.timescale*1000/t.timescale)
	return durationMS, int64(t.payload), nil
}

// parseTrak returns the handler type, plus the parsed track for an AAC (mp4a) sound track.
func parseTrak(r io.ReaderAt, trak box) (string, *soundTrack, error) {
	kids, err := childBoxes(r, trak)
	if err != nil {
		return "", nil, err
	}
	mdia, ok := findBox(kids, "mdia")
	if !ok {
		return "", nil, corrupt()
	}
	mk, err := childBoxes(r, mdia)
	if err != nil {
		return "", nil, err
	}
	hdlr, ok := findBox(mk, "hdlr")
	if !ok {
		return "", nil, corrupt()
	}
	hp, err := boxBytes(r, hdlr, 12)
	if err != nil {
		return "", nil, err
	}
	handler := string(hp[8:12])
	if handler != "soun" {
		return handler, nil, nil
	}

	mdhd, ok := findBox(mk, "mdhd")
	if !ok {
		return "", nil, corrupt()
	}
	var t soundTrack
	if t.timescale, t.mdhdDuration, err = mediaTimes(r, mdhd); err != nil {
		return "", nil, err
	}
	minf, ok := findBox(mk, "minf")
	if !ok {
		return "", nil, corrupt()
	}
	nk, err := childBoxes(r, minf)
	if err != nil {
		return "", nil, err
	}
	stbl, ok := findBox(nk, "stbl")
	if !ok {
		return "", nil, corrupt()
	}
	sk, err := childBoxes(r, stbl)
	if err != nil {
		return "", nil, err
	}
	stsd, ok1 := findBox(sk, "stsd")
	stts, ok2 := findBox(sk, "stts")
	stsz, ok3 := findBox(sk, "stsz")
	if !ok1 || !ok2 || !ok3 {
		return "", nil, corrupt()
	}

	sd, err := boxBytes(r, stsd, 16)
	if err != nil {
		return "", nil, err
	}
	if binary.BigEndian.Uint32(sd[4:8]) == 0 {
		return "", nil, corrupt()
	}
	if string(sd[12:16]) != "mp4a" {
		return handler, nil, nil
	}

	if t.ticks, t.samples, err = sumStts(r, stts); err != nil {
		return "", nil, err
	}
	count, payload, err := sumStsz(r, stsz)
	if err != nil {
		return "", nil, err
	}
	if count != t.samples {
		return "", nil, corrupt()
	}
	t.payload = payload
	return handler, &t, nil
}

func tableReader(r io.ReaderAt, b box, headerLen, entries, entryLen int64) (*bufio.Reader, error) {
	if entries > (b.size-headerLen)/entryLen {
		return nil, corrupt()
	}
	return bufio.NewReaderSize(io.NewSectionReader(r, b.off+headerLen, entries*entryLen), 64<<10), nil
}

func readUint32s(br *bufio.Reader, p []byte) error {
	if _, err := io.ReadFull(br, p); err != nil {
		if isEOF(err) {
			return corrupt()
		}
		return err
	}
	return nil
}

const maxSum = 1 << 62

func sumStts(r io.ReaderAt, b box) (ticks, samples uint64, err error) {
	p, err := boxBytes(r, b, 8)
	if err != nil {
		return 0, 0, err
	}
	br, err := tableReader(r, b, 8, int64(binary.BigEndian.Uint32(p[4:8])), 8)
	if err != nil {
		return 0, 0, err
	}
	e := make([]byte, 8)
	for n := binary.BigEndian.Uint32(p[4:8]); n > 0; n-- {
		if err := readUint32s(br, e); err != nil {
			return 0, 0, err
		}
		c, d := uint64(binary.BigEndian.Uint32(e[0:4])), uint64(binary.BigEndian.Uint32(e[4:8]))
		samples += c
		ticks += c * d
		if ticks > maxSum {
			return 0, 0, corrupt()
		}
	}
	return ticks, samples, nil
}

func sumStsz(r io.ReaderAt, b box) (count, payload uint64, err error) {
	p, err := boxBytes(r, b, 12)
	if err != nil {
		return 0, 0, err
	}
	fixed := uint64(binary.BigEndian.Uint32(p[4:8]))
	count = uint64(binary.BigEndian.Uint32(p[8:12]))
	if fixed != 0 {
		if count != 0 && fixed > maxSum/count {
			return 0, 0, corrupt()
		}
		return count, fixed * count, nil
	}
	br, err := tableReader(r, b, 12, int64(count), 4)
	if err != nil {
		return 0, 0, err
	}
	e := make([]byte, 4)
	for i := uint64(0); i < count; i++ {
		if err := readUint32s(br, e); err != nil {
			return 0, 0, err
		}
		payload += uint64(binary.BigEndian.Uint32(e))
	}
	return count, payload, nil
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}
