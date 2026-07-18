#!/usr/bin/env bash
# Block writing non-test .go files when no *_test.go exists in the same directory.
set -euo pipefail

FILE=$(jq -r '.tool_input.file_path // empty' 2>/dev/null)

# Not a Go file — allow
[[ "$FILE" == *.go ]] || exit 0
# Is a test file itself — allow
[[ "$FILE" == *_test.go ]] && exit 0

DIR=$(dirname "$FILE")

# If a _test.go already exists in that dir, allow
ls "$DIR"/*_test.go &>/dev/null && exit 0

# No test file found — block
PKG=$(basename "$DIR")
printf '{"continue":false,"stopReason":"TDD violation: write %s_test.go FIRST, then implement."}\n' "$PKG"
exit 0
