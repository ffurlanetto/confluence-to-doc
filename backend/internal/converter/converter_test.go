package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/docx"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/domain"
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
	if err := (LibreOffice{}).Convert(ctx, []byte(sample), domain.FormatPDF, &out); err != nil {
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
	if err := (LibreOffice{}).Convert(ctx, []byte(sample), domain.FormatDOCX, &out); err != nil {
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
	err := (LibreOffice{}).Convert(context.Background(), nil, domain.Format("odt"), &bytes.Buffer{})
	if !errors.Is(err, domain.ErrInvalidFormat) {
		t.Fatalf("want ErrInvalidFormat, got %v", err)
	}
}

func TestConvertReportsMissingBinary(t *testing.T) {
	err := (LibreOffice{Binary: "/nonexistent/soffice"}).Convert(context.Background(), []byte(sample), domain.FormatPDF, &bytes.Buffer{})
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
	conv := LibreOffice{Template: minimalTemplate(t)}
	if err := conv.Convert(ctx, []byte(sample), domain.FormatDOCX, &out); err != nil {
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
	conv := LibreOffice{Template: minimalTemplate(t)}
	// Succeeding here means LibreOffice reopened the templated DOCX, which is
	// the strongest available check that the produced package is well formed.
	if err := conv.Convert(ctx, []byte(sample), domain.FormatPDF, &out); err != nil {
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
	zr, err := zip.NewReader(bytes.NewReader(docxBytes), int64(len(docxBytes)))
	if err != nil {
		t.Fatalf("result is not a DOCX package: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
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
	t.Fatal("word/document.xml missing")
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
	if err := (LibreOffice{Template: minimalTemplate(t)}).Convert(ctx, wideSample(), domain.FormatDOCX, &out); err != nil {
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
