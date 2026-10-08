package docx

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A document leaves the application's control the moment it is downloaded: it
// is forwarded, printed and filed. Marking makes every page say where it came
// from — who exported it, when, under which classification — and, for the
// sensitive levels, carries a diagonal watermark across the page.
//
// Both go where Word keeps such things: the line is added to the page footers
// and the watermark is a shape in the page headers, the way Word's own
// "Design > Watermark" stores it. Existing headers and footers (a company
// template's) are kept and extended; a section without one gets ours. The PDF
// is converted from this DOCX, so it carries the same marking.

// Marking describes what to stamp on every page.
type Marking struct {
	// Footer is a single line added at the bottom of every page.
	Footer string
	// Watermark is set diagonally across every page; empty for none.
	Watermark string
}

const (
	relTypeHeader = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/header"
	relTypeFooter = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer"
	ctHeader      = "application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"
	ctFooter      = "application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"

	partSettings = "word/settings.xml"

	nsW   = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsR   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsV   = "urn:schemas-microsoft-com:vml"
	nsO   = "urn:schemas-microsoft-com:office:office"
	nsW10 = "urn:schemas-microsoft-com:office:word"
)

var (
	reSectPrOpen  = regexp.MustCompile(`<w:sectPr(?:\s[^>]*)?>`)
	reSectPrEmpty = regexp.MustCompile(`<w:sectPr(?:\s[^>]*)?/>`)
	reHdrFtrRef   = regexp.MustCompile(`<w:(header|footer)Reference\s[^>]*/>`)
	reRefType     = regexp.MustCompile(`w:type="([a-z]+)"`)
	reRefID       = regexp.MustCompile(`r:id="([^"]+)"`)
	reTitlePg     = regexp.MustCompile(`<w:titlePg(?:\s+w:val="(?:1|true|on)")?\s*/>`)
	reEvenOdd     = regexp.MustCompile(`<w:evenAndOddHeaders(?:\s+w:val="(?:1|true|on)")?\s*/>`)
	reRootOpen    = regexp.MustCompile(`<w:(hdr|ftr)(?:\s[^>]*)?>`)
)

// Mark stamps m on every page of a DOCX package.
func Mark(document []byte, m Marking) ([]byte, error) {
	if m.Footer == "" && m.Watermark == "" {
		return document, nil
	}
	parts, order, err := readZip(document)
	if err != nil {
		return nil, fmt.Errorf("%w: reading document: %w", ErrApply, err)
	}
	doc, ok := parts[partDocument]
	if !ok {
		return nil, fmt.Errorf("%w: document has no %s", ErrApply, partDocument)
	}
	pkg := &markedPackage{
		parts:   parts,
		order:   order,
		rels:    newRelSet(string(parts[partDocumentRels])),
		targets: map[string]string{},
	}
	for _, rel := range parseRels(string(parts[partDocumentRels])) {
		pkg.targets[rel.ID] = "word/" + strings.TrimPrefix(rel.Target, "/word/")
	}
	types := []string{"default"}
	if reEvenOdd.Match(parts[partSettings]) {
		types = append(types, "even")
	}

	body := string(doc)
	if m.Footer != "" {
		body, err = pkg.stamp(body, "footer", types, footerParagraph(m.Footer))
		if err != nil {
			return nil, err
		}
	}
	if m.Watermark != "" {
		body, err = pkg.stamp(body, "header", types, watermarkParagraph(m.Watermark))
		if err != nil {
			return nil, err
		}
	}
	pkg.put(partDocument, []byte(body))
	pkg.put(partDocumentRels, []byte(pkg.rels.marshal()))
	return writeZip(pkg.parts, pkg.order)
}

type markedPackage struct {
	parts   map[string][]byte
	order   []string
	rels    *relSet
	targets map[string]string // relationship id -> part name
	created int
}

func (p *markedPackage) put(name string, content []byte) {
	if _, exists := p.parts[name]; !exists {
		p.order = append(p.order, name)
	}
	p.parts[name] = content
}

// stamp adds paragraph to every header or footer (kind) the sections use, and
// gives the sections that have none a part of their own holding just it.
func (p *markedPackage) stamp(body, kind string, types []string, paragraph string) (string, error) {
	stamped := map[string]bool{}
	ours := "" // relationship id of the part created for bare sections

	var b strings.Builder
	last := 0
	for _, span := range sectionSpans(body) {
		sectPr := body[span[0]:span[1]]
		refs := references(sectPr, kind)
		want := types
		if reTitlePg.MatchString(sectPr) {
			want = append(append([]string{}, types...), "first")
		}
		var missing []string
		for _, t := range want {
			id, ok := refs[t]
			if !ok {
				missing = append(missing, t)
				continue
			}
			name := p.targets[id]
			if stamped[name] {
				continue
			}
			part, ok := p.parts[name]
			if !ok {
				return "", fmt.Errorf("%w: %s reference %s points at a missing part", ErrApply, kind, id)
			}
			extended, err := appendToHdrFtr(string(part), paragraph)
			if err != nil {
				return "", err
			}
			p.parts[name] = []byte(extended)
			stamped[name] = true
		}
		if len(missing) == 0 {
			continue
		}
		if ours == "" {
			ours = p.newPart(kind, paragraph)
		}
		var refsXML strings.Builder
		for _, t := range missing {
			fmt.Fprintf(&refsXML, `<w:%sReference w:type="%s" r:id="%s"/>`, kind, t, ours)
		}
		b.WriteString(body[last:span[0]])
		sectPr = insertReferences(sectPr, refsXML.String())
		if kind == "footer" {
			sectPr = clearOfTheEdge(sectPr)
		}
		b.WriteString(sectPr)
		last = span[1]
	}
	if last == 0 {
		return body, nil
	}
	b.WriteString(body[last:])
	return b.String(), nil
}

// newPart creates a header or footer part holding paragraph and returns the
// id of the relationship pointing at it.
func (p *markedPackage) newPart(kind, paragraph string) string {
	p.created++
	root, relType, contentType := "w:ftr", relTypeFooter, ctFooter
	if kind == "header" {
		root, relType, contentType = "w:hdr", relTypeHeader, ctHeader
	}
	target := fmt.Sprintf("%s-marking%d.xml", kind, p.created)
	part := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<` + root + ` xmlns:w="` + nsW + `" xmlns:r="` + nsR + `" xmlns:v="` + nsV + `" xmlns:o="` + nsO +
		`" xmlns:w10="` + nsW10 + `">` + paragraph + `</` + root + `>`
	p.put("word/"+target, []byte(part))
	ct := string(p.parts[partContentTypes])
	override := `<Override PartName="/word/` + target + `" ContentType="` + contentType + `"/>`
	p.parts[partContentTypes] = []byte(strings.Replace(ct, "</Types>", override+"</Types>", 1))
	id := p.rels.add(relType, target, "")
	p.targets[id] = "word/" + target
	return id
}

// sectionSpans locates every w:sectPr: the body's last one and those closing
// sections inside paragraph properties (the landscape sections, for one).
func sectionSpans(body string) [][2]int {
	var spans [][2]int
	for _, loc := range reSectPrOpen.FindAllStringIndex(body, -1) {
		end := strings.Index(body[loc[1]:], "</w:sectPr>")
		if end < 0 {
			continue
		}
		spans = append(spans, [2]int{loc[0], loc[1] + end + len("</w:sectPr>")})
	}
	for _, loc := range reSectPrEmpty.FindAllStringIndex(body, -1) {
		spans = append(spans, [2]int{loc[0], loc[1]})
	}
	// Self-closing ones were appended after the others; restore document order.
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j][0] < spans[j-1][0]; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	return spans
}

// references maps each header or footer type of a section to its
// relationship id.
func references(sectPr, kind string) map[string]string {
	out := map[string]string{}
	for _, m := range reHdrFtrRef.FindAllStringSubmatch(sectPr, -1) {
		if m[1] != kind {
			continue
		}
		typ := "default"
		if t := reRefType.FindStringSubmatch(m[0]); t != nil {
			typ = t[1]
		}
		if id := reRefID.FindStringSubmatch(m[0]); id != nil {
			out[typ] = id[1]
		}
	}
	return out
}

// minFooterDistance keeps the line printable: LibreOffice's own DOCX puts the
// footer at 0 from the page edge, where most printers cannot reach.
const minFooterDistance = 340 // twips, 6 mm

var reFooterDistance = regexp.MustCompile(`(<w:pgMar\s[^>]*?w:footer=")(\d+)(")`)

// clearOfTheEdge moves the footer of a section away from the paper's edge.
func clearOfTheEdge(sectPr string) string {
	return reFooterDistance.ReplaceAllStringFunc(sectPr, func(attr string) string {
		m := reFooterDistance.FindStringSubmatch(attr)
		if n, err := strconv.Atoi(m[2]); err == nil && n >= minFooterDistance {
			return attr
		}
		return m[1] + strconv.Itoa(minFooterDistance) + m[3]
	})
}

// insertReferences puts header or footer references first in the section
// properties, where the schema wants them.
func insertReferences(sectPr, refs string) string {
	if loc := reSectPrEmpty.FindStringIndex(sectPr); loc != nil && loc[0] == 0 {
		open := strings.TrimSuffix(strings.TrimSpace(sectPr[:len(sectPr)-2]), "/")
		return open + ">" + refs + "</w:sectPr>"
	}
	loc := reSectPrOpen.FindStringIndex(sectPr)
	return sectPr[:loc[1]] + refs + sectPr[loc[1]:]
}

// appendToHdrFtr adds paragraph at the end of a header or footer part,
// declaring the VML namespaces the watermark shape needs if they are missing.
func appendToHdrFtr(part, paragraph string) (string, error) {
	loc := reRootOpen.FindStringSubmatchIndex(part)
	if loc == nil {
		return "", fmt.Errorf("%w: header or footer part without a root element", ErrApply)
	}
	kind := part[loc[2]:loc[3]]
	closing := "</w:" + kind + ">"
	end := strings.LastIndex(part, closing)
	if end < 0 {
		return "", fmt.Errorf("%w: unterminated header or footer part", ErrApply)
	}
	open := part[loc[0]:loc[1]]
	declared := open
	for prefix, ns := range map[string]string{"v": nsV, "o": nsO, "w10": nsW10} {
		if !strings.Contains(declared, "xmlns:"+prefix+"=") {
			declared = strings.TrimSuffix(declared, ">") + ` xmlns:` + prefix + `="` + ns + `">`
		}
	}
	return part[:loc[0]] + declared + part[loc[1]:end] + paragraph + part[end:], nil
}

// footerParagraph is the traceability line: small, grey and centred, so it
// reads as a mark rather than as content.
func footerParagraph(text string) string {
	return `<w:p><w:pPr><w:spacing w:before="60" w:after="0"/><w:jc w:val="center"/></w:pPr>` +
		`<w:r><w:rPr><w:color w:val="666666"/><w:sz w:val="14"/><w:szCs w:val="14"/></w:rPr>` +
		`<w:t xml:space="preserve">` + escapeText(text) + `</w:t></w:r></w:p>`
}

// watermarkParagraph is Word's own watermark: a VML text path, silver and
// half transparent, centred on the margins and rotated 45° upwards, behind
// the text. Word and LibreOffice both recognise it as a watermark by its id.
func watermarkParagraph(text string) string {
	// The paragraph holding the shape is shrunk so it does not push the text.
	return `<w:p><w:pPr><w:spacing w:before="0" w:after="0"/><w:rPr><w:sz w:val="2"/></w:rPr></w:pPr>` +
		`<w:r><w:rPr><w:noProof/><w:sz w:val="2"/></w:rPr><w:pict>` +
		`<v:shapetype id="_x0000_t136" coordsize="21600,21600" o:spt="136" adj="10800" path="m@7,l@8,m@5,21600l@6,21600e">` +
		`<v:formulas><v:f eqn="sum #0 0 10800"/><v:f eqn="prod #0 2 1"/><v:f eqn="sum 21600 0 @1"/>` +
		`<v:f eqn="sum 0 0 @2"/><v:f eqn="sum 21600 0 @3"/><v:f eqn="if @0 @3 0"/><v:f eqn="if @0 21600 @1"/>` +
		`<v:f eqn="if @0 0 @2"/><v:f eqn="if @0 @4 21600"/><v:f eqn="mid @5 @6"/><v:f eqn="mid @8 @5"/>` +
		`<v:f eqn="mid @7 @8"/><v:f eqn="mid @6 @7"/><v:f eqn="sum @6 0 @5"/></v:formulas>` +
		`<v:path textpathok="t" o:connecttype="custom" o:connectlocs="@9,0;@10,10800;@11,21600;@12,10800" o:connectangles="270,180,90,0"/>` +
		`<v:textpath on="t" fitshape="t"/><v:handles><v:h position="#0,bottomRight" xrange="6629,14971"/></v:handles>` +
		`<o:lock v:ext="edit" text="t" shapetype="t"/></v:shapetype>` +
		`<v:shape id="PowerPlusWaterMarkObject" o:spid="_x0000_s2049" type="#_x0000_t136" ` +
		`style="position:absolute;margin-left:0;margin-top:0;width:460pt;height:115pt;rotation:315;z-index:-251657216;` +
		`mso-position-horizontal:center;mso-position-horizontal-relative:margin;mso-position-vertical:center;mso-position-vertical-relative:margin" ` +
		`o:allowincell="f" fillcolor="silver" stroked="f"><v:fill opacity=".5"/>` +
		`<v:textpath style="font-family:&quot;Calibri&quot;;font-size:1pt" string="` + escapeAttr(text) + `"/>` +
		`<w10:wrap anchorx="margin" anchory="margin"/></v:shape></w:pict></w:r></w:p>`
}

var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeText(s string) string { return textEscaper.Replace(s) }
