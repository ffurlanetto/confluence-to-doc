# Architecture

## Overview

A single Go binary embeds three components, selected with `APP_ROLE`:

| Role     | Components                          | Scaling                                              |
| -------- | ----------------------------------- | ---------------------------------------------------- |
| `api`    | REST API, OIDC (BFF), static SPA    | horizontal, stateless (sessions live in the database) |
| `worker` | export worker pool + janitor        | horizontal; `EXPORT_WORKER_CONCURRENCY` per pod       |
| `all`    | both (default, ideal in development)| —                                                     |

PostgreSQL is the only infrastructure dependency: it holds both the data **and** the job queue.

## Export lifecycle

```mermaid
sequenceDiagram
  participant U as SPA
  participant A as API
  participant DB as PostgreSQL
  participant W as Worker
  participant C as Confluence
  participant L as LibreOffice
  participant S as Storage

  U->>A: POST /api/exports {pageId, format, includeChildren}
  A->>C: GET page (validates the PAT, permissions and title)
  A->>DB: INSERT exports (status=queued) if the user is under quota
  A-->>U: 202 Accepted
  loop polls every 2 s while an export is active
    U->>A: GET /api/exports
  end
  W->>DB: UPDATE … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED) → running + 1 min lease
  W->>C: root page + children (bounded concurrent crawl)
  W->>W: HTML assembly (shifted headings, numbering, TOC, inlined images)
  W->>L: soffice --convert-to docx
  L-->>W: plain DOCX
  W->>W: company Word template applied (when configured)
  opt PDF requested
    W->>L: soffice --convert-to pdf (from the templated DOCX)
  end
  W->>S: atomic write of <id>.<ext>
  W->>DB: status=succeeded, expires_at = now()+48h
  U->>A: GET /api/exports/{id}/download
  A->>S: stream (Range supported)
  Note over W,S: Janitor (every 10 min): file deleted, status=expired
```

States: `queued → running → succeeded | failed → expired`. A transient failure (Confluence 5xx/429, a
conversion crash) goes back to `queued` with exponential backoff up to `EXPORT_MAX_ATTEMPTS`. Deterministic
failures (invalid PAT, page not found, tree too large, timeout, template that cannot be applied) fail
immediately.

## Load control

| Mechanism                              | Setting                          |
| -------------------------------------- | -------------------------------- |
| Concurrent workers per instance        | `EXPORT_WORKER_CONCURRENCY`      |
| Parallel Confluence requests per job   | `CONFLUENCE_FETCH_CONCURRENCY`   |
| Active exports per user (429)          | `EXPORT_MAX_ACTIVE_PER_USER`     |
| Maximum tree size                      | `EXPORT_MAX_PAGES`               |
| Maximum job duration                   | `EXPORT_JOB_TIMEOUT`             |
| Maximum image size                     | `EXPORT_MAX_IMAGE_BYTES`         |
| Confluence `Retry-After` handling      | automatic (REST client)          |
| Requests per second to Confluence, all instances together | `CONFLUENCE_RATE_LIMIT` (a token bucket in PostgreSQL) |
| API requests per user / sign-ins per address (429) | `API_RATE_LIMIT`, `AUTH_RATE_LIMIT` |

Queue robustness:

- **Lease** — a worker refreshes `locked_until` every 20 s. If it dies, another worker picks the job up
  once the lease expires.
- **Ownership** — every worker write is conditioned on `locked_by = <worker>`, so a zombie worker cannot
  overwrite another's result.
- **Cancellation** — deleting a running export removes the row; the worker loses its lease, stops and
  cleans up.
- **Graceful shutdown** — on SIGTERM in-flight jobs get 20 s to finish, otherwise they return to the queue
  without consuming an attempt.

## Document rendering

`exporter.RenderHTML` produces one self-contained HTML document, which LibreOffice then converts:

- a cover page (title, date, source URL, page count) followed by a clickable table of contents;
- every page starts on a new page, with an `hN` heading where `N = depth + 1` (capped at 6) and a
  hierarchical number `1.2.3`;
- headings inside a page body are shifted below its title, so the Word/PDF outline mirrors the tree;
- images are downloaded with the user's PAT (same origin only), resized to the printable width and
  embedded as data URIs; an unreachable image degrades to its alternative text;
- links to pages included in the export become internal anchors, other links become absolute URLs;
- paragraphs, list items, quotations, code blocks and headings carry `page-break-inside: avoid`, so a
  block that no longer fits moves to the next page whole instead of being cut in two. It is written as an
  inline style on purpose: LibreOffice turns a stylesheet rule into a paragraph style, which the Word
  template merge would discard;
- a few elements LibreOffice does not map at all (`<del>`, `<ins>`, `<mark>`, task-list checkboxes) are
  rewritten into markup it does, so the marking they carry is not silently lost;
- document properties — title, author, description, keywords, classification and the Confluence source —
  are written as `<meta>` elements, which the converter turns into `docProps`;
- sanitisation: scripts, iframes, forms and event handlers are removed.

Both formats come out of the **same** DOCX: the HTML is converted to DOCX, the template (when configured)
is applied, every page is marked — a traceability footer, and a watermark for sensitive classifications
(`docx.Mark`, ADR 0009) — and a PDF is produced from that document rather than from the HTML. Word and PDF are then one
layout rather than two independent renderings of the same source.

The table of contents is written as paragraphs whose class carries the level; the DOCX step recognises
them, applies the reader's *TOC 1…9* styles and wraps them in a real Word `TOC` field.

Every element's fate is recorded in [html-mapping.md](html-mapping.md).

### Company Word template

When `WORD_TEMPLATE_PATH` is set, the document is produced inside the corporate template
(`internal/docx`): the template package is the base of the result — including its colour and font theme —
and only the generated body is injected into it, with images, hyperlinks, list numbering and style
references remapped to stay valid. Style references are resolved against the template by id and then by
style **name**, because LibreOffice and Word give the same style different ids.

Two consequences shape the pipeline:

- the HTML is rendered **without** its own typography, because the converter would turn font declarations
  into direct formatting that overrides the template's styles;
- PDFs are produced from the templated DOCX rather than from the HTML, so both formats share one layout;
- tables are rescaled as the template's page setup replaces the converter's. LibreOffice lays tables out
  on its own, wider page and records absolute column widths; without the rescale, a table wider than the
  company margins is simply clipped by the reader.

Details and authoring guidance: [word-template.md](word-template.md).

## Document storage

Documents go through the `storage.BlobStore` interface (`Put`, `Open`, `Delete`), with two implementations
selected by a feature flag at startup (see [configuration](configuration.md#document-storage-s3-feature-flag)):

| Backend    | Enabled by       | Notes                                                                          |
| ---------- | ---------------- | ------------------------------------------------------------------------------ |
| Local disk | default          | atomic write (temp file + rename); needs a shared volume when API and workers are separate |
| S3         | `S3_BUCKET` set  | single `PutObject` from a spooled temp file (no partial objects); lazy ranged GETs, so HTTP `Range` requests are served without downloading everything |

One contract test suite (`storage/contract_test.go`) runs against both backends; S3 is faked in memory with
`gofakes3`.

## Security

- **Authentication** — OIDC Authorization Code + PKCE + nonce, server side (BFF pattern). OAuth tokens
  never leave the backend; the browser only holds an opaque session cookie (`HttpOnly`, `SameSite=Lax`,
  `Secure` + `__Host-` prefix over HTTPS). Only its SHA-256 is stored. Sessions are re-checked with the
  provider and ended by back-channel logout (ADR 0012).
- **Personal data** — users can delete their account; unused accounts are deleted after a notice; see
  [gdpr.md](gdpr.md).
- **CSRF** — a mandatory `X-CSRF-Protection: 1` header plus `Origin` / `Sec-Fetch-Site` checks.
- **PAT** — validated against Confluence before being saved, encrypted with AES-256-GCM bound to the user
  id under a key ring that can be rotated without users re-entering their tokens (ADR 0011), never returned by the API, and only ever sent to the configured Confluence origin (redirects to
  another host are blocked).
- **Authorisation** — every export query filters on `user_id` (another user's export looks like a 404).
  Confluence permissions are those of the user's PAT. The administration API (`/api/admin/*`) requires the
  admin role, granted by the identity provider's groups at each sign-in (ADR 0008).
- **Audit trail** — security-relevant actions are recorded in an append-only table and as structured log
  lines for the SIEM (`internal/audit`, ADR 0008).
- **Headers** — strict CSP (`default-src 'self'`), `frame-ancestors 'none'`, `nosniff`, HSTS over HTTPS.
- **Conversion isolation** — LibreOffice fetches whatever an imported HTML document points at. The renderer
  therefore removes every reference that loads something (remote images, stylesheets, backgrounds, media,
  `url()` in styles…: only inlined `data:` images survive), and the LibreOffice process runs with its HTTP
  traffic sent to a proxy that does not exist, so a reference that slipped through fails instead of
  reaching the network. In Kubernetes, NetworkPolicies limit the pods to the services they use (ADR 0010).
- **Files** — storage keys are generated and validated server-side (no path traversal), written
  atomically, and served as `Content-Disposition: attachment`.

## Observability

- Structured JSON logs (`log/slog`), one line per request with a `request_id`.
- `/healthz` (liveness), `/readyz` (database reachable).
- OpenTelemetry traces and metrics pushed over OTLP, enabled by `OTEL_EXPORTER_OTLP_ENDPOINT`
  (see [configuration](configuration.md#telemetry-opentelemetry)). An HTTP request is one trace; an export
  job is another, with a span per pipeline stage — crawl, render, convert — and one per Confluence call.
  Metrics cover the queue depth, export outcomes, durations and sizes.
- Log lines emitted inside a span carry `trace_id` and `span_id`, which links the three signals.

## Possible next steps

- Link an export job's trace to the request that queued it, by storing the W3C trace context on the row
  (a migration and a span link); today the export id is the connection between the two traces.

- Confluence Cloud: email + API token (Basic) authentication alongside the Bearer PAT.
- Export completion notifications (email, SSE); `LISTEN/NOTIFY` to wake remote workers.
- Per-user or per-space template selection, on top of the current global template.
