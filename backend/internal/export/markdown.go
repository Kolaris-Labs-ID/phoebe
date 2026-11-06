package export

import (
	"strconv"
	"strings"

	"phoebe/internal/documents"
)

func renderMarkdown(d *doc) []byte {
	var blocks []string
	blocks = append(blocks, "# "+mdEscape(d.title), mdEscape(d.date))
	if d.participants != "" {
		blocks = append(blocks, mdEscape(d.participants))
	}
	if d.draft {
		blocks = append(blocks, "**"+d.l.draft+"**")
	}
	for _, s := range d.sections {
		if s.heading != "" {
			blocks = append(blocks, "## "+mdEscape(s.heading))
		}
		switch {
		case s.none:
			blocks = append(blocks, d.l.none)
		case s.typ == documents.SectionActionItems:
			blocks = append(blocks, mdActions(d, s.actions))
		case s.typ == documents.SectionDiscussion:
			for _, t := range s.topics {
				blocks = append(blocks, "### "+withRefs(mdEscape(t.title), mdRefs(t.refs)))
				blocks = append(blocks, mdItems(s.style, t.points)...)
			}
		default:
			blocks = append(blocks, mdItems(s.style, s.items)...)
		}
	}
	if d.approximate {
		blocks = append(blocks, mdRefs(d.l.approxLegend))
	}
	return []byte(strings.Join(blocks, "\n\n") + "\n")
}

func mdItems(style string, items []item) []string {
	if len(items) == 0 {
		return nil
	}
	if style == documents.StyleParagraph {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = withRefs(mdEscape(it.text), mdRefs(it.refs))
		}
		return out
	}
	lines := make([]string, len(items))
	for i, it := range items {
		marker := "-"
		if style == documents.StyleNumbered {
			marker = strconv.Itoa(i+1) + "."
		}
		indent := strings.Repeat(" ", len(marker)+1)
		text := strings.ReplaceAll(withRefs(mdEscape(it.text), mdRefs(it.refs)), "\n", "\n"+indent)
		lines[i] = marker + " " + text
	}
	return []string{strings.Join(lines, "\n")}
}

func mdActions(d *doc, actions []action) string {
	l := d.l
	rows := []string{
		"| " + strings.Join([]string{l.no, l.task, l.pic, l.deadline, l.source}, " | ") + " |",
		"| --- | --- | --- | --- | --- |",
	}
	for i, a := range actions {
		src := mdRefs(a.refs)
		if src == "" {
			src = "-"
		}
		cells := []string{strconv.Itoa(i + 1), mdCell(a.desc), mdCell(a.pic), mdCell(a.deadline), src}
		rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
	}
	return strings.Join(rows, "\n")
}

// mdRefs escapes "~" so approximate refs never pair into GFM strikethrough.
func mdRefs(s string) string { return strings.ReplaceAll(s, "~", `\~`) }

func mdCell(s string) string {
	return mdEscape(strings.Join(strings.Fields(s), " "))
}

var mdSpecial = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `_`, `\_`, "`", "\\`", `[`, `\[`, `]`, `\]`, `<`, `\<`, `>`, `\>`, `|`, `\|`)

// mdEscape escapes inline markup and block markers at line starts (api.md §5).
func mdEscape(s string) string {
	lines := strings.Split(mdSpecial.Replace(s), "\n")
	for i, ln := range lines {
		body := strings.TrimLeft(ln, " ")
		lead := ln[:len(ln)-len(body)]
		switch {
		case body == "":
		case body[0] == '#' || body[0] == '+' || body[0] == '-':
			lines[i] = lead + `\` + body
		default:
			n := 0
			for n < len(body) && body[n] >= '0' && body[n] <= '9' {
				n++
			}
			if n > 0 && n < len(body) && body[n] == '.' {
				lines[i] = lead + body[:n] + `\` + body[n:]
			}
		}
	}
	return strings.Join(lines, "\n")
}
