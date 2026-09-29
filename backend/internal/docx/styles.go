package docx

import (
	"fmt"
	"regexp"
	"strings"
)

// LibreOffice and Word disagree about style *ids* while agreeing about style
// *names*: a hyperlink is `InternetLink` in one and `Hyperlink` in the other,
// yet both declare <w:name w:val="Hyperlink"/>. Matching references by id alone
// therefore loses every character style the converter applies — bold from
// <strong>, italics from <em>, the monospace of <code>, and the colour and
// underline of links, which is where a company theme is most visible.
//
// So references are resolved by name when the id does not match, and a
// character style the template has no equivalent for is carried over with its
// definition instead of being dropped.

// style is one <w:style> element of a styles part.
type style struct {
	id   string
	kind string // paragraph | character | table | numbering
	name string // the Word style name, which is not translated in the XML
	xml  string // the element itself, for copying into the result
}

// styleKey identifies a style by what it is called, within its kind.
type styleKey struct{ kind, name string }

// normaliseName is how two style names are compared. Word matches its own
// style names "exactly [on] spelling and spacing, but not necessarily
// capitalisation" — and the case does differ in practice: LibreOffice writes
// `Heading 1` where Word writes `heading 1`.
func normaliseName(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

// The style names LibreOffice gives paragraphs styled by one of this
// exporter's CSS classes are built from the class *and* from its own base
// style, whose name changes between LibreOffice versions ("Body Text.toc-entry-2"
// in one, "Text Body.toc-entry-2" in another). Only the suffix is ours, so only
// the suffix is matched.
var reClassSuffix = regexp.MustCompile(`\.([a-z-]+?)(?:-([1-9]))?$`)

// wordStyleNames translates the names LibreOffice gives its own styles into the
// Word built-in names a company template is likely to define. Only concepts
// that really correspond are listed: a template that defines none of them is no
// worse off.
var wordStyleNames = map[string]string{
	"quotations":        "quote",
	"preformatted text": "html preformatted",
}

// classStyleNames does the same for the classes this exporter puts on its own
// paragraphs, keyed by the class without its level.
var classStyleNames = map[string]string{
	"doc-title": "title",
	"doc-meta":  "subtitle",
	"toc-title": "toc heading",
	"toc-entry": "toc",
}

// wordStyleName returns the Word built-in name a generated style stands for, or
// "" when it stands for none.
func wordStyleName(normalised string) string {
	if word, ok := wordStyleNames[normalised]; ok {
		return word
	}
	m := reClassSuffix.FindStringSubmatch(normalised)
	if m == nil {
		return ""
	}
	word, ok := classStyleNames[m[1]]
	if !ok {
		return ""
	}
	if m[2] != "" {
		return word + " " + m[2]
	}
	return word
}

var (
	reStyleElement = regexp.MustCompile(`(?s)<w:style\s[^>]*>.*?</w:style>`)
	reStyleType    = regexp.MustCompile(`w:type="([^"]*)"`)
	reStyleName    = regexp.MustCompile(`<w:name\s[^>]*/>`)
	reRPrBlock     = regexp.MustCompile(`(?s)<w:rPr>(.*?)</w:rPr>`)
	reStylesClose  = regexp.MustCompile(`</w:styles>`)
)

func parseStyles(xml string) map[string]style {
	out := map[string]style{}
	for _, element := range reStyleElement.FindAllString(xml, -1) {
		s := style{xml: element}
		if m := reStyleID.FindStringSubmatch(element); m != nil {
			s.id = m[1]
		}
		if m := reStyleType.FindStringSubmatch(element); m != nil {
			s.kind = m[1]
		}
		if name := reStyleName.FindString(element); name != "" {
			s.name = attrValue(name)
		}
		if s.id != "" {
			out[s.id] = s
		}
	}
	return out
}

// styleIDsByName indexes a template's styles so a reference can be resolved by
// what the style is called rather than by the id the converter chose.
func styleIDsByName(styles map[string]style) map[styleKey]string {
	out := make(map[styleKey]string, len(styles))
	for _, s := range styles {
		if s.name == "" {
			continue
		}
		key := styleKey{s.kind, normaliseName(s.name)}
		// First definition wins, so a template that declares a name twice
		// resolves the same way every time.
		if _, exists := out[key]; !exists {
			out[key] = s.id
		}
	}
	return out
}

// hasFormatting reports whether a character style carries run properties.
// LibreOffice emits a few empty ones (`Del`), which are not worth copying.
func (s style) hasFormatting() bool {
	m := reRPrBlock.FindStringSubmatch(s.xml)
	return m != nil && strings.TrimSpace(m[1]) != ""
}

// names lists what this style may be matched on: its own name first, then the
// Word built-in the name stands for.
func (s style) names() []string {
	n := normaliseName(s.name)
	if n == "" {
		return nil
	}
	if word := wordStyleName(n); word != "" {
		return []string{n, word}
	}
	return []string{n}
}

// addStyles appends style definitions to a styles part.
func addStyles(styles string, added []style) string {
	if len(added) == 0 {
		return styles
	}
	var b strings.Builder
	for _, s := range added {
		b.WriteString(s.xml)
	}
	if loc := reStylesClose.FindStringIndex(styles); loc != nil {
		return styles[:loc[0]] + b.String() + styles[loc[0]:]
	}
	return styles
}

// Properties worth keeping from a paragraph style the template has no name
// for. They say what the text *is* — a table heading is bold, a quotation is
// indented — as opposed to what it looks like, which is the template's call.
var (
	keptRunProps  = []string{"b", "bCs", "i", "iCs", "u", "strike", "dstrike", "caps", "smallCaps", "vertAlign"}
	keptParaProps = []string{"jc", "ind", "keepNext", "keepLines", "outlineLvl"}
)

var reEmptyElement = regexp.MustCompile(`<w:(\w+)(?:\s[^>]*)?/>`)

// essence returns the style stripped of everything that is presentation: the
// fonts, sizes and colours that the template is there to decide. What is left
// is based on the template's default style, so it inherits the company look
// and only adds the emphasis the converter meant.
//
// It returns the empty string when nothing is left worth defining.
func (s style) essence(basedOn string) string {
	pPr := keepOnly(blockOf(s.xml, "w:pPr"), keptParaProps)
	rPr := keepOnly(blockOf(s.xml, "w:rPr"), keptRunProps)
	if pPr == "" && rPr == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<w:style w:type="%s" w:styleId="%s">`, s.kind, s.id)
	if s.name != "" {
		fmt.Fprintf(&b, `<w:name w:val="%s"/>`, escapeAttr(s.name))
	}
	fmt.Fprintf(&b, `<w:basedOn w:val="%s"/>`, basedOn)
	if pPr != "" {
		b.WriteString(`<w:pPr>` + pPr + `</w:pPr>`)
	}
	if rPr != "" {
		b.WriteString(`<w:rPr>` + rPr + `</w:rPr>`)
	}
	b.WriteString(`</w:style>`)
	return b.String()
}

// blockOf returns the contents of the first <name>…</name> of xml.
func blockOf(xml, name string) string {
	element := firstElement(xml, name)
	if element == "" {
		return ""
	}
	return element[strings.Index(element, ">")+1 : len(element)-len("</"+name+">")]
}

// keepOnly filters a property block down to the listed empty elements, in the
// order they appear — which is the order the schema requires.
func keepOnly(block string, allowed []string) string {
	if block == "" {
		return ""
	}
	wanted := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		wanted[name] = true
	}
	var b strings.Builder
	for _, m := range reEmptyElement.FindAllStringSubmatch(block, -1) {
		if wanted[m[1]] {
			b.WriteString(m[0])
		}
	}
	return b.String()
}
