# ADR 0001 — Job queue in PostgreSQL

- Status: accepted
- Date: 2026-09-27

## Context

Document generation is slow (Confluence crawl plus conversion) and must be asynchronous, with a queue to
keep the load in check. Options considered: Redis (asynq), RabbitMQ, PostgreSQL.

## Decision

Use the `exports` table as the queue: jobs are claimed with `SELECT … FOR UPDATE SKIP LOCKED`, held with a
renewable lease (`locked_until`), every write is conditioned on the owner (`locked_by`), and attempts use
exponential backoff.

## Consequences

- ✅ A single infrastructure dependency, consistent transactions between business state and the queue, and
  a per-user quota checked atomically (advisory lock).
- ✅ Fault tolerance: an abandoned job is picked up again when its lease expires.
- ⚠️ Polling (2 s by default); fine at this volume. `LISTEN/NOTIFY` remains possible later.
- ⚠️ For thousands of jobs per second a dedicated broker would be preferable (well beyond current needs).
