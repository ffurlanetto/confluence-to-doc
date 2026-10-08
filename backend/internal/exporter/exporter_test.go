package exporter_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence/fake"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/exporter"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// newFixture builds:
//
//	1 Root
//	├── 2 Child A
//	│   └── 4 Grandchild
//	└── 3 Child B
func newFixture(t *testing.T) (*fake.Server, *confluence.Client) {
	t.Helper()
	f := fake.New("tok")
	f.AddPage(fake.Page{ID: "1", Title: "Root", Body: `<h1>Intro</h1><p>See <a href="/pages/viewpage.action?pageId=4">grandchild</a> and <a href="/display/X/Other">other</a></p><img src="/download/attachments/1/wide.png" alt="wide"><script>alert(1)</script><table><tr><td>x</td></tr></table>`})
	f.AddPage(fake.Page{ID: "2", Title: "Child A", ParentID: "1", Body: `<h2>Section</h2><p onclick="x()">A</p><img src="/download/attachments/missing.png" alt="gone">`})
	f.AddPage(fake.Page{ID: "3", Title: "Child B", ParentID: "1", Body: `<p>B &amp; co</p>`})
	f.AddPage(fake.Page{ID: "4", Title: "Grandchild <x>", ParentID: "2", Body: `<h5>Deep</h5>`})
	f.AddAttachment("1/wide.png", pngBytes(t, 1280, 200))
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return f, confluence.NewClient(u, "tok", srv.Client())
}

func TestBuildTreePreservesHierarchyAndOrder(t *testing.T) {
	_, c := newFixture(t)
	var mu sync.Mutex
	var progress []int
	root, err := exporter.BuildTree(context.Background(), c, "1", exporter.TreeOptions{
		IncludeChildren: true, MaxPages: 10, Concurrency: 3,
		OnProgress: func(n int) { mu.Lock(); progress = append(progress, n); mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	root.Walk(func(n *exporter.Node) { got = append(got, fmt.Sprintf("%s:%s:%d", n.Number, n.Page.ID, n.Depth)) })
	want := "1:1:0 1.1:2:1 1.1.1:4:2 1.2:3:1"
	if strings.Join(got, " ") != want {
		t.Fatalf("tree = %v, want %s", got, want)
	}
	if root.Count() != 4 {
		t.Errorf("count = %d", root.Count())
	}
	if len(progress) == 0 || progress[0] != 1 {
		t.Errorf("progress not reported: %v", progress)
	}
}

func TestBuildTreeWithoutChildren(t *testing.T) {
	_, c := newFixture(t)
	root, err := exporter.BuildTree(context.Background(), c, "1", exporter.TreeOptions{MaxPages: 10})
	if err != nil {
		t.Fatal(err)
	}
	if root.Count() != 1 {
		t.Fatalf("count = %d, want 1", root.Count())
	}
}

func TestBuildTreeEnforcesMaxPages(t *testing.T) {
	_, c := newFixture(t)
	_, err := exporter.BuildTree(context.Background(), c, "1", exporter.TreeOptions{IncludeChildren: true, MaxPages: 3, Concurrency: 2})
	if !errors.Is(err, exporter.ErrTooManyPages) {
		t.Fatalf("want ErrTooManyPages, got %v", err)
	}
}

func TestBuildTreeRootNotFound(t *testing.T) {
	_, c := newFixture(t)
	_, err := exporter.BuildTree(context.Background(), c, "404", exporter.TreeOptions{IncludeChildren: true, MaxPages: 3})
	if !errors.Is(err, confluence.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRenderHTML(t *testing.T) {
	_, c := newFixture(t)
	root, err := exporter.BuildTree(context.Background(), c, "1", exporter.TreeOptions{IncludeChildren: true, MaxPages: 10, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exporter.RenderHTML(context.Background(), root, exporter.RenderOptions{
		Title: "Root", SourceURL: root.Page.WebURL, GeneratedAt: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
	}, exporter.ConfluenceAssets{Client: c, MaxImageBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)

	mustContain := []string{
		`<h1 id="page-1"`, `1 Root</h1>`, // root title is h1
		`Intro</h2>`,                          // body h1 shifted under the page title
		`<h2 id="page-2"`, `1.1 Child A</h2>`, // children are h2
		`Section</h4>`, // body h2 of a depth-1 page
		`<h3 id="page-4"`, `1.1.1 Grandchild &lt;x&gt;</h3>`,
		`Deep</h6>`,                   // capped at h6
		`href="#page-4"`,              // internal link rewritten
		`/display/X/Other"`,           // external link made absolute
		`src="data:image/png;base64,`, // image inlined
		`width="640" height="100"`,    // and fitted to the page
		`[gone]`,                      // missing image replaced by alt text
		`Table of contents`,
		`border="1"`, // tables get visible borders
	}
	for _, s := range mustContain {
		if !strings.Contains(doc, s) {
			t.Errorf("output does not contain %q", s)
		}
	}
	for _, s := range []string{"<script", "alert(1)", "onclick", "/download/attachments/1/wide.png"} {
		if strings.Contains(doc, s) {
			t.Errorf("output must not contain %q", s)
		}
	}
	if strings.Index(doc, "1.1 Child A</h2>") > strings.Index(doc, "1.2 Child B</h2>") {
		t.Error("pages are not rendered in tree order")
	}
}

type failingAssets struct{}

func (failingAssets) ResolveURL(raw string) (string, error) { return raw, nil }
func (failingAssets) FetchImage(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("boom")
}

func TestRenderHTMLHonoursCancellation(t *testing.T) {
	root := &exporter.Node{Page: confluence.Page{PageSummary: confluence.PageSummary{ID: "1", Title: "R"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := exporter.RenderHTML(ctx, root, exporter.RenderOptions{Title: "R"}, failingAssets{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestRenderKeepsBlocksWholeOnOnePage(t *testing.T) {
	const keep = "page-break-inside: avoid"
	body := `<p>plain</p><ul><li>item</li></ul><blockquote>quoted</blockquote><pre>code</pre>` +
		`<p style="color: red">already styled</p><p style="page-break-inside: auto">page decides</p>` +
		`<table><tbody><tr><td><p>in a cell</p></td></tr></tbody></table>`
	root := &exporter.Node{Page: confluence.Page{
		PageSummary: confluence.PageSummary{ID: "1", Title: "Root"}, BodyHTML: body,
	}}

	out, err := exporter.RenderHTML(context.Background(), root,
		exporter.RenderOptions{Title: "Root", GeneratedAt: time.Now()}, failingAssets{})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)

	// LibreOffice maps this declaration to w:keepLines. It has to be inline:
	// a stylesheet rule becomes a paragraph style, and a Word template
	// replaces the converter's styles with its own.
	for _, want := range []string{
		`<p style="` + keep + `">plain</p>`,
		`<li style="` + keep + `">item</li>`,
		`<blockquote style="` + keep + `">quoted</blockquote>`,
		`<pre style="` + keep + `">code</pre>`,
		`<p style="color: red; ` + keep + `">already styled</p>`,
		`<p style="` + keep + `">in a cell</p>`,
		`<p class="doc-title" style="` + keep + `">Root</p>`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("output does not contain %q", want)
		}
	}
	// A page that decides for itself keeps its declaration.
	if !strings.Contains(doc, `<p style="page-break-inside: auto">page decides</p>`) {
		t.Error("an explicit page-break-inside from the page was overridden")
	}
	// Headings still start a new page, and stay whole.
	if !strings.Contains(doc, `style="page-break-before: always; `+keep+`"`) {
		t.Error("page headings lost either their page break or their keep-together")
	}
	// Table rows are not kept together: a long table has to break somewhere.
	if strings.Contains(doc, `<tr style=`) || strings.Contains(doc, `<td style=`) {
		t.Error("table rows/cells must not be kept together")
	}
}

func TestRenderWritesDocumentProperties(t *testing.T) {
	root := &exporter.Node{Page: confluence.Page{
		PageSummary: confluence.PageSummary{ID: "1", Title: "Root"},
	}}
	out, err := exporter.RenderHTML(context.Background(), root, exporter.RenderOptions{
		Title: "Product documentation", GeneratedAt: time.Now(),
		Author: "Jane Doe", Description: "Exported from Confluence", Classification: "Internal",
		Keywords: []string{"Confluence", "export"},
		Properties: []exporter.Property{
			{Name: "Confluence page ID", Value: "1"},
			{Name: "Source URL", Value: "https://confluence.example.com/x/1?a=b&c=d"},
			{Name: "Empty", Value: ""},
		},
	}, failingAssets{})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)

	// LibreOffice reads these into docProps, which is what reaches the DOCX
	// and the PDF. Unknown names become custom document properties.
	for _, want := range []string{
		`<meta name="author" content="Jane Doe">`,
		`<meta name="description" content="Exported from Confluence">`,
		`<meta name="keywords" content="Confluence, export">`,
		`<meta name="classification" content="Internal">`,
		`<meta name="Confluence page ID" content="1">`,
		`content="https://confluence.example.com/x/1?a=b&amp;c=d"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("output does not contain %q", want)
		}
	}
	if strings.Contains(doc, `name="Empty"`) {
		t.Error("an empty property must not be written")
	}
	// The properties belong to the head, before the document starts.
	if strings.Index(doc, `name="author"`) > strings.Index(doc, "<body>") {
		t.Error("the properties are not in the document head")
	}
}

func TestRenderMarksContentsEntriesByLevel(t *testing.T) {
	root := &exporter.Node{
		Page: confluence.Page{PageSummary: confluence.PageSummary{ID: "1", Title: "Root"}}, Number: "1",
		Children: []*exporter.Node{{
			Page:  confluence.Page{PageSummary: confluence.PageSummary{ID: "2", Title: "Child"}},
			Depth: 1, Number: "1.1",
		}},
	}
	out, err := exporter.RenderHTML(context.Background(), root,
		exporter.RenderOptions{Title: "Root", GeneratedAt: time.Now()}, failingAssets{})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)

	// The level travels in the class, because LibreOffice turns it into a
	// style name — which is how the DOCX step finds these paragraphs again.
	if !strings.Contains(doc, `<p class="toc-entry-1"><a href="#page-1">1 Root</a></p>`) {
		t.Errorf("the root contents entry is missing or unmarked:\n%s", doc)
	}
	if !strings.Contains(doc, `<p class="toc-entry-2"><a href="#page-2">1.1 Child</a></p>`) {
		t.Error("the child contents entry is not marked with its level")
	}
	if got := exporter.TOCEntryClass(20); got != "toc-entry-9" {
		t.Errorf("TOCEntryClass(20) = %q, want the deepest level Word has", got)
	}
}

func TestRenderKeepsMarkupLibreOfficeWouldSwallow(t *testing.T) {
	body := `<p><del>gone</del> <ins>added</ins> <mark>noted</mark></p>` +
		`<ul class="inline-task-list"><li><input type="checkbox" checked>Done</li>` +
		`<li><input type="checkbox">Open</li></ul>` +
		`<p><input type="text" value="secret"></p>`
	root := &exporter.Node{Page: confluence.Page{
		PageSummary: confluence.PageSummary{ID: "1", Title: "Root"}, BodyHTML: body,
	}}
	out, err := exporter.RenderHTML(context.Background(), root,
		exporter.RenderOptions{Title: "Root", GeneratedAt: time.Now()}, failingAssets{})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)

	// LibreOffice maps <del>, <ins> and <mark> to nothing at all, so they are
	// rewritten to markup it does render.
	for _, want := range []string{"<s>gone</s>", "<u>added</u>", "background-color: #ffff00", "☑", "☐"} {
		if !strings.Contains(doc, want) {
			t.Errorf("output does not contain %q", want)
		}
	}
	for _, unwanted := range []string{"<del>", "<ins>", "<mark", "<input", "secret"} {
		if strings.Contains(doc, unwanted) {
			t.Errorf("output must not contain %q", unwanted)
		}
	}
}

// hostileBody names remote resources in every way HTML and CSS allow. Page
// authors control Confluence content, and LibreOffice fetches what the HTML
// it imports points at: none of these may reach the converter.
const hostileBody = `<p style="color: red; background-image: url(http://evil.test/bg.png)">styled</p>
<p style="background: URL( 'http://evil.test/a' ); font-weight: bold">upper-case url</p>
<p style="background:u\72l(http://evil.test/escaped)">escaped</p>
<div style="@import 'http://evil.test/i.css'">import</div>
<table background="http://evil.test/table.png"><tr><td background="http://evil.test/cell.png">cell</td></tr></table>
<img srcset="http://evil.test/1x.png 1x" src="http://evil.test/remote.png" alt="remote">
<img src="file:///etc/passwd" alt="local file">
<video src="http://evil.test/v.mp4" poster="http://evil.test/poster.png"></video>
<audio src="http://evil.test/a.mp3"></audio>
<picture><source srcset="http://evil.test/p.webp"><img src="data:image/png;base64,iVBORw0KGgo=" alt="kept"></picture>
<svg><image href="http://evil.test/svg.png"/></svg>
<input type="image" src="http://evil.test/input.png">
<base href="http://evil.test/">
<blockquote cite="http://evil.test/cite">quote</blockquote>
<span src="http://evil.test/span" href="http://evil.test/span-href">span</span>
<a href="https://example.com/doc" ping="http://evil.test/ping">a link stays a link</a>`

func TestRenderLoadsNothingRemote(t *testing.T) {
	root := &exporter.Node{Page: confluence.Page{PageSummary: confluence.PageSummary{ID: "1", Title: "R"}, BodyHTML: hostileBody}}
	out, err := exporter.RenderHTML(context.Background(), root, exporter.RenderOptions{Title: "R"}, failingAssets{})
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)
	if strings.Contains(doc, "evil.test") || strings.Contains(doc, "file:") {
		t.Errorf("a remote reference survived:\n%s", doc)
	}
	for _, kept := range []string{"styled", `style="color: red; page-break-inside: avoid"`, "font-weight: bold", "cell", "quote", "[remote]", "[local file]",
		`src="data:image/png;base64,iVBORw0KGgo="`, `href="https://example.com/doc"`, "a link stays a link"} {
		if !strings.Contains(doc, kept) {
			t.Errorf("legitimate content lost: %q", kept)
		}
	}
}
