# Record of processing — Confluence Export

A starting point for the entry in your register of processing activities (GDPR art. 30). Fill in the
bracketed parts and have it validated by your data protection officer. Durations are the defaults; each is
configurable (see [configuration.md](configuration.md)).

| | |
| --- | --- |
| **Controller** | [Company, department owning the service] |
| **Contact / DPO** | [name, address] |
| **Purpose** | Let employees export Confluence pages, with their child pages, to Word or PDF; secure the service and trace the use made of it |
| **Legal basis** | Legitimate interest of the employer (internal tooling, security of information) — [to confirm] |
| **Data subjects** | Employees and contractors who sign in to the service |
| **Recipients** | The user themself; administrators of the service (audit trail); the security team (SIEM) |
| **Transfers outside the EU** | [None / identity provider, hosting — to assess] |

## Data held

| Data | Source | Kept for | Notes |
| --- | --- | --- | --- |
| Identity: provider subject, email, display name, admin flag | Identity provider, at sign-in | While the account exists; deleted after 180 days without sign-in (warned 15 days before), or on request | — |
| Confluence personal access token | Entered by the user | Same as the account; removable by the user at any time | Encrypted (AES-256-GCM), never shown again, never logged |
| Preferences (default format), token expiry date | User / Confluence | Same as the account | — |
| Sessions: hash of the session cookie, provider session id, encrypted refresh token | Sign-in | 12 hours at most | Ended by sign-out, provider logout or account deletion |
| Export requests: page id and title, format, classification, status, dates | User | 30 days of history | Deleted with the account |
| Generated documents | Confluence content the user can read | 48 hours | Carry the requester's name and email in their footer and properties, by design (traceability) |
| Audit trail: action, date, user id and email, client IP address, user agent, target, outcome | Use of the service | 1 year in the database; per SIEM policy for the log copy | Kept after an account is deleted: security accountability ([to confirm]) |
| Technical logs and traces | Use of the service | Per the logging platform's retention | No token, cookie or document content |

## Rights

- **Access / portability**: the user sees their exports in the application; an administrator can extract
  their audit events (`Audit` page, filter by email).
- **Erasure**: *Preferences › Delete my account* erases everything but the audit trail, immediately.
- **Rectification**: identity data comes from the identity provider and is refreshed at each sign-in.

## Security measures

OIDC sign-in, server-side sessions, encrypted tokens with key rotation, per-user data isolation, CSRF
protection, audit trail shipped to the SIEM, rate limits, network isolation of the document conversion,
signed container images with an SBOM. See [architecture.md](architecture.md#security).
