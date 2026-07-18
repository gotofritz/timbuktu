.PHONY: build test test-race coverage coverage-html lint vet fmt tidy clean check check-ci serve release

# Build the binary
build:
	mkdir -p bin && go build -o bin/podcast .

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

# Mirror the quality-check CI jobs (lint + build + coverage >= 85%)
check-ci: lint
	go build ./...
	go test -coverpkg=./... ./... -coverprofile=coverage.out -count=1
	@COVERAGE=$$(go tool cover -func=coverage.out | grep "^total:" | awk '{print $$3}' | tr -d '%'); \
	rm -f coverage.out; \
	echo "Total coverage: $${COVERAGE}%"; \
	awk -v cov="$${COVERAGE}" 'BEGIN { if (cov < 85) { print "FAIL: coverage " cov "% is below 85%"; exit 1 } else { print "PASS: coverage " cov "% >= 85%" } }'

# Serve output/ over HTTP for local feed testing (install python3 if needed)
serve:
	cd output && python3 -m http.server 8080

# Cut a release — requires: brew install goreleaser, GITHUB_TOKEN in env, git tag pushed
release:
	goreleaser release --clean
