# ADR 0003 — Server-side OIDC authentication (BFF)

- Status: accepted
- Date: 2026-09-27

## Context

The application must be protected by OAuth2. A SPA can either handle tokens itself (public client) or
delegate to the backend (Backend For Frontend).

## Decision

The backend is a **confidential** OAuth2 client (Authorization Code + PKCE + nonce); it verifies the ID
token, creates a server-side session and sets an opaque `HttpOnly; SameSite=Lax` cookie (`__Host-` and
`Secure` over HTTPS). The flow state (state, nonce, verifier, return_to) travels in a short-lived
AES-GCM-encrypted cookie. State-changing requests require a custom header (CSRF defence).

## Consequences

- ✅ No token is reachable from JavaScript (XSS resistance) and sessions can be revoked instantly (logout).
- ✅ Works with any standards-compliant OIDC provider; RP-initiated logout when `end_session_endpoint` exists.
- ⚠️ The SPA and the API must share an origin (the Vite proxy guarantees this in development).
