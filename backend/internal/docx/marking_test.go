package docx

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
)

var testMarking = Marking{
	Footer:    `Exported by Ann O'Neil <ann@example.com> on 2026-10-08 09:14 UTC · R&D · Ref. 01a1`,
	Watermark: `CONFIDENTIAL & "SECRET"`,
}

// bareDocument mimics LibreOffice's own DOCX: no header, no footer, the
// footer glued to the edge, and a landscape section closed inside a paragraph.
func bareDocument() map[string][]byte {
	const doc = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>
<w:p><w:r><w:t>Hello</w:t></w:r></w:p>
<w:p><w:pPr><w:sectPr><w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/><w:pgMar w:top="567" w:bottom="567" w:footer="0"/></w:sectPr></w:pPr></w:p>
<w:p><w:r><w:t>World</w:t></w:r></w:p>
<w:sectPr><w:type w:val="nextPage"/><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:left="1134" w:right="567" w:top="567" w:footer="0" w:bottom="567"/></w:sectPr>
</w:body></w:document>`
	return map[string][]byte{
		partContentTypes: []byte(`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="` + ctDocumentMain + `"/></Types>`),
		"_rels/.rels":    []byte(packageRels),
		partDocument:     []byte(doc),
		partDocumentRels: []byte(`<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`),
		partStyles:       []byte(`<?xml version="1.0"?><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"/>`),
	}
}

func mark(t *testing.T, parts map[string][]byte, m Marking) map[string][]byte {
	t.Helper()
	out, err := Mark(buildZip(t, parts), m)
	if err != nil {
		t.Fatalf("Mark: %v", err)
	}
	result, _, err := readZip(out)
	if err != nil {
		t.Fatalf("result is not a readable package: %v", err)
	}
	return result
}

func requireWellFormed(t *testing.T, parts map[string][]byte) {
	t.Helper()
	for name, content := range parts {
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
				t.Fatalf("%s is not well-formed XML: %v\n%s", name, err, content)
			}
		}
	}
}

func TestMarkGivesBareSectionsAHeaderAndFooter(t *testing.T) {
	parts := mark(t, bareDocument(), testMarking)
	requireWellFormed(t, parts)

	footer, header := string(parts["word/footer-marking1.xml"]), string(parts["word/header-marking2.xml"])
	if !strings.Contains(footer, `Exported by Ann O'Neil &lt;ann@example.com&gt; on 2026-10-08 09:14 UTC · R&amp;D · Ref. 01a1`) {
		t.Errorf("footer line missing or not escaped:\n%s", footer)
	}
	if !strings.Contains(header, `string="CONFIDENTIAL &amp; &quot;SECRET&quot;"`) || !strings.Contains(header, `id="PowerPlusWaterMarkObject"`) {
		t.Errorf("watermark missing or not escaped:\n%s", header)
	}

	doc := string(parts[partDocument])
	// Both sections, the landscape one included, point at the shared parts.
	for _, ref := range []string{`<w:footerReference w:type="default" r:id="`, `<w:headerReference w:type="default" r:id="`} {
		if n := strings.Count(doc, ref); n != 2 {
			t.Errorf("%s appears %d times, want once per section", ref, n)
		}
	}
	if strings.Contains(doc, `w:footer="0"`) || strings.Count(doc, `w:footer="340"`) != 2 {
		t.Errorf("footer distance not moved off the edge:\n%s", doc)
	}
	if !regexp.MustCompile(`<w:sectPr><w:headerReference[^>]*/><w:footerReference[^>]*/><w:type `).MatchString(doc) {
		t.Errorf("references must come first in the section properties:\n%s", doc)
	}

	ct, rels := string(parts[partContentTypes]), string(parts[partDocumentRels])
	for _, want := range []string{`PartName="/word/footer-marking1.xml" ContentType="` + ctFooter, `PartName="/word/header-marking2.xml" ContentType="` + ctHeader} {
		if !strings.Contains(ct, want) {
			t.Errorf("content type missing: %s", want)
		}
	}
	if !strings.Contains(rels, `Target="footer-marking1.xml"`) || !strings.Contains(rels, `Target="header-marking2.xml"`) {
		t.Errorf("relationships missing:\n%s", rels)
	}
}

// templatedDocument is what Template.Apply produces: the company's header and
// footer, a different first page, and a header root without VML namespaces.
func templatedDocument() map[string][]byte {
	parts := bareDocument()
	parts[partDocument] = []byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>` +
		`<w:p><w:r><w:t>Body</w:t></w:r></w:p>` +
		`<w:sectPr><w:headerReference w:type="default" r:id="rId4"/><w:footerReference w:type="default" r:id="rId5"/>` +
		`<w:headerReference w:type="first" r:id="rId6"/><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1417" w:bottom="1134" w:footer="708"/><w:titlePg/></w:sectPr>` +
		`</w:body></w:document>`)
	parts[partDocumentRels] = []byte(`<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId4" Type="` + relTypeHeader + `" Target="header1.xml"/>` +
		`<Relationship Id="rId5" Type="` + relTypeFooter + `" Target="footer1.xml"/>` +
		`<Relationship Id="rId6" Type="` + relTypeHeader + `" Target="header2.xml"/></Relationships>`)
	parts["word/header1.xml"] = []byte(`<?xml version="1.0"?><w:hdr xmlns:w="` + nsW + `"><w:p><w:r><w:t>ACME</w:t></w:r></w:p></w:hdr>`)
	parts["word/header2.xml"] = []byte(`<?xml version="1.0"?><w:hdr xmlns:w="` + nsW + `" xmlns:v="` + nsV + `"><w:p><w:r><w:t>ACME cover</w:t></w:r></w:p></w:hdr>`)
	parts["word/footer1.xml"] = []byte(`<?xml version="1.0"?><w:ftr xmlns:w="` + nsW + `"><w:p><w:r><w:t>Page</w:t></w:r></w:p></w:ftr>`)
	return parts
}

func TestMarkExtendsTheTemplateHeadersAndFooters(t *testing.T) {
	parts := mark(t, templatedDocument(), testMarking)
	requireWellFormed(t, parts)

	header1, header2 := string(parts["word/header1.xml"]), string(parts["word/header2.xml"])
	for name, h := range map[string]string{"default": header1, "first": header2} {
		if !strings.Contains(h, "ACME") || !strings.Contains(h, "PowerPlusWaterMarkObject") {
			t.Errorf("%s header must keep the company content and gain the watermark:\n%s", name, h)
		}
		if strings.Count(h, `xmlns:v=`) != 1 || !strings.Contains(h, `xmlns:o=`) || !strings.Contains(h, `xmlns:w10=`) {
			t.Errorf("%s header namespaces:\n%s", name, h)
		}
	}
	if footer := string(parts["word/footer1.xml"]); !strings.Contains(footer, "Page") || !strings.Contains(footer, "Exported by") {
		t.Errorf("company footer must keep its content and gain the line:\n%s", footer)
	}

	// The first page has no footer of its own: it gets ours, or it would be
	// the one page without the line.
	doc := string(parts[partDocument])
	if !strings.Contains(doc, `<w:footerReference w:type="first" r:id="`) {
		t.Errorf("first-page footer missing:\n%s", doc)
	}
	if strings.Contains(doc, `<w:headerReference w:type="default" r:id="rId`+"7") || strings.Count(doc, "headerReference") != 2 {
		t.Errorf("headers that exist must not be replaced:\n%s", doc)
	}
	if !strings.Contains(doc, `w:footer="708"`) {
		t.Error("the template's footer distance must be kept")
	}
	if _, ok := parts["word/header-marking1.xml"]; ok {
		t.Error("no header part should be created when every section has one")
	}
}

func TestMarkHonoursEvenAndOddPages(t *testing.T) {
	parts := bareDocument()
	parts[partSettings] = []byte(`<?xml version="1.0"?><w:settings xmlns:w="` + nsW + `"><w:evenAndOddHeaders/></w:settings>`)
	doc := string(mark(t, parts, Marking{Footer: "x"})[partDocument])
	if strings.Count(doc, `w:type="even"`) != 2 {
		t.Errorf("even-page footers missing:\n%s", doc)
	}
}

func TestMarkWithNothingToStampLeavesTheDocumentAlone(t *testing.T) {
	in := buildZip(t, bareDocument())
	out, err := Mark(in, Marking{})
	if err != nil || !bytes.Equal(in, out) {
		t.Fatalf("document changed (err %v)", err)
	}
}

func TestMarkFooterOnly(t *testing.T) {
	parts := mark(t, bareDocument(), Marking{Footer: "line"})
	if strings.Contains(string(parts[partDocument]), "headerReference") {
		t.Error("no watermark asked, no header expected")
	}
}

func TestMarkRejectsDanglingReferences(t *testing.T) {
	parts := templatedDocument()
	delete(parts, "word/footer1.xml")
	if _, err := Mark(buildZip(t, parts), testMarking); !errors.Is(err, ErrApply) {
		t.Fatalf("want ErrApply, got %v", err)
	}
}
