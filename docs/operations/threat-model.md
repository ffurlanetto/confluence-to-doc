# Threat model

Reviewed for the enterprise hardening plan (lot 7). Revisit it when a trust boundary moves: a new
integration, a new kind of upload, a change to how documents are produced. The numbered **invariants**
are those of [CLAUDE.md](../../CLAUDE.md#security-invariants-do-not-break); the tests that guard them are
the first line of defence against regressions.

## What we protect

| Asset | Why it matters |
| --- | --- |
| Users' Confluence personal access tokens | Each grants its owner's full read (and write) access to Confluence |
| Confluence content | Exported documents carry whatever the user can read, possibly confidential |
| Generated documents | Same content, outside Confluence's access control for 48 hours |
| The audit trail | Evidence of who exported what; tampering hides a leak |
| Encryption keys, OIDC client secret, SMTP password | Unlock the above |
| Availability of the service and of Confluence | An export storm must not harm Confluence for everyone |

## Actors

- **Employees** (authenticated through the company IdP): the users. Mostly benign; some curious, a few
  malicious (exfiltration), any of them with a compromised browser.
- **Administrators** (IdP group): see everyone's activity, can block accounts and change the template.
- **Confluence page authors**: anyone who can write a page someone else exports — the content is
  **untrusted input** to the converter.
- **Outside attackers**: no account; reach the public URL, maybe the network.
- **Operators**: hold the cluster, the database and the keys. Trusted, but their actions should be visible.

## Trust boundaries and data flows

```
Browser ──HTTPS──► API ──► PostgreSQL (tokens encrypted, audit, queue, template)
   ▲                │  └─► storage (documents, 48 h)
   │                └────► IdP (OIDC)            Worker ──PAT──► Confluence (configured origin only)
   │                                               │  └─► LibreOffice (proxy to nowhere, throw-away profile)
   └── documents ◄── API ◄── storage ◄─────────────┘  └─► SMTP, Teams (allow-listed hosts)
```

## Threats (STRIDE)

### Spoofing

| Threat | Mitigation | Residual |
| --- | --- | --- |
| Stealing a session | Opaque random token, HttpOnly + Secure + SameSite=Lax cookie, only its SHA-256 stored (6); revalidated against the IdP periodically; back-channel logout | A compromised browser can still act as its user until the session ends |
| Forging the OIDC flow | PKCE, `state` and `nonce` checked in constant time, ID token verified against the issuer's keys | — |
| Posing as an administrator | Admin flag from the IdP's groups only, at each sign-in (9); `requireAdmin` on every `/api/admin/*` route; refusals audited | IdP misconfiguration grants it |
| A blocked user coming back | Sessions deleted on block; blocked accounts never resolve a session; sign-in refused and audited | Documents already downloaded stay with them |
| Spoofed Teams or email notifications | Teams URLs restricted to Microsoft hosts, https, no redirects; mail from a configured sender | Users can be phished outside the application |

### Tampering

| Threat | Mitigation | Residual |
| --- | --- | --- |
| Cross-site request forgery | `X-CSRF-Protection` header and same-origin `Origin` on every state change (5) | — |
| Altering the audit trail through the application | Append-only table (trigger refuses updates); SIEM copy written first, independently (8) | A database superuser can still delete rows: the SIEM copy is the reference |
| A malicious Word template | Admin-only upload, validated: OOXML package, no macros, no ActiveX, no external relationship except hyperlinks, no field loading a file or URL; size-capped; audited | A template can still look wrong: the reference documents are the check |
| Swapping someone's encrypted token | AES-GCM bound to the user id (3): a row copied to another user does not decrypt | — |

### Repudiation

Every security-relevant action is recorded with actor, time, client address, user agent and request id (8):
sign-ins, token changes, exports, downloads, deletions, administration actions (including reading the
audit trail, the queue and the user list). Documents carry the requester, the time and the export id in
their footer and properties, so a leaked document leads back to its audit events.

### Information disclosure

| Threat | Mitigation | Residual |
| --- | --- | --- |
| Reading another user's exports | Every export query filters on `user_id`; another user's export is a 404 (4) | Administrators see titles and owners, not contents |
| Token leaking to another host | Confluence base URL from configuration only (1); token sent only to that origin, redirects to another host refused (2) | — |
| Token leaking through the API or logs | Never returned by the API (3); logs and audit events never contain secrets (8) | — |
| **Server-side request forgery through page content** | Content sanitised: scripts, frames, objects, media, forms dropped, event handlers stripped; images inlined as `data:` and fetched only from the Confluence origin with a size cap; no `url()` left; LibreOffice's HTTP proxy pointed at nothing (7), and the worker pod's egress limited by the NetworkPolicy to the destinations it needs | A LibreOffice parsing vulnerability remains; see below |
| Database dump stolen | Tokens and Teams URLs encrypted; keys stored apart ([backup](backup-and-recovery.md)) | Audit trail and titles readable |
| Documents leaving the company | Classification watermark and traceability footer; 48-hour retention; download audited | A user who can read a page can always copy it: this deters and traces, it does not prevent |

### Denial of service

| Threat | Mitigation | Residual |
| --- | --- | --- |
| Export storm against Confluence | Shared request budget for all instances (`CONFLUENCE_RATE_LIMIT`); `Retry-After` honoured | — |
| One user filling the queue | `EXPORT_MAX_ACTIVE_PER_USER`, per-user API rate limit, per-address sign-in limit | Many users at once is load, not abuse |
| Huge trees or pages | `EXPORT_MAX_PAGES`, image size cap, job timeout, worker memory limits | — |
| Decompression bombs (templates, generated packages) | Each part read with a 64 MB cap; uploads capped at 10 MB | — |

### Elevation of privilege

| Threat | Mitigation | Residual |
| --- | --- | --- |
| Code execution through a crafted page (LibreOffice vulnerability) | No macros run; the converter runs as a non-root user with a throw-away profile, in a pod with a seccomp profile, a read-only root filesystem and egress limited by the NetworkPolicy; the image is scanned (Trivy) and rebuilt for LibreOffice fixes | **Accepted risk.** A worker compromise exposes the tokens it decrypts in memory while working and the database credentials. Keep LibreOffice patched; consider separating the converter into its own sandbox (gVisor, a dedicated pool) if the threat level rises |
| Container escape | Pod security (non-root, dropped capabilities, seccomp `RuntimeDefault`) | Cluster hardening is the platform's |

## Open points

1. **Converter isolation** — the converter shares its pod with the code that decrypts tokens. Splitting it
   into a separate, credential-less service is the next step if LibreOffice's attack surface becomes a
   concern.
2. **Administrator abuse** — administrators can read the activity of everyone, and block accounts. Their
   actions are audited and shipped to the SIEM, where a separate team should review them.
3. **Blocking is local** — it does not disable the person in the IdP or revoke their Confluence token.
