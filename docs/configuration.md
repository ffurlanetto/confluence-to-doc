# Configuration

All configuration comes from environment variables (12-factor), loaded and validated at startup by
`backend/internal/config`. An invalid configuration stops the process and reports **every** error at once.

## Required

| Variable              | Description                                                                          |
| --------------------- | ------------------------------------------------------------------------------------ |
| `DATABASE_URL`        | PostgreSQL DSN, e.g. `postgres://user:pass@host:5432/db?sslmode=require`              |
| `OIDC_ISSUER_URL`     | OIDC issuer URL (discovered through `/.well-known/openid-configuration`)              |
| `OIDC_CLIENT_ID`      | OAuth2 client id (confidential client)                                                |
| `OIDC_CLIENT_SECRET`  | Client secret                                                                         |
| `CONFLUENCE_BASE_URL` | Confluence Server/Data Center base URL (including any context path)                   |
| `ENCRYPTION_KEY`      | 32 random bytes, base64 (`openssl rand -base64 32`), used to encrypt the users' PATs — or `ENCRYPTION_KEYS`, a key ring (see [Rotating the encryption key](#rotating-the-encryption-key)) |

> ⚠️ Replacing the key outright makes existing PATs unreadable: every user has to enter theirs again.
> Rotate it through `ENCRYPTION_KEYS` instead.

## Optional

| Variable                       | Default                 | Description                                                    |
| ------------------------------ | ----------------------- | -------------------------------------------------------------- |
| `APP_ROLE`                     | `all`                   | `all`, `api` or `worker`                                        |
| `PUBLIC_URL`                   | `http://localhost:8080` | Public URL; sets the OIDC redirect URI and the origin check. With `https`, cookies become `Secure` and HSTS is sent |
| `HTTP_ADDR`                    | `:8080`                 | HTTP listener                                                   |
| `STATIC_DIR`                   | _(empty)_               | Directory of the built SPA to serve                             |
| `LOG_LEVEL`                    | `info`                  | `debug`, `info`, `warn`, `error`                                |
| `LOG_FORMAT`                   | `json`                  | `json` or `text`                                                |
| `OIDC_SCOPES`                  | `openid profile email`  | Requested scopes                                                |
| `SESSION_TTL`                  | `12h`                   | Session lifetime                                                |
| `SESSION_REVALIDATE_INTERVAL`  | `15m`                   | How often a session is re-checked with the identity provider through its refresh token; a disabled account then loses access (`0` = never) |
| `ACCOUNT_RETENTION`            | `4320h` (180 days)      | Accounts unused for that long are deleted with their token and exports (`0` = never) |
| `ACCOUNT_DELETION_NOTICE`      | `360h` (15 days)        | Warning given before an inactive account is deleted; no account is deleted without it |
| `OIDC_GROUPS_CLAIM`            | `groups`                | Claim listing the user's groups or roles; a dotted path for nested claims (`realm_access.roles`) |
| `OIDC_ADMIN_GROUPS`            | _(empty)_               | Comma-separated groups (or roles) granting the administration pages. Empty = no administrator |
| `AUDIT_RETENTION`              | `8760h`                 | How long audit events stay in the database (minimum `720h`)     |
| `TRUSTED_PROXIES`              | _(empty)_               | Comma-separated CIDRs of the reverse proxies allowed to set `X-Forwarded-For` (client address in the audit trail) |
| `CONFLUENCE_TIMEOUT`           | `30s`                   | Timeout of a single Confluence request                          |
| `CONFLUENCE_FETCH_CONCURRENCY` | `4`                     | Parallel Confluence requests per export                         |
| `CONFLUENCE_RATE_LIMIT`        | `10`                    | Requests per second that **all instances together** may send to Confluence (`0` = unlimited) |
| `CONFLUENCE_RATE_BURST`        | `20`                    | Requests allowed in a burst above that pace                     |
| `API_RATE_LIMIT`               | `300`                   | API requests per minute per user, per API instance (`0` = unlimited); above it, `429` with `Retry-After` |
| `API_RATE_BURST`               | `60`                    | API requests a user may send in a burst                         |
| `AUTH_RATE_LIMIT`              | `30`                    | Sign-in requests per minute per client address, per API instance (`0` = unlimited) |
| `EXPORT_RETENTION`             | `48h`                   | How long a document stays downloadable                          |
| `EXPORT_WORKER_CONCURRENCY`    | `2`                     | Exports generated simultaneously per worker instance            |
| `EXPORT_MAX_ACTIVE_PER_USER`   | `5`                     | Queued or running exports per user                              |
| `EXPORT_MAX_PAGES`             | `500`                   | Maximum pages per export                                        |
| `EXPORT_MAX_ATTEMPTS`          | `3`                     | Attempts on transient errors                                    |
| `EXPORT_JOB_TIMEOUT`           | `15m`                   | Maximum duration of one export                                  |
| `EXPORT_MAX_IMAGE_BYTES`       | `10485760`              | Maximum size of an embedded image                               |
| `EXPORT_POLL_INTERVAL`         | `2s`                    | Queue polling interval                                          |
| `EXPORT_JANITOR_INTERVAL`      | `10m`                   | How often expired exports are purged                            |
| `EXPORT_STORAGE_DIR`           | `./data/exports`        | Directory of generated documents in local storage (ignored when S3 is enabled) |
| `SOFFICE_PATH`                 | `soffice`               | LibreOffice binary                                              |
| `WORD_TEMPLATE_PATH`           | _(empty)_               | Company Word template applied to every document, unless an administrator uploaded one |
| `DOCUMENT_CLASSIFICATION`      | _(empty)_               | Default classification, used when the requester picks none: subject property and page footer |
| `DOCUMENT_LANGUAGE`            | _(empty)_               | Default language of the documents (BCP 47, e.g. `fr-FR`), declared to screen readers in the tagged PDF. Empty keeps the template's, or `en-US` |
| `DOCUMENT_CLASSIFICATIONS`     | `Public, Internal, Confidential:watermark, Restricted:watermark` | Levels offered when exporting; `:watermark` sets the label diagonally across every page. `none` removes the choice |

## Telemetry (OpenTelemetry)

Traces and metrics are exported over OTLP as soon as `OTEL_EXPORTER_OTLP_ENDPOINT` is set; leaving it empty
disables telemetry, and the instrumentation stays a no-op. There is no `/metrics` endpoint to scrape:
the application pushes to a collector.

| Variable                       | Default             | Description                                             |
| ------------------------------ | ------------------- | ------------------------------------------------------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT`  | _(empty)_           | Collector endpoint; **enables telemetry**               |
| `OTEL_EXPORTER_OTLP_PROTOCOL`  | `grpc`              | `grpc` (port 4317) or `http/protobuf` (port 4318)       |
| `OTEL_SERVICE_NAME`            | `confluence-to-doc` | Service name reported to the backend                    |
| `OTEL_TRACES_SAMPLER_ARG`      | `1`                 | Head sampling ratio for root spans, between 0 and 1     |
| `OTEL_METRIC_EXPORT_INTERVAL`  | `1m`                | How often metrics are pushed                            |
| `TELEMETRY_METRIC_PREFIX`      | _(empty)_           | Prepended to this application's own metric names        |
| `OTEL_RESOURCE_ATTRIBUTES`     | _(empty)_           | Identity carried by every signal, e.g. `team=platform`  |

Every other standard `OTEL_*` variable — `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_CERTIFICATE`,
`OTEL_EXPORTER_OTLP_COMPRESSION`, `OTEL_RESOURCE_ATTRIBUTES` (to add `deployment.environment`, for
example) — is read directly by the OTLP exporters, so anything your collector needs is available.

### What is exported

**Metrics** — five of this application's own, plus the HTTP instrumentation's:

| Metric                           | Type      | Unit       | Attributes                                 |
| -------------------------------- | --------- | ---------- | ------------------------------------------ |
| `c2d.exports.created`            | counter   | `{export}` | `c2d.export.format`                        |
| `c2d.exports.finished`           | counter   | `{export}` | `c2d.export.format`, `c2d.export.outcome`  |
| `c2d.export.duration`            | histogram | `s`        | `c2d.export.format`                        |
| `c2d.export.pages`               | histogram | `{page}`   | `c2d.export.format`                        |
| `c2d.queue.exports`              | gauge     | `{export}` | `c2d.queue.state`                          |
| `http.server.request.duration`   | histogram | `s`        | semconv, including `http.route`            |
| `http.server.request.body.size`  | histogram | `By`       | semconv, including `http.route`            |
| `http.server.response.body.size` | histogram | `By`       | semconv, including `http.route`            |
| `http.client.request.duration`   | histogram | `s`        | semconv (outbound Confluence calls)        |
| `http.client.request.body.size`  | histogram | `By`       | semconv (outbound Confluence calls)        |

Attribute values: `c2d.export.format` is `pdf` or `docx`; `c2d.export.outcome` is `succeeded`, `retry` or
`failed`; `c2d.queue.state` is `queued` or `running`. Server-side HTTP metrics carry `http.route` as the
route pattern (`/api/exports/{exportID}/download`), which keeps cardinality bounded. Every metric also
carries the resource attributes: `service.name`, `service.version`, `host.name`, and whatever
`OTEL_RESOURCE_ATTRIBUTES` adds.

#### Identifying the application and the team

Use resource attributes — that is what they are for, and they land on **every** signal, metrics and traces
alike, without touching a single metric name:

```bash
OTEL_SERVICE_NAME=confluence-to-doc
OTEL_RESOURCE_ATTRIBUTES="service.namespace=docs-platform,team=platform,deployment.environment=prod"
```

`service.namespace` and `team` are the conventional way to answer "whose service is this?". Nothing in the
application reads these variables: the OpenTelemetry SDK does, so any attribute you add is carried along.

How they surface depends on the backend. With the collector's Prometheus exporter, resource attributes are
**not** labels by default — you only get `job="docs-platform/confluence-to-doc"`, built from
`service.namespace` and `service.name`. Promote them when you want to query by team:

```yaml
exporters:
  prometheus:
    endpoint: 0.0.0.0:8889
    resource_to_telemetry_conversion:
      enabled: true   # team, deployment_environment… become labels on every series
```

That promotes *all* resource attributes, `host.name` and the `telemetry.sdk.*` trio included, so drop the
ones you do not want with a `resource` processor rather than paying for their cardinality.

#### Prefixing the metric names

`TELEMETRY_METRIC_PREFIX=acme` renames this application's own metrics — `acme.c2d.exports.created` — and
**leaves the semantic-convention ones alone**: `http.server.request.duration` keeps its standard name,
because that name is the contract your dashboards and backends rely on, and because a service is meant to
be told apart by its resource attributes rather than by renamed instruments.

A separator is added when you omit one; `acme.`, `acme_` and `acme-` are taken as written. An invalid
prefix is rejected at startup, even when telemetry is disabled.

It is not an `OTEL_*` variable on purpose: the specification defines none, and squatting that namespace
risks colliding with a future standard variable.

**Traces**: one trace per HTTP request, and one per export job — the job is a separate trace because it
runs long after the request that queued it. Inside a job you get `confluence.crawl`, `document.render`,
`document.convert`, and a span per Confluence HTTP call, which is where most of the time usually goes.
Spans carry `c2d.export.id`, so a failing export can be found from its identifier.

**Logs** stay on stdout, as containers expect, but every line emitted inside a span carries `trace_id` and
`span_id`, so a log line leads to its trace and back.

### Trying it locally

`make up` starts an OpenTelemetry Collector and a Jaeger alongside the stack, the same shape as a real
deployment: the application speaks OTLP to the collector, which forwards traces to Jaeger and logs metrics.

- Traces: run an export, then open <http://localhost:16686>.
- Metrics: `podman compose logs otel-collector` (or `docker compose logs`).

Jaeger stores traces only — it does not implement the OTLP metrics service — which is why the collector
sits in front of it.

## Company Word template

Setting `WORD_TEMPLATE_PATH` to a `.docx`/`.dotx` file makes every export — Word and PDF — come out in the
company format: header, footer, fonts and page layout. Leaving it empty keeps the built-in styling.

The file is read and validated at startup; every instance running workers needs it. An administrator can
also upload a template from the console, which then takes precedence until it is removed. How to prepare
the template and what it must contain (and must not: macros, external content) is covered in
[word-template.md](word-template.md).

## Document storage (S3 feature flag)

The storage backend is chosen at startup:

- **`S3_BUCKET` set → S3 storage** (AWS S3 or compatible: MinIO, Ceph RGW, Garage, Scaleway, OVH…);
- **otherwise → local disk** (`EXPORT_STORAGE_DIR`).

The choice is logged at startup (`document storage: S3` / `document storage: local disk`). With S3 the
bucket is checked at startup (`HeadBucket`), so a misconfiguration stops the process immediately.

| Variable               | Default       | Description                                                                   |
| ---------------------- | ------------- | ----------------------------------------------------------------------------- |
| `S3_BUCKET`            | _(empty)_     | Bucket name; **enables S3**                                                   |
| `S3_REGION`            | `us-east-1`   | Region                                                                        |
| `S3_ENDPOINT`          | _(empty)_     | URL of an S3-compatible service (e.g. `https://minio.example.com`); empty = AWS |
| `S3_ACCESS_KEY_ID`     | _(empty)_     | Static key; empty uses the AWS default credential chain (`AWS_*`, profile, IAM role / IRSA) |
| `S3_SECRET_ACCESS_KEY` | _(empty)_     | Matching secret (required when the key is set)                                |
| `S3_PREFIX`            | `exports/`    | Object key prefix inside the bucket                                           |
| `S3_FORCE_PATH_STYLE`  | `true` when `S3_ENDPOINT` is set, otherwise `false` | `endpoint/bucket/key` addressing (required by most compatible services) |

Minimum IAM permissions (on `arn:aws:s3:::<bucket>` and `arn:aws:s3:::<bucket>/<prefix>*`):
`s3:ListBucket` (bucket check, and so a missing object answers 404 rather than 403), `s3:PutObject`,
`s3:GetObject`, `s3:DeleteObject`.

The janitor deletes objects when they expire (48 h). As a safety net, add a lifecycle rule on the prefix
(e.g. expire after 3 days): an object removed by that rule is simply reported as not found on download.

## OIDC provider

Declare a **confidential** client with:

- the *Authorization Code* flow (with PKCE S256 if configurable);
- redirect URI: `${PUBLIC_URL}/auth/callback`;
- post-logout redirect URI: `${PUBLIC_URL}/`;
- scopes: `openid profile email`;
- **back-channel logout URI**: `${PUBLIC_URL}/auth/backchannel-logout`, with "session required" (`sid`) on
  if offered. Signing out or disabling a user at the provider then ends their sessions here at once.

Sessions are also re-checked every `SESSION_REVALIDATE_INTERVAL` with the refresh token the provider
issued at sign-in: a refusal ends the session, and group changes (the admin role) apply without waiting for
the next sign-in. Keycloak and Okta issue refresh tokens by default; **Entra ID** only with the
`offline_access` scope (`OIDC_SCOPES=openid profile email offline_access`). Without a refresh token, a
session lasts until `SESSION_TTL` unless a back-channel logout ends it.

## Notifications

Users are told when an export is ready or failed, when their Confluence token is about to expire (14 days
ahead) and when their inactive account is about to be deleted. Every notification is listed under
*Notifications* in the application (with an unread count in the menu, kept 90 days); users choose in
*Preferences* whether finished exports are notified and whether by email. Account notices go to every
channel the user set up, whatever their choices.

| Variable              | Default                 | Description |
| --------------------- | ----------------------- | ----------- |
| `SMTP_HOST`           | _(empty)_               | Mail relay; empty disables email |
| `SMTP_PORT`           | `587`                   | |
| `SMTP_SECURITY`       | `starttls`              | `starttls`, `tls` (implicit, port 465) or `none` (a local relay only; refused with credentials) |
| `SMTP_FROM`           | _(required with a host)_ | Sender, e.g. `Confluence Export <noreply@example.com>` |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | _(empty)_    | Relay credentials, if it asks for them |
| `TEAMS_WEBHOOK_HOSTS` | `logic.azure.com, powerplatform.com, webhook.office.com` | Host suffixes a user's Teams workflow URL may point to |

**Microsoft Teams.** Each user creates a workflow in Teams from the template *Send webhook alerts to a chat*
(or *to a channel*) and pastes its URL in *Preferences*, where a test message checks it. The URL is a
credential — anyone holding it can post in that chat — so it is stored encrypted and never returned. Since
the server makes a request to a URL a user provided, only HTTPS URLs on `TEAMS_WEBHOOK_HOSTS` are accepted
(checked when saved and before each request) and redirects are not followed; with NetworkPolicies, allow
egress to those hosts on port 443.

Deliveries are queued and sent by the workers, retried with backoff (1 minute to 4 hours) for about a day
before being given up. A message carries a title, a status and a link — never a document.

## Account lifecycle and personal data

- Users can **delete their account** from *Preferences*: token, preferences, sessions and exports with
  their documents are erased immediately (`account.delete` audit event).
- Accounts **unused for `ACCOUNT_RETENTION`** are deleted the same way (`account.purge`). Their owner is
  warned `ACCOUNT_DELETION_NOTICE` beforehand — by the notification channels configured, otherwise only in
  the logs — and signing in again cancels the deletion. Nobody is deleted without that notice, accounts
  already past the retention when the feature arrives included.
- The **Confluence token's expiry** is read from Data Center's token API when it is saved; users are warned
  in the application 14 days before.
- [gdpr.md](gdpr.md) is the record of processing to complete with your data protection officer.

### Rotating the encryption key

`ENCRYPTION_KEYS` holds several keys, newest first: `id:base64, id:base64`. The first encrypts; all of
them decrypt. Ids are 1–32 letters, digits, `-` or `_`. A single `ENCRYPTION_KEY` is the key ring
`default:<key>`.

1. Generate a new key and put it in front, keeping the old one (named `default` if it came from
   `ENCRYPTION_KEY`):
   ```
   ENCRYPTION_KEYS=2026-10:<openssl rand -base64 32>, default:<the old key>
   ```
2. Deploy. Tokens are re-encrypted with the new key in the background, by the workers' cleanup pass
   (every `EXPORT_JANITOR_INTERVAL`); each pass is recorded as a `keys.rotate` audit event, and the logs
   report any token no key can read.
3. Once a pass reports nothing left to rotate, remove the old key and deploy again.

A token whose key was removed too early is not deleted: its user is asked to enter it again, and putting
the key back makes it readable.

### Administrators

Every authenticated user can export. The **administration console** (`/admin`: export queue with cancel
and retry, users with blocking, usage, the Word template, the audit trail) is reserved to
members of `OIDC_ADMIN_GROUPS`, read from the claim named by `OIDC_GROUPS_CLAIM` in the ID token — or from
the UserInfo endpoint when the provider only exposes it there. The role is re-evaluated at every sign-in:
removing someone from the group takes effect at their next login (at most `SESSION_TTL` later).

| Provider | Typical setting |
| -------- | --------------- |
| Keycloak | Add a *Group Membership* mapper (claim `groups`, full path off) to the client, or use realm roles with `OIDC_GROUPS_CLAIM=realm_access.roles` |
| Entra ID | Prefer **app roles** (`OIDC_GROUPS_CLAIM=roles`, `OIDC_ADMIN_GROUPS=<role value>`): group claims carry object ids and are left out above 200 groups (“overage”) |
| Okta     | Add a `groups` claim to the ID token with a group filter |
| Others   | Any claim holding a string or a list of strings; namespaced claims such as `https://example.com/groups` work as-is |

## Document marking

Every page of every document — Word and PDF alike — ends with a traceability line:

> Exported by Ann Martin (ann.martin@example.com) on 2026-10-08 09:14 UTC · Confidential · Ref. 0199c0de-…

The reference is the export id, which leads to the export's events in the audit trail. The same id and the
requester's email are also written to the document properties (`Export ID`, `Exported by`).

When exporting, users pick a classification from `DOCUMENT_CLASSIFICATIONS`, or none — in which case
`DOCUMENT_CLASSIFICATION` applies, if set. Levels marked `:watermark` add the label, in capitals, diagonally
across every page, as a Word watermark (*Design › Watermark* shows it, and it can be removed there: it is a
deterrent and a reminder, not a protection). Labels are free text, up to 40 characters, without commas or
colons:

```
DOCUMENT_CLASSIFICATIONS=C0 – Public, C1 – Internal, C2 – Confidential:watermark, C3 – Secret:watermark
```

With a company Word template, the line is appended to the template's own footers and the watermark to its
headers; nothing of the template is replaced. A first page with a header or footer of its own gets the
marking too.

## Audit trail

Security-relevant actions are recorded: sign-in (and failed sign-in), sign-out, token saved or removed,
preference changes, export requested, generated, failed, downloaded, deleted and expired, refused access to
the administration pages, and reads of the audit trail itself. Each event carries the user, the target,
the outcome, the client address, the user agent and the request id.

Events are written to two places:

- the `audit_events` table, append-only (updates are rejected by a trigger), shown to administrators under
  **Audit** and purged after `AUDIT_RETENTION`;
- a JSON log line on stdout tagged `"event.category": "audit"`, with
  [Elastic Common Schema](https://www.elastic.co/guide/en/ecs/current/index.html) field names
  (`event.action`, `event.outcome`, `user.email`, `source.ip`, …). Route these lines to your SIEM: it is the
  tamper-proof copy, and it is written even when the database is unavailable.

Behind a reverse proxy, set `TRUSTED_PROXIES` to its address range, otherwise every event records the
proxy's address. Only proxies in that list may set `X-Forwarded-For`; it is read from the right, so a value
forged by the client is ignored.

The trail never contains a token, a cookie or document content — only titles, page ids, formats and sizes.

## Deployment

- Kubernetes: use the [Helm chart](../charts/confluence-to-doc/), which turns these variables into a
  ConfigMap and references your own Secrets.
- Image: `docker build --target runtime -t confluence-to-doc .` (LibreOffice included, non-root user,
  built-in `HEALTHCHECK` through `server healthcheck`).
- Put the application behind a TLS reverse proxy; `PUBLIC_URL` must be the public `https://` URL.
- With local storage, the `api` and `worker` roles must share `EXPORT_STORAGE_DIR` (RWX volume). With S3
  (`S3_BUCKET`) no shared volume is needed: API and workers can run on different machines.
- Back up PostgreSQL, and the encryption keys separately; the exported documents are ephemeral (48 h) and
  need no backup. See [operations/backup-and-recovery.md](operations/backup-and-recovery.md).
- Alert rules, a dashboard, service level objectives and runbooks: [operations/](operations/README.md).
- Every role serves `/healthz` and `/readyz` on `HTTP_ADDR`, workers included, so a worker pod can be
  probed like any other.
