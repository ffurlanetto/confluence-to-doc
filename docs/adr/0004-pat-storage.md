# ADR 0004 — Stockage des PAT Confluence

- Statut : accepté
- Date : 2026-09-27

## Contexte

Chaque utilisateur accède à Confluence avec son propre PAT, utilisé par les workers de façon asynchrone :
le jeton doit donc être persisté.

## Décision

- L'URL Confluence est fixée par configuration (jamais saisie par l'utilisateur) : pas de SSRF, pas de fuite
  du PAT vers un hôte arbitraire. Le client refuse d'envoyer le jeton à une autre origine, redirections comprises.
- Le PAT est validé (`/rest/api/user/current`) avant enregistrement.
- Il est chiffré en AES-256-GCM avec l'identifiant utilisateur comme donnée associée (une ligne copiée vers un
  autre utilisateur est indéchiffrable) et n'est jamais renvoyé par l'API.

## Conséquences

- ✅ Une fuite de la base seule ne révèle pas les PAT.
- ⚠️ La clé `ENCRYPTION_KEY` doit être gérée comme un secret (coffre) ; sa rotation impose une ressaisie
  (ou une future migration de rechiffrement grâce à l'octet de version du chiffré).
