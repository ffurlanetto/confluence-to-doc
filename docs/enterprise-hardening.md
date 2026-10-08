# Enterprise hardening plan

The application works; this plan makes it fit for company-wide use. Each lot is one pull request. Decisions
taken with the product owner are recorded here so the lots stay consistent.

## Decisions

| Topic | Decision |
| ----- | -------- |
| Who may use the tool | Every authenticated user; an **admin** role from the identity provider's groups |
| Identity provider | One provider per deployment, groups claim configurable (any provider) |
| Audit destination | PostgreSQL table (admin page) + JSON log lines for the SIEM |
| Document traceability | Footer, watermark and classification; classification list configurable, optional |
| PAT encryption keys | Key ring in environment variables, key id stored with each PAT, progressive re-encryption |
| Platform | Kubernetes (Helm chart) |
| Notifications | Email (SMTP), Microsoft Teams (per-user workflow URL, encrypted), in-app |
| UI language | English only |
| Retention | Audit events 1 year; accounts and their PAT deleted after 6 months without sign-in (warned 15 days before) |
| Confluence target | Data Center 9.x |
| Global Confluence budget | 10 requests/s shared by all workers, configurable |

## Lots

1. **Access and audit** — admin role from groups; append-only audit trail (database + SIEM logs);
   audit page. *(ADR 0008, done)*
2. **Document traceability** — configurable classifications; "Exported by … on … – classification –
   reference" footer; diagonal watermark for marked levels; export id and exporter in the document
   properties. *(ADR 0009, done)*
3. **Isolation** — NetworkPolicy and seccomp in the chart; no external resource can reach LibreOffice;
   image scanning in CI; SBOM attached to releases. *(ADR 0010, done)*
4. **Keys and load** — `ENCRYPTION_KEYS` key ring with rotation; global Confluence request budget; per-user
   API rate limit. *(ADR 0011)*
5. **Lifecycle and GDPR** — periodic session revalidation and back-channel logout; PAT expiry warning;
   inactive account purge; self-service data deletion; processing record; tagged (accessible) PDF.
6. **Notifications** — email, Teams, in-app.
7. **Operations** — admin console (queue, cancel/retry, usage, blocking a user, Word template upload);
   alert rules, dashboard and SLOs; runbooks; backup and recovery; Playwright end-to-end tests in CI; load
   tests; reference documents; Confluence 9.x compatibility; threat model.
