# ADR 0012 — Session and account lifecycle

- Status: accepted
- Date: 2026-10-08

## Context

Accounts and sessions only ever grew. A user disabled in the identity provider kept a working session
until `SESSION_TTL`; their admin role stayed until their next sign-in. An employee who left kept an
account, an encrypted token and their export history forever, with no way to erase them short of a
database query. Tokens expired silently, and the first sign was a failed export. Generated PDFs were
tagged only by accident of the LibreOffice version, without a declared language.

## Decision

**Sessions follow the identity provider.**
- Every `SESSION_REVALIDATE_INTERVAL` (15 minutes) the session's refresh token, stored encrypted and bound
  to the session, is exchanged with the provider. `invalid_grant` ends the session (`auth.session_revoked`);
  a provider that cannot be reached does not sign everyone out — the check is tried again. The new ID token
  refreshes the admin role. A row update claims the check, so concurrent requests never present the same
  refresh token twice (providers that rotate them revoke a reused one).
- `/auth/backchannel-logout` implements OpenID Connect Back-Channel Logout: a signed logout token ends the
  sessions of the provider session (`sid`) or of the subject at once.

**Accounts have an end.**
- `DELETE /api/me` (*Preferences › Delete my account*) erases the account, token, preferences, sessions and
  exports with their files.
- Accounts unused for `ACCOUNT_RETENTION` (180 days) are deleted the same way, but only after their owner
  was warned `ACCOUNT_DELETION_NOTICE` (15 days) earlier; signing in again cancels it. The warning goes
  through a `Notifier`, logged until notification channels exist.
- The audit trail is kept across deletions (security accountability, its own retention); deletions are
  themselves audited.

**Tokens and documents.** The token's expiry is read from Data Center's personal access token API when the
token is saved (best effort: a Data Center token is the base64 of `<id>:<secret>`), and users are warned 14
days ahead. PDFs are exported tagged with the PDF/UA flag, and `DOCUMENT_LANGUAGE` sets the language they
declare. [gdpr.md](../gdpr.md) is a draft record of processing.

## Consequences

- A refresh token is now stored per session (encrypted). Entra ID only issues one with `offline_access`.
- Revalidation adds one round trip to the provider per session every 15 minutes, on the request that
  finds it due.
- Back-channel logout tokens are not checked against replay (`jti` is not stored): replaying one can only
  end sessions that the provider already asked to end.
- Deleting files of a deleted account is best effort: a file left behind is unreachable (its row is gone)
  and expires with the storage's own rules.
- The token expiry depends on an undocumented token layout; when it does not hold, the expiry is simply
  unknown.
