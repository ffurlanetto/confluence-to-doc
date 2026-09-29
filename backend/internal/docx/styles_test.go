package docx

import (
	"strings"
	"testing"
)

func TestNormaliseName(t *testing.T) {
	// Word matches style names on spelling and spacing, not capitalisation —
	// and the case does differ: LibreOffice writes "Heading 1" where Word
	// writes "heading 1".
	for _, tc := range []struct{ in, want string }{
		{"Heading 1", "heading 1"},
		{"heading 1", "heading 1"},
		{"  Body   Text ", "body text"},
		{"Text Body.toc-entry-2", "text body.toc-entry-2"},
		{"", ""},
	} {
		if got := normaliseName(tc.in); got != tc.want {
			t.Errorf("normaliseName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStyleNamesIncludeTheWordEquivalent(t *testing.T) {
	quote := style{id: "Quotations", kind: "paragraph", name: "Quotations"}
	if got := quote.names(); len(got) != 2 || got[0] != "quotations" || got[1] != "quote" {
		t.Errorf("names() = %v, want the style's own name then Word's", got)
	}
	plain := style{id: "TableHeading", kind: "paragraph", name: "Table Heading"}
	if got := plain.names(); len(got) != 1 || got[0] != "table heading" {
		t.Errorf("names() = %v, want just the style's own name", got)
	}
	if got := (style{}).names(); got != nil {
		t.Errorf("an unnamed style matches nothing, got %v", got)
	}
}

func TestEssenceKeepsMeaningAndDropsPresentation(t *testing.T) {
	heading := style{
		id: "TableHeading", kind: "paragraph", name: "Table Heading",
		xml: `<w:style w:type="paragraph" w:styleId="TableHeading"><w:name w:val="Table Heading"/>` +
			`<w:pPr><w:jc w:val="center"/><w:spacing w:before="120"/></w:pPr>` +
			`<w:rPr><w:b/><w:bCs/><w:rFonts w:ascii="Liberation Sans"/><w:sz w:val="28"/>` +
			`<w:color w:val="FF0000"/></w:rPr></w:style>`,
	}

	got := heading.essence("Normal")

	for _, want := range []string{`w:styleId="TableHeading"`, `<w:basedOn w:val="Normal"/>`,
		`<w:jc w:val="center"/>`, `<w:b/>`, `<w:bCs/>`} {
		if !strings.Contains(got, want) {
			t.Errorf("essence() dropped %q:\n%s", want, got)
		}
	}
	// The template decides the look, so none of this may survive.
	for _, unwanted := range []string{"Liberation Sans", `w:sz`, "FF0000", "w:spacing"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("essence() kept presentation %q:\n%s", unwanted, got)
		}
	}
	// A style that says nothing about the text is not worth defining.
	plain := style{id: "TableContents", kind: "paragraph", name: "Table Contents",
		xml: `<w:style w:styleId="TableContents"><w:rPr><w:sz w:val="20"/></w:rPr></w:style>`}
	if got := plain.essence("Normal"); got != "" {
		t.Errorf("essence() = %q, want nothing", got)
	}
}

func TestApplyMatchesStyleNamesWhateverTheirCase(t *testing.T) {
	// A template authored in another language keeps the built-in *names* but
	// not the ids: French Word writes styleId="Titre1" with name "heading 1".
	parts := templateParts()
	parts[partStyles] = []byte(`<?xml version="1.0"?><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
		`<w:style w:type="paragraph" w:styleId="Titre1"><w:name w:val="heading 1"/></w:style>` +
		`<w:style w:type="character" w:styleId="Lienhypertexte"><w:name w:val="Hyperlink"/></w:style>` +
		`</w:styles>`)

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
	doc := string(out[partDocument])

	if !strings.Contains(doc, `<w:pStyle w:val="Titre1"/>`) {
		t.Errorf("the heading did not find the template's own heading style:\n%s", doc)
	}
	if strings.Contains(doc, `<w:pStyle w:val="Heading1"/>`) {
		t.Error("the converter's style id survived although the template names one")
	}
	if !strings.Contains(doc, `<w:rStyle w:val="Lienhypertexte"/>`) {
		t.Error("the link did not find the template's own hyperlink style")
	}
}
