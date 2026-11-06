package export

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"

	"phoebe/internal/documents"
)

const (
	nsW        = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	xmlDecl    = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"
	bulletNum  = 1
	textWidth  = 9638 // A4 width minus 2×20 mm margins, in twips
	pageMargin = 1134 // 20 mm
)

var docxColumns = []int{600, 3838, 1600, 1800, 1800}

type docxWriter struct {
	b       strings.Builder
	numbers int // numbered lists so far; each gets its own w:num so it restarts at 1
}

func renderDOCX(d *doc) ([]byte, error) {
	w := &docxWriter{}
	w.para("Title", 0, false, d.title)
	w.para("", 0, false, d.date)
	if d.participants != "" {
		w.para("", 0, false, d.participants)
	}
	if d.draft {
		w.para("", 0, true, d.l.draft)
	}
	for _, s := range d.sections {
		if s.heading != "" {
			w.para("Heading1", 0, false, s.heading)
		}
		switch {
		case s.none:
			w.para("", 0, false, d.l.none)
		case s.typ == documents.SectionActionItems:
			w.table(d, s.actions)
		case s.typ == documents.SectionDiscussion:
			for _, t := range s.topics {
				w.para("Heading2", 0, false, withRefs(t.title, t.refs))
				w.items(s.style, t.points)
			}
		default:
			w.items(s.style, s.items)
		}
	}

	document := xmlDecl + `<w:document ` + nsW + `><w:body>` + w.b.String() +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="` + itoa(pageMargin) + `" w:right="` + itoa(pageMargin) +
		`" w:bottom="` + itoa(pageMargin) + `" w:left="` + itoa(pageMargin) + `" w:header="567" w:footer="567" w:gutter="0"/></w:sectPr></w:body></w:document>`

	created := d.createdAt.Format("2006-01-02T15:04:05Z")
	core := xmlDecl + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:title>` + esc(d.title) + `</dc:title>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">` + created + `</dcterms:created>` +
		`<dcterms:modified xsi:type="dcterms:W3CDTF">` + created + `</dcterms:modified></cp:coreProperties>`

	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelsXML},
		{"docProps/core.xml", core},
		{"word/document.xml", document},
		{"word/styles.xml", stylesXML},
		{"word/numbering.xml", numberingXML(w.numbers)},
		{"word/_rels/document.xml.rels", documentRelsXML},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: d.createdAt})
		if err != nil {
			return nil, err
		}
		if _, err := f.Write([]byte(p.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (w *docxWriter) items(style string, items []item) {
	num := 0
	switch style {
	case documents.StyleBullet:
		num = bulletNum
	case documents.StyleNumbered:
		w.numbers++
		num = bulletNum + w.numbers
	}
	for _, it := range items {
		w.para("", num, false, withRefs(it.text, it.refs))
	}
}

func (w *docxWriter) para(style string, num int, bold bool, text string) {
	w.b.WriteString(`<w:p>`)
	if style != "" || num != 0 {
		w.b.WriteString(`<w:pPr>`)
		if style != "" {
			w.b.WriteString(`<w:pStyle w:val="` + style + `"/>`)
		}
		if num != 0 {
			w.b.WriteString(`<w:numPr><w:ilvl w:val="0"/><w:numId w:val="` + itoa(num) + `"/></w:numPr>`)
		}
		w.b.WriteString(`</w:pPr>`)
	}
	w.run(bold, text)
	w.b.WriteString(`</w:p>`)
}

func (w *docxWriter) run(bold bool, text string) {
	w.b.WriteString(`<w:r>`)
	if bold {
		w.b.WriteString(`<w:rPr><w:b/></w:rPr>`)
	}
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			w.b.WriteString(`<w:br/>`)
		}
		w.b.WriteString(`<w:t xml:space="preserve">` + esc(line) + `</w:t>`)
	}
	w.b.WriteString(`</w:r>`)
}

func (w *docxWriter) table(d *doc, actions []action) {
	w.b.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="` + itoa(textWidth) + `" w:type="dxa"/><w:tblBorders>`)
	for _, side := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		w.b.WriteString(`<w:` + side + ` w:val="single" w:sz="4" w:space="0" w:color="808080"/>`)
	}
	w.b.WriteString(`</w:tblBorders><w:tblLayout w:type="fixed"/></w:tblPr><w:tblGrid>`)
	for _, c := range docxColumns {
		w.b.WriteString(`<w:gridCol w:w="` + itoa(c) + `"/>`)
	}
	w.b.WriteString(`</w:tblGrid>`)
	l := d.l
	w.row(true, []string{l.no, l.task, l.pic, l.deadline, l.source})
	for i, a := range actions {
		src := a.refs
		if src == "" {
			src = "-"
		}
		w.row(false, []string{itoa(i + 1), a.desc, a.pic, a.deadline, src})
	}
	w.b.WriteString(`</w:tbl><w:p/>`)
}

func (w *docxWriter) row(header bool, cells []string) {
	w.b.WriteString(`<w:tr>`)
	if header {
		w.b.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
	}
	for i, c := range cells {
		w.b.WriteString(`<w:tc><w:tcPr><w:tcW w:w="` + itoa(docxColumns[i]) + `" w:type="dxa"/></w:tcPr><w:p>`)
		w.run(header, c)
		w.b.WriteString(`</w:p></w:tc>`)
	}
	w.b.WriteString(`</w:tr>`)
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func itoa(n int) string { return strconv.Itoa(n) }

func numberingXML(numbered int) string {
	lvl := func(format, text string) string {
		return `<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="` + format + `"/><w:lvlText w:val="` + text +
			`"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl>`
	}
	var b strings.Builder
	b.WriteString(xmlDecl + `<w:numbering ` + nsW + `>`)
	b.WriteString(`<w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="singleLevel"/>` + lvl("bullet", "•") + `</w:abstractNum>`)
	b.WriteString(`<w:abstractNum w:abstractNumId="1"><w:multiLevelType w:val="singleLevel"/>` + lvl("decimal", "%1.") + `</w:abstractNum>`)
	b.WriteString(`<w:num w:numId="` + itoa(bulletNum) + `"><w:abstractNumId w:val="0"/></w:num>`)
	for i := 1; i <= numbered; i++ {
		b.WriteString(`<w:num w:numId="` + itoa(bulletNum+i) + `"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="1"/></w:lvlOverride></w:num>`)
	}
	b.WriteString(`</w:numbering>`)
	return b.String()
}

const contentTypesXML = xmlDecl + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
	`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
	`<Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/>` +
	`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
	`</Types>`

const rootRelsXML = xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
	`</Relationships>`

const documentRelsXML = xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>` +
	`</Relationships>`

func headingStyle(id, name string, size, outline int) string {
	return `<w:style w:type="paragraph" w:styleId="` + id + `"><w:name w:val="` + name + `"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/>` +
		`<w:pPr><w:keepNext/><w:spacing w:before="240" w:after="120"/><w:outlineLvl w:val="` + itoa(outline) + `"/></w:pPr>` +
		`<w:rPr><w:b/><w:sz w:val="` + itoa(size) + `"/><w:szCs w:val="` + itoa(size) + `"/></w:rPr></w:style>`
}

var stylesXML = xmlDecl + `<w:styles ` + nsW + `>` +
	`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="Calibri" w:cs="Calibri"/>` +
	`<w:sz w:val="22"/><w:szCs w:val="22"/></w:rPr></w:rPrDefault>` +
	`<w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
	`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/>` +
	`<w:pPr><w:spacing w:after="160"/></w:pPr><w:rPr><w:b/><w:sz w:val="36"/><w:szCs w:val="36"/></w:rPr></w:style>` +
	headingStyle("Heading1", "heading 1", 28, 0) +
	headingStyle("Heading2", "heading 2", 24, 1) +
	headingStyle("Heading3", "heading 3", 22, 2) +
	`</w:styles>`
