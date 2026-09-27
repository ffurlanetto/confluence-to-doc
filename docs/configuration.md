# Configuration

Toute la configuration passe par des variables d'environnement (12-factor), chargées et validées au
démarrage par `backend/internal/config`. Une configuration invalide arrête le processus avec la liste
complète des erreurs.

## Obligatoires

| Variable              | Description                                                                      |
| --------------------- | -------------------------------------------------------------------------------- |
| `DATABASE_URL`        | DSN PostgreSQL, ex. `postgres://user:pass@host:5432/db?sslmode=require`          |
| `OIDC_ISSUER_URL`     | URL de l'émetteur OIDC (découverte via `/.well-known/openid-configuration`)      |
| `OIDC_CLIENT_ID`      | identifiant du client OAuth2 (confidentiel)                                      |
| `OIDC_CLIENT_SECRET`  | secret du client                                                                 |
| `CONFLUENCE_BASE_URL` | URL de l'instance Confluence Server/Data Center (avec chemin de contexte éventuel) |
| `ENCRYPTION_KEY`      | 32 octets aléatoires en base64 (`openssl rand -base64 32`) pour chiffrer les PAT |

> ⚠️ Changer `ENCRYPTION_KEY` rend les PAT existants illisibles : les utilisateurs devront les ressaisir.

## Optionnelles

| Variable                       | Défaut            | Description                                                  |
| ------------------------------ | ----------------- | ------------------------------------------------------------ |
| `APP_ROLE`                     | `all`             | `all`, `api` ou `worker`                                     |
| `PUBLIC_URL`                   | `http://localhost:8080` | URL publique ; sert au redirect URI OIDC et au contrôle d'origine. En `https`, cookies `Secure` + HSTS |
| `HTTP_ADDR`                    | `:8080`           | écoute HTTP                                                  |
| `METRICS_ADDR`                 | `:9090`           | écoute Prometheus (vide = désactivé). Ne pas exposer publiquement |
| `STATIC_DIR`                   | _(vide)_          | dossier du build de la SPA à servir                          |
| `LOG_LEVEL`                    | `info`            | `debug`, `info`, `warn`, `error`                             |
| `LOG_FORMAT`                   | `json`            | `json` ou `text`                                             |
| `OIDC_SCOPES`                  | `openid profile email` | scopes demandés                                         |
| `SESSION_TTL`                  | `12h`             | durée de vie d'une session                                   |
| `CONFLUENCE_TIMEOUT`           | `30s`             | timeout d'une requête vers Confluence                        |
| `CONFLUENCE_FETCH_CONCURRENCY` | `4`               | requêtes Confluence parallèles par export                    |
| `EXPORT_RETENTION`             | `48h`             | durée de disponibilité d'un document                         |
| `EXPORT_WORKER_CONCURRENCY`    | `2`               | exports générés simultanément par instance worker            |
| `EXPORT_MAX_ACTIVE_PER_USER`   | `5`               | exports en file ou en cours par utilisateur                  |
| `EXPORT_MAX_PAGES`             | `500`             | pages maximum par export                                     |
| `EXPORT_MAX_ATTEMPTS`          | `3`               | tentatives en cas d'erreur transitoire                       |
| `EXPORT_JOB_TIMEOUT`           | `15m`             | durée maximale d'un export                                   |
| `EXPORT_MAX_IMAGE_BYTES`       | `10485760`        | taille maximale d'une image embarquée                        |
| `EXPORT_POLL_INTERVAL`         | `2s`              | intervalle de scrutation de la file                          |
| `EXPORT_JANITOR_INTERVAL`      | `10m`             | fréquence de purge des exports expirés                       |
| `EXPORT_STORAGE_DIR`           | `./data/exports`  | dossier des documents (volume partagé entre API et workers)  |
| `SOFFICE_PATH`                 | `soffice`         | binaire LibreOffice                                          |

## Fournisseur OIDC

Déclarer un client **confidentiel** avec :

- flux *Authorization Code* (+ PKCE S256 si configurable) ;
- redirect URI : `${PUBLIC_URL}/auth/callback` ;
- post-logout redirect URI : `${PUBLIC_URL}/` ;
- scopes : `openid profile email`.

## Déploiement

- Image : `docker build --target runtime -t confluence-to-doc .` (LibreOffice inclus, utilisateur non-root,
  `HEALTHCHECK` intégré via `server healthcheck`).
- Placer l'application derrière un reverse proxy TLS ; `PUBLIC_URL` doit être l'URL `https://` publique.
- Les rôles `api` et `worker` doivent partager `EXPORT_STORAGE_DIR` (volume RWX) tant que le stockage est local.
- Sauvegarder PostgreSQL ; les fichiers d'export sont éphémères (48 h) et n'ont pas besoin de sauvegarde.
