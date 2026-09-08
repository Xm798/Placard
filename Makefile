.PHONY: build build-cli vet fmt fmt-check test test-integration test-all run up down gates frontend frontend-install

# Unit tests (no build tags). Every test that touches the database gets its own
# private SQLite one (testutil.OpenTestDB), and the session/device-code stores
# under test are the SQL ones — so nothing external is needed and packages run
# in parallel.
test:
	go test ./...

# The same tests against a Postgres (and, for the handler contract, the
# Redis-backed stores over miniredis). They share one database, so they MUST run
# serialized across packages (-p 1): parallel packages would TRUNCATE each
# other's tables mid-test. Requires `make up` first, or TEST_DSN pointing at
# some other Postgres — which is how CI runs it against a service container.
test-integration:
	go test -tags=integration -p 1 ./...

# Everything: unit + integration (serialized).
test-all: test test-integration

frontend-install:
	cd frontend && npm ci

# Clean only the hashed assets (preserve the committed dist/.gitkeep), then build.
frontend:
	rm -rf internal/web/dist/assets
	cd frontend && npm run build

build: frontend
	go build -ldflags="$(LDFLAGS)" ./...

# CLI is a standalone package main that never imports internal/web, so no
# frontend build is needed — unlike `build`.
build-cli:
	go build -ldflags="$(LDFLAGS)" -o placard ./cmd/placard

# Build metadata injected into internal/version via ldflags. MODULE is read from
# go.mod so the -X import paths stay correct if the module path changes.
MODULE     := $(shell head -1 go.mod | awk '{print $$2}')
# --match 'v[0-9]*' is load-bearing: the CLI ships under a separate cli/vX.Y.Z
# tag namespace, and an unfiltered `git describe` would report those tags as
# the SERVER version (/api/version, startup log, image tag).
VERSION    := $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GIT_BRANCH ?= $(shell git branch --show-current 2>/dev/null || echo unknown)
LDFLAGS    := -s -w \
  -X $(MODULE)/internal/version.Version=$(VERSION) \
  -X $(MODULE)/internal/version.BuildTime=$(BUILD_TIME) \
  -X $(MODULE)/internal/version.GitCommit=$(GIT_COMMIT) \
  -X $(MODULE)/internal/version.GitBranch=$(GIT_BRANCH)

vet:
	go vet ./...

fmt:
	gofmt -l .

# gofmt cleanliness as a standalone gate (excludes docs/).
fmt-check:
	@out="$$(gofmt -l . | grep -v '^docs/' || true)"; \
	if [ -n "$$out" ]; then echo "gofmt needs formatting:"; echo "$$out"; exit 1; fi
	@echo "gofmt clean"

# Full local gate, everything CI runs on a pull request. Integration tests are
# deliberately out: they need a Postgres, which `make test-all` adds.
gates: build vet fmt-check test

# The development dependency stack, kept apart from the deployment compose
# file. Only `make test-integration` needs it; `make test` and `make run` do not.
COMPOSE_DEV := docker compose -f docker-compose.dev.yaml

up:
	$(COMPOSE_DEV) up -d

down:
	$(COMPOSE_DEV) down

# Run the app locally: SQLite under ./data, no Redis. APP_ENV=local is what
# permits auth.dev_mock, which config.Validate rejects anywhere else.
run:
	APP_ENV=local go run ./
