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
	f.AddPage(fake.Page{ID: "1", Title: "Root", Body: `<h1>Intro</h1><p>See <a href="/pages/viewpage.action?pageId=4">grandchild</a> and <a href="/display/X/Other">other</a></p><img src="/download/attachments/1/wide.png" alt="wide"><script>alert(1)</script>`})
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
		`<h2>Intro</h2>`,                      // body h1 shifted under the page title
		`<h2 id="page-2"`, `1.1 Child A</h2>`, // children are h2
		`<h4>Section</h4>`, // body h2 of a depth-1 page
		`<h3 id="page-4"`, `1.1.1 Grandchild &lt;x&gt;</h3>`,
		`<h6>Deep</h6>`,               // capped at h6
		`href="#page-4"`,              // internal link rewritten
		`/display/X/Other"`,           // external link made absolute
		`src="data:image/png;base64,`, // image inlined
		`width="640" height="100"`,    // and fitted to the page
		`[gone]`,                      // missing image replaced by alt text
		`Table des matières`,
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
