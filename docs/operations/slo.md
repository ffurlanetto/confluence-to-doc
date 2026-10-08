# Service level objectives

Three objectives, over a rolling **30 days**. They are what the burn-rate alerts protect and what the
dashboard's first row shows. The figures are starting points for an internal tool: revisit them after a
quarter of measurements, and change the error budgets in `monitoring.thresholds` together with this page.

| Objective | Target | Error budget (30 days) |
| --- | --- | --- |
| API availability | 99.5 % of API requests answer without a server error | ≈ 3 h 36 min of total outage |
| Export success | 99 % of exports do not fail for a system cause | 1 export in 100 |
| Export speed | 90 % of exports are generated within 5 minutes | 1 export in 10 slower |

## Indicators

**API availability** — requests to `/api/*` answered with a status other than 5xx, over all such requests.
Probes (`/healthz`, `/readyz`), sign-in redirects and static files are excluded: they say nothing about
whether users can export.

```promql
1 - sum(increase(http_server_request_duration_seconds_count{job=~"(.+/)?confluence-to-doc", http_route=~"/api/.*", http_response_status_code=~"5.."}[30d]))
  / sum(increase(http_server_request_duration_seconds_count{job=~"(.+/)?confluence-to-doc", http_route=~"/api/.*"}[30d]))
```

**Export success** — exports that end failed with `c2d_export_cause="system"`, over exports that ended
(succeeded or failed; a retried attempt is not an end). Failures the requester can fix — an expired token,
a page they cannot read, a tree over `EXPORT_MAX_PAGES` — are excluded: the service did its job by saying so.

```promql
1 - sum(increase(c2d_exports_finished_total{c2d_export_outcome="failed", c2d_export_cause="system"}[30d]))
  / sum(increase(c2d_exports_finished_total{c2d_export_outcome=~"succeeded|failed"}[30d]))
```

**Export speed** — successful exports whose generation took at most 300 s (a histogram bucket boundary).
Time spent waiting in the queue is not included; `C2DQueueBacklog` watches it instead.

```promql
sum(increase(c2d_export_duration_seconds_bucket{le="300"}[30d]))
  / sum(increase(c2d_export_duration_seconds_count[30d]))
```

## What is not covered

- **Confluence itself.** An outage of Confluence fails exports with a system cause and spends the budget,
  deliberately: users experience it as this service failing. The runbook tells the two apart.
- **The identity provider.** If nobody can sign in, the API sees no request and its availability looks
  perfect. Watch the provider separately, or add a synthetic sign-in probe.
- **Correctness of the documents.** No metric says a table was laid out well; the
  [reference documents](reference-documents.md) are the check for that, after every upgrade.

## Error budget policy

- **Budget left:** ship as usual.
- **Budget spent over the last 30 days:** releases are limited to fixes of the cause until the indicator is
  back above target; the post-incident review lists what spent it.
- **A single incident spending more than a third of the budget** gets a written post-incident review,
  whatever is left.
