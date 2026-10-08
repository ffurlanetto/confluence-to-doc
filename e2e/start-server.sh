#!/usr/bin/env bash
# Starts the application for the end-to-end tests on a fresh database, against
# the fake Confluence and identity provider of `go run ./cmd/devmocks`.
set -euo pipefail

admin_url="${E2E_ADMIN_DATABASE_URL:-postgres://c2d:c2d@localhost:5432/postgres?sslmode=disable}"
db="${E2E_DATABASE:-c2d_e2e}"
psql "$admin_url" -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" \
  -c "CREATE DATABASE $db"

e2e="$(cd "$(dirname "$0")" && pwd)"
storage="$e2e/.storage"
rm -rf "$storage" && mkdir -p "$storage"

# Built, then exec'd, so that stopping this script stops the server itself.
cd "$e2e/../backend"
go build -o "$e2e/.bin/server" ./cmd/server
DATABASE_URL="${admin_url%/*}/$db?sslmode=disable" \
OIDC_ISSUER_URL=http://localhost:8091 \
OIDC_CLIENT_ID=confluence-to-doc \
OIDC_CLIENT_SECRET=dev-secret \
OIDC_ADMIN_GROUPS=c2d-admins \
CONFLUENCE_BASE_URL=http://localhost:8090 \
ENCRYPTION_KEY=MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY= \
PUBLIC_URL=http://localhost:8080 \
STATIC_DIR=../frontend/dist \
EXPORT_STORAGE_DIR="$storage" \
EXPORT_POLL_INTERVAL=500ms \
DOCUMENT_CLASSIFICATIONS="Internal,Confidential:watermark" \
exec "$e2e/.bin/server"
