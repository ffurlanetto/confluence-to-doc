// Package docx applies a company Word template to a generated DOCX document.
//
// The converter produces a plain DOCX from HTML; this package re-homes that
// content inside the corporate template so the result carries the company
// header, footer, fonts, colours and page setup.
//
// The template package is the base of the result: every part of it is kept
// (styles, theme, headers, footers, settings, page setup), and only the body
// content of the generated document is injected into it. References that are
// local to the generated document — images, hyperlinks, list numbering and
// paragraph styles — are remapped so they stay valid inside the template.
package docx

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Part names of the OPC package this code manipulates.
const (
	partContentTypes = "[Content_Types].xml"
	partDocument     = "word/document.xml"
	partDocumentRels = "word/_rels/document.xml.rels"
	partStyles       = "word/styles.xml"
	partNumbering    = "word/numbering.xml"
)

const (
	relTypeImage     = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/image"
	relTypeHyperlink = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink"
	relTypeNumbering = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering"

	ctDocumentMain = "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	ctTemplateMain = "application/vnd.openxmlformats-officedocument.wordprocessingml.template.main+xml"
	ctNumbering    = "application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"
)

var (
	// ErrInvalidTemplate is returned when a file is not a usable Word template.
	ErrInvalidTemplate = errors.New("docx: invalid Word template")
	// ErrApply is returned when the template cannot be applied to a generated
	// document. Such a failure is deterministic: retrying it is pointless.
	ErrApply = errors.New("docx: cannot apply the Word template")
)

// Template is a company Word template (.docx or .dotx) loaded in memory.
// It is immutable and safe for concurrent use by several workers.
type Template struct {
	name  string
	parts map[string][]byte
	order []string

	styleIDs              map[string]bool
	styleIDsByName        map[styleKey]string
	defaultParagraphStyle string
	sectPr                string
	// textWidth is the printable width of the template's page, in twips; 0 when
	// the template does not say.
	textWidth int

	maxAbstractNumID int
	maxNumID         int
}

// LoadTemplate reads and validates a template from disk. Validation happens
// once at startup so a broken template fails fast instead of at export time.
func LoadTemplate(pathOnDisk string) (*Template, error) {
	data, err := os.ReadFile(pathOnDisk) //nolint:gosec // G304: operator-provided configuration path
	if err != nil {
		return nil, fmt.Errorf("docx: reading template: %w", err)
	}
	t, err := parseTemplate(data)
	if err != nil {
		return nil, err
	}
	t.name = path.Base(pathOnDisk)
	return t, nil
}

func parseTemplate(data []byte) (*Template, error) {
	parts, order, err := readZip(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTemplate, err)
	}
	for _, required := range []string{partContentTypes, partDocument, partDocumentRels, partStyles} {
		if _, ok := parts[required]; !ok {
			return nil, fmt.Errorf("%w: missing %s", ErrInvalidTemplate, required)
		}
	}

	sectPr, err := extractSectPr(string(parts[partDocument]))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTemplate, err)
	}

	styles := string(parts[partStyles])
	t := &Template{
		parts:                 parts,
		order:                 order,
		sectPr:                sectPr,
		textWidth:             textWidthOf(sectPr),
		styleIDs:              styleIDsOf(styles),
		styleIDsByName:        styleIDsByName(parseStyles(styles)),
		defaultParagraphStyle: defaultParagraphStyleOf(styles),
	}
	if numbering, ok := parts[partNumbering]; ok {
		t.maxAbstractNumID = maxAttrValue(string(numbering), reAbstractNumDef)
		t.maxNumID = maxAttrValue(string(numbering), reNumDef)
	}
	return t, nil
}

// Name is the template file name, for logs and the API.
func (t *Template) Name() string { return t.name }

// Styles lists the paragraph style ids the template defines; content styled
// with anything else is remapped onto DefaultParagraphStyle.
func (t *Template) Styles() []string {
	out := make([]string, 0, len(t.styleIDs))
	for id := range t.styleIDs {
		out = append(out, id)
	}
	return out
}

// DefaultParagraphStyle is the template's default paragraph style id.
func (t *Template) DefaultParagraphStyle() string { return t.defaultParagraphStyle }

// Apply returns a DOCX made of the template's presentation and the generated
// document's content.
func (t *Template) Apply(generated []byte) ([]byte, error) {
	genParts, _, err := readZip(generated)
	if err != nil {
		return nil, fmt.Errorf("%w: reading generated document: %w", ErrApply, err)
	}
	genDoc, ok := genParts[partDocument]
	if !ok {
		return nil, fmt.Errorf("%w: generated document has no %s", ErrApply, partDocument)
	}

	prologue, body, err := splitDocument(string(genDoc))
	if err != nil {
		return nil, err
	}

	out := make(map[string][]byte, len(t.parts)+8)
	order := make([]string, 0, len(t.order)+8)
	for _, name := range t.order {
		out[name] = t.parts[name]
		order = append(order, name)
	}
	add := func(name string, content []byte) {
		if _, exists := out[name]; !exists {
			order = append(order, name)
		}
		out[name] = content
	}

	rels := newRelSet(string(t.parts[partDocumentRels]))

	// Images: copy the media parts under names that cannot clash with the
	// template's own media, and point new relationships at them.
	idMap := map[string]string{}
	for _, rel := range parseRels(string(genParts[partDocumentRels])) {
		switch rel.Type {
		case relTypeImage:
			source := "word/" + path.Clean(rel.Target)
			content, ok := genParts[source]
			if !ok {
				continue // relationship without a part: drop it rather than dangle
			}
			target := "media/export-" + path.Base(rel.Target)
			add("word/"+target, content)
			idMap[rel.ID] = rels.add(relTypeImage, target, "")
		case relTypeHyperlink:
			idMap[rel.ID] = rels.add(relTypeHyperlink, rel.Target, rel.TargetMode)
		}
	}
	body = remapRelationshipIDs(body, idMap)

	// Lists: keep the generated numbering definitions, shifted past the
	// template's own so the two sets cannot collide.
	if genNumbering, ok := genParts[partNumbering]; ok {
		merged, numIDMap := t.mergeNumbering(string(genNumbering))
		add(partNumbering, []byte(merged))
		body = remapNumIDs(body, numIDMap)
		if !rels.has(relTypeNumbering) {
			rels.add(relTypeNumbering, "numbering.xml", "")
		}
	}

	// Styles: references are matched against the template by id, then by style
	// name; a paragraph style it does not have falls back to its default, so
	// text inherits the company fonts instead of the converter's.
	body, borrowed := t.remapStyles(body, parseStyles(string(genParts[partStyles])))
	if len(borrowed) > 0 {
		add(partStyles, []byte(addStyles(string(t.parts[partStyles]), borrowed)))
	}

	// Tables: LibreOffice sized them for its own, wider page. The template's
	// page replaces it just below, so anything too wide is scaled down now
	// rather than clipped by the reader.
	body = fitTables(body, t.textWidth)

	add(partDocument, []byte(prologue+body+t.sectPr+"</w:body></w:document>"))
	add(partDocumentRels, []byte(rels.marshal()))
	// Keep the generated metadata: it carries the export title and date.
	for _, name := range []string{"docProps/core.xml", "docProps/app.xml"} {
		if content, ok := genParts[name]; ok {
			if _, inTemplate := out[name]; inTemplate {
				out[name] = content
			}
		}
	}
	add(partContentTypes, []byte(t.contentTypes(out)))

	return writeZip(out, order)
}

// contentTypes adapts the template's content types to the result package: a
// template main part becomes a document main part, and every media extension
// present in the package is declared.
func (t *Template) contentTypes(parts map[string][]byte) string {
	ct := string(t.parts[partContentTypes])
	ct = strings.ReplaceAll(ct, ctTemplateMain, ctDocumentMain)

	declared := map[string]bool{}
	for _, m := range reDefaultExt.FindAllStringSubmatch(ct, -1) {
		declared[strings.ToLower(m[1])] = true
	}
	var additions strings.Builder
	for name := range parts {
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
		if ext == "" || declared[ext] {
			continue
		}
		if mediaType, ok := mediaTypes[ext]; ok {
			declared[ext] = true
			fmt.Fprintf(&additions, `<Default Extension="%s" ContentType="%s"/>`, ext, mediaType)
		}
	}
	if _, ok := parts[partNumbering]; ok && !strings.Contains(ct, "/word/numbering.xml") {
		fmt.Fprintf(&additions, `<Override PartName="/word/numbering.xml" ContentType="%s"/>`, ctNumbering)
	}
	if additions.Len() > 0 {
		ct = strings.Replace(ct, "</Types>", additions.String()+"</Types>", 1)
	}
	return ct
}

var mediaTypes = map[string]string{
	"png":  "image/png",
	"jpeg": "image/jpeg",
	"jpg":  "image/jpeg",
	"gif":  "image/gif",
	"bmp":  "image/bmp",
	"tiff": "image/tiff",
	"svg":  "image/svg+xml",
	"emf":  "image/x-emf",
	"wmf":  "image/x-wmf",
}

// remapStyles rewrites references to styles the template does not define.
// remapStyles rewrites the generated document's style references so they point
// at the template. It returns the rewritten body and the character styles that
// have to be carried over from the generated document because the template has
// no equivalent.
func (t *Template) remapStyles(body string, generated map[string]style) (string, []style) {
	// resolve answers: which style of the template does this reference mean?
	// The id first — the converter and Word agree on `Heading1` — then the
	// style name, which is where `InternetLink` meets `Hyperlink`.
	resolve := func(kind, id string) (string, bool) {
		if t.styleIDs[id] {
			return id, true
		}
		if g, ok := generated[id]; ok && g.name != "" {
			if templateID, ok := t.styleIDsByName[styleKey{kind, g.name}]; ok {
				return templateID, true
			}
		}
		return "", false
	}

	body = rePStyle.ReplaceAllStringFunc(body, func(m string) string {
		id, ok := resolve("paragraph", attrValue(m))
		if !ok {
			id = t.defaultParagraphStyle
		}
		return `<w:pStyle w:val="` + id + `"/>`
	})

	var borrowed []style
	carried := map[string]bool{}
	body = reRStyle.ReplaceAllStringFunc(body, func(m string) string {
		id := attrValue(m)
		if templateID, ok := resolve("character", id); ok {
			return `<w:rStyle w:val="` + templateID + `"/>`
		}
		// No equivalent in the template. Dropping the reference would lose
		// what it stands for — the bold of <strong>, the monospace of <code> —
		// so the converter's own definition comes along instead. Its id cannot
		// clash: the template does not define it.
		g, ok := generated[id]
		if !ok || !g.hasFormatting() {
			return ""
		}
		if !carried[id] {
			carried[id] = true
			borrowed = append(borrowed, g)
		}
		return m
	})

	// Table styles have no safe default: dropping the reference keeps the
	// direct formatting (borders, shading) and inherits the rest.
	body = reTblStyle.ReplaceAllStringFunc(body, func(m string) string {
		if id, ok := resolve("table", attrValue(m)); ok {
			return `<w:tblStyle w:val="` + id + `"/>`
		}
		return ""
	})
	return body, borrowed
}

// mergeNumbering appends the generated list definitions to the template's,
// shifting their ids. It returns the merged part and the numId mapping to
// apply to the body.
func (t *Template) mergeNumbering(generated string) (string, map[int]int) {
	templateNumbering, ok := t.parts[partNumbering]
	if !ok {
		// No template numbering: the generated part can be used as it is.
		return generated, nil
	}
	abstractOffset, numOffset := t.maxAbstractNumID+1, t.maxNumID+1

	shifted := reAbstractNumDef.ReplaceAllStringFunc(generated, shiftAttr(abstractOffset))
	shifted = reAbstractNumRef.ReplaceAllStringFunc(shifted, shiftAttr(abstractOffset))
	shifted = reNumDef.ReplaceAllStringFunc(shifted, shiftAttr(numOffset))

	numIDMap := map[int]int{}
	for _, m := range reNumDef.FindAllStringSubmatch(generated, -1) {
		if id, err := strconv.Atoi(m[1]); err == nil {
			numIDMap[id] = id + numOffset
		}
	}

	merged := string(templateNumbering)
	// The schema requires every <w:abstractNum> before the first <w:num>.
	if abstracts := strings.Join(elements(shifted, "w:abstractNum"), ""); abstracts != "" {
		if i := strings.Index(merged, "<w:num "); i >= 0 {
			merged = merged[:i] + abstracts + merged[i:]
		} else {
			merged = strings.Replace(merged, "</w:numbering>", abstracts+"</w:numbering>", 1)
		}
	}
	if nums := strings.Join(elements(shifted, "w:num"), ""); nums != "" {
		merged = strings.Replace(merged, "</w:numbering>", nums+"</w:numbering>", 1)
	}
	return merged, numIDMap
}

// ---------------------------------------------------------------- XML helpers

var (
	reBodyOpen       = regexp.MustCompile(`<w:body(?:\s[^>]*)?>`)
	reSectPr         = regexp.MustCompile(`(?s)<w:sectPr(?:\s[^>]*)?>.*?</w:sectPr>|<w:sectPr(?:\s[^>]*)?/>`)
	rePStyle         = regexp.MustCompile(`<w:pStyle\s[^>]*/>`)
	reRStyle         = regexp.MustCompile(`<w:rStyle\s[^>]*/>`)
	reTblStyle       = regexp.MustCompile(`<w:tblStyle\s[^>]*/>`)
	reStyleID        = regexp.MustCompile(`w:styleId="([^"]*)"`)
	reDefaultStyle   = regexp.MustCompile(`(?s)<w:style\s[^>]*?w:type="paragraph"[^>]*?w:default="(?:1|true|on)"[^>]*?>`)
	reAttrVal        = regexp.MustCompile(`w:val="([^"]*)"`)
	reRelID          = regexp.MustCompile(`(r:(?:id|embed|link)=")([^"]*)(")`)
	reNumIDRef       = regexp.MustCompile(`<w:numId\s+w:val="(\d+)"\s*/>`)
	reAbstractNumDef = regexp.MustCompile(`w:abstractNumId="(\d+)"`)
	reAbstractNumRef = regexp.MustCompile(`<w:abstractNumId\s+w:val="(\d+)"\s*/>`)
	reNumDef         = regexp.MustCompile(`<w:num\s+w:numId="(\d+)"`)
	reDefaultExt     = regexp.MustCompile(`<Default\s+Extension="([^"]*)"`)
	reDigits         = regexp.MustCompile(`\d+`)
)

// splitDocument returns everything up to and including <w:body>, and the body
// content without its trailing section properties.
func splitDocument(doc string) (prologue, body string, err error) {
	open := reBodyOpen.FindStringIndex(doc)
	if open == nil {
		return "", "", fmt.Errorf("%w: generated document has no <w:body>", ErrApply)
	}
	end := strings.LastIndex(doc, "</w:body>")
	if end < 0 || end < open[1] {
		return "", "", fmt.Errorf("%w: generated document has no </w:body>", ErrApply)
	}
	body = doc[open[1]:end]
	if loc := reSectPr.FindStringIndex(body); loc != nil {
		body = body[:loc[0]] + body[loc[1]:]
	}
	return doc[:open[1]], body, nil
}

// extractSectPr returns the body-level section properties of a document: the
// page size, margins and header/footer references of the template.
func extractSectPr(doc string) (string, error) {
	end := strings.LastIndex(doc, "</w:body>")
	if end < 0 {
		return "", errors.New("template has no </w:body>")
	}
	matches := reSectPr.FindAllString(doc[:end], -1)
	if len(matches) == 0 {
		return "", errors.New("template has no <w:sectPr> (page setup)")
	}
	return matches[len(matches)-1], nil
}

func styleIDsOf(styles string) map[string]bool {
	ids := map[string]bool{}
	for _, m := range reStyleID.FindAllStringSubmatch(styles, -1) {
		ids[m[1]] = true
	}
	return ids
}

func defaultParagraphStyleOf(styles string) string {
	if m := reDefaultStyle.FindString(styles); m != "" {
		if id := reStyleID.FindStringSubmatch(m); id != nil {
			return id[1]
		}
	}
	return "Normal"
}

func attrValue(element string) string {
	if m := reAttrVal.FindStringSubmatch(element); m != nil {
		return m[1]
	}
	return ""
}

func maxAttrValue(xml string, re *regexp.Regexp) int {
	maxID := -1
	for _, m := range re.FindAllStringSubmatch(xml, -1) {
		if v, err := strconv.Atoi(m[1]); err == nil && v > maxID {
			maxID = v
		}
	}
	return maxID
}

func shiftAttr(offset int) func(string) string {
	return func(m string) string {
		return reDigits.ReplaceAllStringFunc(m, func(d string) string {
			v, err := strconv.Atoi(d)
			if err != nil {
				return d
			}
			return strconv.Itoa(v + offset)
		})
	}
}

// elements returns the outer XML of every top-level <name> element.
func elements(xml, name string) []string {
	var out []string
	open, closeTag := "<"+name+" ", "</"+name+">"
	for i := 0; ; {
		start := strings.Index(xml[i:], open)
		if start < 0 {
			return out
		}
		start += i
		end := strings.Index(xml[start:], closeTag)
		if end < 0 {
			return out
		}
		end += start + len(closeTag)
		out = append(out, xml[start:end])
		i = end
	}
}

func remapRelationshipIDs(body string, idMap map[string]string) string {
	if len(idMap) == 0 {
		return body
	}
	return reRelID.ReplaceAllStringFunc(body, func(m string) string {
		parts := reRelID.FindStringSubmatch(m)
		if newID, ok := idMap[parts[2]]; ok {
			return parts[1] + newID + parts[3]
		}
		return m
	})
}

func remapNumIDs(body string, numIDMap map[int]int) string {
	if len(numIDMap) == 0 {
		return body
	}
	return reNumIDRef.ReplaceAllStringFunc(body, func(m string) string {
		old, err := strconv.Atoi(reNumIDRef.FindStringSubmatch(m)[1])
		if err != nil {
			return m
		}
		if newID, ok := numIDMap[old]; ok {
			return `<w:numId w:val="` + strconv.Itoa(newID) + `"/>`
		}
		return m
	})
}

// ------------------------------------------------------------- relationships

type relationship struct {
	ID         string
	Type       string
	Target     string
	TargetMode string
}

var reRelationship = regexp.MustCompile(`<Relationship\s[^>]*>`)

func parseRels(xml string) []relationship {
	attr := func(element, name string) string {
		re := regexp.MustCompile(name + `="([^"]*)"`)
		if m := re.FindStringSubmatch(element); m != nil {
			return m[1]
		}
		return ""
	}
	var out []relationship
	for _, element := range reRelationship.FindAllString(xml, -1) {
		out = append(out, relationship{
			ID:         attr(element, "Id"),
			Type:       attr(element, "Type"),
			Target:     unescapeAttr(attr(element, "Target")),
			TargetMode: attr(element, "TargetMode"),
		})
	}
	return out
}

// relSet builds the result's relationship part, allocating ids that cannot
// collide with the template's.
type relSet struct {
	existing []relationship
	added    []relationship
	next     int
}

func newRelSet(templateRels string) *relSet {
	existing := parseRels(templateRels)
	next := 1
	for _, rel := range existing {
		if n, err := strconv.Atoi(strings.TrimPrefix(rel.ID, "rId")); err == nil && n >= next {
			next = n + 1
		}
	}
	return &relSet{existing: existing, next: next}
}

func (s *relSet) has(relType string) bool {
	for _, rel := range s.existing {
		if rel.Type == relType {
			return true
		}
	}
	return false
}

func (s *relSet) add(relType, target, targetMode string) string {
	id := "rId" + strconv.Itoa(s.next)
	s.next++
	s.added = append(s.added, relationship{ID: id, Type: relType, Target: target, TargetMode: targetMode})
	return id
}

func (s *relSet) marshal() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for _, rel := range append(append([]relationship{}, s.existing...), s.added...) {
		fmt.Fprintf(&b, `<Relationship Id="%s" Type="%s" Target="%s"`, rel.ID, rel.Type, escapeAttr(rel.Target))
		if rel.TargetMode != "" {
			fmt.Fprintf(&b, ` TargetMode="%s"`, rel.TargetMode)
		}
		b.WriteString(`/>`)
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

var (
	attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	// Single pass, so an unescaped "&" in the input cannot be re-processed.
	attrUnescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
)

func escapeAttr(s string) string { return attrEscaper.Replace(s) }

func unescapeAttr(s string) string { return attrUnescaper.Replace(s) }

// ------------------------------------------------------------------ OPC zip

// maxPartSize bounds a single part read from a package (templates and
// generated documents are small; this guards against zip bombs).
const maxPartSize = 64 << 20

func readZip(data []byte) (map[string][]byte, []string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, err
	}
	parts := make(map[string][]byte, len(zr.File))
	order := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		content, err := io.ReadAll(io.LimitReader(rc, maxPartSize))
		_ = rc.Close()
		if err != nil {
			return nil, nil, err
		}
		if _, exists := parts[f.Name]; !exists {
			order = append(order, f.Name)
		}
		parts[f.Name] = content
	}
	return parts, order, nil
}

func writeZip(parts map[string][]byte, order []string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range order {
		content, ok := parts[name]
		if !ok {
			continue
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(content); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
