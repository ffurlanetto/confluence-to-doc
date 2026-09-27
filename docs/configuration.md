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
| `ENCRYPTION_KEY`      | 32 random bytes, base64 (`openssl rand -base64 32`), used to encrypt the users' PATs  |

> ⚠️ Changing `ENCRYPTION_KEY` makes existing PATs unreadable: every user has to enter theirs again.

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
| `CONFLUENCE_TIMEOUT`           | `30s`                   | Timeout of a single Confluence request                          |
| `CONFLUENCE_FETCH_CONCURRENCY` | `4`                     | Parallel Confluence requests per export                         |
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
| `WORD_TEMPLATE_PATH`           | _(empty)_               | Company Word template applied to every document                 |

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

The file is read and validated at startup; every instance running workers needs it. How to prepare the
template and what it must contain is covered in [word-template.md](word-template.md).

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
- scopes: `openid profile email`.

## Deployment

- Image: `docker build --target runtime -t confluence-to-doc .` (LibreOffice included, non-root user,
  built-in `HEALTHCHECK` through `server healthcheck`).
- Put the application behind a TLS reverse proxy; `PUBLIC_URL` must be the public `https://` URL.
- With local storage, the `api` and `worker` roles must share `EXPORT_STORAGE_DIR` (RWX volume). With S3
  (`S3_BUCKET`) no shared volume is needed: API and workers can run on different machines.
- Back up PostgreSQL; the exported documents are ephemeral (48 h) and need no backup.
