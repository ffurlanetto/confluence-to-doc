# ADR 0003 — Authentification OIDC côté serveur (BFF)

- Statut : accepté
- Date : 2026-09-27

## Contexte

L'application doit être protégée par OAuth2. Une SPA peut soit gérer elle-même les jetons (client public),
soit déléguer au backend (Backend For Frontend).

## Décision

Le backend est un client OAuth2 **confidentiel** (Authorization Code + PKCE + nonce) ; il vérifie l'ID token,
crée une session serveur et pose un cookie opaque `HttpOnly; SameSite=Lax` (`__Host-` + `Secure` en HTTPS).
L'état du flux (state, nonce, verifier, return_to) voyage dans un cookie chiffré AES-GCM de courte durée.
Les requêtes mutantes exigent un en-tête personnalisé (anti-CSRF).

## Conséquences

- ✅ Aucun jeton accessible au JavaScript (résistance XSS), révocation immédiate des sessions (logout).
- ✅ Compatible avec tout fournisseur OIDC standard ; déconnexion RP-initiated si `end_session_endpoint` existe.
- ⚠️ La SPA et l'API doivent partager la même origine (le proxy Vite le garantit en développement).
