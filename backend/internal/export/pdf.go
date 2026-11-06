package export

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"

	"phoebe/internal/documents"
)

const (
	pdfMargin  = 20.0
	pdfBody    = 11.0
	pdfLine    = 5.5
	pdfIndent  = 7.0
	pdfGap     = 1.5
	pdfNbAlias = "{nb}"
)

type pdfWriter struct {
	f        *fpdf.Fpdf
	limit    float64
	fallback map[string]bool
}

func renderPDF(d *doc, compress bool) ([]byte, error) {
	f := fpdf.New("P", "mm", "A4", "")
	f.SetMargins(pdfMargin, pdfMargin, pdfMargin)
	f.SetAutoPageBreak(true, pdfMargin)
	f.SetCompression(compress)
	f.SetCatalogSort(true)
	f.SetCreationDate(d.createdAt)
	f.SetModificationDate(d.createdAt)
	f.SetTitle(pdfText(d.title), true)
	f.AddUTF8FontFromBytes(pdfFont, "", fontRegular)
	f.AddUTF8FontFromBytes(pdfFont, "B", fontBold)
	f.AliasNbPages(pdfNbAlias)
	f.SetHeaderFuncMode(func() {
		if d.draft {
			f.SetFont(pdfFont, "B", 9)
			f.SetXY(pdfMargin, 10)
			f.CellFormat(0, 5, pdfText(d.l.draft), "", 0, "R", false, 0, "")
		}
	}, true)
	f.SetFooterFunc(func() {
		f.SetFont(pdfFont, "", 9)
		f.SetXY(pdfMargin, -14)
		f.CellFormat(0, 5, strconv.Itoa(f.PageNo())+"/"+pdfNbAlias, "", 0, "C", false, 0, "")
	})
	_, pageH := f.GetPageSize()
	w := &pdfWriter{f: f, limit: pageH - pdfMargin, fallback: map[string]bool{}}
	f.AddPage()

	w.block("B", 18, 8, d.title)
	w.block("", pdfBody, pdfLine, d.date)
	if d.participants != "" {
		w.block("", pdfBody, pdfLine, d.participants)
	}
	if d.draft {
		w.block("B", pdfBody, pdfLine, d.l.draft)
	}
	for _, s := range d.sections {
		if s.heading != "" {
			w.heading(14, 7, s.heading)
		}
		switch {
		case s.none:
			w.block("", pdfBody, pdfLine, d.l.none)
		case s.typ == documents.SectionActionItems:
			for i, a := range s.actions {
				src := a.refs
				if src == "" {
					src = "-"
				}
				w.listItem(strconv.Itoa(i+1)+".", a.desc)
				w.text("", pdfBody, pdfLine, pdfMargin+pdfIndent, d.l.pic+": "+a.pic+" · "+d.l.deadline+": "+a.deadline+" · "+d.l.source+": "+src)
				f.Ln(pdfGap)
			}
		case s.typ == documents.SectionDiscussion:
			for _, t := range s.topics {
				w.heading(12, 6, withRefs(t.title, t.refs))
				w.items(s.style, t.points)
			}
		default:
			w.items(s.style, s.items)
		}
	}
	if d.approximate {
		f.Ln(2)
		w.block("", pdfBody, pdfLine, d.l.approxLegend)
	}

	var buf bytes.Buffer
	if err := f.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (w *pdfWriter) block(style string, size, h float64, text string) {
	w.text(style, size, h, pdfMargin, text)
	w.f.Ln(pdfGap)
}

// fpdf Write wraps at the left margin, so mixed-font runs move the margin to x meanwhile.
func (w *pdfWriter) text(style string, size, h, x float64, text string) {
	text = pdfText(text)
	runs := fontRuns(text, style)
	if len(runs) <= 1 {
		fam := pdfFont
		if len(runs) == 1 {
			fam = runs[0].family
		}
		w.setFont(fam, style, size)
		w.f.SetX(x)
		w.f.MultiCell(0, h, text, "", "L", false)
		return
	}
	left, _, _, _ := w.f.GetMargins()
	w.f.SetLeftMargin(x)
	w.f.SetX(x)
	for _, r := range runs {
		w.setFont(r.family, style, size)
		w.f.Write(h, r.text)
	}
	w.f.Ln(h)
	w.f.SetLeftMargin(left)
}

// Registered on first use so documents that never need the fallback don't embed it.
func (w *pdfWriter) setFont(family, style string, size float64) {
	if family == pdfFallback && !w.fallback[style] {
		ttf := fallbackRegular
		if style == "B" {
			ttf = fallbackBold
		}
		w.f.AddUTF8FontFromBytes(pdfFallback, style, ttf)
		w.fallback[style] = true
	}
	w.f.SetFont(family, style, size)
}

// heading moves to a new page when it would otherwise end a page without a body line after it.
func (w *pdfWriter) heading(size, h float64, text string) {
	w.f.Ln(2)
	if w.f.GetY()+h+pdfLine > w.limit {
		w.f.AddPage()
	}
	w.block("B", size, h, text)
}

func (w *pdfWriter) items(style string, items []item) {
	for i, it := range items {
		text := withRefs(it.text, it.refs)
		switch style {
		case documents.StyleParagraph:
			w.block("", pdfBody, pdfLine, text)
		case documents.StyleNumbered:
			w.listItem(strconv.Itoa(i+1)+".", text)
			w.f.Ln(pdfGap)
		default:
			w.listItem("•", text)
			w.f.Ln(pdfGap)
		}
	}
}

// listItem prints the marker as a cell of the same height as the first text line, so
// an automatic page break happens before the marker, never between it and its text.
func (w *pdfWriter) listItem(marker, text string) {
	w.f.SetFont(pdfFont, "", pdfBody)
	w.f.SetX(pdfMargin)
	w.f.CellFormat(pdfIndent, pdfLine, marker, "", 0, "L", false, 0, "")
	w.text("", pdfBody, pdfLine, pdfMargin+pdfIndent, text)
}

// pdfText replaces runes outside the BMP, which fpdf's UTF-8 width table cannot hold.
func pdfText(s string) string {
	return strings.Map(func(r rune) rune {
		if r > 0xFFFF {
			return '?'
		}
		return r
	}, s)
}
