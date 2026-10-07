# ADR 0008 — Administrator role from the identity provider, and an audit trail

- Status: accepted
- Date: 2026-10-07

## Context

The application lets anyone with a Confluence token extract a whole space in one click. That is its
purpose, and also the first question a security team asks about it: *who exported what, and when?* Until
now the only trace was the access log, which lists URLs, not exports, and is not kept long.

Operating the service also needs people with more rights than ordinary users — to read that trail first,
and later to manage the queue — and the company already manages people and groups in its identity
provider.

## Decision

**Administrators are designated by the identity provider.** `OIDC_ADMIN_GROUPS` lists the groups (or
roles) granting the role, read from the claim named by `OIDC_GROUPS_CLAIM`. The claim may be a string or a
list, nested (`realm_access.roles`) or namespaced (`https://example.com/groups`), and is read from the ID
token or, when absent there, from UserInfo. The flag is stored on the user at every sign-in; there is no
API to grant it, so the application holds no second source of truth about who is an administrator.
Everyone else keeps the same rights as before: access to the tool is not restricted by group.

**Every security-relevant action is an audit event**, recorded by `internal/audit` and written twice:

1. as a JSON log line tagged `event.category=audit` with Elastic Common Schema field names, *first*, so the
   SIEM copy is complete even when the database is not;
2. in an `audit_events` table read by the administration page, append-only (a trigger rejects updates) and
   purged after `AUDIT_RETENTION` (one year by default, never less than 30 days).

Recording never fails the action it describes: a refused insert is logged at error level with the whole
event. Events carry no secret and no document content, and the actor's email is copied into the event so
it survives the account. Reading the trail is itself an audited action.

The client address comes from `X-Forwarded-For` only when the peer is in `TRUSTED_PROXIES`, read
right-to-left so that a client cannot forge it.

## Consequences

- Removing someone from the admin group takes effect at their next sign-in, up to `SESSION_TTL` later.
  Periodic revalidation of sessions is planned with the session lifecycle work.
- With Entra ID, group claims carry object ids and disappear above 200 groups; the documentation steers
  towards app roles.
- The table is tamper-*evident* at best: whoever owns the database can delete rows (the retention purge
  must). The SIEM copy, outside the application's reach, is the authoritative one.
- Failed sign-ins are recorded although the caller is anonymous, so a flood of bogus callbacks becomes a
  flood of inserts. The per-client rate limit planned in the hardening plan (lot 4) bounds it.
- A few more database writes per request (one per audited action), all on indexed, append-only rows.
- Worker events (`export.complete`, `export.fail`) are attributed to the export's owner, on whose behalf
  and with whose token the worker acts; they carry no email, so filtering by email does not show them.
