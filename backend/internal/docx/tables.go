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
	rePgSz        = regexp.MustCompile(`<w:pgSz\s[^>]*/>`)
	rePgMar       = regexp.MustCompile(`<w:pgMar\s[^>]*/>`)
	reWidthAttr   = regexp.MustCompile(`w:w="(-?\d+)"`)
	reLeftAttr    = regexp.MustCompile(`w:left="(-?\d+)"`)
	reRightAttr   = regexp.MustCompile(`w:right="(-?\d+)"`)
	reTypeAttr    = regexp.MustCompile(`w:type="([^"]*)"`)
	reGridCol     = regexp.MustCompile(`<w:gridCol\s[^>]*/>`)
	reGridSpan    = regexp.MustCompile(`<w:gridSpan\s[^>]*/>`)
	reValAttr     = regexp.MustCompile(`w:val="(-?\d+)"`)
	reIndent      = regexp.MustCompile(`<w:ind\s[^>]*/>`)
	reHangingAttr = regexp.MustCompile(`w:hanging="(-?\d+)"`)
	reTableWidth  = regexp.MustCompile(`<w:(?:tcW|tblW)\s[^>]*/>`)
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

// minColumnWidth is the narrowest a column may be squeezed to: roughly six
// characters of body text plus the cell margins. Below that a heading like
// "Data Centre" is set one letter per line and the column is unreadable,
// which is worse than a table that is merely tight.
const minColumnWidth = 700

// maxIndentShare bounds how much of a narrow cell a list or quotation indent
// may eat. LibreOffice indents a bullet by 709 twips whatever the column is
// worth, which in a squeezed cell leaves a couple of characters per line and
// sets the text one letter at a time.
const maxIndentShare = 4

// fitTables brings every table in body inside available twips. Tables that
// already fit are untouched, and so are widths expressed as a percentage:
// those follow the page on their own.
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

// fitTable lays one table out again inside available twips.
//
// Scaling every column by the same factor is not enough. LibreOffice sizes
// columns from their content and readily overflows its own page — a nine
// column table came out half as wide again as the text area — so its narrowest
// columns are already close to unreadable, and shrinking them by the same
// factor as the roomy ones finishes them off. The widths are therefore
// redistributed: no column goes below a floor, and what that costs is taken
// from the columns that have room to spare.
func fitTable(table string, available int) string {
	columns := gridWidths(table)
	if len(columns) == 0 {
		return fitWithoutAGrid(table, available)
	}
	if sum(columns) <= available {
		return table
	}
	fitted := redistribute(columns, available)

	var b strings.Builder
	b.WriteString(setTableWidth(headerOf(table), fitted))
	b.WriteString(fitCells(table[len(headerOf(table)):], fitted))
	return b.String()
}

// redistribute returns column widths summing to at most available, keeping the
// relative sizes of the columns that can afford it and holding the rest at the
// floor.
func redistribute(columns []int, available int) []int {
	floor := min(minColumnWidth, available/len(columns))
	total := sum(columns)

	fitted := make([]int, len(columns))
	for i, w := range columns {
		fitted[i] = max(w*available/total, floor)
	}
	// Raising the narrow columns to the floor costs width the page does not
	// have. Take it back from the columns that are above the floor, in
	// proportion to how far above it they are.
	over := sum(fitted) - available
	for over > 0 {
		spare := 0
		for _, w := range fitted {
			spare += w - floor
		}
		if spare <= 0 {
			break // every column is at the floor; nothing left to give
		}
		taken := 0
		for i, w := range fitted {
			give := min((w-floor)*over/spare, w-floor)
			fitted[i] -= give
			taken += give
		}
		if taken == 0 {
			// Rounding left a remainder of a few twips: take it from the
			// widest column, which is the one that will not miss it.
			widest := 0
			for i, w := range fitted {
				if w > fitted[widest] {
					widest = i
				}
			}
			if fitted[widest] <= floor {
				break
			}
			fitted[widest]--
			taken = 1
		}
		over -= taken
	}
	return fitted
}

// headerOf is everything up to a table's first row: its properties and grid.
func headerOf(table string) string {
	if row := strings.Index(table, "<w:tr"); row >= 0 {
		return table[:row]
	}
	return table
}

// setTableWidth writes the fitted widths into a table's properties and grid.
func setTableWidth(header string, fitted []int) string {
	header = reTableWidth.ReplaceAllStringFunc(header, func(el string) string {
		if typeAttr(el) != "dxa" {
			return el
		}
		return setWidth(el, sum(fitted))
	})
	i := 0
	return reGridCol.ReplaceAllStringFunc(header, func(el string) string {
		if i >= len(fitted) {
			return el
		}
		el = setWidth(el, fitted[i])
		i++
		return el
	})
}

// fitCells walks the rows, giving each cell the width of the columns it spans
// and refitting any table nested inside it to the cell it now sits in.
func fitCells(rows string, fitted []int) string {
	var b strings.Builder
	last := 0
	for _, row := range spansOf(rows, "w:tr") {
		b.WriteString(rows[last:row[0]])
		b.WriteString(fitRow(rows[row[0]:row[1]], fitted))
		last = row[1]
	}
	b.WriteString(rows[last:])
	return b.String()
}

func fitRow(row string, fitted []int) string {
	var b strings.Builder
	last, column := 0, 0
	for _, span := range spansOf(row, "w:tc") {
		cell := row[span[0]:span[1]]
		width := 0
		for i := column; i < column+cellSpan(cell) && i < len(fitted); i++ {
			width += fitted[i]
		}
		column += cellSpan(cell)
		b.WriteString(row[last:span[0]])
		b.WriteString(fitCell(cell, width))
		last = span[1]
	}
	b.WriteString(row[last:])
	return b.String()
}

func fitCell(cell string, width int) string {
	if width <= 0 {
		return cell
	}
	open := strings.Index(cell, ">")
	if open < 0 {
		return cell
	}
	head, content := cell[:open+1], cell[open+1:]
	// The properties are this cell's only when they open it. Looking for a
	// <w:tcPr> anywhere would find the first cell of a table nested inside,
	// and rewrite that table's widths as if they were this cell's.
	if isTag(content, 0, "<w:tcPr") {
		if end := strings.Index(content, "</w:tcPr>"); end >= 0 {
			end += len("</w:tcPr>")
			properties := reTableWidth.ReplaceAllStringFunc(content[:end], func(el string) string {
				if typeAttr(el) != "dxa" {
					return el
				}
				return setWidth(el, width)
			})
			// A table inside the cell has to fit the cell, not the page.
			return head + properties + fitTables(capIndents(content[end:], width), width)
		}
	}
	return head + fitTables(capIndents(content, width), width)
}

// capIndents brings the paragraph indents of a cell back within the width the
// cell actually has. Paragraphs inside a table nested in this cell are left
// alone: they are capped against that table's own cells.
func capIndents(content string, width int) string {
	cap := width / maxIndentShare
	if cap <= 0 {
		return content
	}
	var b strings.Builder
	last := 0
	for _, span := range spansOf(content, "w:p") {
		paragraph := content[span[0]:span[1]]
		capped := reIndent.ReplaceAllStringFunc(paragraph, func(el string) string {
			left := intAttr(el, reLeftAttr)
			if left <= cap {
				return el
			}
			// Keep the hanging indent in proportion, or the bullet ends up
			// further right than the text it belongs to.
			el = reHangingAttr.ReplaceAllStringFunc(el, func(h string) string {
				return `w:hanging="` + strconv.Itoa(intAttr(h, reHangingAttr)*cap/left) + `"`
			})
			return reLeftAttr.ReplaceAllString(el, `w:left="`+strconv.Itoa(cap)+`"`)
		})
		b.WriteString(content[last:span[0]])
		b.WriteString(capped)
		last = span[1]
	}
	if last == 0 {
		return content
	}
	b.WriteString(content[last:])
	return b.String()
}

// cellSpan is how many columns of the grid a cell covers.
func cellSpan(cell string) int {
	if el := reGridSpan.FindString(cell); el != "" {
		if n := intAttr(el, reValAttr); n > 0 {
			return n
		}
	}
	return 1
}

// gridWidths returns a table's own column widths. Only what precedes the first
// row is read: everything after it may belong to a table nested in a cell.
func gridWidths(table string) []int {
	grid := firstElement(headerOf(table), "w:tblGrid")
	if grid == "" {
		return nil
	}
	var widths []int
	for _, col := range reGridCol.FindAllString(grid, -1) {
		widths = append(widths, intAttr(col, reWidthAttr))
	}
	return widths
}

// fitWithoutAGrid handles the rare table that declares a width but no column
// grid: there is nothing to redistribute, only a total to bring down.
func fitWithoutAGrid(table string, available int) string {
	el := reTableWidth.FindString(firstElement(headerOf(table), "w:tblPr"))
	if el == "" || typeAttr(el) != "dxa" || intAttr(el, reWidthAttr) <= available {
		return table
	}
	total := intAttr(el, reWidthAttr)
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

func sum(values []int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}

// spansOf returns the ranges of the <name> elements that belong to this table,
// stepping over any table nested in one of its cells: that table's rows and
// cells are its own business.
func spansOf(xml, name string) [][2]int {
	openTag, closeTag := "<"+name, "</"+name+">"
	var spans [][2]int
	depth, nested, start := 0, 0, 0
	for i := 0; i < len(xml); {
		switch {
		case isTag(xml, i, "<w:tbl"):
			nested++
		case strings.HasPrefix(xml[i:], "</w:tbl>"):
			if nested > 0 {
				nested--
			}
			i += len("</w:tbl>")
			continue
		case nested == 0 && isTag(xml, i, openTag):
			if depth == 0 {
				start = i
			}
			depth++
		case nested == 0 && strings.HasPrefix(xml[i:], closeTag):
			if depth > 0 {
				depth--
				if depth == 0 {
					spans = append(spans, [2]int{start, i + len(closeTag)})
				}
			}
			i += len(closeTag)
			continue
		}
		i++
	}
	return spans
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
