# syntax=docker/dockerfile:1

# ---- Frontend build -------------------------------------------------------
FROM node:26-bookworm-slim AS web
WORKDIR /web
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---- Backend build --------------------------------------------------------
FROM golang:1.27-bookworm AS api
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/devmocks ./cmd/devmocks

# ---- Runtime --------------------------------------------------------------
# LibreOffice (Writer only, no GUI) performs the HTML -> PDF/DOCX conversion.
FROM debian:bookworm-slim AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      libreoffice-writer-nogui fonts-liberation fonts-dejavu-core ca-certificates tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --create-home --home-dir /home/app app \
 && mkdir -p /data/exports && chown -R app:app /data

COPY --from=api /out/server /app/server
COPY --from=web /web/dist /app/web

ENV STATIC_DIR=/app/web \
    EXPORT_STORAGE_DIR=/data/exports \
    HTTP_ADDR=:8080 \
    HOME=/home/app

USER app
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s CMD ["/app/server", "healthcheck"]
ENTRYPOINT ["/usr/bin/tini", "--", "/app/server"]

# ---- Development mocks (fake Confluence + fake OIDC provider) -------------
FROM gcr.io/distroless/static-debian12:nonroot AS devmocks
COPY --from=api /out/devmocks /devmocks
ENTRYPOINT ["/devmocks"]
