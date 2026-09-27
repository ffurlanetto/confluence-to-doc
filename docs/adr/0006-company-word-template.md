# ADR 0006 — Company Word template applied to generated documents

- Status: accepted
- Date: 2026-09-27

## Context

Exports must look like company documents: letterhead, footer, corporate fonts, page layout. LibreOffice
produces a correct but generic document, and `--convert-to` offers no way to apply a template.

Options considered:

1. **Drive LibreOffice with a Basic macro** that opens the template and injects the content — slow,
   fragile, and hard to test.
2. **Generate the DOCX natively in Go** from the template — a rewrite of the converter, with a large
   surface to get right (tables, images, page breaks).
3. **Post-process the produced DOCX**: merge it with the template package. No extra runtime dependency,
   deterministic, unit-testable.

## Decision

Option 3, with the **template as the base** of the result rather than the generated document: every part
of the template is kept (styles, theme, headers, footers, settings, page setup) and only the generated
body is injected into it (`internal/docx`).

That direction keeps the corporate package internally consistent and reduces the work to remapping what is
local to the generated document:

- **Images and hyperlinks** — copied under a namespaced media name, with freshly allocated relationship ids.
- **List numbering** — the generated definitions are appended to the template's, shifted past its ids.
- **Styles** — references the template does not define (the converter invents names such as `BodyText`)
  are remapped onto the template's default paragraph style.

Two consequences follow, and are deliberate:

- The HTML is rendered **without** its own typography when a template is configured: the converter would
  otherwise emit direct formatting, which wins over the template's styles and would defeat the purpose.
- **PDFs are produced from the templated DOCX**, not from the HTML, so both formats share one layout, at
  the cost of a second LibreOffice pass.

Provisioning is **global, by configuration** (`WORD_TEMPLATE_PATH`), not per user: the goal is one company
format, and a deployment artefact needs no upload endpoint, no per-user storage and no file validation of
untrusted input. The template is loaded and validated once at startup.

## Consequences

- ✅ Word and PDF carry the company header, fonts and page setup, with no extra service or dependency.
- ✅ Unchanged behaviour when no template is configured.
- ✅ Applying the template is pure Go: fully unit-tested, including XML well-formedness, with an
  end-to-end test over real LibreOffice output.
- ⚠️ Only `Heading 1…6` and the default paragraph style are honoured; any other style is flattened to the
  default. A template that relies on custom styles for body content will not see them applied.
- ⚠️ The template must define page setup and heading styles; this is validated (page setup) or documented
  (styles) in [word-template.md](../word-template.md).
- ⚠️ PDF generation costs a second conversion pass.
- ⚠️ The merge manipulates OOXML as text. It is guarded by tests, but a template produced by an unusual
  tool may need adjustment.
