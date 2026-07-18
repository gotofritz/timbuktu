# timbuktu

Local-first CLI knowledge base for indexing and querying personal documents with RAG. Single SQLite database, modular architecture, provider-agnostic LLM and embedding interfaces.

## Status

| Subplan | Area | State |
|---------|------|-------|
| 01 — Foundation | CLI skeleton, config loading, `tbuk init` | ✅ done |
| 02 — Storage | SQLite schema, migrations, typed repositories, FTS5 | ✅ done |
| 03 — Preprocessing | Text extraction (Markdown, plain text, PDF, HTML), chunking, SHA256 | ✅ done |
| 04 — Embeddings | Embedding provider interface + adapters | planned |
| 05 — LLM Providers | LLM interface + adapters (Ollama, Claude, OpenAI) | planned |
| 06 — Ingestion | SHA256 dedup, chunking, store pipeline | planned |
| 07 — Search | Vector search, FTS5 keyword search, hybrid | planned |
| 08 — RAG | Retrieval pipeline, prompt templates, streaming | planned |
| 09 — Management | `tbuk stats`, `tbuk delete`, `tbuk update` | planned |

## Requirements

- Go 1.25+
- `golangci-lint` v2 (`go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`)

## Quick start

```bash
make install             # installs to $(go env GOPATH)/bin — defaults to ~/go/bin
tbuk init                # create ~/.tbuk/ with default config and prompt dirs
tbuk version
tbuk doctor              # check config, database, LLM connectivity, and extractors
tbuk preprocess <path>   # extract and chunk text from a document (--format text|json)
```

If `tbuk` is not found after install, add Go's bin dir to your shell profile:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

## Development

```bash
make test          # run all tests
make test-race     # tests with race detector
make lint          # golangci-lint
make coverage      # total coverage percentage
make check-ci      # full CI gate: lint + build + coverage ≥ 85%
```

## Configuration

Default config at `~/.tbuk/config.yaml` (created by `tbuk init`):

```yaml
database:
  path: ~/.tbuk/tbuk.sqlite

llm:
  provider: llama    # llama | ollama | claude | openai
  model: ""          # provider default when empty

embedding:
  provider: llama    # llama | ollama | openai | voyage
  model: ""
  dimension: 768

chunking:
  size: 800          # tokens (approximated as chars/4)
  overlap: 100
```

Override config file: `tbuk --config /path/to/config.yaml <cmd>`

## Architecture

```
cmd/tbuk/           entry point

internal/
  cli/              cobra root + subcommands
  config/           Config struct, Load(), Defaults()
  storage/          SQLite: Open, migrations, DocumentRepo, ChunkRepo, MetadataRepo
  preprocess/       Extractor interface; Markdown, plain-text, HTML, PDF backends; SHA256 helpers
  chunking/         Chunker.Split — greedy sentence accumulation, configurable size/overlap
  embeddings/       (planned) Embedder interface + provider adapters
  ingest/           (planned) ingestion pipeline
  llm/              (planned) LLM interface + provider adapters
  retrieval/        (planned) vector + FTS5 + hybrid search
  prompts/          (planned) template loader and renderer
  search/           (planned) search command handler
  metadata/         (planned) metadata command handler
```

Dependencies point inward. Providers depend only on shared interfaces defined in `internal/llm` and `internal/embeddings`.

## Storage schema

```sql
documents   — path, sha256, title, mime_type, timestamps
chunks      — document_id, chunk_index, text, token_count, embedding BLOB
metadata    — document_id, key, value  (key/value per document)
chunks_fts  — FTS5 virtual table over chunks.text (auto-synced via triggers)
```

Embeddings stored as little-endian `[]float32` BLOBs. Cascade delete on document removal.

## License

[MIT](./LICENSE)
