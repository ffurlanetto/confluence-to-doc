package docx

import (
	"regexp"
	"strconv"
	"strings"
)

// Some Confluence tables do not belong on a portrait page at all. An inventory
// of servers with nine columns needs about 28 cm of paper; squeezed into the
// 9 cm a portrait A4 with 2.5 cm margins leaves, every column ends up at the
// floor and the table is technically inside the margins and practically
// unreadable.
//
// Word's answer is a section of its own, turned sideways, and that is what
// happens here: the table is put in a landscape section derived from the page
// the reader already uses — same paper, same margins, same header — and the
// document returns to portrait straight after.

// comfortablyCramped is how many columns may be forced down to the floor
// before the table is better off sideways. One is tolerable; two means the
// table has outgrown the page rather than merely overflowed it.
const comfortablyCramped = 2

var (
	rePgSzElement = regexp.MustCompile(`<w:pgSz\s[^>]*/>`)
	reOrientAttr  = regexp.MustCompile(`\s*w:orient="[^"]*"`)
	reHeightAttr  = regexp.MustCompile(`w:h="(-?\d+)"`)
)

// layOutTables gives every table in body a page it fits on: the reader's own,
// or a landscape section of it when portrait cannot hold the table.
func layOutTables(body, sectPr string) string {
	portrait := textWidthOf(sectPr)
	if portrait <= 0 {
		return body
	}
	landscape := turnSideways(sectPr)
	wide := textWidthOf(landscape)

	var b strings.Builder
	last := 0
	for _, span := range tableSpans(body) {
		table := body[span[0]:span[1]]
		columns := gridWidths(table)
		if sum(columns) <= portrait {
			continue
		}
		b.WriteString(body[last:span[0]])
		if wide > portrait && outgrew(columns, portrait, wide) {
			b.WriteString(sectionBreak(sectPr))
			b.WriteString(fitTable(table, wide))
			b.WriteString(sectionBreak(landscape))
		} else {
			b.WriteString(fitTable(table, portrait))
		}
		last = span[1]
	}
	if last == 0 {
		return body
	}
	b.WriteString(body[last:])
	return b.String()
}

// outgrew reports whether the table is better off sideways: portrait would
// flatten several columns onto the floor, and turning the page rescues them.
func outgrew(columns []int, portrait, landscape int) bool {
	cramped := atTheFloor(columns, portrait)
	return cramped >= comfortablyCramped && atTheFloor(columns, landscape) < cramped
}

// atTheFloor counts the columns that plain scaling would push below the
// narrowest width still worth reading.
func atTheFloor(columns []int, available int) int {
	total := sum(columns)
	if total <= available || total == 0 {
		return 0
	}
	floor := min(minColumnWidth, available/len(columns))
	count := 0
	for _, w := range columns {
		if w*available/total < floor {
			count++
		}
	}
	return count
}

// turnSideways returns the same page, rotated: same paper and margins, same
// header and footer, so only the orientation changes.
func turnSideways(sectPr string) string {
	pgSz := rePgSzElement.FindString(sectPr)
	if pgSz == "" {
		return ""
	}
	width, height := intAttr(pgSz, reWidthAttr), intAttr(pgSz, reHeightAttr)
	if width <= 0 || height <= 0 || width >= height {
		return "" // already sideways, or a page this cannot reason about
	}
	rotated := reWidthAttr.ReplaceAllString(pgSz, `w:w="`+strconv.Itoa(height)+`"`)
	rotated = reHeightAttr.ReplaceAllString(rotated, `w:h="`+strconv.Itoa(width)+`"`)
	rotated = reOrientAttr.ReplaceAllString(rotated, "")
	rotated = strings.TrimSuffix(rotated, "/>") + ` w:orient="landscape"/>`
	return strings.Replace(sectPr, pgSz, rotated, 1)
}

// sectionBreak is the empty paragraph that closes a section. Its properties
// are the ones the section it ends was laid out with, which is how Word
// records a change of orientation mid-document.
//
// The paragraph is real and takes a line, so it is made as small as a
// paragraph can be.
func sectionBreak(sectPr string) string {
	if sectPr == "" {
		return ""
	}
	// The schema fixes the order inside <w:pPr>: spacing, then the run
	// properties of the paragraph mark, and the section properties last.
	return `<w:p><w:pPr>` +
		`<w:spacing w:before="0" w:after="0" w:line="20" w:lineRule="exact"/>` +
		`<w:rPr><w:sz w:val="2"/><w:szCs w:val="2"/></w:rPr>` +
		sectPr + `</w:pPr></w:p>`
}
