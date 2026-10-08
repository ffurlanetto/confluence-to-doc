package docx

import (
	"errors"
	"strings"
	"testing"
)

func TestParseTemplateAcceptsAPlainTemplate(t *testing.T) {
	tpl, err := ParseTemplate(`C:\Users\me\ACME template.dotx`, buildZip(t, templateParts()))
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	if tpl.Name() != "ACME template.dotx" {
		t.Errorf("Name() = %q, want the base name", tpl.Name())
	}
}

func TestParseTemplateRejectsWrongFiles(t *testing.T) {
	valid := buildZip(t, templateParts())
	for name, tc := range map[string]struct {
		file string
		data []byte
	}{
		"wrong extension": {"template.pdf", valid},
		"not a package":   {"template.docx", []byte("not a zip")},
		"too large":       {"template.docx", make([]byte, MaxTemplateSize+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTemplate(tc.file, tc.data); !errors.Is(err, ErrInvalidTemplate) {
				t.Errorf("err = %v, want ErrInvalidTemplate", err)
			}
		})
	}
}

func TestParseTemplateRejectsUnsafeContent(t *testing.T) {
	const w = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	for name, change := range map[string]func(map[string][]byte){
		"external image": func(p map[string][]byte) {
			p[partDocumentRels] = []byte(strings.Replace(templateRels, "</Relationships>",
				`<Relationship Id="rId99" Type="`+relTypeImage+`" Target="https://evil.example/x.png" TargetMode="External"/></Relationships>`, 1))
		},
		"attached template": func(p map[string][]byte) {
			p["word/_rels/settings.xml.rels"] = []byte(`<Relationships><Relationship Id="rId1" ` +
				`Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/attachedTemplate" ` +
				`Target="file://server/share/x.dotm" TargetMode="External"/></Relationships>`)
		},
		"macros": func(p map[string][]byte) { p["word/vbaProject.bin"] = []byte("x") },
		"macro-enabled type": func(p map[string][]byte) {
			p[partContentTypes] = []byte(strings.Replace(string(p[partContentTypes]), ctTemplateMain,
				"application/vnd.ms-word.template.macroEnabledTemplate.main+xml", 1))
		},
		"activex": func(p map[string][]byte) { p["word/activeX/activeX1.xml"] = []byte("<x/>") },
		"includepicture field split across runs": func(p map[string][]byte) {
			p["word/header1.xml"] = []byte(`<w:hdr ` + w + `><w:p><w:r><w:instrText xml:space="preserve"> INCLUDE</w:instrText></w:r>` +
				`<w:r><w:instrText>PICTURE "https://evil.example/x.png" \d </w:instrText></w:r></w:p></w:hdr>`)
		},
		"simple link field": func(p map[string][]byte) {
			p["word/footer1.xml"] = []byte(`<w:ftr ` + w + `><w:fldSimple w:instr=" LINK Excel.Sheet.8 &quot;\\\\server\\x.xls&quot; "/></w:ftr>`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			parts := templateParts()
			change(parts)
			if _, err := ParseTemplate("template.dotx", buildZip(t, parts)); !errors.Is(err, ErrUnsafeTemplate) {
				t.Errorf("err = %v, want ErrUnsafeTemplate", err)
			}
		})
	}
}

func TestParseTemplateAllowsHarmlessFieldsAndText(t *testing.T) {
	parts := templateParts()
	parts["word/footer1.xml"] = []byte(`<w:ftr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:p><w:r><w:t>Link to the intranet</w:t></w:r><w:r><w:instrText> PAGE </w:instrText></w:r>` +
		`<w:fldSimple w:instr=" NUMPAGES "/></w:p></w:ftr>`)
	parts[partDocumentRels] = []byte(strings.Replace(templateRels, "</Relationships>",
		`<Relationship Id="rId98" Type="`+relTypeHyperlink+`" Target="https://intranet.example" TargetMode="External"/></Relationships>`, 1))
	if _, err := ParseTemplate("template.dotx", buildZip(t, parts)); err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
}
