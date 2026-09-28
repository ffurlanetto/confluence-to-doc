# ADR 0007 — OpenTelemetry instead of Prometheus

- Status: accepted
- Date: 2026-09-27
- Supersedes the monitoring part of the initial setup (a `/metrics` endpoint scraped by Prometheus)

## Context

The first implementation exposed Prometheus metrics on a separate listener (`METRICS_ADDR`, `/metrics`).
That covers "how many exports failed", but not "why was this export slow": the pipeline crosses an HTTP
request, a queue, a Confluence crawl, LibreOffice and a storage backend, and a counter cannot show where
the time went. It also required a pull-based network path to every instance, which is awkward for workers.

## Decision

Replace the Prometheus client with OpenTelemetry, exporting **traces and metrics over OTLP**.

- **Feature flag by configuration**, consistent with S3 and the Word template: `OTEL_EXPORTER_OTLP_ENDPOINT`
  enables telemetry; without it the global providers stay no-op and nothing is exported.
- **Standard variables**: only the endpoint, protocol, service name, sampler ratio and export interval go
  through `internal/config`. Headers, TLS, compression and resource attributes are read by the OTLP
  exporters themselves, so the deployment uses the vocabulary operators already know.
- **Explicit exporters** (`otlptracegrpc`/`otlptracehttp`, `otlpmetricgrpc`/`otlpmetrichttp`) rather than
  the `autoexport` helper, which would pull a Prometheus bridge back into the dependency tree.
- **Traces**: `otelhttp` on the server and on the Confluence client; an export job opens a *new root trace*
  with a span per pipeline stage. A job is not a child of the request that queued it — it runs long after,
  and a trace spanning the queue wait would be noise. `c2d.export.id` connects the two.
- **Logs** stay on stdout, where the platform collects them, but carry `trace_id` and `span_id` when they
  are emitted inside a span.
- Instruments are package-level and created against the global provider before it is installed;
  OpenTelemetry re-points them on `Setup`, which a test pins down.

## Consequences

- ✅ Latency is attributable: crawl, render, convert and each Confluence call are separate spans.
- ✅ Push-based, so workers need no inbound network path, and the `/metrics` listener disappears.
- ✅ Any OTLP backend works (Jaeger, Tempo, Datadog, Honeycomb, or a collector fanning out — including to
  Prometheus, if that is where the metrics should ultimately land).
- ✅ Off by default: no endpoint, no telemetry, no cost.
- ⚠️ **Breaking for deployments that scrape `/metrics`**: `METRICS_ADDR` is gone, and a collector is now
  required to see metrics. Metric names also change (`c2d_exports_created_total` → `c2d.exports.created`,
  which a Prometheus exporter renders as `c2d_exports_created_total` again).
- ⚠️ A larger dependency tree (SDK plus four exporters) than the Prometheus client.
- ⚠️ Sampling is head-based; a trace kept for a failing export is not guaranteed unless the ratio is 1.
