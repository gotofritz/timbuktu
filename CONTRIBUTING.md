# Contributing

## One-time setup

The repository enforces formatting, linting, tests, and commit-message
conventions through [pre-commit](https://pre-commit.com) hooks. Install them
once after cloning:

```bash
# Python tooling for the commit-msg gate (pipx keeps them isolated)
pipx install pre-commit
pipx install commitizen

# Go tooling used by the hooks
go install golang.org/x/tools/cmd/goimports@latest
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2  # match CI (see README)

# Wire the hooks into .git/hooks (installs both pre-commit and commit-msg)
pre-commit install --install-hooks
```

`commitizen`, `pre-commit`, and `goimports` must be on your `PATH` — the hooks
run them as system commands. (This is the one place the project needs Python
tooling; everything else is the standard Go toolchain.)

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org):

```
<type>(<optional scope>): <imperative subject ≤ 72 chars>
```

Common types: `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `build`,
`ci`, `chore`. The `feat`/`fix` prefixes drive the GoReleaser changelog
grouping, so they are not cosmetic.

The `commit-msg` hook runs `cz check` and **rejects** any subject that does not
match. Examples:

```
feat(search): add hybrid RRF ranking
fix(ingest): guard against embedding count mismatch
ci: run the test suite on macOS as well as Linux
```

## Before opening a PR

```bash
make check-ci   # golangci-lint + build + coverage ≥ 85% (total and per package)
```

Keep PRs focused, include tests for behaviour changes, and update the docs
under `docs/` when you touch architecture, boundaries, or core patterns.
