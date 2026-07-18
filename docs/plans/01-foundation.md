# Subplan 01: Project Foundation

## Goal

Bootstrap the Go project: module, directory layout, config loading, CLI skeleton,
and `tbuk init` command. No storage or LLM logic here — just the skeleton every
other subplan builds on.

## Deliverables

- `go.mod` / `go.sum` with minimal deps
- Full internal directory skeleton (empty packages with package declarations)
- Config loading from `~/.tbuk/config.yaml` with sane defaults
- `cobra`-based CLI root command
- `tbuk init` command (creates `~/.tbuk/` and default config + prompt dirs)
- Unit tests ≥ 85 % coverage for `internal/config` and `internal/cli`

## Directory Layout

```
cmd/
  tbuk/
    main.go

internal/
  cli/
    root.go          ← cobra root, global flags (--config, --db)
    init.go          ← `tbuk init` subcommand
    version.go       ← `tbuk version` subcommand

  config/
    config.go        ← Config struct, Load(), Defaults()
    config_test.go

  storage/           ← empty stub (package declaration only)
  ingest/            ← empty stub
  preprocess/        ← empty stub
  chunking/          ← empty stub
  embeddings/        ← empty stub
  retrieval/         ← empty stub
  prompts/           ← empty stub
  llm/               ← empty stub
  search/            ← empty stub
  metadata/          ← empty stub
```

## Config Schema

```yaml
database:
  path: ~/.tbuk/tbuk.sqlite

llm:
  provider: ollama    # ollama | claude | openai | llama
  model: ""           # provider default when empty

embedding:
  provider: ollama    # ollama | openai | voyage
  model: ""
  dimension: 768

chunking:
  size: 800           # tokens (approximated as chars/4)
  overlap: 100
```

## `tbuk init` Behaviour

1. Create `~/.tbuk/` if absent
2. Write default `config.yaml` unless it exists (never overwrite)
3. Create `~/.tbuk/prompts/` with one built-in `qa/` template
4. Print confirmation with path

## Dependencies

| Package | Version | Reason |
|---------|---------|--------|
| `github.com/spf13/cobra` | latest | CLI framework |
| `gopkg.in/yaml.v3` | latest | YAML config |

## Tests

- `TestLoadConfig_defaults` — missing file → defaults applied
- `TestLoadConfig_partial` — partial YAML → missing keys get defaults
- `TestLoadConfig_badYAML` — malformed YAML → error returned
- `TestInitCommand_creates_dirs` — `tbuk init` creates expected paths
- `TestInitCommand_idempotent` — second `tbuk init` does not overwrite existing config

## PR Scope

One PR. Touches only: `go.mod`, `go.sum`, `cmd/`, `internal/` stubs + config + cli.
No storage, no HTTP calls, no external services.
