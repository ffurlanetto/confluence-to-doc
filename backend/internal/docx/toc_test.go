package docx

import (
	"strings"
	"testing"
)

// contentsDocument mimics what the converter produces from the renderer's
// table of contents: paragraphs whose style name carries the level.
const contentsDocument = `<w:body>` +
	`<w:p><w:pPr><w:pStyle w:val="TextBodytoctitle"/></w:pPr><w:r><w:t>Table of contents</w:t></w:r></w:p>` +
	`<w:p><w:pPr><w:pStyle w:val="Entry1"/></w:pPr><w:hyperlink w:anchor="page-1"><w:r><w:t>1 Root</w:t></w:r></w:hyperlink></w:p>` +
	`<w:p><w:pPr><w:pStyle w:val="Entry2"/></w:pPr><w:hyperlink w:anchor="page-2"><w:r><w:t>1.1 Child</w:t></w:r></w:hyperlink></w:p>` +
	`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>1 Root</w:t></w:r></w:p>` +
	`</w:body>`

func contentsStyles() map[string]style {
	return map[string]style{
		"Entry1":           {id: "Entry1", kind: "paragraph", name: "Text Body.toc-entry-1"},
		"Entry2":           {id: "Entry2", kind: "paragraph", name: "Text Body.toc-entry-2"},
		"TextBodytoctitle": {id: "TextBodytoctitle", kind: "paragraph", name: "Text Body.toc-title"},
		"Heading1":         {id: "Heading1", kind: "paragraph", name: "Heading 1"},
	}
}

func TestInsertTOCWrapsTheEntriesInAField(t *testing.T) {
	got := insertTOC(contentsDocument, tocEntryStyles(contentsStyles()))

	for _, want := range []string{
		`<w:docPartGallery w:val="Table of Contents"/>`,
		`<w:fldChar w:fldCharType="begin" w:dirty="true"/>`,
		`<w:instrText xml:space="preserve"> TOC \o "1-9" \h \z \u </w:instrText>`,
		`<w:fldChar w:fldCharType="separate"/>`,
		`<w:fldChar w:fldCharType="end"/>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the field is incomplete, %q is missing:\n%s", want, got)
		}
	}
	// The entries stay written out, so the contents read correctly before any
	// update and in the PDF, where a field is never refreshed.
	for _, want := range []string{"1 Root", "1.1 Child", `w:anchor="page-1"`, `w:anchor="page-2"`} {
		if !strings.Contains(got, want) {
			t.Errorf("entry content %q was lost", want)
		}
	}
	// Only the entries: the heading above them and the document below are out.
	sdt := got[strings.Index(got, "<w:sdt>"):strings.Index(got, "</w:sdt>")]
	if strings.Contains(sdt, "Table of contents") {
		t.Error("the contents heading must stay outside the field")
	}
	if strings.Contains(sdt, `w:val="Heading1"`) {
		t.Error("the document body was swallowed by the field")
	}
	if strings.Count(got, "<w:sdt>") != 1 {
		t.Error("more than one field was inserted")
	}
}

func TestInsertTOCLeavesADocumentWithoutContentsAlone(t *testing.T) {
	// A single page has no table of contents.
	const plain = `<w:body><w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>1 Root</w:t></w:r></w:p></w:body>`
	if got := insertTOC(plain, tocEntryStyles(contentsStyles())); got != plain {
		t.Errorf("the document was modified:\n%s", got)
	}
	if got := insertTOC(contentsDocument, nil); got != contentsDocument {
		t.Error("a document whose styles say nothing about contents was modified")
	}
}

func TestPolishAddsTheFieldWithoutATemplate(t *testing.T) {
	parts := generatedParts()
	parts[partDocument] = []byte(strings.Replace(generatedDocument,
		"<w:body>", "<w:body>"+contentsEntries, 1))
	parts[partStyles] = []byte(strings.Replace(generatedStyles, "</w:styles>",
		`<w:style w:type="paragraph" w:styleId="Entry1"><w:name w:val="Text Body.toc-entry-1"/></w:style>`+
			`<w:style w:type="paragraph" w:styleId="Entry2"><w:name w:val="Text Body.toc-entry-2"/></w:style>`+
			`</w:styles>`, 1))

	out, err := Polish(buildZip(t, parts))
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := readZip(out)
	if err != nil {
		t.Fatalf("Polish produced an unreadable package: %v", err)
	}
	doc := string(result[partDocument])
	if !strings.Contains(doc, `<w:docPartGallery w:val="Table of Contents"/>`) {
		t.Errorf("no contents field without a template:\n%s", doc)
	}
	if !strings.Contains(doc, "Body paragraph") {
		t.Error("the document content was lost")
	}
}

const contentsEntries = `<w:p><w:pPr><w:pStyle w:val="Entry1"/></w:pPr><w:r><w:t>1 Root</w:t></w:r></w:p>` +
	`<w:p><w:pPr><w:pStyle w:val="Entry2"/></w:pPr><w:r><w:t>1.1 Child</w:t></w:r></w:p>`

func TestPolishLeavesAPackageItCannotImproveAlone(t *testing.T) {
	original := buildZip(t, generatedParts())
	out, err := Polish(original)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(original) {
		t.Error("a document with no contents was rewritten")
	}
}
