package docx

import (
	"regexp"
	"strconv"
	"strings"
)

// Tables reach this package sized for LibreOffice's own page, which has wider
// text than most company templates: LibreOffice fits a table to the text area
// it knows about and writes the result as absolute widths with
// <w:tblLayout w:type="fixed"/>. Swapping in the template's section properties
// then narrows the page underneath the table, and Word clips whatever no longer
// fits. Scaling the widths as the page changes keeps every table inside the
// printable area.

var (
	rePgSz       = regexp.MustCompile(`<w:pgSz\s[^>]*/>`)
	rePgMar      = regexp.MustCompile(`<w:pgMar\s[^>]*/>`)
	reWidthAttr  = regexp.MustCompile(`w:w="(-?\d+)"`)
	reLeftAttr   = regexp.MustCompile(`w:left="(-?\d+)"`)
	reRightAttr  = regexp.MustCompile(`w:right="(-?\d+)"`)
	reTypeAttr   = regexp.MustCompile(`w:type="([^"]*)"`)
	reGridCol    = regexp.MustCompile(`<w:gridCol\s[^>]*/>`)
	reTableWidth = regexp.MustCompile(`<w:(?:tcW|tblW)\s[^>]*/>`)
)

// textWidthOf returns the printable width of a section, in twips, or 0 when the
// section does not describe its page (in which case nothing can be scaled).
func textWidthOf(sectPr string) int {
	pgSz, pgMar := rePgSz.FindString(sectPr), rePgMar.FindString(sectPr)
	if pgSz == "" || pgMar == "" {
		// Without both, the printable width is a guess; leave tables alone.
		return 0
	}
	available := intAttr(pgSz, reWidthAttr) - intAttr(pgMar, reLeftAttr) - intAttr(pgMar, reRightAttr)
	if available <= 0 {
		return 0
	}
	return available
}

// fitTables scales down every table in body that is wider than available twips,
// keeping the relative column widths. Tables that already fit are untouched,
// and so are widths expressed as a percentage: those follow the page on their
// own.
func fitTables(body string, available int) string {
	if available <= 0 {
		return body
	}
	var b strings.Builder
	last := 0
	for _, span := range tableSpans(body) {
		table := body[span[0]:span[1]]
		fitted := fitTable(table, available)
		if fitted == table {
			continue
		}
		b.WriteString(body[last:span[0]])
		b.WriteString(fitted)
		last = span[1]
	}
	if last == 0 {
		return body
	}
	b.WriteString(body[last:])
	return b.String()
}

// fitTable scales one table, nested tables included: a table inside a cell has
// to shrink with the cell that holds it.
func fitTable(table string, available int) string {
	total := tableWidth(table)
	if total <= available || total <= 0 {
		return table
	}
	// Truncating integer division keeps the sum of the scaled columns at or
	// below the printable width, which is the property that matters. The loss
	// is at most one twip per column — 1/1440 inch.
	scale := func(v int) int { return max(v*available/total, 1) }
	table = reGridCol.ReplaceAllStringFunc(table, func(el string) string {
		return setWidth(el, scale(intAttr(el, reWidthAttr)))
	})
	return reTableWidth.ReplaceAllStringFunc(table, func(el string) string {
		if typeAttr(el) != "dxa" {
			return el
		}
		return setWidth(el, scale(intAttr(el, reWidthAttr)))
	})
}

// tableWidth is the width the table occupies: the sum of its own column grid,
// which is what a fixed-layout table is actually laid out from. The declared
// table width is only a fallback, for the rare table without a grid.
func tableWidth(table string) int {
	// Only what precedes the first row: everything after it may belong to a
	// table nested in a cell, whose grid is not this table's.
	header := table
	if row := strings.Index(header, "<w:tr"); row >= 0 {
		header = header[:row]
	}
	grid := firstElement(header, "w:tblGrid")
	if grid == "" {
		if w := firstElement(header, "w:tblPr"); w != "" {
			if el := reTableWidth.FindString(w); el != "" && typeAttr(el) == "dxa" {
				return intAttr(el, reWidthAttr)
			}
		}
		return 0
	}
	total := 0
	for _, col := range reGridCol.FindAllString(grid, -1) {
		total += intAttr(col, reWidthAttr)
	}
	return total
}

// tableSpans returns the byte ranges of the outermost <w:tbl> elements, so that
// a table nested in a cell is scaled as part of the table that contains it
// rather than measured against the page on its own.
func tableSpans(body string) [][2]int {
	var spans [][2]int
	depth, start := 0, 0
	for i := 0; i < len(body); {
		switch {
		case isTag(body, i, "<w:tbl"):
			if depth == 0 {
				start = i
			}
			depth++
		case strings.HasPrefix(body[i:], "</w:tbl>"):
			if depth > 0 {
				depth--
				if depth == 0 {
					spans = append(spans, [2]int{start, i + len("</w:tbl>")})
				}
			}
			i += len("</w:tbl>")
			continue
		}
		i++
	}
	return spans
}

// isTag reports whether body has element <name> at i, and not merely a longer
// element name starting with the same letters (<w:tbl> versus <w:tblPr>).
func isTag(body string, i int, name string) bool {
	if !strings.HasPrefix(body[i:], name) {
		return false
	}
	rest := body[i+len(name):]
	return strings.HasPrefix(rest, ">") || strings.HasPrefix(rest, " ") ||
		strings.HasPrefix(rest, "\t") || strings.HasPrefix(rest, "\n") || strings.HasPrefix(rest, "\r")
}

// firstElement returns the first <name>…</name> in xml, or "" when absent.
func firstElement(xml, name string) string {
	open, closeTag := "<"+name, "</"+name+">"
	for i := 0; ; {
		start := strings.Index(xml[i:], open)
		if start < 0 {
			return ""
		}
		start += i
		// Skip a longer element that merely starts the same way, such as
		// <w:tblGridChange> for <w:tblGrid>.
		if !isTag(xml, start, open) {
			i = start + len(open)
			continue
		}
		end := strings.Index(xml[start:], closeTag)
		if end < 0 {
			return ""
		}
		return xml[start : start+end+len(closeTag)]
	}
}

func intAttr(element string, re *regexp.Regexp) int {
	m := re.FindStringSubmatch(element)
	if m == nil {
		return 0
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return v
}

func typeAttr(element string) string {
	if m := reTypeAttr.FindStringSubmatch(element); m != nil {
		return m[1]
	}
	return ""
}

func setWidth(element string, width int) string {
	return reWidthAttr.ReplaceAllString(element, `w:w="`+strconv.Itoa(width)+`"`)
}
