#!/usr/bin/env bash
# Coverage gate shared by `make check-ci` and the CI coverage job, so the local
# and CI gates cannot drift. Enforces AGENTS.md's rule: coverage >= 85% for the
# total AND for every package (a package with no test files also fails).
set -euo pipefail

go test -coverpkg=./... ./internal/... -coverprofile=coverage.out -count=1
COVERAGE=$(go tool cover -func=coverage.out | grep "^total:" | awk '{print $3}' | tr -d '%')
rm -f coverage.out
echo "Total coverage: ${COVERAGE}%"
awk -v cov="${COVERAGE}" 'BEGIN {
  if (cov < 85) { print "FAIL: coverage " cov "% is below 85%"; exit 1 }
  else { print "PASS: coverage " cov "% >= 85%" }
}'

echo "Per-package coverage:"
go test ./internal/... -cover -count=1 | awk '
  /coverage:/ { for (i = 1; i <= NF; i++) if ($i ~ /%$/) { c = $i; sub(/%$/, "", c) };
                printf "  %-55s %s%%\n", $2, c;
                if (c + 0 < 85) { bad = 1; fail = fail "  " $2 " (" c "%)\n" } }
  /\[no test files\]/ { printf "  %-55s no tests\n", $2; bad = 1; fail = fail "  " $2 " (no test files)\n" }
  END { if (bad) { printf "FAIL: packages below 85%%:\n%s", fail; exit 1 }
        else print "PASS: every package >= 85%" }'
