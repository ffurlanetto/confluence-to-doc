# ADR 0002 — HTML → PDF/DOCX conversion with LibreOffice

- Status: accepted
- Date: 2026-09-27

## Context

Confluence exposes the rendered HTML of its pages (`body.export_view`). We need faithful PDF **and** Word
output (headings, tables, images) with a usable document outline.

Options considered: native DOCX generation in Go (expensive to make faithful), Pandoc (good DOCX, PDF via
a heavy LaTeX toolchain), Chromium (PDF only), Gotenberg (an extra service), headless LibreOffice.

## Decision

Assemble a single HTML document, then convert it with `soffice --headless` (import filter
“HTML (StarWriter)”) to `writer_pdf_Export` or `MS Word 2007 XML`. Each conversion uses a throw-away
LibreOffice profile, which makes parallel conversions safe (bounded by the worker pool).

## Consequences

- ✅ One engine for both formats; `hN` elements become real “Heading N” styles in Word and PDF bookmarks;
  page breaks, tables and data-URI images are embedded correctly (verified by tests).
- ✅ Hidden behind the `converter.Converter` interface, so it can be replaced (Gotenberg, Pandoc…) without
  touching the rest.
- ⚠️ A heavier Docker image (~400 MB) and 1–2 s of start-up per conversion.
- ⚠️ CSS support is partial: rendering relies on simple HTML attributes (for example table `border`).
