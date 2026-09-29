package docx

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestTextWidthOf(t *testing.T) {
	tests := []struct {
		name   string
		sectPr string
		want   int
	}{
		{
			name:   "A4 with 2.5 cm margins",
			sectPr: `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1418" w:right="1418" w:bottom="1418" w:left="1418"/></w:sectPr>`,
			want:   9070,
		},
		{
			name:   "landscape",
			sectPr: `<w:sectPr><w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/><w:pgMar w:right="1134" w:left="1134"/></w:sectPr>`,
			want:   14570,
		},
		{
			name:   "no page size",
			sectPr: `<w:sectPr><w:pgMar w:right="1134" w:left="1134"/></w:sectPr>`,
			want:   0,
		},
		{
			name:   "no margins: the printable width is a guess",
			sectPr: `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr>`,
			want:   0,
		},
		{
			name:   "margins wider than the page",
			sectPr: `<w:sectPr><w:pgSz w:w="2000" w:h="16838"/><w:pgMar w:right="1500" w:left="1500"/></w:sectPr>`,
			want:   0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := textWidthOf(tc.sectPr); got != tc.want {
				t.Errorf("textWidthOf() = %d, want %d", got, tc.want)
			}
		})
	}
}

// table builds a one-row table whose grid and cells carry the given widths.
func table(widths ...int) string {
	var grid, cells strings.Builder
	total := 0
	for _, w := range widths {
		total += w
		grid.WriteString(`<w:gridCol w:w="` + strconv.Itoa(w) + `"/>`)
		cells.WriteString(`<w:tc><w:tcPr><w:tcW w:w="` + strconv.Itoa(w) + `" w:type="dxa"/></w:tcPr><w:p/></w:tc>`)
	}
	return `<w:tbl><w:tblPr><w:tblW w:w="` + strconv.Itoa(total) + `" w:type="dxa"/><w:tblLayout w:type="fixed"/></w:tblPr>` +
		`<w:tblGrid>` + grid.String() + `</w:tblGrid><w:tr>` + cells.String() + `</w:tr></w:tbl>`
}

func widths(t *testing.T, xml string, re *regexp.Regexp) []int {
	t.Helper()
	var out []int
	for _, m := range re.FindAllStringSubmatch(xml, -1) {
		v, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparsable width %q", m[1])
		}
		out = append(out, v)
	}
	return out
}

var (
	reTestGridCol = regexp.MustCompile(`<w:gridCol w:w="(\d+)"/>`)
	reTestTcW     = regexp.MustCompile(`<w:tcW w:w="(\d+)" w:type="dxa"/>`)
	reTestTblW    = regexp.MustCompile(`<w:tblW w:w="(\d+)" w:type="dxa"/>`)
)

func sum(v []int) int {
	total := 0
	for _, n := range v {
		total += n
	}
	return total
}

func TestFitTablesScalesAnOversizedTable(t *testing.T) {
	const available = 9070
	body := "<w:p/>" + table(1283, 1268, 1268, 1268, 1268, 1268, 1268, 1314) + "<w:p/>"

	got := fitTables(body, available)

	grid := widths(t, got, reTestGridCol)
	if total := sum(grid); total > available {
		t.Errorf("table is %d twips wide, the page holds %d: %v", total, available, grid)
	}
	// Close to the full width: the table should not be shrunk more than needed.
	if total := sum(grid); total < available-len(grid) {
		t.Errorf("table shrunk to %d, expected about %d", total, available)
	}
	if cells := widths(t, got, reTestTcW); sum(cells) != sum(grid) {
		t.Errorf("cell widths (%d) no longer match the column grid (%d)", sum(cells), sum(grid))
	}
	// Columns keep their relative sizes: the widest stays the widest.
	if grid[0] <= grid[1] || grid[7] <= grid[0] {
		t.Errorf("column proportions changed: %v", grid)
	}
	if tblW := widths(t, got, reTestTblW); len(tblW) != 1 || tblW[0] > available {
		t.Errorf("declared table width %v does not fit in %d", tblW, available)
	}
	if !strings.HasPrefix(got, "<w:p/>") || !strings.HasSuffix(got, "<w:p/>") {
		t.Error("content around the table was lost")
	}
}

func TestFitTablesLeavesAFittingTableAlone(t *testing.T) {
	body := table(2000, 3000)
	if got := fitTables(body, 9070); got != body {
		t.Errorf("a table that already fits was modified:\n%s", got)
	}
}

func TestFitTablesWithoutAKnownPageWidth(t *testing.T) {
	body := table(6000, 6000)
	if got := fitTables(body, 0); got != body {
		t.Error("tables were scaled although the page width is unknown")
	}
}

func TestFitTablesLeavesRelativeWidthsAlone(t *testing.T) {
	// A table expressed as a percentage of the text width already follows the
	// page; only its column grid needs scaling.
	body := `<w:tbl><w:tblPr><w:tblW w:w="5000" w:type="pct"/></w:tblPr>` +
		`<w:tblGrid><w:gridCol w:w="6000"/><w:gridCol w:w="6000"/></w:tblGrid>` +
		`<w:tr><w:tc><w:tcPr><w:tcW w:w="2500" w:type="pct"/></w:tcPr><w:p/></w:tc></w:tr></w:tbl>`

	got := fitTables(body, 9070)

	if !strings.Contains(got, `<w:tblW w:w="5000" w:type="pct"/>`) {
		t.Error("the percentage table width was rewritten")
	}
	if !strings.Contains(got, `<w:tcW w:w="2500" w:type="pct"/>`) {
		t.Error("the percentage cell width was rewritten")
	}
	if total := sum(widths(t, got, reTestGridCol)); total > 9070 {
		t.Errorf("column grid is %d twips wide, the page holds 9070", total)
	}
}

func TestFitTablesScalesANestedTableWithItsParent(t *testing.T) {
	inner := table(4000, 4000)
	outer := `<w:tbl><w:tblPr><w:tblW w:w="12000" w:type="dxa"/></w:tblPr>` +
		`<w:tblGrid><w:gridCol w:w="4000"/><w:gridCol w:w="8000"/></w:tblGrid>` +
		`<w:tr><w:tc><w:p/></w:tc><w:tc>` + inner + `</w:tc></w:tr></w:tbl>`

	got := fitTables(outer, 6000)

	all := widths(t, got, reTestGridCol)
	if len(all) != 4 {
		t.Fatalf("expected 4 columns across both tables, got %v", all)
	}
	// Outer grid first, then the nested one: both halved by the same factor.
	if outerSum := all[0] + all[1]; outerSum > 6000 {
		t.Errorf("outer table is %d twips wide, the page holds 6000", outerSum)
	}
	if innerSum := all[2] + all[3]; innerSum > 6000 {
		t.Errorf("nested table is %d twips wide, the page holds 6000", innerSum)
	}
	if all[2] != 2000 || all[3] != 2000 {
		t.Errorf("nested table not scaled with its parent: %v", all)
	}
}

func TestFitTablesUsesTheDeclaredWidthWithoutAGrid(t *testing.T) {
	body := `<w:tbl><w:tblPr><w:tblW w:w="12000" w:type="dxa"/></w:tblPr>` +
		`<w:tr><w:tc><w:tcPr><w:tcW w:w="12000" w:type="dxa"/></w:tcPr><w:p/></w:tc></w:tr></w:tbl>`

	got := fitTables(body, 6000)

	if tblW := widths(t, got, reTestTblW); len(tblW) != 1 || tblW[0] != 6000 {
		t.Errorf("tblW = %v, want [6000]", tblW)
	}
	if cells := widths(t, got, reTestTcW); len(cells) != 1 || cells[0] != 6000 {
		t.Errorf("tcW = %v, want [6000]", cells)
	}
}

func TestFitTablesKeepsEveryColumnVisible(t *testing.T) {
	// An extreme table must not end up with zero-width columns.
	var cols []int
	for range 200 {
		cols = append(cols, 100)
	}
	got := fitTables(table(cols...), 500)
	for i, w := range widths(t, got, reTestGridCol) {
		if w < 1 {
			t.Fatalf("column %d has width %d", i, w)
		}
	}
}

func TestFitTablesIgnoresANestedGridWhenMeasuring(t *testing.T) {
	// The outer table declares no grid of its own; the one inside a cell must
	// not be mistaken for it.
	body := `<w:tbl><w:tblPr><w:tblW w:w="12000" w:type="dxa"/></w:tblPr>` +
		`<w:tr><w:tc>` + table(500, 500) + `</w:tc></w:tr></w:tbl>`

	got := fitTables(body, 6000)

	if tblW := widths(t, got, reTestTblW); len(tblW) != 2 || tblW[0] != 6000 {
		t.Errorf("outer table width = %v, want the first to be 6000", tblW)
	}
	if grid := widths(t, got, reTestGridCol); grid[0] != 250 || grid[1] != 250 {
		t.Errorf("nested grid = %v, want it halved with its parent", grid)
	}
}
