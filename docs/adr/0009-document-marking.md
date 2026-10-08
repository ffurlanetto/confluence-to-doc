# ADR 0009 — Marking every page: traceability footer and classification watermark

- Status: accepted
- Date: 2026-10-08

## Context

Confluence permissions stop at the download. From then on the document is forwarded, printed and filed, and
nothing in it says where it came from, who extracted it or how sensitive it is. The audit trail (ADR 0008)
answers "who exported what" from the application's side; the document itself must answer it from the
reader's side, on paper included.

## Decision

**Every page of every document is marked**, Word and PDF alike, by `docx.Mark` on the delivered DOCX —
after the company template is applied and before the PDF is converted from it, so both formats carry the
same marking:

- a **footer line**: "Exported by *name (email)* on *date* UTC · *classification* · Ref. *export id*". The
  export id leads to the export's events in the audit trail. It is always on: traceability that can be
  switched off is not traceability.
- for classifications configured with `:watermark`, a **diagonal watermark** with the label in capitals,
  written the way Word's own *Design › Watermark* stores it (a VML text path named
  `PowerPlusWaterMarkObject` in the page headers). Word shows it as a watermark, and LibreOffice recognises
  it and draws it in the PDF.
- the export id and the requester's email as **custom document properties**.

Headers and footers that exist (a company template's) are **extended, never replaced**; a section without
one gets a part of its own. First-page and even-page variants are covered, so no page escapes.

**Classification** is a configurable list (`DOCUMENT_CLASSIFICATIONS`), chosen per export and optional:
without a choice, `DOCUMENT_CLASSIFICATION` applies if set. The chosen label is stored with the export,
audited with it, written to the subject property and printed in the footer.

## Consequences

- The marking is a deterrent and a reminder, not a protection: a Word user can remove a watermark, and a
  PDF can be edited. Protection against extraction is the access control and the audit trail.
- The footer line adds one line of small print under each footer; a template with a tight bottom margin may
  see its body area shrink slightly. When the converter put the footer on the paper's edge (LibreOffice's
  own DOCX does), it is moved 6 mm in so printers do not cut it.
- The page header and footer parts are edited as text, like the rest of `internal/docx`; the tests parse
  every part to keep that surgery honest.
- Watermark text is not extractable as text from the PDF (it is drawn as a shape), so search and copy do
  not pick it up — which is the expected behaviour of a watermark.
