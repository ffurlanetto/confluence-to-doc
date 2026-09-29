package docx

import (
	"regexp"
	"strconv"
	"strings"
)

// The renderer writes the table of contents as ordinary paragraphs carrying a
// `toc-entry-N` class, which LibreOffice turns into a style named
// "Text Body.toc-entry-N". That is the handle used here to turn them into a
// real Word table of contents: the reader's own "TOC 1…9" styles, wrapped in a
// TOC field so Word recognises it, offers to update it, and fills in page
// numbers — which nothing upstream can know.
//
// The entries stay written out in full, so the contents are readable before any
// update and in the PDF, where a field is never refreshed.

const tocInstruction = ` TOC \o "1-9" \h \z \u `

var reTOCEntryName = regexp.MustCompile(`^text body\.toc-entry-([1-9])$`)

// tocEntryStyles returns the generated style ids that stand for a contents
// entry. Which of the reader's styles they end up with is remapStyles' job:
// `wordStyleNames` already points them at "toc 1…9".
func tocEntryStyles(generated map[string]style) map[string]int {
	level := map[string]int{}
	for id, s := range generated {
		if m := reTOCEntryName.FindStringSubmatch(normaliseName(s.name)); m != nil {
			n, _ := strconv.Atoi(m[1])
			level[id] = n
		}
	}
	return level
}

// insertTOC wraps the contents paragraphs in a TOC field. It must run before
// the style references are remapped, while the entries still carry the style
// ids the converter gave them. A document without contents — a single page has
// none — comes back unchanged.
func insertTOC(body string, entryStyles map[string]int) string {
	if len(entryStyles) == 0 {
		return body
	}
	first, last := -1, -1
	var entries [][2]int
	for _, span := range paragraphSpans(body) {
		id := pStyleOf(body[span[0]:span[1]])
		if _, ok := entryStyles[id]; !ok {
			if first >= 0 {
				break // the contents are written in one run; this ends it
			}
			continue
		}
		if first < 0 {
			first = span[0]
		}
		last = span[1]
		entries = append(entries, span)
	}
	if first < 0 {
		return body
	}

	var b strings.Builder
	b.WriteString(body[:first])
	b.WriteString(`<w:sdt><w:sdtPr><w:docPartObj>` +
		`<w:docPartGallery w:val="Table of Contents"/><w:docPartUnique/>` +
		`</w:docPartObj></w:sdtPr><w:sdtContent>`)
	for i, span := range entries {
		paragraph := body[span[0]:span[1]]
		if i == 0 {
			paragraph = openField(paragraph)
		}
		if i == len(entries)-1 {
			paragraph = closeField(paragraph)
		}
		b.WriteString(paragraph)
	}
	b.WriteString(`</w:sdtContent></w:sdt>`)
	b.WriteString(body[last:])
	return b.String()
}

// openField starts the TOC field inside the first entry, before its content.
// `w:dirty` asks Word to refresh the field when the document is opened, which
// is what fills in the page numbers.
func openField(paragraph string) string {
	field := `<w:r><w:fldChar w:fldCharType="begin" w:dirty="true"/></w:r>` +
		`<w:r><w:instrText xml:space="preserve">` + tocInstruction + `</w:instrText></w:r>` +
		`<w:r><w:fldChar w:fldCharType="separate"/></w:r>`
	return insertAfterProperties(paragraph, field)
}

// closeField ends the field at the very end of the last entry.
func closeField(paragraph string) string {
	end := `<w:r><w:fldChar w:fldCharType="end"/></w:r>`
	closing := strings.LastIndex(paragraph, "</w:p>")
	if closing < 0 {
		return paragraph
	}
	return paragraph[:closing] + end + paragraph[closing:]
}

// insertAfterProperties puts content right after a paragraph's <w:pPr>, which
// the schema requires to come first.
func insertAfterProperties(paragraph, content string) string {
	if pPr := firstElement(paragraph, "w:pPr"); pPr != "" {
		at := strings.Index(paragraph, pPr) + len(pPr)
		return paragraph[:at] + content + paragraph[at:]
	}
	open := strings.Index(paragraph, ">")
	if open < 0 {
		return paragraph
	}
	return paragraph[:open+1] + content + paragraph[open+1:]
}

// paragraphSpans returns the byte ranges of the body's <w:p> elements.
// Paragraphs do not nest, so a flat scan is enough.
func paragraphSpans(body string) [][2]int {
	var spans [][2]int
	for i := 0; i < len(body); {
		if !isTag(body, i, "<w:p") {
			i++
			continue
		}
		end := strings.Index(body[i:], "</w:p>")
		if end < 0 {
			break
		}
		end += i + len("</w:p>")
		spans = append(spans, [2]int{i, end})
		i = end
	}
	return spans
}

func pStyleOf(paragraph string) string {
	if m := rePStyle.FindString(paragraph); m != "" {
		return attrValue(m)
	}
	return ""
}
