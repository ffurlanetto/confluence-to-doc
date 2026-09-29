package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures below mimic the two inputs of the merge: a corporate template
// authored in Word, and the flat DOCX that LibreOffice produces from our HTML.

const templateDocument = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<w:body><w:p><w:r><w:t>placeholder body</w:t></w:r></w:p>
<w:sectPr><w:headerReference w:type="default" r:id="rId4"/><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1417" w:right="1134" w:bottom="1134" w:left="1134" w:header="708"/></w:sectPr>
</w:body></w:document>`

const templateStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Corporate Sans" w:hAnsi="Corporate Sans"/><w:sz w:val="20"/></w:rPr></w:rPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/></w:style>
<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/></w:style>
<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/></w:style>
</w:styles>`

const templateNumbering = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl></w:abstractNum>
<w:abstractNum w:abstractNumId="1"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl></w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>
<w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num>
</w:numbering>`

const templateContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Default Extension="jpeg" ContentType="image/jpeg"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.template.main+xml"/>
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
<Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/>
<Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/>
</Types>`

const templateRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>
<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/>
</Relationships>`

// packageRels is the package-level relationship that points Word at the main
// document part; without it the file is not a readable OPC package.
const packageRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

func templateParts() map[string][]byte {
	return map[string][]byte{
		partContentTypes:        []byte(templateContentTypes),
		"_rels/.rels":           []byte(packageRels),
		partDocument:            []byte(templateDocument),
		partDocumentRels:        []byte(templateRels),
		partStyles:              []byte(templateStyles),
		partNumbering:           []byte(templateNumbering),
		"word/header1.xml":      []byte(`<?xml version="1.0"?><w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>ACME CORPORATION</w:t></w:r></w:p></w:hdr>`),
		"word/theme/theme1.xml": []byte(`<?xml version="1.0"?><a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:themeElements/></a:theme>`),
		"word/media/logo.jpeg":  []byte("template-logo-bytes"),
		"docProps/core.xml":     []byte(`<?xml version="1.0"?><cp:coreProperties xmlns:cp="x"><dc:title xmlns:dc="y">Template</dc:title></cp:coreProperties>`),
	}
}

// generatedDocument mirrors LibreOffice output: styles it invented
// (BodyText, TableGrid2), its own numbering, an image, a hyperlink and a
// trailing sectPr that must be replaced by the template's.
const generatedDocument = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>1 Product documentation</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="BodyText"/></w:pPr><w:r><w:rPr><w:rStyle w:val="StrongEmphasis"/></w:rPr><w:t>Body paragraph</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>First bullet</w:t></w:r></w:p>
<w:p><w:r><w:drawing><a:blip xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" r:embed="rId3"/></w:drawing></w:r></w:p>
<w:p><w:hyperlink r:id="rId4"><w:r><w:t>Confluence</w:t></w:r></w:hyperlink></w:p>
<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid2"/></w:tblPr><w:tr><w:tc><w:p><w:r><w:t>cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134"/></w:sectPr>
</w:body></w:document>`

const generatedNumbering = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/><w:lvlText w:val="LO-BULLET"/></w:lvl></w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>
</w:numbering>`

const generatedRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>
<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://confluence.example.com/x/1" TargetMode="External"/>
</Relationships>`

func generatedParts() map[string][]byte {
	return map[string][]byte{
		partContentTypes:        []byte(`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="png" ContentType="image/png"/><Override PartName="/word/document.xml" ContentType="` + ctDocumentMain + `"/></Types>`),
		partDocument:            []byte(generatedDocument),
		partDocumentRels:        []byte(generatedRels),
		partStyles:              []byte(`<?xml version="1.0"?><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:style w:type="paragraph" w:styleId="BodyText"><w:rPr><w:rFonts w:ascii="Liberation Sans"/></w:rPr></w:style></w:styles>`),
		partNumbering:           []byte(generatedNumbering),
		"word/media/image1.png": []byte("generated-image-bytes"),
		"docProps/core.xml":     []byte(`<?xml version="1.0"?><cp:coreProperties xmlns:cp="x"><dc:title xmlns:dc="y">Product documentation</dc:title></cp:coreProperties>`),
	}
}

func buildZip(t *testing.T, parts map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func loadFixtureTemplate(t *testing.T) *Template {
	t.Helper()
	tpl, err := parseTemplate(buildZip(t, templateParts()))
	if err != nil {
		t.Fatalf("parseTemplate: %v", err)
	}
	return tpl
}

// applyFixtures runs the merge and returns the parts of the resulting package.
func applyFixtures(t *testing.T) map[string][]byte {
	t.Helper()
	tpl := loadFixtureTemplate(t)
	result, err := tpl.Apply(buildZip(t, generatedParts()))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	parts, _, err := readZip(result)
	if err != nil {
		t.Fatalf("result is not a readable package: %v", err)
	}
	return parts
}

func TestApplyKeepsTemplatePresentation(t *testing.T) {
	parts := applyFixtures(t)

	if got := string(parts[partStyles]); got != templateStyles {
		t.Error("styles.xml must be the template's, untouched")
	}
	if !strings.Contains(string(parts["word/header1.xml"]), "ACME CORPORATION") {
		t.Error("the template header is missing from the result")
	}
	if string(parts["word/media/logo.jpeg"]) != "template-logo-bytes" {
		t.Error("the template logo is missing from the result")
	}
	if _, ok := parts["word/theme/theme1.xml"]; !ok {
		t.Error("the template theme is missing from the result")
	}

	doc := string(parts[partDocument])
	if !strings.Contains(doc, `<w:headerReference w:type="default" r:id="rId4"/>`) {
		t.Error("the template header reference is missing from the section properties")
	}
	if !strings.Contains(doc, `<w:pgSz w:w="11906" w:h="16838"/>`) {
		t.Errorf("page setup must come from the template (A4), got:\n%s", doc)
	}
	if strings.Contains(doc, `w:w="12240"`) {
		t.Error("the converter's own page setup must be dropped")
	}
	if strings.Contains(doc, "placeholder body") {
		t.Error("the template's sample body must not appear in the result")
	}
}

func TestApplyKeepsGeneratedContent(t *testing.T) {
	parts := applyFixtures(t)
	doc := string(parts[partDocument])

	for _, want := range []string{"1 Product documentation", "Body paragraph", "First bullet", "Confluence", "cell"} {
		if !strings.Contains(doc, want) {
			t.Errorf("content %q is missing from the result", want)
		}
	}
	if string(parts["word/media/export-image1.png"]) != "generated-image-bytes" {
		t.Error("the exported image is missing or was not renamed out of the template's namespace")
	}
	if title := string(parts["docProps/core.xml"]); !strings.Contains(title, "Product documentation") {
		t.Error("document metadata must carry the export title, not the template's")
	}
}

func TestApplyRemapsStylesToTemplate(t *testing.T) {
	doc := string(applyFixtures(t)[partDocument])

	if !strings.Contains(doc, `<w:pStyle w:val="Heading1"/>`) {
		t.Error("a style the template defines must be kept")
	}
	for _, unknown := range []string{"BodyText", "ListParagraph", "StrongEmphasis", "TableGrid2"} {
		if strings.Contains(doc, `"`+unknown+`"`) {
			t.Errorf("style %q is unknown to the template and must not be referenced", unknown)
		}
	}
	// Two paragraphs had an unknown paragraph style; both fall back to Normal.
	if n := strings.Count(doc, `<w:pStyle w:val="Normal"/>`); n != 2 {
		t.Errorf("got %d paragraphs remapped to Normal, want 2:\n%s", n, doc)
	}
}

func TestApplyRemapsImageAndHyperlinkRelationships(t *testing.T) {
	parts := applyFixtures(t)
	doc, rels := string(parts[partDocument]), string(parts[partDocumentRels])

	// rId3/rId4 in the generated document collide with the template's own
	// ids, so they must have been reallocated.
	if strings.Contains(doc, `r:embed="rId3"`) {
		t.Error("the image relationship id must be reallocated")
	}
	if !strings.Contains(doc, `r:id="rId4"`) {
		t.Error("the template's header reference (rId4) must survive in the section properties")
	}
	for _, rel := range parseRels(rels) {
		if rel.Type == relTypeHyperlink && rel.Target != "https://confluence.example.com/x/1" {
			t.Errorf("hyperlink target altered: %q", rel.Target)
		}
	}

	// Every relationship id referenced by the body must exist exactly once.
	declared := map[string]string{}
	for _, rel := range parseRels(rels) {
		if _, dup := declared[rel.ID]; dup {
			t.Fatalf("relationship id %s declared twice", rel.ID)
		}
		declared[rel.ID] = rel.Type
	}
	for _, m := range reRelID.FindAllStringSubmatch(doc, -1) {
		if _, ok := declared[m[2]]; !ok {
			t.Errorf("body references undeclared relationship %s", m[2])
		}
	}
	if !strings.Contains(rels, "media/export-image1.png") {
		t.Error("the image relationship must point at the renamed media part")
	}
}

func TestApplyMergesNumbering(t *testing.T) {
	parts := applyFixtures(t)
	numbering, doc := string(parts[partNumbering]), string(parts[partDocument])

	if !strings.Contains(numbering, "LO-BULLET") {
		t.Error("the generated list definition must be carried over")
	}
	if strings.Count(numbering, "<w:abstractNum ") != 3 || strings.Count(numbering, "<w:num ") != 3 {
		t.Errorf("template and generated definitions must both be present:\n%s", numbering)
	}
	// The template already uses abstractNumId 0-1 and numId 1-2, so the
	// generated ids are shifted by max+1 (2 -> 2+1, 1 -> 1+3).
	if !strings.Contains(numbering, `w:abstractNumId="2"`) || !strings.Contains(numbering, `<w:num w:numId="4"`) {
		t.Errorf("generated ids must be shifted past the template's:\n%s", numbering)
	}
	if !strings.Contains(doc, `<w:numId w:val="4"/>`) {
		t.Errorf("the body must reference the shifted numId:\n%s", doc)
	}
	// Schema order: every abstractNum comes before the first num.
	if strings.Index(numbering, "<w:num ") < strings.LastIndex(numbering, "<w:abstractNum ") {
		t.Error("abstractNum elements must precede num elements")
	}
}

func TestApplyFixesContentTypes(t *testing.T) {
	ct := string(applyFixtures(t)[partContentTypes])

	if strings.Contains(ct, ctTemplateMain) {
		t.Error("a .dotx template main part must become a document main part")
	}
	if !strings.Contains(ct, ctDocumentMain) {
		t.Error("the result must declare the document main part")
	}
	if !strings.Contains(ct, `<Default Extension="png"`) {
		t.Error("the extension of the copied image must be declared")
	}
	if strings.Count(ct, `Extension="jpeg"`) != 1 {
		t.Error("extensions already declared must not be duplicated")
	}
}

func TestApplyWithoutTemplateNumbering(t *testing.T) {
	parts := templateParts()
	delete(parts, partNumbering)
	tpl, err := parseTemplate(buildZip(t, parts))
	if err != nil {
		t.Fatal(err)
	}
	result, err := tpl.Apply(buildZip(t, generatedParts()))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := readZip(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out[partNumbering]), "LO-BULLET") {
		t.Error("the generated numbering must be used as-is when the template has none")
	}
	if !strings.Contains(string(out[partDocument]), `<w:numId w:val="1"/>`) {
		t.Error("numbering ids must be left alone when there is nothing to collide with")
	}
	if !strings.Contains(string(out[partContentTypes]), "/word/numbering.xml") {
		t.Error("the added numbering part must be declared in the content types")
	}
	if !strings.Contains(string(out[partDocumentRels]), "numbering.xml") {
		t.Error("the added numbering part must be related to the document")
	}
}

func TestTemplateMetadata(t *testing.T) {
	tpl := loadFixtureTemplate(t)
	if tpl.DefaultParagraphStyle() != "Normal" {
		t.Errorf("default paragraph style = %q", tpl.DefaultParagraphStyle())
	}
	if len(tpl.Styles()) != 4 {
		t.Errorf("styles = %v, want 4", tpl.Styles())
	}
}

func TestLoadTemplateRejectsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, content []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if _, err := LoadTemplate(filepath.Join(dir, "absent.docx")); err == nil {
		t.Error("a missing file must be reported")
	}
	if _, err := LoadTemplate(write("not-a-zip.docx", []byte("hello"))); !errors.Is(err, ErrInvalidTemplate) {
		t.Errorf("want ErrInvalidTemplate, got %v", err)
	}

	incomplete := templateParts()
	delete(incomplete, partStyles)
	if _, err := LoadTemplate(write("no-styles.docx", buildZip(t, incomplete))); !errors.Is(err, ErrInvalidTemplate) {
		t.Errorf("want ErrInvalidTemplate for a package without styles, got %v", err)
	}

	noSection := templateParts()
	noSection[partDocument] = []byte(`<?xml version="1.0"?><w:document><w:body><w:p/></w:body></w:document>`)
	_, err := LoadTemplate(write("no-sectpr.docx", buildZip(t, noSection)))
	if !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), "page setup") {
		t.Errorf("a template without page setup must be rejected, got %v", err)
	}
}

func TestApplyRejectsMalformedDocument(t *testing.T) {
	tpl := loadFixtureTemplate(t)
	if _, err := tpl.Apply([]byte("not a zip")); err == nil {
		t.Error("a non-package input must be reported")
	}
	broken := generatedParts()
	broken[partDocument] = []byte(`<w:document/>`)
	if _, err := tpl.Apply(buildZip(t, broken)); err == nil {
		t.Error("a document without a body must be reported")
	}
}

func TestApplyProducesReadablePackage(t *testing.T) {
	tpl := loadFixtureTemplate(t)
	result, err := tpl.Apply(buildZip(t, generatedParts()))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(result), int64(len(result)))
	if err != nil {
		t.Fatalf("result is not a valid zip: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		if seen[f.Name] {
			t.Errorf("part %s appears twice in the package", f.Name)
		}
		seen[f.Name] = true
	}
	for _, required := range []string{partContentTypes, partDocument, partDocumentRels, partStyles, "_rels/.rels"} {
		if !seen[required] {
			t.Errorf("part %s is missing from the result", required)
		}
	}
}

// TestApplyProducesWellFormedXML guards the string surgery in this package:
// every XML part of the result must parse, so an unbalanced tag or a broken
// attribute cannot reach Word.
func TestApplyProducesWellFormedXML(t *testing.T) {
	for name, content := range applyFixtures(t) {
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".rels") {
			continue
		}
		decoder := xml.NewDecoder(bytes.NewReader(content))
		for {
			_, err := decoder.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Errorf("%s is not well-formed XML: %v\n%s", name, err, content)
				break
			}
		}
	}
}

// TestApplyPreservesEscapedHyperlinkTargets guards against double escaping:
// Confluence URLs routinely carry query parameters separated by "&", which is
// stored escaped in the relationship part.
func TestApplyPreservesEscapedHyperlinkTargets(t *testing.T) {
	const rawURL = "https://confluence.example.com/pages/viewpage.action?pageId=1&spaceKey=DOC"

	parts := generatedParts()
	parts[partDocumentRels] = []byte(strings.Replace(generatedRels,
		`Target="https://confluence.example.com/x/1"`,
		`Target="https://confluence.example.com/pages/viewpage.action?pageId=1&amp;spaceKey=DOC"`, 1))

	tpl := loadFixtureTemplate(t)
	result, err := tpl.Apply(buildZip(t, parts))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := readZip(result)
	if err != nil {
		t.Fatal(err)
	}
	rels := string(out[partDocumentRels])
	if strings.Contains(rels, "&amp;amp;") {
		t.Errorf("hyperlink target was escaped twice:\n%s", rels)
	}
	var found bool
	for _, rel := range parseRels(rels) {
		if rel.Type == relTypeHyperlink {
			found = true
			if rel.Target != rawURL {
				t.Errorf("hyperlink target = %q, want %q", rel.Target, rawURL)
			}
		}
	}
	if !found {
		t.Error("the hyperlink relationship is missing from the result")
	}
}

func TestApplyFitsTablesToTheTemplatePage(t *testing.T) {
	// The template's page (A4, 1134 twip margins) leaves 9638 twips of text;
	// LibreOffice sized this table for its own, wider page.
	const oversized = `<w:tbl><w:tblPr><w:tblW w:w="10205" w:type="dxa"/><w:tblLayout w:type="fixed"/></w:tblPr>` +
		`<w:tblGrid><w:gridCol w:w="5000"/><w:gridCol w:w="5205"/></w:tblGrid>` +
		`<w:tr><w:tc><w:tcPr><w:tcW w:w="5000" w:type="dxa"/></w:tcPr><w:p><w:r><w:t>wide</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:tcPr><w:tcW w:w="5205" w:type="dxa"/></w:tcPr><w:p/></w:tc></w:tr></w:tbl>`

	parts := generatedParts()
	parts[partDocument] = []byte(strings.Replace(generatedDocument, "<w:tbl>", oversized+"<w:tbl>", 1))

	tpl := loadFixtureTemplate(t)
	if tpl.textWidth != 9638 {
		t.Fatalf("fixture template text width = %d, want 9638", tpl.textWidth)
	}
	result, err := tpl.Apply(buildZip(t, parts))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := readZip(result)
	if err != nil {
		t.Fatal(err)
	}

	doc := string(out[partDocument])
	total := 0
	for _, col := range reGridCol.FindAllString(doc, -1) {
		total += intAttr(col, reWidthAttr)
	}
	if total == 0 {
		t.Fatal("the table grid disappeared from the result")
	}
	if total > tpl.textWidth {
		t.Errorf("table is %d twips wide, the template page holds %d", total, tpl.textWidth)
	}
	if !strings.Contains(doc, "wide") {
		t.Error("the table content was lost")
	}
}
