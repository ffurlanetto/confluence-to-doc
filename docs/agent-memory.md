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
- LibreOffice invents style **ids** (`BodyText`, `TableContents`, `PreformattedText`, `InternetLink`,
  `StrongEmphasis`, `Emphasis`, `SourceText`, `Del`) but records the **Word style name** inside each of
  them: `InternetLink` declares `<w:name w:val="Hyperlink"/>`, `StrongEmphasis` declares `Strong`. The
  name is the interoperable key — Word does not translate it, so it also matches a template authored in
  another language. `docx.remapStyles` resolves by id first, then by name.
- Inline markup goes through those character styles rather than direct formatting: `<strong>` becomes
  `<w:rStyle w:val="StrongEmphasis"/>` with **no** `<w:b/>` on the run. Dropping an unknown character
  style therefore loses the formatting outright; the merge carries the definition over instead. (`<b>`
  and `<i>`, by contrast, do produce direct `<w:b/>`/`<w:i/>`.)
- LibreOffice's `Del`, `Ins` and `Q` styles have an **empty** `<w:rPr>`: `<del>`, `<ins>` and `<q>` lose
  their marking on import, and `<mark>` is not mapped at all. The renderer rewrites the first three into
  `<s>`, `<u>` and a background-coloured span, which do map. Measured, not assumed — see
  [html-mapping.md](html-mapping.md) for the whole table.
- **`<meta>` elements become document properties.** `author`, `description` and `keywords` land in
  `docProps/core.xml`, `classification` becomes `dc:subject`, and *any other name* becomes a custom
  property in `docProps/custom.xml`. That is the whole mechanism behind the export's metadata — there is
  no OOXML written by hand for it.
- **A CSS class becomes part of the style name**: `<p class="toc-entry-2">` yields a style named
  `Text Body.toc-entry-2`. That is the handle the DOCX step uses to find the table of contents again;
  nothing else survives the HTML round trip well enough to mark a paragraph. **Match the suffix only** —
  the part before the dot is LibreOffice's own base style, and it is `Text Body` in 7.4 but `Body Text`
  in later versions. CI caught that; the container used for local checks did not.
- `Template.Apply` keeps the *template* package as the base, so the generated `docProps` are only visible
  if their **package relationships and content types** are added too. A template carrying no properties of
  its own has neither, and the metadata silently disappears — including from the PDF.
- LibreOffice's own DOCX has **no theme part at all** and never emits `w:themeColor`. A company theme can
  therefore only come from the template, and it does: part, relationship and content type all survive the
  merge, so styles referencing `accent1` or the hyperlink colour resolve.
- **Tables are sized from their content, and overflow the page.** The HTML import uses A4 with 1134/567
  twip margins (10205 of text) and writes absolute `dxa` widths with `<w:tblLayout w:type="fixed"/>` — but
  it does **not** clamp to that width: a nine-column Confluence table came out at 16099 twips, 58% over.
  So `docx.fitTables` runs whether or not a template narrows the page.
- Nothing in the HTML changes that layout. `width="100%"` on the table, `table-layout: fixed`,
  `word-wrap: break-word` — all four variants produced a byte-identical grid. The fix has to be in the
  DOCX step.
- **Scaling proportionally is not enough.** LibreOffice's narrow columns are already close to unreadable,
  and the same factor finishes them off: a "Data Centre" heading set one letter per line. `redistribute`
  holds every column above a floor (`minColumnWidth`, ~1.2 cm) and takes the cost from the columns above
  it.
- A **bullet inside a table cell** carries `<w:ind w:left="709"/>` whatever the column is worth. In a
  squeezed cell that leaves a couple of characters per line, which looks far worse than the column width
  alone suggests. `capIndents` bounds it to a quarter of the cell.
- Beyond a certain width no redistribution helps, and the answer is a **landscape section**: an empty
  paragraph whose `w:pPr` carries the *outgoing* section's properties closes a section, so
  portrait-break, table, landscape-break puts one table sideways and returns to portrait. Inside
  `w:pPr` the schema puts `w:sectPr` **last**, after `w:rPr` — the other order is rejected. The landscape
  page is derived from the template's own `w:pgSz` (swap `w:w`/`w:h`, add `w:orient="landscape"`), so it
  works for any template and keeps the header references.
- **CSS widths on tables are ignored**, both `table { width: … }` and `<col style="width: …">`. LibreOffice
  sizes columns from their content. The HTML *attribute* `width="100%"` is honoured, and is the only way
  to get a relative `<w:tblW w:type="pct"/>` out of the import.
- **`page-break-inside: avoid` maps to `<w:keepLines/>`** — on `p`, `li`, `blockquote`, `pre` and headings.
  Where it lands depends on how it is written: a **stylesheet rule** becomes part of a generated paragraph
  style (which the Word template merge throws away), an **inline style** becomes direct formatting on the
  paragraph (which survives). The renderer therefore writes it inline; see `exporter.keepTogether`.
- `page-break-after: avoid` is **not** mapped: LibreOffice emits no `<w:keepNext/>` for it.

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
- Swapping the section properties changes the printable width under content that was already laid out.
  `fitTables` scales table widths (`w:tblW`, `w:gridCol`, `w:tcW`, `dxa` only) by truncating integer
  division, so the column grid can only end up narrower than the page, never wider. Nested tables are
  scaled with the table that holds them, not measured against the page on their own.

### Telemetry (`internal/observability`)

- Instruments are package-level variables created against the **global** meter provider before `Setup`
  runs. OpenTelemetry's global registry re-points them once the real provider is installed; that
  behaviour is what `TestInstrumentsReachTheProvider` pins down. Do not "fix" it with lazy initialisation.
- The global meter provider can only be replaced once per process, which is why the package test installs
  it in `TestMain` and shares one manual reader across tests.
- Telemetry is off unless `OTEL_EXPORTER_OTLP_ENDPOINT` is set, but the propagators are installed either
  way, so inbound `traceparent` headers are honoured even when nothing is exported.
- Only the endpoint, protocol, service name, sampler ratio and export interval go through
  `internal/config`. Headers, TLS and compression are read by the OTLP exporters straight from the
  standard `OTEL_EXPORTER_OTLP_*` variables — do not duplicate them in the config package.
- An export job is deliberately a **new root trace** (`trace.WithNewRoot`): it runs long after the request
  that queued it, and a single trace spanning the queue wait would be useless. `c2d.export.id` connects
  the two.
- The observable gauge callback swallows database errors on purpose: returning one marks the whole metric
  collection as failed.
- `TELEMETRY_METRIC_PREFIX` renames metrics through an SDK **View**, and deliberately matches only names
  under `observability.MetricNamespace` (`c2d.`). Semantic-convention metrics keep their names: those are
  the contract dashboards rely on, and services are distinguished by resource attributes
  (`service.name`, `service.namespace`, `OTEL_RESOURCE_ATTRIBUTES`) instead.
- The view leaves `Stream.Aggregation` unset so each instrument keeps its default aggregation *and* the
  bucket boundaries advised at creation — a test pins that down, because setting an aggregation there
  would silently flatten the custom histogram buckets.
- Resource attributes are not Prometheus labels by default: the collector's Prometheus exporter only
  derives `job` from `service.namespace`/`service.name`. `resource_to_telemetry_conversion` promotes the
  rest, at the cost of `host.name` and `telemetry.sdk.*` becoming labels too.
- **Jaeger does not implement the OTLP metrics service.** Pointing the application straight at it makes
  every metric export fail with `unknown service opentelemetry.proto.collector.metrics.v1.MetricsService`.
  The dev stack therefore sends OTLP to a collector, which fans out to Jaeger and to its own log.

### Kubernetes chart

- The chart lives in `charts/confluence-to-doc/` because that is where `chart-releaser` and
  `chart-testing` look by default.
- A worker serves no API, yet Kubernetes still needs probe endpoints, which is why `cmd/server` starts a
  health-only HTTP server when `APP_ROLE=worker`. Remove it and worker pods lose their liveness probe.
- API and worker pods share nothing, so filesystem storage needs ReadWriteMany; the chart refuses the
  combination rather than letting exports vanish between pods.
- `readOnlyRootFilesystem: true` is safe and verified: LibreOffice only writes under `/tmp` and `$HOME`,
  both `emptyDir`, and the converter points `HOME` at its own working directory anyway.

### Local stack (compose)

- `devmocks` joins the app's network namespace (`network_mode: service:app`). Restarting `app` alone
  leaves devmocks attached to the dead namespace, the mock IdP becomes unreachable, and the app exits
  after its OIDC retries. Recreate the stack instead of restarting a single container.
- Pin image tags that exist: `jaegertracing/all-in-one:1.62` never did, and compose only fails at pull
  time, which no CI job exercises.

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

### Audit trail (`internal/audit`)

- The log line is written **before** the database insert, and independently of it: the SIEM copy must be
  complete even during a database outage. Do not reorder.
- `Record` uses `context.WithoutCancel` for the insert: a client that disconnects right after downloading
  must not erase the trace of the download.
- A download served as several `Range` requests is recorded once, for the request starting at byte 0 (or
  without `Range`).
- `X-Forwarded-For` is read **right to left** and only when the peer is in `TRUSTED_PROXIES`: the leftmost
  entries are written by the client and can be anything.
- Audit pagination orders by `id`: ids are UUIDv7, time-ordered, which gives a stable cursor without a
  composite one.
- The `audit_events` trigger rejects `UPDATE` only. `DELETE` stays possible because the retention purge needs
  it; the minimum `AUDIT_RETENTION` (30 days) is enforced by the configuration, not the database.

### Document marking (`internal/docx/marking.go`)

- **LibreOffice recognises Word's watermark** — a VML `v:shape` with `id="PowerPlusWaterMarkObject"` and a
  `v:textpath` in a header part — and draws it in the PDF. DrawingML text boxes would need far more XML for
  the same result. The header root must declare the `v`, `o` and `w10` namespaces; template headers usually
  do not, so `appendToHdrFtr` adds them.
- LibreOffice's own DOCX writes `w:footer="0"` in `w:pgMar`: a footer created there sits on the paper's edge.
  `clearOfTheEdge` moves it to 6 mm, only for sections that get our footer part.
- With `<w:titlePg/>` a section's first page uses the `first` header/footer, and with
  `<w:evenAndOddHeaders/>` in `settings.xml` even pages use `even` ones. A missing variant shows *nothing*
  on those pages, so `Mark` adds references for them too.
- Header and footer references must come **first** in `w:sectPr`; landscape sections (inside `w:pPr`) need
  them as well, or the sideways pages lose the marking.
- The watermark is drawn, not written: `pdftotext` does not see it. Tests check the footer text in the PDF
  and the watermark in the DOCX headers.
