package docx

import (
	"fmt"
	"regexp"
	"strings"
)

// The language of a document is what a screen reader pronounces it in, and
// what a tagged PDF declares as /Lang. LibreOffice ignores the lang attribute
// of the HTML it imports and writes en-US as the default language of every
// DOCX; the PDF inherits it. SetLanguage sets the default language instead —
// only the default: a style that deliberately says otherwise (code, a quote in
// another language) keeps its own.

var (
	reDocDefaults = regexp.MustCompile(`(?s)<w:docDefaults>.*?</w:docDefaults>`)
	reLangElement = regexp.MustCompile(`<w:lang\b[^>]*/>`)
	reLangVal     = regexp.MustCompile(`\bw:val="[^"]*"`)
	reRPrDefault  = regexp.MustCompile(`<w:rPrDefault>\s*<w:rPr>`)
	reStylesOpen  = regexp.MustCompile(`<w:styles\b[^>]*>`)
)

// SetLanguage makes lang (a BCP 47 tag such as fr-FR) the document's default
// language. An empty lang leaves the document as it is.
func SetLanguage(document []byte, lang string) ([]byte, error) {
	if lang == "" {
		return document, nil
	}
	parts, order, err := readZip(document)
	if err != nil {
		return nil, fmt.Errorf("%w: reading document: %w", ErrApply, err)
	}
	styles, ok := parts[partStyles]
	if !ok {
		return document, nil
	}
	parts[partStyles] = []byte(withDefaultLanguage(string(styles), lang))
	return writeZip(parts, order)
}

func withDefaultLanguage(styles, lang string) string {
	attr := `w:val="` + escapeAttr(lang) + `"`
	defaults := reDocDefaults.FindStringIndex(styles)
	if defaults == nil {
		open := reStylesOpen.FindStringIndex(styles)
		if open == nil {
			return styles
		}
		return styles[:open[1]] + `<w:docDefaults><w:rPrDefault><w:rPr><w:lang ` + attr +
			`/></w:rPr></w:rPrDefault></w:docDefaults>` + styles[open[1]:]
	}
	block := styles[defaults[0]:defaults[1]]
	switch {
	case reLangElement.MatchString(block):
		block = reLangElement.ReplaceAllStringFunc(block, func(el string) string {
			if reLangVal.MatchString(el) {
				return reLangVal.ReplaceAllString(el, attr)
			}
			return strings.Replace(el, "<w:lang", "<w:lang "+attr, 1)
		})
	case reRPrDefault.MatchString(block):
		loc := reRPrDefault.FindStringIndex(block)
		block = block[:loc[1]] + `<w:lang ` + attr + `/>` + block[loc[1]:]
	default:
		block = strings.Replace(block, "<w:docDefaults>",
			`<w:docDefaults><w:rPrDefault><w:rPr><w:lang `+attr+`/></w:rPr></w:rPrDefault>`, 1)
	}
	return styles[:defaults[0]] + block + styles[defaults[1]:]
}
