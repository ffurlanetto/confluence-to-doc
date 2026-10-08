# Runbooks

One section per alert, then the routine procedures. Commands assume the Helm release is called `c2d` in
namespace `docs`; adapt the names (`c2d-confluence-to-doc-api`, `…-worker`) to yours.

Useful everywhere:

```sh
kubectl -n docs get pods -l app.kubernetes.io/instance=c2d
kubectl -n docs logs deploy/c2d-confluence-to-doc-worker --since=30m | jq 'select(.level != "INFO")'
kubectl -n docs logs deploy/c2d-confluence-to-doc-api --since=30m | jq 'select(.msg == "request failed")'
```

The **administration console** (`/admin`) shows the queue across users, lets you cancel or retry an export,
and the **audit trail** (`/admin/audit`) has every export's history: search for `export.fail` to see the
user-facing reason of recent failures.

---

## Alerts

### API errors

`C2DAPIErrorBudgetBurn` — the API answers 5xx often enough to spend the month's budget in hours.

1. Find the routes: dashboard panel *API requests per second*, or
   `sum by (http_route) (rate(http_server_request_duration_seconds_count{http_response_status_code=~"5.."}[5m]))`.
2. Read the errors: the API logs `request failed` with `err` and `request_id`; the user saw the same
   `request_id`.
3. Usual causes:
   - **PostgreSQL** unreachable or saturated (`/readyz` fails, `connecting to database`, `too many clients`):
     check the database first; the API holds a pool of connections per pod.
   - **Storage** (`503`/`500` on downloads): the S3 endpoint or the shared volume.
   - **A bad release**: errors started with a rollout → `helm rollback c2d`.
4. `502 confluence_error` answers are Confluence failing, not this service; see
   [Confluence errors](#confluence-errors).

### Export failures

`C2DExportErrorBudgetBurn` — exports fail for system causes (not a user's token or permissions).

1. Which cause: `/admin/queue` → *Failed*, or the audit trail filtered on *Export failed*: the `reason`
   detail is the message the user got.
2. By message:
   - *The company Word template could not be applied* → the template in use breaks on some content. Check
     `/admin/template`; if it was just uploaded, remove it (documents fall back to the server's template or
     the built-in styling), reproduce with the [reference documents](reference-documents.md), fix the
     template, upload it again.
   - *Generation exceeded the maximum allowed time* → very large trees or a slow Confluence; see
     [Exports slow](#exports-slow).
   - *A technical error occurred* → worker logs around the export id (`export_id` field): LibreOffice
     crashes (`soffice`), out-of-memory kills (`kubectl describe pod` shows `OOMKilled`: raise
     `worker.resources.limits.memory` or lower `EXPORT_WORKER_CONCURRENCY`), storage errors.
3. Once the cause is fixed, retry the failed exports from `/admin/queue` (users are told by notification
   when they finish).

### Queue backlog

`C2DQueueBacklog` — more exports wait than usual.

1. Are workers running and finishing? If none finishes, it is [Workers stalled](#workers-stalled).
2. If they finish, capacity is short: `replicas × EXPORT_WORKER_CONCURRENCY` exports run at once.
   - Scale: `kubectl -n docs scale deploy/c2d-confluence-to-doc-worker --replicas=4` (and set
     `worker.replicaCount` so the next release keeps it). Each concurrent export needs about 1 GiB.
   - More workers do not help when Confluence is the bottleneck: they share `CONFLUENCE_RATE_LIMIT`
     requests per second. Look at *Confluence requests per second*: flat at the limit means raising the
     limit (with the Confluence administrators' agreement) is the lever.
3. One user flooding the queue: `/admin/queue` shows the owners; each user is capped at
   `EXPORT_MAX_ACTIVE_PER_USER`, but many users at once is legitimate load.

### Workers stalled

`C2DWorkersStalled` — exports are queued and nothing has finished for 30 minutes.

1. `kubectl get pods`: workers crash-looping, pending (no node fits the memory request), or absent
   (`worker.enabled: false` with an API that does not run `APP_ROLE=all`).
2. Worker logs: `claiming export` errors mean PostgreSQL; `heartbeat failed` means jobs are lost and retried.
3. A job stuck *running* whose worker died is picked up again when its lease expires (one minute); a job that
   keeps killing its worker fails after `EXPORT_MAX_ATTEMPTS`. To unblock the queue at once, cancel it from
   `/admin/queue`.
4. Restart as a last resort: `kubectl rollout restart deploy/c2d-confluence-to-doc-worker`. Running jobs are
   released back to the queue on a graceful shutdown.

### Exports slow

`C2DExportsSlow` — generation time's 95th percentile is above the threshold.

1. Traces: search `c2d.export.id` of a slow export; the spans `confluence.crawl`, `document.render` and
   `document.convert` say where the time went.
2. Crawl slow → Confluence latency or the shared rate limit (see [Confluence errors](#confluence-errors) and
   [Queue backlog](#queue-backlog)).
3. Convert slow → CPU starvation (`worker.resources.requests.cpu`, node pressure) or huge documents; the
   *Pages per export* panel shows whether exports grew.

### API slow

`C2DAPISlow` — API latency is high (downloads and uploads excluded).

1. *API latency* panel per route. Routes under `/api/confluence/*` wait for Confluence: compare with
   *Confluence latency*.
2. Other routes are PostgreSQL: slow queries, connection pool exhausted, or CPU throttling of the API pods
   (`kubectl top pods`). Scale the API (`api.replicaCount`, or the HPA bounds).

### Confluence errors

`C2DConfluenceErrors` — Confluence answers 429 or 5xx to more than 10 % of calls.

1. **429**: Confluence's own rate limiting rejects the application. Lower `CONFLUENCE_RATE_LIMIT`, or ask the
   Confluence administrators to exempt the service's traffic. The client honours `Retry-After`.
2. **5xx**: Confluence is unwell; tell its administrators. Exports retry transient errors with backoff up to
   `EXPORT_MAX_ATTEMPTS`, then fail with a system cause — expect the export budget to suffer meanwhile.
3. Check the URL still points at the right place: a reverse proxy in front of Confluence answering 502 looks
   the same.

### Telemetry absent

`C2DTelemetryAbsent` — Prometheus receives nothing from the application.

1. Is the application up? If not, this is an outage: start with `kubectl get pods`.
2. If it is, the pipeline broke: application logs for `telemetry` warnings (exporter errors), the
   collector's logs, the remote-write or scrape status in Prometheus.
3. A change of `telemetry.serviceName`, `service.namespace` or `telemetry.metricPrefix` changes the series
   names or the `job` label: align `monitoring.selector`.

---

## Procedures

### Cancel or retry an export

`/admin/queue`. Cancelling marks the export failed with *Cancelled by an administrator.* and notifies its
owner; a worker running it stops at its next heartbeat (within about 20 s). *Retry* puts a failed export back
in the queue with fresh attempts. Both are audited (`admin.export.cancel`, `admin.export.retry`).

### Block a user

`/admin/users` → *Block*, with a reason. The person's sessions end, their pending exports are cancelled, and
the next sign-in is refused until someone unblocks them; documents already downloaded are out of reach, the
audit trail tells which (`export.download`). Blocking does not touch the identity provider: for someone
leaving the company, disable the account there too. An administrator cannot block themselves.

### Replace the Word template

`/admin/template`. Test a new template on the [reference documents](reference-documents.md) first: upload
it, export *Rendering reference* in Word and PDF, check the list. If something is wrong, *Remove the
uploaded template* restores the previous behaviour at once. Templates carrying macros, ActiveX controls or
links to external content are refused.

### Rotate the encryption key

See [configuration › Rotating the encryption key](../configuration.md#rotating-the-encryption-key). Never
remove the old key before a `keys.rotate` audit event reports nothing left to rotate, and never lose a key
that still encrypts tokens: the backups cannot help with that (see
[backup and recovery](backup-and-recovery.md)).

### A user cannot export

1. Their exports page shows the reason. The common ones are their own to fix: token missing, expired or
   revoked (*Preferences*), no permission on the page in Confluence, tree larger than `EXPORT_MAX_PAGES`
   (export a sub-branch).
2. *Your Confluence personal access token can no longer be read*: the key that encrypted it was removed
   from `ENCRYPTION_KEYS`; they must enter it again (or put the key back).
3. Blocked? `/admin/users` shows it.

### Restore from a backup

See [backup and recovery](backup-and-recovery.md#restoring).
