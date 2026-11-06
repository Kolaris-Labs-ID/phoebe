package export

import (
	_ "embed"
	"encoding/binary"
)

//go:embed fonts/NotoSans-Regular.ttf
var fontRegular []byte

//go:embed fonts/NotoSans-Bold.ttf
var fontBold []byte

//go:embed fonts/DejaVuSans.ttf
var fallbackRegular []byte

//go:embed fonts/DejaVuSans-Bold.ttf
var fallbackBold []byte

const (
	pdfFont      = "NotoSans"
	pdfFallback  = "DejaVuSans"
	docxFont     = "Noto Sans"
	docxFallback = "DejaVu Sans"
)

// BMP only: runes above U+FFFF are never covered (pdfText replaces them).
type coverage [1 << 16 / 64]uint64

func (c *coverage) has(r rune) bool {
	return r >= 0 && r <= 0xFFFF && c[r/64]&(1<<(r%64)) != 0
}

var (
	primaryCoverage  = map[string]*coverage{"": cmapCoverage(fontRegular), "B": cmapCoverage(fontBold)}
	fallbackCoverage = map[string]*coverage{"": cmapCoverage(fallbackRegular), "B": cmapCoverage(fallbackBold)}
)

// (3,1) is the Windows Unicode BMP cmap; only its format 4 subtable is read.
func cmapCoverage(ttf []byte) *coverage {
	u16 := func(off int) int { return int(binary.BigEndian.Uint16(ttf[off:])) }
	u32 := func(off int) int { return int(binary.BigEndian.Uint32(ttf[off:])) }
	cmap := -1
	for i, n := 0, u16(4); i < n; i++ {
		rec := 12 + 16*i
		if string(ttf[rec:rec+4]) == "cmap" {
			cmap = u32(rec + 8)
		}
	}
	sub := -1
	for i, n := 0, u16(cmap+2); i < n; i++ {
		rec := cmap + 4 + 8*i
		if u16(rec) == 3 && u16(rec+2) == 1 {
			sub = cmap + u32(rec+4)
		}
	}
	if cmap < 0 || sub < 0 || u16(sub) != 4 {
		panic("export: embedded font has no format 4 Unicode cmap")
	}
	segs := u16(sub+6) / 2
	ends, starts, deltas, offsets := sub+14, sub+16+2*segs, sub+16+4*segs, sub+16+6*segs
	c := &coverage{}
	for i := 0; i < segs; i++ {
		start, end, delta, ro := u16(starts+2*i), u16(ends+2*i), u16(deltas+2*i), u16(offsets+2*i)
		for r := start; r <= end && r != 0xFFFF; r++ {
			g := r
			if ro != 0 {
				if g = u16(offsets + 2*i + ro + 2*(r-start)); g == 0 {
					continue
				}
			}
			if (g+delta)&0xFFFF != 0 {
				c[r/64] |= 1 << (r % 64)
			}
		}
	}
	return c
}

type fontRun struct{ family, text string }

func fontRuns(s, style string) []fontRun {
	var runs []fontRun
	start := 0
	for i, r := range s {
		fam := pdfFont
		switch {
		case len(runs) > 0 && (r == ' ' || !primaryCoverage[style].has(r) && !fallbackCoverage[style].has(r)):
			fam = runs[len(runs)-1].family
		case !primaryCoverage[style].has(r) && fallbackCoverage[style].has(r):
			fam = pdfFallback
		}
		if len(runs) == 0 || runs[len(runs)-1].family != fam {
			if len(runs) > 0 {
				runs[len(runs)-1].text = s[start:i]
			}
			runs = append(runs, fontRun{family: fam})
			start = i
		}
	}
	if len(runs) > 0 {
		runs[len(runs)-1].text = s[start:]
	}
	return runs
}
