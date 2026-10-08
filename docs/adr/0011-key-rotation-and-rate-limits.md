# ADR 0011 — Rotating the encryption key, and limiting request rates

- Status: accepted
- Date: 2026-10-08

## Context

**Keys.** Users' Confluence tokens were encrypted with a single `ENCRYPTION_KEY`. Changing it — because it
leaked, because policy asks for yearly rotation, because someone left — made every stored token
unreadable and forced every user to enter theirs again. In practice that means the key is never rotated.

**Load.** Concurrency was bounded per instance (`EXPORT_WORKER_CONCURRENCY` ×
`CONFLUENCE_FETCH_CONCURRENCY`), so the load on Confluence grew with the number of worker pods, and the
autoscaler adds pods precisely when the queue is long. Nothing bounded the API either: a script looping on
an endpoint, or a flood of bogus sign-in callbacks (each one an audit insert), went straight through.

## Decision

**A key ring.** `ENCRYPTION_KEYS` lists keys, current first; a single `ENCRYPTION_KEY` is the ring
`default:<key>`. Ciphertexts now record the id of the key that wrote them (authenticated with the
associated data), and older ciphertexts without one are still read. The workers' cleanup pass re-encrypts,
in batches, every token not written by the current key — a compare-and-swap update, so a token changed by
its user in the meantime is left alone — and records a `keys.rotate` audit event. A token no key can read
is kept, and its user is asked to enter it again (`pat_unreadable`).

**A Confluence budget shared by the whole deployment.** Every Confluence request — exports and interactive
browsing alike, retries included — first takes a token from a bucket stored in PostgreSQL
(`rate_limits`), refilled at `CONFLUENCE_RATE_LIMIT` per second up to `CONFLUENCE_RATE_BURST`. A row lock
makes it atomic across pods. If the database cannot be reached the request goes through: failing exports
because the limiter is down would be worse, and an unreachable database stops exports anyway.

**API limits per instance.** In-memory token buckets: `API_RATE_LIMIT` requests per minute per user on
`/api`, `AUTH_RATE_LIMIT` per client address on `/auth`. Over the limit: `429` with `Retry-After`.

## Consequences

- Rotating the key is a configuration change and two deployments, with no user involvement.
- One extra PostgreSQL round trip per Confluence request; at the default 10 requests per second this is
  negligible. When exports compete for the budget, interactive browsing waits its turn too.
- The API limits are per instance, so with N API pods a client gets up to N times the limit. Their purpose
  is to stop runaway clients, not to meter usage; a shared limit would cost a database round trip per API
  request.
- Behind a proxy, the sign-in limit is per proxy address unless `TRUSTED_PROXIES` is set.
