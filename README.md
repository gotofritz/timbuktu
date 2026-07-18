# timbuktu

Local-first CLI knowledge base for indexing and querying personal documents with RAG. Single SQLite database, modular architecture, provider-agnostic LLM and embedding interfaces.

## Documentation

See [User Guide](docs/user-guide.md) for a full walkthrough — what RAG is,
how to index your documents, and how to query your knowledge base.

## Requirements

- Go 1.25+
- `golangci-lint` v2 (`go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`)

## Quick start

```bash
make install             # installs to $(go env GOPATH)/bin — defaults to ~/go/bin
tbuk init                # create ~/.tbuk/ with default config and prompt dirs
tbuk version
tbuk doctor              # check config, database, LLM connectivity, and extractors
tbuk preprocess <path>   # extract text from document → save to ~/.tbuk/extracted/ (--dry-run, --output-dir)
tbuk ingest <path>       # read extracted text → chunk → embed → store in DB (--force, --verbose)
tbuk search <query>      # search chunks by vector/keyword/hybrid (--mode, --top, --min-score, --format)
tbuk find <key=value>... # find documents by metadata filters (--limit, --format)
tbuk ask <question>      # RAG: retrieve relevant chunks, render prompt template, stream LLM answer
tbuk template list       # list prompt templates in ~/.tbuk/prompts/
tbuk template show <n>   # print manifest + template files
tbuk template edit <n>   # open template manifest in $EDITOR
tbuk delete <path>       # remove a document and all its chunks (--yes skips prompt)
tbuk update <path>       # re-ingest if SHA256 changed, skip otherwise (--force)
tbuk stats               # knowledge base summary: doc/chunk counts, size (--format text|json)
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
  provider: llama    # llama | ollama | openai
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
  embeddings/       Embedder interface; llama.cpp, Ollama, OpenAI adapters
  ingest/           Ingester: SHA256 dedup, extract → chunk → embed → store pipeline
  llm/              LLM interface; Claude, OpenAI, Ollama adapters (SSE + JSON-lines streaming)
  search/           Searcher: Vector (cosine), Keyword (FTS5 BM25), Metadata, Hybrid (RRF)
  retrieval/        Retriever: hybrid search → RetrievedChunk with Citation string
  prompts/          TemplateDir, Manifest, Template.Render — disk-based text/template system
  metadata/         stub (not yet active)
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

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `tbuk` command not found after install | Go bin dir not in PATH | Add `export PATH="$PATH:$(go env GOPATH)/bin"` to shell profile and restart terminal |
| `tbuk doctor` shows LLM or embedding unreachable | llama.cpp not running, or wrong port | Start llama.cpp; verify `llm.base_url` / `embedding.base_url` in `~/.tbuk/config.yaml` |
| `tbuk ingest` produces 0 chunks | File is empty or extension not supported | Check file has content; supported: `.md`, `.txt`, `.pdf`, `.html`, `.htm` |
| `tbuk ask` returns irrelevant or vague answers | Low retrieval quality or document not ingested | Run `tbuk search <query>` to inspect retrieved chunks; run `tbuk update <path>` if the file changed |
| `tbuk ask` is very slow | Large `--top` value, slow model, or large chunks | Reduce `--top`; use a faster LLM model; reduce `chunking.size` in config |
| Database error on start | DB file missing or corrupted | Check `database.path` in config; run `tbuk init` to recreate missing dirs (does not overwrite existing DB) |
| Embedding dimension mismatch error | Model changed since last ingest | Set `embedding.dimension` in config to match the new model; re-ingest all documents with `--force` |

## License

[MIT](./LICENSE)
