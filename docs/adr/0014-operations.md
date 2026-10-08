# ADR 0014 — Operating the service: administration console, alerting, end-to-end and load tests

- Status: accepted
- Date: 2026-10-08

## Context

The previous lots made the application safe to open to the whole company. Running it day to day still
required shell access: a stuck export meant SQL, a departing employee could keep exporting until their IdP
account was disabled, changing the Word template meant a release, and nothing said when the service was
unhealthy. Lot 7 of the [hardening plan](../enterprise-hardening.md) closes those gaps.

## Decision

**An administration console** in the application, behind the existing admin role (`requireAdmin`), every
action audited, reading included:

- **Queue** of every user's exports, with *cancel* (queued or running → failed, the owner is notified; the
  worker loses its lease at its next heartbeat) and *retry* (failed → queued with fresh attempts).
- **Users**, with *block*: sessions deleted, pending exports cancelled, sign-in refused until unblocked. A
  blocked account resolves no session at all, so a session opened by a flow already in progress is useless.
  Blocking is local to the application — the IdP stays the source of identity — and an administrator cannot
  block themselves.
- **Usage**, computed from the audit trail (`export.complete` / `export.fail`), which already holds who,
  what format, how many pages and bytes, and is kept a year — longer than the exports themselves. No
  second ledger to keep consistent.
- **Word template upload.** The template moves into PostgreSQL (one row) so every instance uses the same,
  and takes precedence over `WORD_TEMPLATE_PATH`, which stays as the fallback. Each export reads the
  template's checksum once, at its start, and uses the parsed template cached for that checksum: rendering
  and conversion agree even if it changes meanwhile, and an upload applies to the next exports without a
  restart. Since administrators are not operators, uploads are validated more strictly than before — and
  so is `WORD_TEMPLATE_PATH` from now on: no macros, no ActiveX, no external relationship other than
  hyperlinks, no field that loads a file or URL (`INCLUDEPICTURE`, `INCLUDETEXT`, `LINK`, `DDE`…), 10 MB
  at most. Every part of the template ends up in documents LibreOffice opens again, so this is security
  invariant 7 applied to the template.

**Alerting on symptoms, against objectives** ([SLOs](../operations/slo.md)): API availability 99.5 %, exports
without a *system* failure 99 %, exports generated within 5 minutes 90 %. To tell a system failure from a
user's (expired token, no permission, page gone), `c2d.exports.finished` gains a `c2d.export.cause`
attribute. Burn-rate alerts (multi-window, 14.4× and 6×) protect the first two objectives; queue backlog,
stalled workers, latency, Confluence errors and missing telemetry complete them. The rules and a Grafana
dashboard ship in the chart, opt-in, assuming the OTLP metrics reach Prometheus through a collector; the
rules are validated and unit-tested with `promtool` in CI.

**Tests of the whole system**: Playwright drives the real server (API, worker, LibreOffice, PostgreSQL) with
the built SPA against the fake Confluence and IdP, in CI. A **reference page tree** in the fake Confluence
exercises every HTML element, the Confluence macros, many scripts and hostile content; the end-to-end tests
export it, a unit test checks nothing hostile survives rendering, and a checklist turns it into the
acceptance test for a LibreOffice, renderer or template change. A **k6 script** measures capacity against
the same fakes. A read-only check (`make confluence-check`) runs the application's client against a real
Confluence Data Center to establish compatibility of a given instance.

**Operations documentation** in `docs/operations/`: monitoring, SLOs, runbooks (one section per alert),
backup and recovery, threat model, Confluence compatibility, reference documents, load testing.

## Consequences

- ✅ Day-to-day operations need no database or cluster access; every administrative act is in the audit
  trail and the SIEM.
- ✅ A template change is an upload with an immediate rollback (*Remove*), not a release.
- ✅ Alerts fire on what users experience, and a user's own mistakes do not page anyone.
- ✅ Regressions of the full chain (sign-in → export → LibreOffice → download) are caught in CI.
- ⚠️ A `WORD_TEMPLATE_PATH` template with macros or external references no longer starts the service; such
  a template was already a risk, but this is a breaking change for whoever had one.
- ⚠️ Each export makes one more query (the template's checksum).
- ⚠️ Usage figures depend on the audit trail: if recording an event failed, the figure misses it (the SIEM
  copy, written first, does not).
- ⚠️ The alert rules assume the collector's default name translation; other pipelines must adapt the
  selector or the names.
- ⚠️ CI gains an end-to-end job of a few minutes.
