package docx

import (
	"encoding/xml"
	"strings"
	"testing"
)

const portraitSection = `<w:sectPr><w:headerReference w:type="default" r:id="rId4"/>` +
	`<w:pgSz w:w="11906" w:h="16838"/>` +
	`<w:pgMar w:top="1418" w:right="1418" w:bottom="1418" w:left="1418"/></w:sectPr>`

func TestTurnSideways(t *testing.T) {
	got := turnSideways(portraitSection)

	if !strings.Contains(got, `<w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/>`) {
		t.Errorf("the page was not turned: %s", got)
	}
	if !strings.Contains(got, `r:id="rId4"`) {
		t.Error("the company header was lost when turning the page")
	}
	if !strings.Contains(got, `w:left="1418"`) {
		t.Error("the margins changed when turning the page")
	}
	// A4 landscape: 16838 less the two margins.
	if w := textWidthOf(got); w != 14002 {
		t.Errorf("landscape text width = %d, want 14002", w)
	}
	// A page that is already sideways, or that says nothing, stays as it is.
	if got := turnSideways(got); got != "" {
		t.Errorf("a landscape page was turned again: %s", got)
	}
	if got := turnSideways(`<w:sectPr><w:pgMar w:left="1418" w:right="1418"/></w:sectPr>`); got != "" {
		t.Errorf("a page with no size was turned: %s", got)
	}
}

func TestOutgrewThePortraitPage(t *testing.T) {
	// The nine-column inventory table: portrait flattens two columns onto the
	// floor, landscape rescues both.
	wide := []int{2870, 2330, 1100, 1475, 875, 1550, 1839, 1250, 2810}
	if !outgrew(wide, 9070, 14002) {
		t.Error("a table needing 28 cm was kept on a portrait page")
	}
	// A table only slightly over stays where it is: scaling costs it nothing
	// a reader would notice.
	if outgrew([]int{5000, 5205}, 9638, 14002) {
		t.Error("a table that scales down cleanly was turned sideways")
	}
	// Turning the page has to actually help.
	if outgrew([]int{300, 300, 300, 300}, 800, 900) {
		t.Error("the page was turned although landscape is no better")
	}
}

func TestLayOutTablesTurnsOnlyTheTablesThatNeedIt(t *testing.T) {
	wide := table(2870, 2330, 1100, 1475, 875, 1550, 1839, 1250, 2810)
	narrow := table(1200, 1100)
	body := `<w:p><w:r><w:t>before</w:t></w:r></w:p>` + wide +
		`<w:p><w:r><w:t>between</w:t></w:r></w:p>` + narrow +
		`<w:p><w:r><w:t>after</w:t></w:r></w:p>`

	got := layOutTables(body, portraitSection)

	// Portrait, landscape, portrait: two breaks around the wide table only.
	if n := strings.Count(got, "<w:sectPr>"); n != 2 {
		t.Errorf("got %d section breaks, want 2:\n%s", n, got)
	}
	if n := strings.Count(got, `w:orient="landscape"`); n != 1 {
		t.Errorf("got %d landscape sections, want 1", n)
	}
	// The break that ends the landscape section restores the portrait page for
	// what follows.
	landscapeAt := strings.Index(got, `w:orient="landscape"`)
	if strings.Index(got, "between") < landscapeAt {
		t.Error("the section ends before the table it was opened for")
	}
	if !strings.Contains(got[landscapeAt:], "between") {
		t.Error("the content after the table was lost")
	}
	// The narrow table is left exactly as it was.
	if !strings.Contains(got, narrow) {
		t.Error("a table that already fits was rewritten")
	}
	for _, want := range []string{"before", "between", "after"} {
		if !strings.Contains(got, want) {
			t.Errorf("content %q was lost", want)
		}
	}
}

func TestLayOutTablesWithoutAPageItUnderstands(t *testing.T) {
	body := table(9000, 9000)
	if got := layOutTables(body, `<w:sectPr/>`); got != body {
		t.Error("tables were laid out against a page with no dimensions")
	}
}

func TestSectionBreakIsWellFormedAndOrdered(t *testing.T) {
	got := sectionBreak(portraitSection)

	if err := xml.Unmarshal([]byte(wrapWordML(got)), new(struct{})); err != nil {
		t.Fatalf("the section break is not well-formed XML: %v", err)
	}
	// The schema fixes the order inside <w:pPr>; the section properties come
	// last, after the paragraph mark's run properties.
	if strings.Index(got, "<w:rPr>") > strings.Index(got, "<w:sectPr>") {
		t.Errorf("the properties are out of order: %s", got)
	}
	if sectionBreak("") != "" {
		t.Error("a break was produced for a section that does not exist")
	}
}

func wrapWordML(fragment string) string {
	return `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		fragment + `</w:document>`
}
