.PHONY: build install test test-race coverage coverage-html lint vet fmt tidy clean check check-ci serve release

# Build the binary
build:
	mkdir -p bin && go build -o bin/tbuk ./cmd/tbuk

# Install to $GOPATH/bin (adds tbuk to PATH if GOPATH/bin is on PATH)
install:
	go install ./cmd/tbuk

# Run all tests
test:
	go test ./... -v -count=1

# Run tests and print total coverage percentage
coverage:
	go test ./... -coverprofile=coverage.out -count=1
	go tool cover -func=coverage.out | tail -1
	@rm -f coverage.out

# Run tests and open HTML coverage report
coverage-html:
	go test ./... -coverprofile=coverage.out -count=1
	go tool cover -html=coverage.out

# Run tests with race detector
test-race:
	go test -race ./... -count=1

# Format all Go files
fmt:
	gofmt -w .

# Run go vet
vet:
	go vet ./...

# Run golangci-lint (install: brew install golangci-lint)
lint:
	golangci-lint run ./...

# Tidy go.mod and go.sum
tidy:
	go mod tidy

# Remove built binary
clean:
	rm -rf bin/

# Full check — run before committing
check: fmt vet lint test

# Mirror the quality-check CI jobs (lint + build + coverage >= 85% total and
# per package — AGENTS.md demands >= 85% for every package, not just the total)
check-ci: lint
	go build ./...
	go test -coverpkg=./... ./internal/... -coverprofile=coverage.out -count=1
	@COVERAGE=$$(go tool cover -func=coverage.out | grep "^total:" | awk '{print $$3}' | tr -d '%'); \
	rm -f coverage.out; \
	echo "Total coverage: $${COVERAGE}%"; \
	awk -v cov="$${COVERAGE}" 'BEGIN { if (cov < 85) { print "FAIL: coverage " cov "% is below 85%"; exit 1 } else { print "PASS: coverage " cov "% >= 85%" } }'
	@echo "Per-package coverage:"; \
	go test ./internal/... -cover -count=1 | awk '\
	  /coverage:/ { for (i = 1; i <= NF; i++) if ($$i ~ /%$$/) { c = $$i; sub(/%$$/, "", c) }; \
	                printf "  %-55s %s%%\n", $$2, c; \
	                if (c + 0 < 85) { bad = 1; fail = fail "  " $$2 " (" c "%)\n" } } \
	  /\[no test files\]/ { printf "  %-55s no tests\n", $$2; bad = 1; fail = fail "  " $$2 " (no test files)\n" } \
	  END { if (bad) { printf "FAIL: packages below 85%%:\n%s", fail; exit 1 } \
	        else print "PASS: every package >= 85%" }'

# Serve output/ over HTTP for local feed testing (install python3 if needed)
serve:
	cd output && python3 -m http.server 8080

# Cut a release — requires: brew install goreleaser, GITHUB_TOKEN in env, git tag pushed
release:
	goreleaser release --clean
