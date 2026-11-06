package export

import (
	"strconv"
	"strings"

	"phoebe/internal/documents"
)

// renderWhatsApp: MoM keeps only summary, decisions and action items; notes keep all
// sections. No refs.
func renderWhatsApp(d *doc) []byte {
	head := []string{"*" + d.title + "*", d.date}
	if d.participants != "" {
		head = append(head, d.participants)
	}
	if d.draft {
		head = append(head, "_"+d.l.draft+"_")
	}
	blocks := []string{strings.Join(head, "\n")}
	for _, s := range d.sections {
		style := s.style
		if d.kind == documents.KindMoM {
			switch s.typ {
			case documents.SectionSummary:
			case documents.SectionDecisions, documents.SectionActionItems:
				style = documents.StyleNumbered
			default:
				continue
			}
		}
		var body []string
		if s.heading != "" {
			body = append(body, "*"+s.heading+"*")
		}
		switch {
		case s.none:
			body = append(body, d.l.none)
		case s.typ == documents.SectionActionItems:
			for i, a := range s.actions {
				m := strconv.Itoa(i+1) + ". "
				body = append(body, m+waIndent(a.desc, len(m)), "   PIC: "+a.pic+" · Deadline: "+a.deadline)
			}
		default:
			body = append(body, waItems(style, s.items))
		}
		blocks = append(blocks, strings.Join(body, "\n"))
	}
	return []byte(strings.Join(blocks, "\n\n") + "\n")
}

func waItems(style string, items []item) string {
	out := make([]string, len(items))
	for i, it := range items {
		switch style {
		case documents.StyleParagraph:
			out[i] = it.text
		case documents.StyleNumbered:
			m := strconv.Itoa(i+1) + ". "
			out[i] = m + waIndent(it.text, len(m))
		default:
			out[i] = "- " + waIndent(it.text, 2)
		}
	}
	sep := "\n"
	if style == documents.StyleParagraph {
		sep = "\n\n"
	}
	return strings.Join(out, sep)
}

func waIndent(s string, n int) string {
	return strings.ReplaceAll(s, "\n", "\n"+strings.Repeat(" ", n))
}
