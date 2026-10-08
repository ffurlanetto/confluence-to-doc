# ADR 0013 — Notifications: in the application, by email, in Teams

- Status: accepted
- Date: 2026-10-08

## Context

Exports are asynchronous, so users either kept the page open or came back to check. The previous lots also
created things users must hear about without opening the application: a Confluence token about to expire,
an inactive account about to be deleted (until now only logged).

## Decision

**Every notification is stored and shown in the application**, under *Notifications* with an unread count,
for 90 days. It is also delivered on the user's external channels:

- **email**, when the operator configured an SMTP relay (`SMTP_HOST`), with the standard library's
  `net/smtp`: STARTTLS, implicit TLS, or a plain local relay (credentials refused in clear);
- **Microsoft Teams**, through a workflow each user creates (*Send webhook alerts to a chat*) and whose URL
  they paste in their preferences: no application to register in Entra ID, no tenant-wide consent. The
  card is an Adaptive Card the workflow posts as is.

Export outcomes can be turned off; account notices (token expiring, account to be deleted) cannot, and go
to every configured channel.

**Delivery goes through an outbox**: a notification and its deliveries are rows written with the event; a
dispatcher on the workers claims due deliveries (`FOR UPDATE SKIP LOCKED` plus a lease), sends them and
retries transient failures with backoff — 1 minute doubling to 4 hours, eight attempts. A refusal (an SMTP
5xx, a 4xx from the workflow, a removed URL) is final. A mail relay that is down delays messages; it never
slows an export or loses a notification.

**The Teams URL is a credential and a destination chosen by a user.** It is encrypted (bound to the user and
to its purpose, so it cannot be swapped with the PAT) and never returned. Before it is saved and before each
request it must be HTTPS on a host suffix of `TEAMS_WEBHOOK_HOSTS` (Microsoft's workflow and webhook
domains by default), on port 443, without credentials; redirects are not followed. That keeps the server
from being pointed at internal addresses.

Messages carry a title, a status and a link back to the application — never a document or its content.

## Consequences

- No new dependency; `net/smtp` is frozen but sufficient for one relay. OAuth-authenticated SMTP (Microsoft
  365 without basic authentication) is not supported: use a relay that accepts the application.
- Dev: `make up` starts Mailpit (`http://localhost:8025`), which catches the emails.
- Email addresses come from the identity provider; an account without one gets no email.
- The recipient is read when the delivery is sent, so a removed Teams URL or a changed email is honoured
  even for queued messages.
- With NetworkPolicies, the workers need egress to the SMTP relay and to the Teams hosts.
