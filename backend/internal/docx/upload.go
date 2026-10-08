package docx

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// MaxTemplateSize bounds a template file. Company templates are a few
// hundred kilobytes; the bound keeps an upload from filling the database.
const MaxTemplateSize = 10 << 20

// ErrUnsafeTemplate is a template that could make LibreOffice load something
// from outside the document, or that carries active content. Every part of
// the template ends up in each exported document, which LibreOffice opens
// again to produce the PDF.
var ErrUnsafeTemplate = errors.New("docx: unsafe Word template")

// ParseTemplate validates a template held in memory, such as one uploaded
// from the administration console. name is the file name shown to users.
func ParseTemplate(name string, data []byte) (*Template, error) {
	base := path.Base(strings.ReplaceAll(name, `\`, "/"))
	ext := strings.ToLower(path.Ext(base))
	if ext != ".docx" && ext != ".dotx" {
		return nil, fmt.Errorf("%w: the file must be a .docx or .dotx document", ErrInvalidTemplate)
	}
	if len(data) > MaxTemplateSize {
		return nil, fmt.Errorf("%w: larger than %d MB", ErrInvalidTemplate, MaxTemplateSize>>20)
	}
	t, err := parseTemplate(data)
	if err != nil {
		return nil, err
	}
	if err := checkSafe(t.parts); err != nil {
		return nil, err
	}
	t.name = base
	return t, nil
}

// fieldLoadingContent matches the Word field codes that pull in another file
// or a URL when the document is opened or its fields are updated.
var fieldLoadingContent = regexp.MustCompile(`(?i)(^|\s)(INCLUDEPICTURE|INCLUDETEXT|LINK|DDE|DDEAUTO|IMPORT)\s`)

// Field instructions live in w:instrText runs (possibly split across several)
// and in the w:instr attribute of w:fldSimple.
var (
	reInstrText = regexp.MustCompile(`<w:instrText[^>]*>([^<]*)</w:instrText>`)
	reFldSimple = regexp.MustCompile(`<w:fldSimple\s[^>]*w:instr="([^"]*)"`)
)

// checkSafe refuses external references (other than hyperlinks, which are
// only followed by a reader who clicks them), macros and ActiveX controls.
func checkSafe(parts map[string][]byte) error {
	if strings.Contains(strings.ToLower(string(parts[partContentTypes])), "macroenabled") {
		return fmt.Errorf("%w: macro-enabled documents are not accepted", ErrUnsafeTemplate)
	}
	for name, content := range parts {
		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, "vbaproject.bin"), strings.HasPrefix(lower, "word/activex/"):
			return fmt.Errorf("%w: %s holds macros or ActiveX controls", ErrUnsafeTemplate, name)
		case strings.HasSuffix(lower, ".rels"):
			for _, rel := range parseRels(string(content)) {
				if strings.EqualFold(rel.TargetMode, "External") && rel.Type != relTypeHyperlink {
					return fmt.Errorf("%w: %s refers to the external resource %q", ErrUnsafeTemplate, name, rel.Target)
				}
			}
		case strings.HasPrefix(lower, "word/") && strings.HasSuffix(lower, ".xml"):
			if field := loadingField(string(content)); field != "" {
				return fmt.Errorf("%w: %s has a %s field", ErrUnsafeTemplate, name, strings.ToUpper(field))
			}
		}
	}
	return nil
}

// loadingField returns the first field code of xml that loads content, "" if
// there is none.
func loadingField(xml string) string {
	var instructions strings.Builder
	for _, m := range reInstrText.FindAllStringSubmatch(xml, -1) {
		instructions.WriteString(unescapeAttr(m[1]))
	}
	instructions.WriteByte(' ')
	for _, m := range reFldSimple.FindAllStringSubmatch(xml, -1) {
		instructions.WriteString(unescapeAttr(m[1]))
		instructions.WriteByte(' ')
	}
	if m := fieldLoadingContent.FindStringSubmatch(instructions.String() + " "); m != nil {
		return m[2]
	}
	return ""
}
