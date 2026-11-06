package audio

import (
	"encoding/binary"
	"io"
)

func probeWAV(r io.ReaderAt, size int64) (int64, error) {
	var byteRate int64
	hdr := make([]byte, 16)
	for pos := int64(12); ; {
		if size-pos < 8 {
			return 0, corrupt()
		}
		if err := readFull(r, hdr[:8], pos); err != nil {
			return 0, err
		}
		id := string(hdr[:4])
		sz := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if sz < 16 || sz > size-body {
				return 0, corrupt()
			}
			if err := readFull(r, hdr, body); err != nil {
				return 0, err
			}
			switch binary.LittleEndian.Uint16(hdr[0:2]) {
			case 1, 3, 0xFFFE:
			default:
				return 0, unsupported()
			}
			channels := binary.LittleEndian.Uint16(hdr[2:4])
			sampleRate := int64(binary.LittleEndian.Uint32(hdr[4:8]))
			byteRate = int64(binary.LittleEndian.Uint32(hdr[8:12]))
			blockAlign := int64(binary.LittleEndian.Uint16(hdr[12:14]))
			if channels == 0 || byteRate == 0 || blockAlign == 0 || byteRate != sampleRate*blockAlign {
				return 0, corrupt()
			}
		case "data":
			if byteRate == 0 {
				return 0, corrupt()
			}
			if diff := size - body - sz; diff < -1 || diff > 1 {
				if diff < 0 {
					return 0, corrupt()
				}
				if err := trailingChunks(r, body+sz+sz&1, size); err != nil {
					return 0, err
				}
			}
			return sz * 1000 / byteRate, nil
		}
		next := body + sz + sz&1
		if next > size {
			return 0, corrupt()
		}
		pos = next
	}
}

func trailingChunks(r io.ReaderAt, pos, size int64) error {
	hdr := make([]byte, 8)
	for pos < size {
		if size-pos < 8 {
			return corrupt()
		}
		if err := readFull(r, hdr, pos); err != nil {
			return err
		}
		switch string(hdr[:4]) {
		case "LIST", "id3 ", "ID3 ":
		default:
			return corrupt()
		}
		sz := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		end := pos + 8 + sz
		if end > size {
			return corrupt()
		}
		if end+sz&1 >= size {
			return nil
		}
		pos = end + sz&1
	}
	return nil
}
