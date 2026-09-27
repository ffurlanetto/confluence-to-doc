# Configuration

All configuration comes from environment variables (12-factor), loaded and validated at startup by
`backend/internal/config`. An invalid configuration stops the process and reports **every** error at once.

## Required

| Variable              | Description                                                                          |
| --------------------- | ------------------------------------------------------------------------------------ |
| `DATABASE_URL`        | PostgreSQL DSN, e.g. `postgres://user:pass@host:5432/db?sslmode=require`              |
| `OIDC_ISSUER_URL`     | OIDC issuer URL (discovered through `/.well-known/openid-configuration`)              |
| `OIDC_CLIENT_ID`      | OAuth2 client id (confidential client)                                                |
| `OIDC_CLIENT_SECRET`  | Client secret                                                                         |
| `CONFLUENCE_BASE_URL` | Confluence Server/Data Center base URL (including any context path)                   |
| `ENCRYPTION_KEY`      | 32 random bytes, base64 (`openssl rand -base64 32`), used to encrypt the users' PATs  |

> ⚠️ Changing `ENCRYPTION_KEY` makes existing PATs unreadable: every user has to enter theirs again.

## Optional

| Variable                       | Default                 | Description                                                    |
| ------------------------------ | ----------------------- | -------------------------------------------------------------- |
| `APP_ROLE`                     | `all`                   | `all`, `api` or `worker`                                        |
| `PUBLIC_URL`                   | `http://localhost:8080` | Public URL; sets the OIDC redirect URI and the origin check. With `https`, cookies become `Secure` and HSTS is sent |
| `HTTP_ADDR`                    | `:8080`                 | HTTP listener                                                   |
| `METRICS_ADDR`                 | `:9090`                 | Prometheus listener (empty disables it). Do not expose publicly |
| `STATIC_DIR`                   | _(empty)_               | Directory of the built SPA to serve                             |
| `LOG_LEVEL`                    | `info`                  | `debug`, `info`, `warn`, `error`                                |
| `LOG_FORMAT`                   | `json`                  | `json` or `text`                                                |
| `OIDC_SCOPES`                  | `openid profile email`  | Requested scopes                                                |
| `SESSION_TTL`                  | `12h`                   | Session lifetime                                                |
| `CONFLUENCE_TIMEOUT`           | `30s`                   | Timeout of a single Confluence request                          |
| `CONFLUENCE_FETCH_CONCURRENCY` | `4`                     | Parallel Confluence requests per export                         |
| `EXPORT_RETENTION`             | `48h`                   | How long a document stays downloadable                          |
| `EXPORT_WORKER_CONCURRENCY`    | `2`                     | Exports generated simultaneously per worker instance            |
| `EXPORT_MAX_ACTIVE_PER_USER`   | `5`                     | Queued or running exports per user                              |
| `EXPORT_MAX_PAGES`             | `500`                   | Maximum pages per export                                        |
| `EXPORT_MAX_ATTEMPTS`          | `3`                     | Attempts on transient errors                                    |
| `EXPORT_JOB_TIMEOUT`           | `15m`                   | Maximum duration of one export                                  |
| `EXPORT_MAX_IMAGE_BYTES`       | `10485760`              | Maximum size of an embedded image                               |
| `EXPORT_POLL_INTERVAL`         | `2s`                    | Queue polling interval                                          |
| `EXPORT_JANITOR_INTERVAL`      | `10m`                   | How often expired exports are purged                            |
| `EXPORT_STORAGE_DIR`           | `./data/exports`        | Directory of generated documents in local storage (ignored when S3 is enabled) |
| `SOFFICE_PATH`                 | `soffice`               | LibreOffice binary                                              |
| `WORD_TEMPLATE_PATH`           | _(empty)_               | Company Word template applied to every document                 |

## Company Word template

Setting `WORD_TEMPLATE_PATH` to a `.docx`/`.dotx` file makes every export — Word and PDF — come out in the
company format: header, footer, fonts and page layout. Leaving it empty keeps the built-in styling.

The file is read and validated at startup; every instance running workers needs it. How to prepare the
template and what it must contain is covered in [word-template.md](word-template.md).

## Document storage (S3 feature flag)

The storage backend is chosen at startup:

- **`S3_BUCKET` set → S3 storage** (AWS S3 or compatible: MinIO, Ceph RGW, Garage, Scaleway, OVH…);
- **otherwise → local disk** (`EXPORT_STORAGE_DIR`).

The choice is logged at startup (`document storage: S3` / `document storage: local disk`). With S3 the
bucket is checked at startup (`HeadBucket`), so a misconfiguration stops the process immediately.

| Variable               | Default       | Description                                                                   |
| ---------------------- | ------------- | ----------------------------------------------------------------------------- |
| `S3_BUCKET`            | _(empty)_     | Bucket name; **enables S3**                                                   |
| `S3_REGION`            | `us-east-1`   | Region                                                                        |
| `S3_ENDPOINT`          | _(empty)_     | URL of an S3-compatible service (e.g. `https://minio.example.com`); empty = AWS |
| `S3_ACCESS_KEY_ID`     | _(empty)_     | Static key; empty uses the AWS default credential chain (`AWS_*`, profile, IAM role / IRSA) |
| `S3_SECRET_ACCESS_KEY` | _(empty)_     | Matching secret (required when the key is set)                                |
| `S3_PREFIX`            | `exports/`    | Object key prefix inside the bucket                                           |
| `S3_FORCE_PATH_STYLE`  | `true` when `S3_ENDPOINT` is set, otherwise `false` | `endpoint/bucket/key` addressing (required by most compatible services) |

Minimum IAM permissions (on `arn:aws:s3:::<bucket>` and `arn:aws:s3:::<bucket>/<prefix>*`):
`s3:ListBucket` (bucket check, and so a missing object answers 404 rather than 403), `s3:PutObject`,
`s3:GetObject`, `s3:DeleteObject`.

The janitor deletes objects when they expire (48 h). As a safety net, add a lifecycle rule on the prefix
(e.g. expire after 3 days): an object removed by that rule is simply reported as not found on download.

## OIDC provider

Declare a **confidential** client with:

- the *Authorization Code* flow (with PKCE S256 if configurable);
- redirect URI: `${PUBLIC_URL}/auth/callback`;
- post-logout redirect URI: `${PUBLIC_URL}/`;
- scopes: `openid profile email`.

## Deployment

- Image: `docker build --target runtime -t confluence-to-doc .` (LibreOffice included, non-root user,
  built-in `HEALTHCHECK` through `server healthcheck`).
- Put the application behind a TLS reverse proxy; `PUBLIC_URL` must be the public `https://` URL.
- With local storage, the `api` and `worker` roles must share `EXPORT_STORAGE_DIR` (RWX volume). With S3
  (`S3_BUCKET`) no shared volume is needed: API and workers can run on different machines.
- Back up PostgreSQL; the exported documents are ephemeral (48 h) and need no backup.
