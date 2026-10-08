# Monitoring

The application pushes traces and metrics over OTLP ([ADR 0007](../adr/0007-opentelemetry-instead-of-prometheus.md)).
The alert rules and the dashboard shipped with the chart assume those metrics end up in **Prometheus**
through an **OpenTelemetry collector**, which is the common setup; any other backend can reuse the queries
below once the names are translated.

```
app (api, worker) ──OTLP──► OpenTelemetry collector ──► Prometheus ──► Alertmanager / Grafana
                                                   └──► Tempo, Jaeger… (traces)
```

## Enabling it

1. Point the application at the collector (chart: `telemetry.enabled`, `telemetry.endpoint`).
2. Give the deployment an identity with `telemetry.resourceAttributes` (`service.namespace` at least): the
   collector turns `service.namespace/service.name` into the `job` label the rules select on.
3. Export to Prometheus from the collector, for instance:

   ```yaml
   receivers:
     otlp:
       protocols: { grpc: {}, http: {} }
   exporters:
     prometheusremotewrite:
       endpoint: https://prometheus.example.com/api/v1/write
       resource_to_telemetry_conversion: { enabled: false }
   service:
     pipelines:
       metrics: { receivers: [otlp], exporters: [prometheusremotewrite] }
   ```

   The `prometheus` exporter (scraped by Prometheus) works the same way. Keep the default name translation
   (`add_metric_suffixes: true`): the rules expect `_total` and unit suffixes.
4. Install the rules and the dashboard with the chart:

   ```yaml
   monitoring:
     prometheusRule:
       enabled: true          # needs the Prometheus Operator
       labels: { release: kube-prometheus-stack }   # whatever your Prometheus selects rules by
     grafanaDashboard:
       enabled: true          # a ConfigMap for the Grafana dashboard sidecar
   ```

   Without the operator, render the rules (`make check-alerts` leaves them in
   `charts/confluence-to-doc/tests/rules.yaml`) and load that file into Prometheus. The dashboard JSON is
   [`charts/confluence-to-doc/files/grafana-dashboard.json`](../../charts/confluence-to-doc/files/grafana-dashboard.json)
   and can be imported by hand.

If the collector labels the series differently, set `monitoring.selector` (for example
`service_name="confluence-to-doc", k8s_namespace_name="docs"`). A `telemetry.metricPrefix` is applied to
the application's metric names in the rules and the dashboard alike.

## Metrics

| OpenTelemetry name | Prometheus name | Labels | Meaning |
| --- | --- | --- | --- |
| `c2d.exports.created` | `c2d_exports_created_total` | — | Exports accepted into the queue |
| `c2d.exports.finished` | `c2d_exports_finished_total` | `c2d_export_format`, `c2d_export_outcome` (`succeeded`, `retry`, `failed`), `c2d_export_cause` (`user`, `system`, on retries and failures) | Export attempts finished |
| `c2d.export.duration` | `c2d_export_duration_seconds` (histogram) | `c2d_export_format` | Generation time of a successful export, queue wait excluded |
| `c2d.export.pages` | `c2d_export_pages` (histogram) | `c2d_export_format` | Pages per successful export |
| `c2d.queue.exports` | `c2d_queue_exports` (gauge) | `c2d_queue_state` (`queued`, `running`) | Queue depth, read from PostgreSQL by every instance that exports telemetry |
| `http.server.request.duration` | `http_server_request_duration_seconds` (histogram) | `http_route`, `http_request_method`, `http_response_status_code` | API requests (from `otelhttp`) |
| `http.client.request.duration` | `http_client_request_duration_seconds` (histogram) | `server_address`, `http_response_status_code` | Calls to Confluence, Teams and the identity provider |

`c2d_export_cause="user"` marks failures the requester can fix (token missing, invalid or expired, no
permission, page deleted, tree too large). They are reported to the user and do not count against the
[service level objectives](slo.md).

The queue gauge is read from PostgreSQL by every instance that exports telemetry, so all of them report
the same value: aggregate it with `max`, never `sum` (the rules and the dashboard do).

## Alerts

Every alert links to its section of the [runbooks](runbooks.md).

| Alert | Severity | Fires when |
| --- | --- | --- |
| `C2DAPIErrorBudgetBurn` | critical | 5xx answers burn the API availability budget 14.4× (1 h) or 6× (6 h) too fast |
| `C2DExportErrorBudgetBurn` | critical | System failures burn the export success budget 14.4× or 6× too fast |
| `C2DWorkersStalled` | critical | Exports are queued and none finished for 30 minutes |
| `C2DQueueBacklog` | warning | More than `monitoring.thresholds.queueBacklog` exports wait for 15 minutes |
| `C2DExportsSlow` | warning | 95th percentile of generation time above `exportP95Seconds` for 30 minutes |
| `C2DAPISlow` | warning | 95th percentile of API latency (downloads and uploads excluded) above `apiP95Seconds` |
| `C2DConfluenceErrors` | warning | More than 10 % of Confluence calls answer 429 or 5xx |
| `C2DTelemetryAbsent` | warning | No metric from the application for 15 minutes: the other alerts are blind |

The rules are validated by `promtool` and unit-tested in
[`charts/confluence-to-doc/tests/alerts.test.yaml`](../../charts/confluence-to-doc/tests/alerts.test.yaml)
(`make check-alerts`, run by CI).

## Dashboard

*Confluence export* (uid `confluence-to-doc`) shows, for one or several deployments:

- the three service level indicators over 30 days, and the exports waiting now;
- the queue, finished attempts per minute by outcome and cause, generation time, pages per export;
- API request rate and latency per route, Confluence request rate and latency.

## Logs and traces

Logs are JSON on stdout (`LOG_FORMAT=json`), with `trace_id` and `span_id` inside a span; the audit trail
is the subset tagged `event.category=audit`. A slow or failed export is best investigated from its trace:
search the tracing backend for `c2d.export.id=<id>` — the id is shown in the admin queue, in the audit trail
and in the document footer.
