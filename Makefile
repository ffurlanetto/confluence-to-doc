# Common developer tasks. Run `make help` for the list.
SHELL := /bin/bash
.DEFAULT_GOAL := help

BACKEND  := backend
FRONTEND := frontend
TEST_DATABASE_URL ?= postgres://c2d:c2d@localhost:5432/c2d?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------- setup
.PHONY: install
install: ## Install frontend dependencies and download Go modules
	cd $(FRONTEND) && npm ci
	cd $(BACKEND) && go mod download

## ---------------------------------------------------------------- quality
.PHONY: check
check: lint test ## Everything CI runs (lint + tests)

.PHONY: lint
lint: lint-backend lint-frontend ## Lint backend and frontend

.PHONY: lint-backend
lint-backend:
	cd $(BACKEND) && gofmt -l . | (! grep .) && go vet ./... && golangci-lint run ./...

.PHONY: lint-frontend
lint-frontend:
	cd $(FRONTEND) && npm run typecheck && npm run lint && npm run format:check

.PHONY: fmt
fmt: ## Format all code
	cd $(BACKEND) && gofmt -w . && go mod tidy
	cd $(FRONTEND) && npm run format

.PHONY: test
test: test-backend test-frontend ## Run all tests

.PHONY: test-backend
test-backend: ## Backend tests (PostgreSQL tests run when TEST_DATABASE_URL is reachable)
	cd $(BACKEND) && TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -race -count=1 ./...

.PHONY: test-backend-short
test-backend-short: ## Backend unit tests only (no PostgreSQL, no LibreOffice)
	cd $(BACKEND) && TEST_DATABASE_URL= go test -short ./...

.PHONY: test-frontend
test-frontend:
	cd $(FRONTEND) && npm test

.PHONY: e2e
e2e: ## End-to-end tests in a browser: real server and LibreOffice, fake Confluence and IdP (needs PostgreSQL)
	cd $(FRONTEND) && npm run build
	cd e2e && npm ci && npx playwright test

.PHONY: check-alerts
check-alerts: ## Validate and unit-test the alert rules of the chart (needs helm, yq, promtool)
	helm template c2d charts/confluence-to-doc -f charts/confluence-to-doc/values-production.yaml \
		--set monitoring.prometheusRule.enabled=true --show-only templates/prometheusrule.yaml \
		| yq '.spec' > charts/confluence-to-doc/tests/rules.yaml
	promtool check rules charts/confluence-to-doc/tests/rules.yaml
	promtool test rules charts/confluence-to-doc/tests/alerts.test.yaml

.PHONY: confluence-check
confluence-check: ## Read-only compatibility check against a real Confluence (CONFLUENCE_LIVE_URL, _PAT, _PAGE)
	cd $(BACKEND) && go test -tags live -count=1 -v -run TestLiveConfluence ./internal/confluence/

## ---------------------------------------------------------------- run
.PHONY: build
build: ## Build the SPA and the Go binaries into backend/bin
	cd $(FRONTEND) && npm run build
	cd $(BACKEND) && go build -o bin/ ./cmd/...

.PHONY: dev-mocks
dev-mocks: ## Run the fake Confluence (:8090) and fake OIDC provider (:8091)
	cd $(BACKEND) && go run ./cmd/devmocks

.PHONY: dev-backend
dev-backend: ## Run the backend on :8080 with .env (see .env.example)
	cd $(BACKEND) && set -a && source ../.env && set +a && go run ./cmd/server

.PHONY: dev-frontend
dev-frontend: ## Run the Vite dev server on :5173 (proxies /api and /auth to :8080)
	cd $(FRONTEND) && npm run dev

.PHONY: up
up: ## Start the full stack with docker compose (app + postgres + mocks)
	docker compose up --build

.PHONY: down
down: ## Stop the docker compose stack
	docker compose down

.PHONY: up-release
up-release: ## Start the stack from the released image (no build); needs a real .env
	docker compose -f docker-compose.release.yml up -d

.PHONY: down-release
down-release: ## Stop the released-image stack
	docker compose -f docker-compose.release.yml down
