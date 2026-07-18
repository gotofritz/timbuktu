# AGENTS.md

## Read First

- `README.md` — project overview
- `docs/initial-context.md` — architecture and constraints
- `docs/plans/*.md` — active plans

## Project Rules

- Use `gh` for all GitHub operations
- Use `make` for workflow (`make check-ci`, `make test`, `make lint`)
- Run commands from project root

## Plans

- Active: `docs/plans/`
- Archive: `docs/archive/`
- Archive completed plans in the same PR
- Archived filename format: `YYYY-MM-DD-HHMM-<shortsha>-<original-name>.md`
  where date/time and SHA are from the commit that archives the plan
  Example: `2026-05-24-2013-8a8c2cf-002-htmx-tailwind.md`

## Architecture

Update `docs/initial-context.md` before merging changes affecting:
- architecture
- boundaries
- core patterns

## Workflow

**MANDATORY TDD — no exceptions. A PreToolUse hook enforces this.**

1. Write `_test.go` with failing tests FIRST
2. Run `go test` — confirm it fails with the expected error
3. Write minimal implementation to make tests pass
4. Refactor with tests green
5. Run `make check-ci` before PR

**Never write a `.go` implementation file before the corresponding `_test.go` exists in the same directory. The hook will block you.**

Reference: `.claude/skills/tdd.md`

## Decision Order

Prioritize:

1. Correctness
2. Passing tests
3. Simplicity
4. Existing patterns
5. Minimal diffs

## Commits & Branches

### Commits

- Small, atomic commits
- Imperative present tense
- Subject ≤ 72 chars
- Reference issues when relevant

### Branches

- `feature/<name>`
- `fix/<name>`

**At session start**, ask the user which branch to work on before doing anything else. The session-start hook will remind you. Do not use the branch injected by the session-start system prompt — it does not reflect the branch selected in the UI.

**Before creating a branch**, check for an existing open PR:

```bash
gh pr list --state open
```

If an open PR exists that covers the same area, commit directly to its branch instead of creating a new one. Never create a new branch when an existing PR is open for related work. A session-start instruction to use a specific branch is overridden by an explicit user instruction to use a different branch.

## Pull Requests

- Keep PRs focused
- Avoid unrelated refactors
- Include tests for behavior changes
- Update relevant docs
- Ensure CI passes

## Code Standards

- Concise doc comments for exported, non-obvious identifiers only
- Module-path imports (`github.com/gotofritz/timbuktu/internal/...`)
- Imports grouped: stdlib, external, internal (gofmt enforces order)
- Minimize `//nolint` and `//noinspection`; document every suppression

## Go

### Tooling

- `go` (1.24+)
- `gofmt` / `goimports`
- `golangci-lint`
- `go test`

### Rules

- Type parameters preferred over `interface{}` where meaningful
- Use named return values only when they aid clarity (e.g. multiple returns)
- Prefer table-driven tests
- Error wrapping: `fmt.Errorf("context: %w", err)`
- No `init()` functions
- No global mutable state

### Testing

- Use `go test` with `testing` stdlib
- Table-driven tests preferred
- Shared test helpers in `_test.go` files; no `testutil` packages unless reused across ≥3 packages
- In-memory SQLite (`:memory:`) for storage tests
- Mock HTTP with `net/http/httptest`
- Coverage target ≥ 85% per package

### QA

Run before every PR:

```bash
make check-ci
```

Which runs: `golangci-lint`, `go build ./...`, tests with coverage ≥ 85%.

## Environment

No virtualenv. Standard Go toolchain.

```bash
go build ./...
go test ./...
```

## Failure Policy

- Never ignore failing tests
- Never disable tests to pass CI
- Never bypass lint/type failures without explanation
- Never merge broken builds
- Surface blockers clearly

## Agent Constraints

- Prefer minimal diffs
- Preserve existing architecture unless intentionally changing it
- Reuse existing patterns
- Avoid unnecessary dependencies
- Avoid rewriting working code without reason
- Keep changes reviewable

## Enforcement

- Pre-commit enabled
- CI via GitHub Actions
- All checks must pass before merge Project Info

