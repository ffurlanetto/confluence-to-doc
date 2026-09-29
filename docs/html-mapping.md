# What becomes of each HTML element

Confluence page bodies are HTML. The renderer assembles them into one document, LibreOffice converts that
to DOCX, and — when one is configured — the company Word template is applied. This page records what
survives each step, and it is a record of **measurements**, not of intentions: every row was produced by
converting the element with the LibreOffice the container ships and reading the OOXML that came out.

Re-measure it when the LibreOffice version changes. The conversion tests in
`backend/internal/converter` guard the mappings this application depends on; the rest of the table is
documentation.

## Structure

| HTML | OOXML | Result |
| --- | --- | --- |
| `<h1>`…`<h6>` | `w:pStyle` `Heading1`…`Heading6`, with `w:outlineLvl` | ✅ Word navigation pane and PDF bookmarks |
| `<p>` | `w:pStyle` `TextBody` (*Body Text*) | ✅ |
| `<br>` | `w:br` | ✅ |
| `<hr>` | paragraph with a bottom `w:pBdr` | ✅ |
| `<ul>` / `<ol>`, nested | `w:numPr` with `w:ilvl` per level | ✅ numbering merged with the template's |
| `<dl>`, `<dt>`, `<dd>` | `ListHeading` / `ListContents` styles | ✅ structure kept, styles are LibreOffice's |
| `<blockquote>` | `Quotations` style, indented | ✅ mapped to the template's **Quote** style when it defines one |
| `<pre>` | `PreformattedText` style, runs in `SourceText` | ✅ monospace survives on the runs |
| `<table>` | `w:tbl` with a fixed grid | ✅ scaled to the page — see [word-template.md](word-template.md) |
| `<thead>` | `w:tblHeader` on the row | ✅ the header row repeats on every page |
| `<th>` | `TableHeading` style (bold, centred) | ✅ bold kept even when the template names no such style |
| `colspan` / `rowspan` | `w:gridSpan` / `w:vMerge` | ✅ |
| nested `<table>` | nested `w:tbl` | ✅ scaled with the table that holds it |
| `<caption>` | a centred paragraph | ⚠️ not Word's *Caption* style |
| `<details>` / `<summary>` | two ordinary paragraphs | ⚠️ always expanded; a static document cannot fold |
| `<a href="#…">` | `w:hyperlink w:anchor`, target `w:bookmarkStart` | ✅ internal links are clickable |
| `<a href="http…">` | `w:hyperlink r:id`, `Hyperlink` style | ✅ takes the template's link colour |
| `<img>` | `w:drawing` with the image part | ✅ inlined, fitted to the printable width |

## Inline formatting

The interesting part: LibreOffice expresses most inline markup as **character styles**, not as direct
formatting. A run reads `<w:rStyle w:val="StrongEmphasis"/>` with no `<w:b/>` of its own, so losing the
style reference loses the bold itself. The merge therefore resolves those styles against the template by
name and carries over the ones it cannot match.

| HTML | OOXML | Result |
| --- | --- | --- |
| `<strong>` | `w:rStyle` `StrongEmphasis` (*Strong*) | ✅ |
| `<b>` | `w:b` | ✅ |
| `<em>` | `w:rStyle` `Emphasis` | ✅ |
| `<i>` | `w:i` | ✅ |
| `<u>` | `w:u` | ✅ |
| `<s>` | `w:strike` | ✅ |
| `<del>` | `w:rStyle` `Del`, **an empty style** | ⚠️ rewritten to `<s>` by the renderer, or it would be invisible |
| `<ins>` | `w:rStyle` `Ins`, **an empty style** | ⚠️ rewritten to `<u>` for the same reason |
| `<mark>` | nothing at all | ⚠️ rewritten to a span with a background colour |
| `<code>` | `w:rStyle` `SourceText` (monospace) | ✅ |
| `<kbd>` / `<samp>` / `<var>` | `UserEntry` / `Example` / `Variable` | ✅ |
| `<cite>` / `<q>` | `Quotation` (italic) / `Q` (empty) | ⚠️ `<q>` loses its quotation marks |
| `<sub>` / `<sup>` | `w:position` + smaller `w:sz` | ✅ |
| `<small>` | smaller `w:sz` | ✅ |
| `<abbr>` | nothing | ⚠️ the text stays, the title is lost |
| `style="color"` | `w:color` | ✅ |
| `style="background-color"` | `w:shd` | ✅ |
| `style="text-align"` | `w:jc` | ✅ |
| `page-break-before` | `w:pageBreakBefore` | ✅ each page starts a new page |
| `page-break-inside: avoid` | `w:keepLines` | ✅ **inline only** — as a CSS rule it lands in a style the template merge discards |

## Confluence specifics

| Source | Result |
| --- | --- |
| Task list (`<input type="checkbox">`) | ✅ becomes ☑ or ☐ — the checkbox itself is dropped by the converter, and with it the state |
| Info / warning / note panels | ⚠️ the text is kept, the coloured box is not |
| Status lozenges, emoticons | ✅ the underlying span or image is kept |
| Entities, accents, `&nbsp;` | ✅ |
| `<script>`, `<iframe>`, `<form>`, `<button>`, event handlers | ✅ removed before conversion, on purpose |

## Document properties

The renderer writes `<meta>` elements, which LibreOffice turns into `docProps`. They reach the DOCX and,
through it, the PDF.

| `<meta name=…>` | Property |
| --- | --- |
| *(the `<title>`)* | `dc:title` |
| `author` | `dc:creator` — the Confluence account whose token fetched the pages |
| `description` | `dc:description` — the source page, space and export date |
| `keywords` | `cp:keywords` |
| `classification` | `dc:subject`, from `DOCUMENT_CLASSIFICATION` |
| anything else | a **custom** document property: page id, page title, space, source URL, page count, export date |

## Table of contents

The entries are written as paragraphs carrying a `toc-entry-N` class, which LibreOffice turns into a style
name. The DOCX step finds them by that name, gives them the reader's own *TOC 1…9* styles and wraps them
in a real `TOC` field, so Word treats the block as a table of contents and refreshes it — filling in page
numbers, which nothing upstream can know — when the document is opened.

The entries stay written out in full, so the contents are readable before any refresh, and in the PDF,
where a field is never evaluated. The PDF also gets a real bookmark tree from the heading outline levels.

Updating the field in Word regenerates the entries from the heading styles. The hierarchical numbering
(`1.2.3`) is part of the heading text, so it survives that.

## Known limits

- `<del>`, `<ins>`, `<mark>`, `<q>` and `<abbr>` are not mapped by LibreOffice. The first three are
  rewritten by the renderer into markup that is; `<q>` and `<abbr>` keep their text and lose their marking.
- Confluence's coloured panels become plain paragraphs.
- `<details>` is always expanded.
- A `<caption>` is a centred paragraph rather than a *Caption* style.
