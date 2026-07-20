.PHONY: help build install test test-verbose test-race coverage coverage-html lint vet fmt tidy clean check check-ci release release-snapshot release-patch release-minor release-major _bump

.DEFAULT_GOAL := help

# Version derived from git (matches the tag goreleaser stamps at release time).
# Falls back to "dev" outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/gotofritz/timbuktu/internal/cli.version=$(VERSION)

# List all documented targets (the `## ...` text after each target name).
help: ## Show this help
	@echo "Usage: make <target>"
	@echo
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | sort \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the tbuk binary into bin/
	mkdir -p bin && go build -ldflags "$(LDFLAGS)" -o bin/tbuk ./cmd/tbuk

install: ## Install tbuk to $GOPATH/bin
	go install -ldflags "$(LDFLAGS)" ./cmd/tbuk

test: ## Run all tests
	go test ./... -count=1

test-verbose: ## Run all tests with per-test output (-v)
	go test ./... -v -count=1

coverage: ## Print total coverage percentage
	go test ./... -coverprofile=coverage.out -count=1
	go tool cover -func=coverage.out | tail -1
	@rm -f coverage.out

coverage-html: ## Open HTML coverage report
	go test ./... -coverprofile=coverage.out -count=1
	go tool cover -html=coverage.out

test-race: ## Run tests with the race detector
	go test -race ./... -count=1

fmt: ## Format all Go files
	gofmt -w .

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

clean: ## Remove built binaries
	rm -rf bin/

check: fmt vet lint test ## Format, vet, lint, and test (run before committing)

# Mirror the quality-check CI jobs (lint + build + coverage >= 85% total and
# per package — AGENTS.md demands >= 85% for every package, not just the total).
# The coverage gate lives in scripts/check-coverage.sh, which CI invokes too, so
# the local and CI gates cannot drift.
check-ci: lint ## Full CI gate: lint + build + coverage >= 85%
	go build ./...
	@./scripts/check-coverage.sh

# Cut a release from an already-pushed tag (CI does this automatically on tag
# push; run manually only for a local/off-CI release). Requires goreleaser and
# GITHUB_TOKEN in env.
release: ## Run goreleaser against an already-pushed tag (normally CI does this)
	goreleaser release --clean

# Dry-run the release locally: builds all archives into dist/ without tagging,
# pushing, or publishing. Use to sanity-check the goreleaser config.
release-snapshot: ## Dry-run a release locally into dist/ (no tag, no push)
	goreleaser release --snapshot --clean

# Latest semver tag (v-prefixed), or v0.0.0 if none exists yet.
LATEST_TAG := $(shell git describe --tags --abbrev=0 --match='v[0-9]*' 2>/dev/null || echo v0.0.0)

# Tag the next version and push it, which triggers the Release workflow.
# Usage: make release-patch | release-minor | release-major
release-patch: ## Bump patch (v0.1.0 -> v0.1.1) and push tag
	@$(MAKE) --no-print-directory _bump BUMP=patch
release-minor: ## Bump minor (v0.1.1 -> v0.2.0) and push tag
	@$(MAKE) --no-print-directory _bump BUMP=minor
release-major: ## Bump major (v0.2.0 -> v1.0.0) and push tag
	@$(MAKE) --no-print-directory _bump BUMP=major

# Internal: compute the next tag from LATEST_TAG + BUMP, then tag and push it.
_bump:
	@if [ -n "$$(git status --porcelain)" ]; then \
	  echo "working tree is dirty — commit or stash before releasing"; exit 1; fi
	@branch=$$(git rev-parse --abbrev-ref HEAD); \
	if [ "$$branch" != "main" ]; then \
	  echo "releases must be cut from main (on '$$branch')"; exit 1; fi
	@cur='$(LATEST_TAG)'; v=$${cur#v}; \
	case "$$v" in \
	  [0-9]*.[0-9]*.[0-9]*) ;; \
	  *) echo "cannot parse latest tag '$$cur' as vMAJOR.MINOR.PATCH"; exit 1;; \
	esac; \
	major=$$(echo "$$v" | cut -d. -f1); \
	minor=$$(echo "$$v" | cut -d. -f2); \
	patch=$$(echo "$$v" | cut -d. -f3); \
	case "$(BUMP)" in \
	  major) major=$$((major+1)); minor=0; patch=0;; \
	  minor) minor=$$((minor+1)); patch=0;; \
	  patch) patch=$$((patch+1));; \
	  *) echo "BUMP must be patch|minor|major"; exit 1;; \
	esac; \
	next="v$$major.$$minor.$$patch"; \
	echo "Current: $$cur  ->  Next: $$next"; \
	printf "Tag and push %s? [y/N] " "$$next"; read ans; \
	[ "$$ans" = "y" ] || { echo "aborted"; exit 1; }; \
	git tag -a "$$next" -m "Release $$next" && \
	git push origin "$$next" && \
	echo "Pushed $$next — the Release workflow will build and publish it."
