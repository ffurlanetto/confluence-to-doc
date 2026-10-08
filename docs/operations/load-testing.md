# Load testing

[`loadtest/exports.js`](../../loadtest/exports.js) is a [k6](https://k6.io) script with two scenarios run
together:

- **browse** — `BROWSERS` people (20) opening their exports, preferences and inbox, and searching pages;
- **exports** — `EXPORTS_PER_MINUTE` new exports (20) of a page tree with its children, 60 % PDF, each
  polled until ready and downloaded.

Its thresholds are the [service level objectives](slo.md) with margin: browsing requests fail less than
0.5 % of the time and answer within a second at the 95th percentile; fewer than 1 % of exports fail; 90 %
are ready within five minutes **including the wait in the queue** — what a user experiences.

## What it measures, and what not

It measures **the application's capacity**: API instances, workers, LibreOffice, PostgreSQL and storage.
Run it against a stack where Confluence and the identity provider are the fakes of
`go run ./cmd/devmocks`, so the test neither depends on nor harms the real ones:

- `-confluence-latency 80ms` makes the fake Confluence about as slow as a real one per call;
- the fake identity provider signs in one user; give the test stack limits that do not get in the way —
  `EXPORT_MAX_ACTIVE_PER_USER=1000`, `API_RATE_LIMIT=0` — since one person's quotas are not what is
  measured;
- `CONFLUENCE_RATE_LIMIT` stays as in production: it is what bounds throughput against the real Confluence.

Load against the **real** Confluence must be agreed with its administrators first; the shared budget
(`CONFLUENCE_RATE_LIMIT`) is the number to discuss, not this script's rate.

## Running it

```sh
# A stack on this machine: the fakes, then the application on a fresh database.
go -C backend run ./cmd/devmocks -confluence-latency 80ms &
E2E_DATABASE=c2d_load EXPORT_MAX_ACTIVE_PER_USER=1000 API_RATE_LIMIT=0 ./e2e/start-server.sh &

k6 run loadtest/exports.js
k6 run -e EXPORTS_PER_MINUTE=60 -e BROWSERS=50 -e DURATION=15m loadtest/exports.js
```

Against a staging deployment, set `BASE_URL`. Behind a real identity provider, sign in with a browser and
pass the session cookie's value as `SESSION` (its name is `__Host-c2d_session` over https); `PAT` is then a
token valid on the Confluence that staging uses, and `PAGE_ID` and `SEARCH` a tree and a word that exist
there.

## Reading the results

- `export_duration` growing while `http_req_duration{scenario:browse}` stays flat: the workers are the
  bottleneck. Add worker replicas (each concurrent export needs about 1 GiB) or raise
  `EXPORT_WORKER_CONCURRENCY` on bigger nodes — until Confluence's budget is the limit.
- Browsing slows down too: the API or PostgreSQL. With `APP_ROLE=all`, LibreOffice competes with the API
  for CPU; separate the roles (the chart does by default).
- `export_refused` above zero: a per-user cap or rate limit was hit; raise it on the test stack.
- Watch the [dashboard](monitoring.md#dashboard) during the run: queue depth, generation time, Confluence
  request rate against `CONFLUENCE_RATE_LIMIT`.

## A first measurement

On a development container (4 vCPU, everything on one machine, `APP_ROLE=all` with
`EXPORT_WORKER_CONCURRENCY=4`, fake Confluence answering in 30 ms), exporting the 14-page reference tree:

| Exports per minute | Browsing users | Exports ready, 90th percentile | Browsing, 95th percentile | Failures |
| --- | --- | --- | --- | --- |
| 12 | 5 | 4 s | 34 ms | 0 |
| 40 | 10 | 26 s (queue building up) | 970 ms | 0 |

One such machine generates roughly 30 exports of this size a minute before they start to queue. These are
not production figures — run the test on your own infrastructure and record its results here.
