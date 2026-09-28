# ADR 0004 — Storing Confluence PATs

- Status: accepted
- Date: 2026-09-27

## Context

Each user reaches Confluence with their own personal access token, which workers use asynchronously, so
the token has to be persisted.

## Decision

- The Confluence URL comes from configuration only (never from user input): no SSRF, and no way to leak a
  PAT to an arbitrary host. The client refuses to send the token to any other origin, redirects included.
- The PAT is validated (`/rest/api/user/current`) before being stored.
- It is encrypted with AES-256-GCM using the user id as associated data (a row copied to another user is
  undecryptable) and is never returned by the API.

## Consequences

- ✅ A database leak alone does not expose the PATs.
- ⚠️ `ENCRYPTION_KEY` must be handled as a secret (vault); rotating it forces users to re-enter their token
  (or a future re-encryption migration, which the version byte in the ciphertext leaves room for).
