#!/usr/bin/env bash
# Install the pinned golangci-lint, built with the module's own Go toolchain.
#
# Why this exists: the prebuilt golangci-lint binaries — and a plain
# `go install …/golangci-lint@vX` under the default GOTOOLCHAIN=auto — are
# compiled with an *older* Go than this module targets, and then refuse to lint
# it:
#
#   can't load config: the Go language version (go1.25) used to build
#   golangci-lint is lower than the targeted Go version (1.26.2)
#
# The fix is to build golangci-lint with the same Go version it must lint. This
# script pins GOTOOLCHAIN to the module's `go` directive and sources the linter
# version from the CI workflow, so local and CI never drift. `make lint` runs
# it automatically; it is a no-op once the right binary is installed.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workflow="$repo_root/.github/workflows/quality-check.yml"
gomod="$repo_root/go.mod"

# Pinned linter version — single source of truth is the CI workflow.
version="$(grep -oE 'version:[[:space:]]*v[0-9]+\.[0-9]+\.[0-9]+' "$workflow" \
  | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
if [ -z "${version:-}" ]; then
  echo "install-golangci-lint: could not read pinned version from $workflow" >&2
  exit 1
fi

# Go toolchain the module targets, e.g. "1.26.2" -> GOTOOLCHAIN "go1.26.2".
gover="$(grep -oE '^go[[:space:]]+[0-9]+\.[0-9]+(\.[0-9]+)?' "$gomod" \
  | awk '{print $2}')"
if [ -z "${gover:-}" ]; then
  echo "install-golangci-lint: could not read go version from $gomod" >&2
  exit 1
fi
toolchain="go${gover}"
gomajor="go$(printf '%s' "$gover" | cut -d. -f1,2)"   # go1.26.2 -> go1.26

bindir="$(go env GOPATH)/bin"
bin="$bindir/golangci-lint"
want_num="${version#v}"

# Skip the (slow, source) build when the installed binary already matches both
# the pinned version and the required Go toolchain line — a wrong-toolchain
# binary reports e.g. "built with go1.25.x" and must be rebuilt.
if [ -x "$bin" ]; then
  have="$("$bin" version 2>/dev/null || true)"
  if printf '%s' "$have" | grep -qF "$want_num" \
    && printf '%s' "$have" | grep -qF "built with $gomajor"; then
    echo "golangci-lint $want_num (built with $gomajor) already installed"
    exit 0
  fi
fi

echo "Installing golangci-lint $version, built with $toolchain …"
GOTOOLCHAIN="$toolchain" GOBIN="$bindir" \
  go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$version"
"$bin" version
