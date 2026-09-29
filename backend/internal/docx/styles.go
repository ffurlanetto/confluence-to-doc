package docx

import (
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
		key := styleKey{s.kind, s.name}
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
