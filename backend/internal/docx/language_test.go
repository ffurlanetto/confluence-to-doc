package docx

import (
	"strings"
	"testing"
)

func TestWithDefaultLanguage(t *testing.T) {
	const open = `<w:styles xmlns:w="` + nsW + `">`
	cases := map[string]struct{ in, want string }{
		"replaces the converter's default": {
			open + `<w:docDefaults><w:rPrDefault><w:rPr><w:lang w:val="en-US" w:eastAsia="zh-CN" w:bidi="hi-IN"/></w:rPr></w:rPrDefault></w:docDefaults><w:style><w:rPr><w:lang w:val="de-DE"/></w:rPr></w:style></w:styles>`,
			`<w:lang w:val="fr-FR" w:eastAsia="zh-CN" w:bidi="hi-IN"/></w:rPr></w:rPrDefault></w:docDefaults><w:style><w:rPr><w:lang w:val="de-DE"/>`,
		},
		"adds it to existing run defaults": {
			open + `<w:docDefaults><w:rPrDefault><w:rPr><w:sz w:val="20"/></w:rPr></w:rPrDefault></w:docDefaults></w:styles>`,
			`<w:rPrDefault><w:rPr><w:lang w:val="fr-FR"/><w:sz w:val="20"/>`,
		},
		"creates run defaults": {
			open + `<w:docDefaults><w:pPrDefault/></w:docDefaults></w:styles>`,
			`<w:docDefaults><w:rPrDefault><w:rPr><w:lang w:val="fr-FR"/></w:rPr></w:rPrDefault><w:pPrDefault/>`,
		},
		"creates document defaults": {
			open + `<w:style/></w:styles>`,
			open + `<w:docDefaults><w:rPrDefault><w:rPr><w:lang w:val="fr-FR"/></w:rPr></w:rPrDefault></w:docDefaults><w:style/>`,
		},
	}
	for name, tc := range cases {
		got := withDefaultLanguage(tc.in, "fr-FR")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s:\n got %s\nwant %s", name, got, tc.want)
		}
		requireWellFormed(t, map[string][]byte{"styles.xml": []byte(got)})
	}
}

func TestSetLanguageLeavesDocumentAloneWhenUnset(t *testing.T) {
	doc := buildZip(t, bareDocument())
	out, err := SetLanguage(doc, "")
	if err != nil || string(out) != string(doc) {
		t.Fatalf("document changed: %v", err)
	}
}
