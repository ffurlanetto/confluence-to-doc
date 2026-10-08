package converter

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/exporter"
)

// sample keeps an accented word on purpose: it exercises UTF-8 round-tripping
// through LibreOffice.
const sample = `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body>
<h1>1 Résumé</h1><p>Hello</p><h2 style="page-break-before: always">1.1 Child</h2><p>World</p></body></html>`

func requireSoffice(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("soffice"); err != nil {
		t.Skip("soffice not installed")
	}
	if testing.Short() {
		t.Skip("skipping LibreOffice conversion in -short mode")
	}
}

func TestConvertPDF(t *testing.T) {
	requireSoffice(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := (LibreOffice{}).Convert(ctx, []byte(sample), domain.FormatPDF, nil, docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("%PDF-")) {
		t.Fatalf("output is not a PDF: %q", out.Bytes()[:min(16, out.Len())])
	}
}

func TestConvertDOCX(t *testing.T) {
	requireSoffice(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := (LibreOffice{}).Convert(ctx, []byte(sample), domain.FormatDOCX, nil, docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("output is not a zip/docx: %v", err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			found = true
		}
	}
	if !found {
		t.Fatal("word/document.xml missing")
	}
}

func TestConvertRejectsUnknownFormat(t *testing.T) {
	err := (LibreOffice{}).Convert(context.Background(), nil, domain.Format("odt"), nil, docx.Marking{}, &bytes.Buffer{})
	if !errors.Is(err, domain.ErrInvalidFormat) {
		t.Fatalf("want ErrInvalidFormat, got %v", err)
	}
}

func TestConvertReportsMissingBinary(t *testing.T) {
	err := (LibreOffice{Binary: "/nonexistent/soffice"}).Convert(context.Background(), []byte(sample), domain.FormatPDF, nil, docx.Marking{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error")
	}
}

// --- Company Word template ---------------------------------------------------
//
// These tests run the real pipeline: LibreOffice turns the HTML into a DOCX,
// the template is applied, and — for PDF — LibreOffice reopens the result.
// That reopening is itself the strongest check that the produced package is
// valid, since soffice refuses a corrupt document.

const companyFont = "Corporate Sans"

// minimalTemplate builds a small but valid .docx template: A4 page setup, a
// header, a default font and a Heading1 style.
func minimalTemplate(t *testing.T) *docx.Template {
	t.Helper()
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
			`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
			`<Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/>` +
			`</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
			`</Relationships>`,
		"word/_rels/document.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/>` +
			`</Relationships>`,
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document ` + ns + `><w:body>` +
			`<w:p><w:r><w:t>template placeholder</w:t></w:r></w:p>` +
			`<w:sectPr><w:headerReference w:type="default" r:id="rId2"/>` +
			`<w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1417" w:right="1134" w:bottom="1134" w:left="1134" w:header="708" w:footer="708" w:gutter="0"/>` +
			`</w:sectPr></w:body></w:document>`,
		"word/styles.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:styles ` + ns + `>` +
			`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="` + companyFont + `" w:hAnsi="` + companyFont + `"/><w:sz w:val="20"/></w:rPr></w:rPrDefault></w:docDefaults>` +
			`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
			`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:rPr><w:b/><w:sz w:val="36"/></w:rPr></w:style>` +
			`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:rPr><w:b/><w:sz w:val="28"/></w:rPr></w:style>` +
			// Word calls it Hyperlink, LibreOffice InternetLink; only the name matches.
			`<w:style w:type="character" w:styleId="Hyperlink"><w:name w:val="Hyperlink"/><w:rPr><w:color w:val="C00000" w:themeColor="accent1"/><w:u w:val="single"/></w:rPr></w:style>` +
			`</w:styles>`,
		"word/header1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:hdr ` + ns + `>` +
			`<w:p><w:r><w:t>ACME CORPORATION — INTERNAL</w:t></w:r></w:p></w:hdr>`,
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "company-template.docx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	tpl, err := docx.LoadTemplate(path)
	if err != nil {
		t.Fatalf("loading the template: %v", err)
	}
	return tpl
}

func TestConvertDOCXWithCompanyTemplate(t *testing.T) {
	requireSoffice(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var out bytes.Buffer
	conv, tpl := LibreOffice{}, minimalTemplate(t)
	if err := conv.Convert(ctx, []byte(sample), domain.FormatDOCX, tpl, docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("result is not a DOCX package: %v", err)
	}
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[f.Name] = string(content)
	}

	if !strings.Contains(parts["word/header1.xml"], "ACME CORPORATION") {
		t.Error("the company header is missing from the generated document")
	}
	if !strings.Contains(parts["word/styles.xml"], companyFont) {
		t.Error("the company font is missing from the generated document")
	}
	if !strings.Contains(parts["word/document.xml"], `<w:pgSz w:w="11906" w:h="16838"/>`) {
		t.Error("the company page setup (A4) is missing from the generated document")
	}
	if !strings.Contains(parts["word/document.xml"], "Hello") {
		t.Error("the exported content is missing from the generated document")
	}
	if strings.Contains(parts["word/document.xml"], "template placeholder") {
		t.Error("the template's own body leaked into the generated document")
	}
	// Every style the body refers to must exist in the company template.
	for _, m := range regexp.MustCompile(`<w:pStyle w:val="([^"]+)"/>`).FindAllStringSubmatch(parts["word/document.xml"], -1) {
		if !strings.Contains(parts["word/styles.xml"], `w:styleId="`+m[1]+`"`) {
			t.Errorf("body uses style %q which the template does not define", m[1])
		}
	}
}

func TestConvertPDFWithCompanyTemplate(t *testing.T) {
	requireSoffice(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var out bytes.Buffer
	conv, tpl := LibreOffice{}, minimalTemplate(t)
	// Succeeding here means LibreOffice reopened the templated DOCX, which is
	// the strongest available check that the produced package is well formed.
	if err := conv.Convert(ctx, []byte(sample), domain.FormatPDF, tpl, docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("%PDF-")) {
		t.Fatalf("output is not a PDF: %q", out.Bytes()[:min(16, out.Len())])
	}
}

// wideSample mirrors what exporter.RenderHTML produces: a table LibreOffice
// sizes for its own, wider page, and blocks carrying the inline
// `page-break-inside: avoid` that the renderer adds to every paragraph.
func wideSample() []byte {
	var heads, cells strings.Builder
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&heads, "<th>Heading number %d of the table</th>", i)
		fmt.Fprintf(&cells, "<td>Cell %d with a reasonably long sentence that must wrap</td>", i)
	}
	return []byte(`<!DOCTYPE html><html><head><meta charset="utf-8"></head><body>` +
		`<p style="page-break-inside: avoid">Before.</p>` +
		`<table border="1" cellpadding="4"><tbody><tr>` + heads.String() + `</tr><tr>` + cells.String() + `</tr></tbody></table>` +
		`<p style="page-break-inside: avoid">After.</p></body></html>`)
}

func documentXML(t *testing.T, docxBytes []byte) string {
	t.Helper()
	return partOf(t, docxBytes, "word/document.xml")
}

func partOf(t *testing.T, docxBytes []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(docxBytes), int64(len(docxBytes)))
	if err != nil {
		t.Fatalf("result is not a DOCX package: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		content, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	t.Fatalf("%s missing", name)
	return ""
}

// TestConvertFitsTablesToTheTemplatePage is the end-to-end check for the two
// layout rules: a table has to stay inside the printable width of the company
// page, and a paragraph must not be split across two pages. Both depend on how
// this LibreOffice version maps our HTML, which only a real conversion shows.
func TestConvertFitsTablesToTheTemplatePage(t *testing.T) {
	requireSoffice(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var out bytes.Buffer
	if err := (LibreOffice{}).Convert(ctx, wideSample(), domain.FormatDOCX, minimalTemplate(t), docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	doc := documentXML(t, out.Bytes())

	// The template's page: A4 less 1134 twips of margin on each side.
	const printable = 11906 - 1134 - 1134
	total := 0
	columns := regexp.MustCompile(`<w:gridCol w:w="(\d+)"/>`).FindAllStringSubmatch(doc, -1)
	if len(columns) == 0 {
		t.Fatal("the converted document has no table grid")
	}
	for _, m := range columns {
		w, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparsable column width %q", m[1])
		}
		total += w
	}
	if total > printable {
		t.Errorf("table is %d twips wide, the company page holds %d", total, printable)
	}

	// `page-break-inside: avoid` has to survive as direct formatting: the
	// template replaced the converter's styles with its own.
	if !strings.Contains(doc, "<w:keepLines/>") {
		t.Error("paragraphs are not kept whole: no w:keepLines in the templated document")
	}
}

// TestConvertKeepsInlineFormatting covers what happens between LibreOffice's
// style ids and Word's: a link has to pick up the company's link style, and
// bold has to survive even though the template names no style for it.
func TestConvertKeepsInlineFormatting(t *testing.T) {
	requireSoffice(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const html = `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body>` +
		`<p>A <strong>bold</strong> word, an <em>emphasised</em> one, and a ` +
		`<a href="https://confluence.example.com/x/1">link</a>.</p></body></html>`

	var out bytes.Buffer
	if err := (LibreOffice{}).Convert(ctx, []byte(html), domain.FormatDOCX, minimalTemplate(t), docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	doc := documentXML(t, out.Bytes())

	if !strings.Contains(doc, `<w:rStyle w:val="Hyperlink"/>`) {
		t.Errorf("the link does not use the company Hyperlink style:\n%s", doc)
	}
	if strings.Contains(doc, "InternetLink") {
		t.Error("the converter's own style id leaked into the result")
	}

	// Bold and italics come from character styles the template does not name;
	// their definitions must have come along, or the words lose their look.
	styles := partOf(t, out.Bytes(), "word/styles.xml")
	for _, want := range []string{"bold", "emphasised"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("%q is missing from the document", want)
		}
	}
	for _, m := range regexp.MustCompile(`<w:rStyle w:val="([^"]+)"/>`).FindAllStringSubmatch(doc, -1) {
		if !strings.Contains(styles, `w:styleId="`+m[1]+`"`) {
			t.Errorf("run style %q is referenced but not defined", m[1])
		}
	}
	if !strings.Contains(styles, "<w:b/>") {
		t.Errorf("no bold left anywhere in the styles:\n%s", styles)
	}
}

// TestConvertProducesAStructuredDocument runs the renderer's own output through
// the whole pipeline: the mappings it relies on are LibreOffice's, and only a
// real conversion shows whether this version still honours them.
func TestConvertProducesAStructuredDocument(t *testing.T) {
	requireSoffice(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	html, err := exporter.RenderHTML(ctx, twoLevelTree(), exporter.RenderOptions{
		Title: "Product documentation", GeneratedAt: time.Now(), UseTemplateStyles: true,
		Author: "Jane Doe", Description: "Exported from Confluence", Classification: "Internal",
		Keywords:   []string{"Confluence", "export"},
		Properties: []exporter.Property{{Name: "Confluence page ID", Value: "1"}},
	}, noAssets{})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := (LibreOffice{}).Convert(ctx, html, domain.FormatDOCX, minimalTemplate(t), docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	doc := documentXML(t, out.Bytes())

	// Page titles are headings, at the level of their depth in the tree.
	for _, want := range []string{`<w:pStyle w:val="Heading1"/>`, `<w:pStyle w:val="Heading2"/>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page titles are not headings, %q is missing", want)
		}
	}
	// The contents are a real field, with the entries written out.
	if !strings.Contains(doc, `<w:docPartGallery w:val="Table of Contents"/>`) {
		t.Errorf("the table of contents is not a field:\n%s", doc)
	}
	if !strings.Contains(doc, `w:anchor="page-2"`) {
		t.Error("the contents entries do not link to the pages")
	}
	// The properties reached the package.
	core := partOf(t, out.Bytes(), "docProps/core.xml")
	for _, want := range []string{"Product documentation", "Jane Doe", "Internal"} {
		if !strings.Contains(core, want) {
			t.Errorf("document property %q is missing:\n%s", want, core)
		}
	}
	if custom := partOf(t, out.Bytes(), "docProps/custom.xml"); !strings.Contains(custom, "Confluence page ID") {
		t.Errorf("the custom properties are missing:\n%s", custom)
	}
}

type noAssets struct{}

func (noAssets) ResolveURL(raw string) (string, error) { return raw, nil }
func (noAssets) FetchImage(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("no images in this test")
}

func twoLevelTree() *exporter.Node {
	page := func(id, title, body string) confluence.Page {
		return confluence.Page{
			PageSummary: confluence.PageSummary{ID: id, Title: title}, BodyHTML: body,
		}
	}
	return &exporter.Node{
		Page: page("1", "Root page", "<p>Root body.</p>"), Number: "1",
		Children: []*exporter.Node{{
			Page: page("2", "Child page", "<p>Child body.</p>"), Depth: 1, Number: "1.1",
		}},
	}
}

// TestConvertFitsAWideConfluenceTable uses the shape that showed the problem in
// practice: nine columns, one of them a bullet list. LibreOffice sizes such a
// table half as wide again as its own page, so this is the case where the
// widths have to be laid out again rather than merely scaled.
func TestConvertFitsAWideConfluenceTable(t *testing.T) {
	requireSoffice(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"></head><body>` +
		`<table border="1" cellpadding="4"><tbody><tr>`)
	for _, head := range []string{"Environment", "Service Name", "Type", "FQDN / Server Name",
		"Data Centre", "IP", "Details", "Additional Setup", "IV2 Link"} {
		fmt.Fprintf(&b, "<th>%s</th>", head)
	}
	b.WriteString(`</tr><tr><td>AGASSI-PRD-TOMCAT-1</td><td>SVC2AUXAGASSIP</td><td>Red Hat 8.8 (VMware)</td>` +
		`<td>eurvlii74826.xmp.net.intra</td><td>ME</td><td>10.118.170.65</td>` +
		`<td><ul><li>cpu: 16 vCPU</li><li>ram: 64 GB</li><li>data_disk_size: 150 Go</li></ul></td>` +
		`<td></td><td>AGASSI-PRD-REDHAT-1 (group.echonet)</td></tr></tbody></table></body></html>`)

	var out bytes.Buffer
	if err := (LibreOffice{}).Convert(ctx, []byte(b.String()), domain.FormatDOCX, minimalTemplate(t), docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	doc := documentXML(t, out.Bytes())

	// A4 turned sideways, less the template's margins.
	const printable = 16838 - 1134 - 1134
	columns := regexp.MustCompile(`<w:gridCol w:w="(\d+)"/>`).FindAllStringSubmatch(doc, -1)
	if len(columns) != 9 {
		t.Fatalf("got %d columns, want 9", len(columns))
	}
	total, narrowest := 0, 1<<30
	for _, m := range columns {
		w, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparsable column width %q", m[1])
		}
		total += w
		narrowest = min(narrowest, w)
	}
	if total > printable {
		t.Errorf("the table is %d twips wide, the company page holds %d", total, printable)
	}
	// Every column has to stay wide enough to read: scaling this table down
	// proportionally would leave the narrowest at about 490 twips, roughly two
	// characters, and set its heading one letter per line.
	if narrowest < 700 {
		t.Errorf("the narrowest column is %d twips, too narrow to read", narrowest)
	}
	// Nine columns do not belong on a portrait page: the table gets a
	// landscape section of its own, and the document returns to portrait.
	if !strings.Contains(doc, `w:orient="landscape"`) {
		t.Errorf("the table was left on a portrait page:\n%s", doc)
	}
	if n := strings.Count(doc, "<w:sectPr>"); n != 3 {
		t.Errorf("got %d sections, want portrait/landscape/portrait", n)
	}

	// A bullet in a narrow cell must not be indented past its own text.
	for _, m := range regexp.MustCompile(`<w:ind w:left="(\d+)"`).FindAllStringSubmatch(doc, -1) {
		left, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		if left >= narrowest {
			t.Errorf("an indent of %d twips does not fit a %d twip column", left, narrowest)
		}
	}
}

// TestMarkingReachesBothFormats runs the real conversion: the footer line and
// the watermark must survive LibreOffice, with and without a company template.
func TestMarkingReachesBothFormats(t *testing.T) {
	requireSoffice(t)
	m := docx.Marking{Footer: "Exported by Ann on 2026-10-08 09:14 UTC · Confidential · Ref. 0199-test", Watermark: "CONFIDENTIAL"}
	for name, tpl := range map[string]*docx.Template{"built-in styling": nil, "company template": minimalTemplate(t)} {
		conv := LibreOffice{}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			var docxOut bytes.Buffer
			if err := conv.Convert(ctx, []byte(sample), domain.FormatDOCX, tpl, m, &docxOut); err != nil {
				t.Fatal(err)
			}
			zr, err := zip.NewReader(bytes.NewReader(docxOut.Bytes()), int64(docxOut.Len()))
			if err != nil {
				t.Fatal(err)
			}
			var footers, headers string
			for _, f := range zr.File {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				switch {
				case strings.HasPrefix(f.Name, "word/footer"):
					footers += string(b)
				case strings.HasPrefix(f.Name, "word/header"):
					headers += string(b)
				}
			}
			if !strings.Contains(footers, "Ref. 0199-test") || !strings.Contains(headers, `string="CONFIDENTIAL"`) {
				t.Fatalf("DOCX not marked:\nfooters: %s\nheaders: %s", footers, headers)
			}

			var pdf bytes.Buffer
			if err := conv.Convert(ctx, []byte(sample), domain.FormatPDF, tpl, m, &pdf); err != nil {
				t.Fatal(err)
			}
			if _, err := exec.LookPath("pdftotext"); err != nil {
				t.Skip("pdftotext not installed: PDF text not checked")
			}
			path := filepath.Join(t.TempDir(), "out.pdf")
			if err := os.WriteFile(path, pdf.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			text, err := exec.Command("pdftotext", path, "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			// Two pages in the sample: the line is on each of them.
			if n := strings.Count(string(text), "Ref. 0199-test"); n != 2 {
				t.Fatalf("footer line found %d times in the PDF, want once per page:\n%s", n, text)
			}
		})
	}
}

// TestLibreOfficeReachesNoNetwork feeds LibreOffice HTML the renderer would
// never produce — remote stylesheet, image, background — and checks that no
// request leaves the conversion: the second line of defence must hold on its
// own, even if the HTML sanitisation missed something.
func TestLibreOfficeReachesNoNetwork(t *testing.T) {
	requireSoffice(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		t.Logf("LibreOffice requested %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	html := `<!DOCTYPE html><html><head><meta charset="utf-8"><link rel="stylesheet" href="` + srv.URL + `/style.css"></head><body>` +
		`<p>remote</p><img src="` + srv.URL + `/image.png">` +
		`<table background="` + srv.URL + `/table.png"><tr><td>cell</td></tr></table></body></html>`

	// The server process's own proxy settings must not leak into LibreOffice.
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("no_proxy", "*")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	if err := (LibreOffice{}).Convert(ctx, []byte(html), domain.FormatPDF, nil, docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("LibreOffice made %d request(s) during the conversion", n)
	}
}

func TestIsolatedEnv(t *testing.T) {
	env := isolatedEnv([]string{"PATH=/usr/bin", "HTTPS_PROXY=http://corp:3128", "no_proxy=.corp", "HOME=/root", "LANG=C"}, "/work")
	got := strings.Join(env, " ")
	for _, want := range []string{"PATH=/usr/bin", "LANG=C", "HTTPS_PROXY=" + blackhole, "https_proxy=" + blackhole, "http_proxy=" + blackhole, "no_proxy= ", "HOME=/work"} {
		if !strings.Contains(got+" ", want) {
			t.Errorf("env lacks %q: %s", want, got)
		}
	}
	for _, leaked := range []string{"corp", "HOME=/root"} {
		if strings.Contains(got, leaked) {
			t.Errorf("env leaks %q: %s", leaked, got)
		}
	}
}

// pdfContent returns the PDF with every Flate stream inflated, so that the
// structure and metadata LibreOffice compresses can be searched.
func pdfContent(t *testing.T, pdf []byte) string {
	t.Helper()
	out := string(pdf)
	re := regexp.MustCompile(`(?s)stream\r?\n(.*?)endstream`)
	for _, m := range re.FindAllSubmatch(pdf, -1) {
		r, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(r)
		out += string(b)
	}
	return out
}

// TestPDFIsTaggedForAccessibility checks the PDF carries its structure (for
// screen readers), declares PDF/UA, and states the configured language.
func TestPDFIsTaggedForAccessibility(t *testing.T) {
	requireSoffice(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	conv := LibreOffice{Language: "fr-FR"}
	if err := conv.Convert(ctx, []byte(sample), domain.FormatPDF, nil, docx.Marking{}, &out); err != nil {
		t.Fatal(err)
	}
	content := pdfContent(t, out.Bytes())
	for _, want := range []string{"/StructTreeRoot", "/MarkInfo", "/H1", "pdfuaid:part", "/Lang(fr-FR)"} {
		if !strings.Contains(strings.ReplaceAll(content, "/Lang (", "/Lang("), want) {
			t.Errorf("PDF lacks %s", want)
		}
	}
}
