package exporter

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	_ "image/gif" // register decoders used to size images
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strconv"
	"strings"
	"time"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence"
)

// maxImageWidthPx keeps images inside an A4 page with default margins.
const maxImageWidthPx = 640

// Assets resolves resources referenced by page bodies.
type Assets interface {
	// ResolveURL makes a (possibly relative) Confluence URL absolute.
	ResolveURL(raw string) (string, error)
	// FetchImage returns the image bytes and MIME type for src.
	FetchImage(ctx context.Context, src string) ([]byte, string, error)
}

type RenderOptions struct {
	Title       string
	SourceURL   string
	GeneratedAt time.Time
	// UseTemplateStyles omits this document's own typography so that the
	// styles of a company Word template apply instead.
	UseTemplateStyles bool
}

// RenderHTML assembles the whole tree into one self-contained HTML document
// (images inlined as data URIs) suitable for conversion by LibreOffice.
//
// The page hierarchy is mapped to heading levels: the root page title is an
// <h1>, its children <h2>, and so on (capped at <h6>). Headings inside page
// bodies are shifted below their page title so the document outline in
// Word/PDF readers mirrors the Confluence tree.
func RenderHTML(ctx context.Context, root *Node, opts RenderOptions, assets Assets) ([]byte, error) {
	r := &renderer{ctx: ctx, assets: assets, ids: map[string]bool{}, images: map[string]string{}}
	root.Walk(func(n *Node) { r.ids[n.Page.ID] = true })

	var b bytes.Buffer
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>`)
	b.WriteString(html.EscapeString(opts.Title))
	css := structuralCSS
	if !opts.UseTemplateStyles {
		css += typographyCSS
	}
	b.WriteString(`</title><style>` + css + `</style></head><body>`)

	// Cover page.
	fmt.Fprintf(&b, `<p class="doc-title">%s</p>`, html.EscapeString(opts.Title))
	fmt.Fprintf(&b, `<p class="doc-meta">Exported from Confluence on %s</p>`, opts.GeneratedAt.Format("2006-01-02 15:04 MST"))
	if opts.SourceURL != "" {
		fmt.Fprintf(&b, `<p class="doc-meta">Source: <a href="%[1]s">%[1]s</a></p>`, html.EscapeString(opts.SourceURL))
	}
	count := root.Count()
	fmt.Fprintf(&b, `<p class="doc-meta">%d page(s)</p>`, count)

	// Table of contents (only meaningful when there is a hierarchy).
	if count > 1 {
		b.WriteString(`<p class="toc-title" style="page-break-before: always">Table of contents</p>`)
		root.Walk(func(n *Node) {
			fmt.Fprintf(&b, `<p class="toc-entry" style="margin-left: %.1fcm"><a href="#%s">%s %s</a></p>`,
				float64(n.Depth)*0.8, anchor(n.Page.ID), n.Number, html.EscapeString(n.Page.Title))
		})
	}

	var renderErr error
	root.Walk(func(n *Node) {
		if renderErr != nil {
			return
		}
		if err := ctx.Err(); err != nil {
			renderErr = err
			return
		}
		level := min(n.Depth+1, 6)
		fmt.Fprintf(&b, `<h%d id="%s" style="page-break-before: always"><a name="%s"></a>%s %s</h%d>`,
			level, anchor(n.Page.ID), anchor(n.Page.ID), n.Number, html.EscapeString(n.Page.Title), level)
		body, err := r.transformBody(n.Page.BodyHTML, n.Depth+1)
		if err != nil {
			renderErr = fmt.Errorf("page %s: %w", n.Page.ID, err)
			return
		}
		b.WriteString(body)
	})
	if renderErr != nil {
		return nil, renderErr
	}
	b.WriteString(`</body></html>`)
	return b.Bytes(), nil
}

// structuralCSS carries layout that no Word template can supply, because it
// describes this document's own structure: table rules, cover and contents
// spacing, code blocks.
const structuralCSS = `
.doc-title { font-size: 26pt; font-weight: bold; margin-top: 6cm; }
.doc-meta { color: #555555; }
.toc-title { font-size: 16pt; font-weight: bold; }
.toc-entry { margin-top: 0; margin-bottom: 0.1cm; }
table { border-collapse: collapse; }
th, td { border: 1px solid #999999; padding: 3px; vertical-align: top; }
th { background-color: #f0f0f0; }
pre, code { font-family: "Liberation Mono", monospace; font-size: 9pt; }
pre { background-color: #f5f5f5; }
`

// typographyCSS sets the body and heading typeface. It is omitted when a Word
// template is configured: the converter would turn these declarations into
// direct formatting, which overrides the template's styles and would defeat
// the company look.
const typographyCSS = `
body { font-family: "Liberation Sans", Arial, sans-serif; font-size: 10.5pt; }
h1, h2, h3, h4, h5, h6 { font-family: "Liberation Sans", Arial, sans-serif; }
`

func anchor(pageID string) string { return "page-" + pageID }

type renderer struct {
	ctx    context.Context
	assets Assets
	ids    map[string]bool   // page ids present in the export
	images map[string]string // src -> data URI (deduplicates downloads)
}

// Elements that are either unsafe or meaningless in a static document.
var droppedElements = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true,
	atom.Embed: true, atom.Form: true, atom.Input: true, atom.Button: true,
	atom.Noscript: true, atom.Link: true, atom.Meta: true, atom.Select: true,
	atom.Textarea: true,
}

var headingLevels = map[atom.Atom]int{atom.H1: 1, atom.H2: 2, atom.H3: 3, atom.H4: 4, atom.H5: 5, atom.H6: 6}

func (r *renderer) transformBody(body string, shift int) (string, error) {
	parent := &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := nethtml.ParseFragment(strings.NewReader(body), parent)
	if err != nil {
		return "", err
	}
	// Attach top-level nodes to a container so they get filtered too.
	container := &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		container.AppendChild(n)
	}
	r.transformNode(container, shift)
	var b bytes.Buffer
	for n := container.FirstChild; n != nil; n = n.NextSibling {
		if err := nethtml.Render(&b, n); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func (r *renderer) transformNode(n *nethtml.Node, shift int) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == nethtml.ElementNode && droppedElements[c.DataAtom] {
			n.RemoveChild(c)
		} else if c.Type == nethtml.CommentNode {
			n.RemoveChild(c)
		} else {
			r.transformNode(c, shift)
		}
		c = next
	}
	if n.Type != nethtml.ElementNode {
		return
	}
	stripEventHandlers(n)

	if lvl, ok := headingLevels[n.DataAtom]; ok {
		newLvl := min(lvl+shift, 6)
		n.Data = "h" + strconv.Itoa(newLvl)
		n.DataAtom = atom.Lookup([]byte(n.Data))
		return
	}
	switch n.DataAtom {
	case atom.Img:
		r.inlineImage(n)
	case atom.A:
		r.rewriteLink(n)
	case atom.Table:
		// LibreOffice ignores CSS borders on cells but honours these attributes.
		if v, _ := getAttr(n, "border"); v == "" || v == "0" {
			setAttr(n, "border", "1")
		}
		if v, _ := getAttr(n, "cellpadding"); v == "" {
			setAttr(n, "cellpadding", "4")
		}
	}
}

func stripEventHandlers(n *nethtml.Node) {
	attrs := n.Attr[:0]
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if strings.HasPrefix(key, "on") || key == "srcset" {
			continue
		}
		if (key == "href" || key == "src") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Val)), "javascript:") {
			continue
		}
		attrs = append(attrs, a)
	}
	n.Attr = attrs
}

func getAttr(n *nethtml.Node, key string) (string, int) {
	for i, a := range n.Attr {
		if a.Key == key {
			return a.Val, i
		}
	}
	return "", -1
}

func setAttr(n *nethtml.Node, key, val string) {
	if _, i := getAttr(n, key); i >= 0 {
		n.Attr[i].Val = val
		return
	}
	n.Attr = append(n.Attr, nethtml.Attribute{Key: key, Val: val})
}

func removeAttr(n *nethtml.Node, key string) {
	if _, i := getAttr(n, key); i >= 0 {
		n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
	}
}

func (r *renderer) inlineImage(n *nethtml.Node) {
	src, _ := getAttr(n, "src")
	if src == "" || strings.HasPrefix(src, "data:") {
		return
	}
	dataURI, ok := r.images[src]
	if !ok {
		data, mime, err := r.assets.FetchImage(r.ctx, src)
		if err == nil && !strings.HasPrefix(mime, "image/") {
			mime = httpDetect(data)
		}
		if err != nil || !strings.HasPrefix(mime, "image/") {
			// Replace a broken image by its alternative text rather than
			// leaving a reference the converter cannot resolve.
			alt, _ := getAttr(n, "alt")
			if alt == "" {
				alt = "image"
			}
			n.Type = nethtml.TextNode
			n.Data = "[" + alt + "]"
			n.DataAtom = 0
			n.Attr = nil
			r.images[src] = ""
			return
		}
		dataURI = "data:" + strings.Split(mime, ";")[0] + ";base64," + base64.StdEncoding.EncodeToString(data)
		r.images[src] = dataURI
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			fitImage(n, cfg.Width, cfg.Height)
		}
	} else if dataURI == "" {
		n.Type, n.Data, n.DataAtom, n.Attr = nethtml.TextNode, "[image]", 0, nil
		return
	}
	setAttr(n, "src", dataURI)
	for _, k := range []string{"data-image-src", "data-src", "data-base-url"} {
		removeAttr(n, k)
	}
}

// fitImage bounds the display size of an image to the printable width while
// preserving the aspect ratio. Explicit widths from Confluence are honoured
// when smaller.
func fitImage(n *nethtml.Node, w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	if v, _ := getAttr(n, "width"); v != "" {
		if cw, err := strconv.Atoi(v); err == nil && cw > 0 {
			h = h * cw / w
			w = cw
		}
	}
	if w > maxImageWidthPx {
		h = h * maxImageWidthPx / w
		w = maxImageWidthPx
	}
	setAttr(n, "width", strconv.Itoa(w))
	setAttr(n, "height", strconv.Itoa(max(h, 1)))
}

// rewriteLink points links to exported pages at the in-document anchor and
// makes every other relative link absolute (so it still works offline).
func (r *renderer) rewriteLink(n *nethtml.Node) {
	href, _ := getAttr(n, "href")
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") {
		return
	}
	abs, err := r.assets.ResolveURL(href)
	if err != nil {
		removeAttr(n, "href")
		return
	}
	if ref, ok := confluence.ParsePageRef(abs); ok && ref.ID != "" && r.ids[ref.ID] {
		setAttr(n, "href", "#"+anchor(ref.ID))
		return
	}
	setAttr(n, "href", abs)
}

func httpDetect(data []byte) string { return http.DetectContentType(data) }
