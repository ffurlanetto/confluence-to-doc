# Reference documents

A fixed page tree that exercises everything the exporter converts. Exporting it, and checking the result
against the list below, is how to accept a change that could alter documents:

- a new LibreOffice version (a new image);
- a change to the renderer, the converter or the template merge;
- a new company Word template, before announcing it.

## Where it lives

The tree is served by the fake Confluence (`make dev-mocks`, `docker compose up`), space `REF`, root page
**Rendering reference** (id `900`). Its source is
[`backend/internal/confluence/fake/reference.go`](../../backend/internal/confluence/fake/reference.go);
the end-to-end tests export it in both formats, and `TestReferenceDocumentsRenderSafely` checks that
nothing hostile survives the renderer.

To check a real instance, copy the pages' HTML into a Confluence space of yours — the content is plain
storage-compatible HTML — and export it there.

| Page | Covers |
| --- | --- |
| Rendering reference | The root: an inlined image, the table of contents |
| Structure | Headings 1–6, paragraphs, line breaks, rules, nested lists, definition lists, quotes, preformatted text, details |
| Tables | Header row, caption, merged cells, nested table, a 14-column table, an 80-row table |
| Links and images | Internal anchor, external link, link to another page of the tree, a scaled image |
| Inline formatting | Bold, italic, underline, strike-through, sub/superscript, code, keyboard, highlight, colour |
| Confluence macros | Info, note, warning and tip panels, status lozenges, code block, expand, table of contents, tasks |
| Languages and scripts | French, German, Polish, Greek, Russian, Japanese, Chinese, Arabic and Hebrew (right to left), symbols, emoji |
| Hostile content | Scripts, frames, objects, media, forms, external images and stylesheets, `url()`, event handlers, `javascript:` links |
| Long page → Depth 2…6 | Several pages of text; a branch five levels deep |

## Checklist

Export **Rendering reference** with its children, once in Word and once in PDF, with and without the
company template, and with a watermarked classification.

**Whole document**

- [ ] The table of contents lists every page, numbered `1`, `1.1`, `1.1.1`… down to *Depth 6*.
- [ ] The traceability footer on every page; the watermark across every page when the classification has
      one; the document properties (*File › Info* in Word) carry the export id and the requester.
- [ ] With a template: its header, footer, fonts and margins; body text in its body style.
- [ ] PDF: bookmarks follow the headings; the document is tagged (*Accessibility* in Acrobat, or
      `pdfinfo` shows `Tagged: yes`).

**Structure and tables**

- [ ] Heading levels are shifted under each page's title and stay in Word's navigation pane.
- [ ] Nested lists keep their levels and numbering restarts where it should.
- [ ] The wide table sits in a landscape section and fits the page; the long table repeats its header row on
      every page; merged cells are merged.

**Links, images, text**

- [ ] The internal link and the link to *Structure* jump inside the document; the external link opens a
      browser.
- [ ] Images are present and fitted to the page width.
- [ ] Every script is readable (no empty boxes): the fonts of the container cover them; Arabic and Hebrew
      read right to left.

**Macros**

- [ ] Panels show their text; status lozenges show their label; the code block is monospaced; the expand
      macro's content is shown.

**Hostile content**

- [ ] Exactly this remains of the page: its two paragraphs of text, *[External image, dropped]* (the
      external image's alternative text), the reference image (without its event handlers) and the link text
      *A script link, made inert*, no longer a link. No form field, frame, media or script output.
- [ ] Nothing was fetched while converting. `TestLibreOfficeReachesNoNetwork` checks it in CI with a
      listening trap; on a cluster, the NetworkPolicy's denied connections (if your network plugin logs
      them) should show none from the worker during the export.

Keep the exported files of an accepted version: comparing a new export with them, page by page, is faster
than going through the list again.
