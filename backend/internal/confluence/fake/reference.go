package fake

import (
	"fmt"
	"strings"
)

// ReferenceRootID is the root page of the reference documents.
const ReferenceRootID = "900"

// SeedReference adds the reference documents: a page tree exercising every
// element docs/html-mapping.md lists, the markup Confluence Data Center 9
// produces for its common macros, several scripts and writing directions, and
// content the sanitiser must neutralise. Exporting it is how a change of
// LibreOffice, of the renderer or of the company template is checked (see
// docs/operations/reference-documents.md). image is served as the only
// attachment.
func SeedReference(f *Server, image []byte) {
	f.AddAttachment(ReferenceRootID+"/chart.png", image)
	add := func(id, parent, title, body string) {
		f.AddPage(Page{ID: id, ParentID: parent, Title: title, SpaceKey: "REF", Body: body})
	}

	add(ReferenceRootID, "", "Rendering reference", `
<p>This tree exercises everything the exporter converts. Each child page covers one family of elements;
compare an export with the checklist in docs/operations/reference-documents.md.</p>
<p>Reference image, inlined and fitted to the page width:</p>
<p><img src="/download/attachments/`+ReferenceRootID+`/chart.png" alt="Reference chart"></p>`)

	add("901", ReferenceRootID, "Structure", `
<h1>Heading level 1</h1><p>Paragraph under a level 1 heading.</p>
<h2>Heading level 2</h2><p>Paragraph with a line<br>break.</p>
<h3>Heading level 3</h3><h4>Heading level 4</h4><h5>Heading level 5</h5><h6>Heading level 6</h6>
<hr>
<ul><li>Bullet one<ul><li>Nested bullet<ul><li>Third level</li></ul></li></ul></li><li>Bullet two</li></ul>
<ol><li>First step</li><li>Second step<ol><li>Sub-step a</li><li>Sub-step b</li></ol></li><li>Third step</li></ol>
<dl><dt>Term</dt><dd>Definition of the term.</dd><dt>Another term</dt><dd>Its definition.</dd></dl>
<blockquote><p>A quotation, which should take the template's Quote style.</p></blockquote>
<pre>preformatted   text
    keeps its    spacing</pre>
<details><summary>Expandable section</summary><p>Always shown expanded in a document.</p></details>`)

	add("902", ReferenceRootID, "Tables", `
<table><caption>Table with a header row and a caption</caption>
<thead><tr><th>Name</th><th>Role</th><th>Since</th></tr></thead>
<tbody><tr><td>Ann</td><td>Author</td><td>2024</td></tr><tr><td>Bob</td><td>Reviewer</td><td>2025</td></tr></tbody></table>
<h2>Merged cells</h2>
<table><tr><th colspan="2">Spans two columns</th><th>C</th></tr>
<tr><td rowspan="2">Spans two rows</td><td>B1</td><td>C1</td></tr><tr><td>B2</td><td>C2</td></tr></table>
<h2>Nested table</h2>
<table><tr><td>Outer cell</td><td><table><tr><td>Inner 1</td><td>Inner 2</td></tr></table></td></tr></table>
<h2>Wide table (gets a landscape section)</h2>
`+wideTable(14, 4)+`
<h2>Long table (header repeats on every page)</h2>
`+longTable(80))

	add("903", ReferenceRootID, "Links and images", `
<h2 id="target">Anchor target</h2>
<p><a href="#target">Internal link to the anchor above</a>, an
<a href="https://confluence.example.com/display/REF/Structure">external link</a>, and a
<a href="/pages/viewpage.action?pageId=901">link to another page of the tree</a>.</p>
<p>The same image, scaled down by the page: <img src="/download/attachments/`+ReferenceRootID+`/chart.png" alt="Small chart" width="120"></p>`)

	add("904", ReferenceRootID, "Inline formatting", `
<p><strong>strong</strong>, <b>bold</b>, <em>emphasis</em>, <i>italic</i>, <u>underline</u>, <s>strike-through</s>,
<del>deleted</del>, <ins>inserted</ins>, H<sub>2</sub>O, x<sup>2</sup>, <code>inline code</code>, <kbd>Ctrl</kbd>+<kbd>C</kbd>,
<mark>highlighted</mark>, <small>small</small>, <abbr title="Data Center">DC</abbr>,
<span style="color: rgb(255,86,48);">coloured text</span>.</p>`)

	add("905", ReferenceRootID, "Confluence macros", `
<div class="confluence-information-macro confluence-information-macro-information"><span class="aui-icon aui-icon-small aui-iconfont-info confluence-information-macro-icon"></span>
<div class="confluence-information-macro-body"><p>Info panel.</p></div></div>
<div class="confluence-information-macro confluence-information-macro-note"><div class="confluence-information-macro-body"><p>Note panel.</p></div></div>
<div class="confluence-information-macro confluence-information-macro-warning"><div class="confluence-information-macro-body"><p>Warning panel.</p></div></div>
<div class="confluence-information-macro confluence-information-macro-tip"><div class="confluence-information-macro-body"><p>Tip panel.</p></div></div>
<p>Status: <span class="status-macro aui-lozenge aui-lozenge-success">DONE</span> <span class="status-macro aui-lozenge aui-lozenge-current">IN PROGRESS</span></p>
<div class="code panel pdl"><div class="codeHeader panelHeader pdl"><b>main.go</b></div><div class="codeContent panelContent pdl">
<pre class="syntaxhighlighter-pre" data-syntaxhighlighter-params="brush: go; gutter: false">func main() {
	fmt.Println("hello")
}</pre></div></div>
<div class="expand-container"><div class="expand-control"><span class="expand-control-text">Click to expand</span></div>
<div class="expand-content"><p>Content of an expand macro.</p></div></div>
<div class="toc-macro"><ul><li><a href="#target">Table of contents macro</a></li></ul></div>
<ul class="inline-task-list"><li class="checked">A done task</li><li>An open task</li></ul>`)

	add("906", ReferenceRootID, "Languages and scripts", `
<p lang="fr">Français : « Les élèves déjà inscrits reçoivent leur relevé à la fin de l'été. » — œ, ç, à.</p>
<p lang="de">Deutsch: Größenänderung der Straßenübersicht.</p>
<p lang="pl">Polski: zażółć gęślą jaźń.</p>
<p lang="el">Ελληνικά: Καλημέρα κόσμε.</p>
<p lang="ru">Русский: Съешь же ещё этих мягких французских булок.</p>
<p lang="ja">日本語：いろはにほへと ちりぬるを。</p>
<p lang="zh">中文：敏捷的棕色狐狸跳过了懒狗。</p>
<p lang="ar" dir="rtl">العربية: نص من اليمين إلى اليسار.</p>
<p lang="he" dir="rtl">עברית: טקסט מימין לשמאל.</p>
<p>Symbols: € £ ¥ © ® ™ ≤ ≥ ≠ ∞ → ✓, and emoji 📄✅.</p>`)

	add("907", ReferenceRootID, "Hostile content", `
<p>Everything below must vanish or stay inert in the document, and nothing may be fetched while converting
(security invariant 7).</p>
<script>document.body.innerHTML = "script ran"</script>
<iframe src="https://evil.example/frame"></iframe>
<object data="https://evil.example/object.swf"></object>
<embed src="https://evil.example/embed.swf">
<video src="https://evil.example/video.mp4"></video>
<audio src="https://evil.example/audio.mp3"></audio>
<link rel="stylesheet" href="https://evil.example/style.css">
<style>p { background: url(https://evil.example/bg.png) }</style>
<p style="background-image: url(https://evil.example/inline.png)">Paragraph whose background must not load.</p>
<p><img src="https://evil.example/tracker.png" alt="External image, dropped"></p>
<p><img src="/download/attachments/`+ReferenceRootID+`/chart.png" onerror="alert(1)" onload="alert(2)" alt="Image with event handlers"></p>
<p><a href="javascript:alert(1)">A script link, made inert</a></p>
<form action="https://evil.example/collect"><input name="q" value="form field"></form>
<svg><image href="https://evil.example/svg.png"/></svg>`)

	long := make([]string, 0, 60)
	for i := 1; i <= 60; i++ {
		long = append(long, fmt.Sprintf("<p>Paragraph %d. The quick brown fox jumps over the lazy dog; this "+
			"sentence repeats so the page runs over several pages and shows the footer on each of them.</p>", i))
	}
	add("908", ReferenceRootID, "Long page", "<h1>Several pages of text</h1>"+strings.Join(long, "\n"))

	// Depth: the hierarchy becomes heading levels and the table of contents.
	parent := "908"
	for depth := 1; depth <= 5; depth++ {
		id := fmt.Sprintf("91%d", depth)
		add(id, parent, fmt.Sprintf("Depth %d", depth+1), fmt.Sprintf("<p>A page at depth %d of the tree.</p>", depth+1))
		parent = id
	}
}

func wideTable(cols, rows int) string {
	var b strings.Builder
	b.WriteString("<table><thead><tr>")
	for c := 1; c <= cols; c++ {
		fmt.Fprintf(&b, "<th>Column %d</th>", c)
	}
	b.WriteString("</tr></thead><tbody>")
	for r := 1; r <= rows; r++ {
		b.WriteString("<tr>")
		for c := 1; c <= cols; c++ {
			fmt.Fprintf(&b, "<td>Value %d.%d</td>", r, c)
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")
	return b.String()
}

func longTable(rows int) string {
	var b strings.Builder
	b.WriteString("<table><thead><tr><th>#</th><th>Item</th><th>Status</th></tr></thead><tbody>")
	for r := 1; r <= rows; r++ {
		fmt.Fprintf(&b, "<tr><td>%d</td><td>Item number %d</td><td>OK</td></tr>", r, r)
	}
	b.WriteString("</tbody></table>")
	return b.String()
}
