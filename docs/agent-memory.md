# Agent memory

Durable project knowledge for AI agents and new contributors: the things the code does not say out loud,
and the traps that cost time to discover. [CLAUDE.md](../CLAUDE.md) is the short entry point loaded
automatically by agents; this file is the long-form memory behind it.

Keep it current: when a change invalidates an entry, update the entry in the same pull request. When a
debugging session uncovers a non-obvious fact, add it here rather than in a comment nobody will find.

## Ground rules

- **Everything is in English**: code, comments, tests, documentation, commit messages, UI text and
  user-facing error messages. No exceptions, including strings shown in generated documents.
- **Decisions go in an ADR** (`docs/adr/`), not in a commit message. Pick the next free number, keep the
  existing shape (Context / Decision / Consequences), and list the downsides honestly.
- **A skipped test is not a passing test.** `make test-backend` silently skips the PostgreSQL suites when
  `TEST_DATABASE_URL` is unreachable and the conversion suites when `soffice` is missing. Read the output.

## Hard-won facts

### Conversion (LibreOffice)

- The import filter matters: without `--infilter=HTML (StarWriter)` LibreOffice imports the HTML as a
  *Writer/Web* document, and page breaks, heading styles and the export filters then misbehave.
- **CSS borders on table cells are ignored** by the HTML import. Visible table rules come from the HTML
  `border` and `cellpadding` attributes, which `exporter.transformBody` adds on the fly.
- **CSS fonts become direct formatting** in the produced DOCX, and direct formatting overrides paragraph
  styles. This is why `RenderOptions.UseTemplateStyles` drops the typography CSS when a Word template is
  configured — otherwise the company font would never win.
- Each conversion gets a throw-away LibreOffice profile (`-env:UserInstallation=…`); without it parallel
  conversions collide over a shared profile.
- LibreOffice invents style names (`BodyText`, `TableContents`, `PreformattedText`, `BodyTextdoc-title`).
  Only `Heading1…6` overlaps with what a Word template defines — everything else is remapped.

### Word template (`internal/docx`)

- The **template package is the base** of the result, not the generated document: that keeps the corporate
  parts (styles, theme, headers, footers, settings, page setup) internally consistent. See ADR 0006.
- The result's `word/document.xml` keeps the **generated** root element, because it declares every
  namespace prefix the converter used; only the section properties come from the template.
- Relationship ids of the generated document collide with the template's by construction (both start at
  `rId1`), so image and hyperlink relationships are always reallocated.
- List numbering is merged with an **offset**, and the schema requires every `<w:abstractNum>` before the
  first `<w:num>`.
- A `.dotx` declares a *template* main content type; the result must declare the *document* one, or Word
  refuses to open it.
- Applying a template fails deterministically, so `export.retryable` treats `docx.ErrApply` as final: a
  retry would burn a LibreOffice run to reach the same error.
- The merge manipulates OOXML as text. `TestApplyProducesWellFormedXML` parses every part of the result;
  keep that test green, it is the cheapest guard against a broken package.

### Queue and workers

- The `exports` table **is** the queue. Every worker write is conditioned on `locked_by = workerID`; drop
  that and a zombie worker can overwrite a healthy one's result.
- The job context is deliberately detached from the pool context (`context.WithoutCancel`) so that a
  shutdown grants a grace period instead of killing work in flight.
- Deleting a row is how an export is cancelled: the worker notices through a failed heartbeat
  (`store.ErrLeaseLost`), stops and cleans up its blob.

### Storage

- `storage.BlobStore` has two implementations and one shared contract test suite. Any change to storage
  behaviour must keep `internal/storage/contract_test.go` green **for both**.
- S3 uploads spool to a temporary file and use a single `PutObject`: the SDK needs a replayable body of
  known length, and a failure then leaves no partial object. The SDK's transfer helpers were rejected —
  `feature/s3/manager` is deprecated and `transfermanager` is still v0.x.
- Reads are lazy ranged GETs so `http.ServeContent` can answer `Range` requests without pulling the whole
  object.

### Toolchain and dependencies

- `backend/go.mod` pins `toolchain go1.26.8` while CI runs Go 1.27; both work, but a local Go older than
  the toolchain line will try to download it.
- **TypeScript stays on 6.x**: `typescript-eslint` 8.x requires `typescript <6.1`, so `npm ci` breaks on
  TypeScript 7. Dependabot is configured to ignore that major; revisit when typescript-eslint catches up.
- Test doubles live in `internal/confluence/fake` and `internal/auth/oidcmock`, and `gofakes3` fakes S3.
  None of them may ever be wired into `cmd/server`.

## Verification habits

- Run the narrowest test first, then `make check`.
- For anything touching the generated document, the meaningful check is the real LibreOffice round-trip in
  `internal/converter` — it runs in CI even when it skips locally.
- The mock stack (`make dev-mocks`) gives a full end-to-end run with no external service: PAT `dev-pat`,
  demo tree under “Documentation”.
