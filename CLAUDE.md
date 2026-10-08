# CLAUDE.md — guide for AI coding agents (and humans)

This file is the entry point for anyone (human or agent) changing this repository.
Read it fully before editing code. Keep it up to date when conventions change.

## What this app does

Users pick a Confluence page; the app exports it **with all descendants, preserving the hierarchy**,
to DOCX or PDF. Protected by OIDC; Confluence is accessed with each user's PAT (stored encrypted).
Generation is asynchronous through a PostgreSQL-backed queue; files are downloadable for 48 h.

## Commands (always run from the repo root)

| Goal                         | Command                    |
| ---------------------------- | -------------------------- |
| Everything CI checks         | `make check`               |
| Format                       | `make fmt`                 |
| Backend tests (with PG)      | `make test-backend`        |
| Backend tests (no deps)      | `make test-backend-short`  |
| Frontend tests               | `make test-frontend`       |
| Run locally                  | `make dev-mocks` + `make dev-backend` + `make dev-frontend` |
| Full stack in Docker         | `make up`                  |
| End-to-end tests (browser)   | `make e2e` (needs PostgreSQL and LibreOffice) |
| Alert rules (promtool)       | `make check-alerts`        |
| Real Confluence check        | `make confluence-check` (read-only, see docs/operations) |

`make test-backend` expects PostgreSQL at `TEST_DATABASE_URL`
(default `postgres://c2d:c2d@localhost:5432/c2d`); each test creates and drops its own database.
Conversion tests need `soffice` and are skipped otherwise.

**Definition of done:** `make check` is green, new behaviour has tests, docs updated.

**Read [docs/agent-memory.md](docs/agent-memory.md) before changing the conversion, the Word template or
the queue.** It holds the non-obvious facts behind this codebase; add to it when you learn one.

## Architecture map

```
HTTP request ─► httpapi (router, CSRF, auth middleware) ─► account / export services ─► store (PostgreSQL)
                                                                                     └► storage (local disk | S3)
Worker loop  ─► export.Pool ─► exporter.BuildTree (confluence client) ─► exporter.RenderHTML
                             ─► converter (LibreOffice) ─► docx (company Word template) ─► storage
Janitor      ─► store.ListExpired ─► storage.Delete ─► store.MarkExpired
```

- `backend/internal/domain` has the core types and sentinel errors; **no infrastructure imports**.
- Packages depend on small interfaces declared **by the consumer** (e.g. `export.Repo`, `httpapi.Accounts`).
- `internal/confluence/fake` and `internal/auth/oidcmock` are test/dev doubles — never wire them in `cmd/server`.
- Frontend: `src/api/client.ts` is the only place calling `fetch`; components use hooks from `src/api/hooks.ts`.
  Types in `src/api/types.ts` mirror the Go DTOs in `backend/internal/httpapi/handlers.go` — change both together.

- Document storage goes through `storage.BlobStore`; the backend is picked in `cmd/server` (`newBlobStore`):
  S3 when `S3_BUCKET` is set, local disk otherwise. Any change to storage behaviour must keep
  `internal/storage/contract_test.go` passing for **both** backends.
- `internal/observability` owns OpenTelemetry. Instruments are package-level and created against the
  global provider, which is a no-op until `Setup` runs — OTel re-points them, so never create instruments
  lazily "to be safe". Telemetry is enabled by `OTEL_EXPORTER_OTLP_ENDPOINT`; there is no `/metrics`.
- `internal/docx` applies the company Word template. `internal/doctemplate` chooses it per export: the one
  uploaded from the admin console (PostgreSQL), else `WORD_TEMPLATE_PATH` (validated at startup), else none.
  The worker reads it **once per export** and passes it to both the renderer (`UseTemplateStyles`) and
  `converter.Convert`; PDFs are produced from the templated DOCX. Every template goes through
  `docx.checkSafe` (no macros, ActiveX, external content). See [docs/word-template.md](docs/word-template.md),
  ADR 0006 and ADR 0014.
- `internal/admin` holds the admin console's use cases (queue, cancel/retry, users/blocking, usage from the
  audit trail). Every admin endpoint records an audit event, reads included.
- `e2e/` (Playwright) runs the real server against `cmd/devmocks`; the fake Confluence also serves the
  reference documents (`fake.SeedReference`, page 900). Operations docs live in `docs/operations/`.

## Conventions

### Go
- Go 1.26, standard library first. Errors: wrap with `%w`, compare with `errors.Is/As`, sentinel errors in `domain`.
- Logging: `log/slog` with key/value pairs; never log secrets (PAT, tokens, cookies) or document contents.
- Every DB write that a worker performs is guarded by `locked_by = workerID` (lease ownership). Keep it that way.
- User-facing error text is English and produced in one place (`httpapi/errors.go`, `export.UserMessage`).
- Tests: table-driven where it helps; use `fake.Server` for Confluence and `testutil.NewStore` for PostgreSQL.
- Lint: `golangci-lint` (config in `backend/.golangci.yml`). A `//nolint` needs a justification comment.

### TypeScript / React
- Strict TS, no `any`. Data fetching only via TanStack Query hooks.
- Accessibility: every input has a label, interactive elements are buttons/links, tests query by role/label.
- Tests with Vitest + Testing Library, mocking `fetch` through `src/test/utils.tsx` (`mockApi`).

### Database
- Migrations are forward-only SQL files in `backend/internal/store/migrations/NNNN_description.sql`,
  embedded and applied at startup. Never edit an applied migration: add a new one.

## Security invariants (do not break)

1. The Confluence base URL comes from configuration only — never from user input (SSRF / token leak).
2. The PAT is only sent to that exact origin (`confluence.Client.sameOrigin`, redirect guard).
3. PATs are encrypted with AES-GCM bound to the user id; they are never returned by the API.
4. Every export query filters on `user_id` (ownership): another user's export must look like a 404.
5. State-changing requests require the `X-CSRF-Protection: 1` header and a same-origin `Origin`.
6. Sessions: opaque random token in an HttpOnly SameSite=Lax cookie; only its SHA-256 is stored.
7. Content from Confluence is sanitised (`exporter.droppedElements`, event handlers stripped) before conversion,
   and nothing in the HTML may make LibreOffice load a resource: only `data:` images, no `url()`, no media
   (`exporter.loadingAttributes`). LibreOffice runs with its HTTP proxy pointed at nothing (`converter.isolatedEnv`).
8. Security-relevant actions are recorded through the `Auditor` (`internal/audit`): a new endpoint that changes
   state, reveals a document or touches credentials records an event. Events never contain secrets or content.
9. `/api/admin/*` routes sit behind `requireAdmin`; the admin flag comes from the IdP groups only.

## Language

Everything in this repository is written in English: code, comments, tests, documentation, commit
messages, UI text, and the strings that appear inside generated documents.

## Working style for agents

- Start by reading the relevant package and its tests; mirror existing patterns.
- Make small, focused changes; run the narrowest test first, then `make check`.
- When adding an API endpoint: handler + route + DTO + `handleError` mapping + API test + TS type + hook.
- When adding config: `internal/config` (+ test), `.env.example`, `docs/configuration.md`.
- Record significant design decisions as a new ADR in `docs/adr/`, and non-obvious findings in
  `docs/agent-memory.md`.
- Do not add dependencies casually; justify them in the PR description.
