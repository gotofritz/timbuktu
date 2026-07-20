# timbuktu

Local-first CLI knowledge base for indexing and querying personal documents with RAG. Single SQLite database, modular architecture, provider-agnostic LLM and embedding interfaces.

## Documentation

See [User Guide](docs/user-guide.md) for a full walkthrough — what RAG is,
how to index your documents, and how to query your knowledge base.

## Install

### Pre-built binary (recommended)

Each tagged release publishes standalone binaries for Linux, macOS, and Windows
(amd64 and arm64) on the [Releases page](https://github.com/gotofritz/timbuktu/releases).
No Go toolchain required — the binary is statically linked (pure-Go SQLite).

Download the archive for your platform, extract `tbuk`, and put it on your
`PATH`. For example, on macOS/Linux:

```bash
# pick the asset matching your OS/arch from the latest release
VERSION=v0.1.1          # replace with the latest tag
OS=$(uname -s | tr '[:upper:]' '[:lower:]')   # linux or darwin
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

# the download path keeps the leading "v"; the asset filename drops it
curl -sSL -o tbuk.tar.gz \
  "https://github.com/gotofritz/timbuktu/releases/download/${VERSION}/tbuk_${VERSION#v}_${OS}_${ARCH}.tar.gz"
tar -xzf tbuk.tar.gz tbuk
sudo mv tbuk /usr/local/bin/     # or any dir on your PATH
tbuk version
```

On Windows, download the `_windows_amd64.zip` (or `_windows_arm64.zip`)
asset, unzip it, and move `tbuk.exe` to a folder on your `PATH`.

### From source

Requires:

- Go 1.25+
- `golangci-lint` v2 — needed only for `make lint` / `check` / `check-ci`.
  Install the version CI uses (build from source with your Go toolchain):
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`
  (note the `/v2` in the path — the bare path is v1). Keep the version in sync
  with `.github/workflows/quality-check.yml`, which is the single source of truth.

```bash
make install             # installs to $(go env GOPATH)/bin — defaults to ~/go/bin
```

## Quick start

```bash
tbuk init                # create ~/.tbuk/ with default config and prompt dirs
tbuk version
tbuk doctor              # check config, database, LLM connectivity, and extractors
tbuk preprocess <path>   # extract text from document → save to ~/.tbuk/extracted/ (--dry-run, --output-dir)
tbuk ingest <path>       # read extracted text → chunk → embed → store in DB (--force, --verbose)
tbuk search <query>      # search chunks by vector/keyword/hybrid (--mode, --top, --min-score, --format)
                         #   --min-score filters hybrid on fused RRF sums (different scale from cosine)
tbuk find <key=value>... # find documents by metadata filters (--limit, --format)
tbuk meta set <path> k=v # attach metadata to a document (repeatable key=value pairs)
tbuk meta list <path>    # list all metadata for a document
tbuk ask <question>      # RAG: retrieve relevant chunks, render prompt template, stream LLM answer
                         #   (--top, --template, --no-stream, --require-context to abort when no context matches)
tbuk template list       # list prompt templates in ~/.tbuk/prompts/
tbuk template show <n>   # print manifest + template files
tbuk template edit <n>   # open template manifest in $EDITOR
tbuk delete <path>       # remove a document, its chunks, and its extracted-text cache (--yes skips prompt)
tbuk update <path>       # re-ingest if SHA256 changed, skip otherwise (--force)
tbuk stats               # knowledge base summary: doc/chunk counts, size (--format text|json)
tbuk list                # list indexed documents: path, title, chunk count, updated (--limit, --format)
```

If `tbuk` is not found after install, add Go's bin dir to your shell profile:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

## Development

New contributors: see **[CONTRIBUTING.md](CONTRIBUTING.md)** for one-time
setup (pre-commit hooks, commit-message convention, and the tooling the hooks
need on `PATH`).

Run `make` (or `make help`) to list every target with a description — the
Makefile is self-documenting, so this is always current:

```
$ make
Usage: make <target>

  build            Build the tbuk binary into bin/
  check            Format, vet, lint, and test (run before committing)
  check-ci         Full CI gate: lint + build + coverage >= 85%
  clean            Remove built binaries
  coverage         Print total coverage percentage
  coverage-html    Open HTML coverage report
  fmt              Format all Go files
  help             Show this help
  install          Install tbuk to $GOPATH/bin
  lint             Run golangci-lint
  release-major    Bump major (v0.2.0 -> v1.0.0) and push tag
  release-minor    Bump minor (v0.1.1 -> v0.2.0) and push tag
  release-patch    Bump patch (v0.1.0 -> v0.1.1) and push tag
  release-snapshot Dry-run a release locally into dist/ (no tag, no push)
  release          Run goreleaser against an already-pushed tag (normally CI does this)
  serve            Serve output/ over HTTP for local feed testing
  test             Run all tests
  test-race        Run tests with the race detector
  tidy             Tidy go.mod and go.sum
  vet              Run go vet
```

Common ones during development:

```bash
make test          # run all tests
make test-race     # tests with race detector
make lint          # golangci-lint
make coverage      # total coverage percentage
make check-ci      # full CI gate: lint + build + coverage ≥ 85% (total and per package)
```

## Releasing

Releases are cut from a git **tag**. Pushing a `v*` tag triggers the
[Release workflow](.github/workflows/release.yml), which runs
[GoReleaser](https://goreleaser.com) to build binaries for Linux/macOS/Windows
(amd64 + arm64) and publish them, plus checksums and auto-generated release
notes, to the [Releases page](https://github.com/gotofritz/timbuktu/releases).

### Cutting a release

From a clean `main`, use the helper targets — they compute the next
[semver](https://semver.org) tag from the latest existing tag, then tag and push
it (after a confirmation prompt):

```bash
make release-patch   # bug fixes only:        v0.1.0 -> v0.1.1
make release-minor   # backwards-compatible:  v0.1.1 -> v0.2.0
make release-major   # breaking changes:      v0.2.0 -> v1.0.0
```

Each pushes the new tag, and CI does the rest. To do it by hand instead:

```bash
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

### Versioning

The version is **not** stored in source — it comes from the git tag. GoReleaser
(and `make build` / `make install`) inject it into the binary via `ldflags`, so
`tbuk version` reports the tag it was built from. Between tags or on a dirty
tree, `make build` reports a `git describe` value like `v0.1.0-3-gabc1234`;
outside a git checkout it falls back to `dev`.

### Release notes / changelog

Release notes are generated by GoReleaser from the commit subjects since the
previous tag. Commits are grouped into **Features** (`feat:`) and **Bug fixes**
(`fix:`); `docs:`, `test:`, `chore:`, `ci:`, and merge commits are excluded.
Writing [Conventional Commit](https://www.conventionalcommits.org) subjects
therefore produces clean, categorised release notes automatically. Preview a
release locally without tagging or publishing:

```bash
make release-snapshot   # builds into dist/, no tag, no push
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

ingest:
  embed_concurrency: 4   # embed batches in flight per file (>=1; 1 = serial)
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
  chunking/         Chunker.Split — sentence-boundary search, rune-safe, configurable size/overlap
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

### Metadata

Ingestion writes automatic metadata for every document: `filename`,
`extension` (lowercased, no leading dot), `mime`, and `dir`. These refresh on
re-ingest, so `tbuk find filename=README.md` or `tbuk find extension=md` work
after a plain `tbuk ingest`. Attach your own tags with
`tbuk meta set <path> tag=design` (user-set keys survive re-ingest) and inspect
them with `tbuk meta list <path>`.

### Prompt templates

A template's `manifest.yaml` drives the LLM call: `model`, `temperature`, and
`max_tokens` are passed through to the provider on every `tbuk ask`. Omit
`temperature` to use the provider default; set `temperature: 0` for a
deterministic answer (an explicit `0` is honored, not treated as "unset").

### Re-ingesting

`tbuk ingest --force` and `tbuk update` replace a document's chunks atomically:
text extraction and embedding run first, then the old and new chunks are
swapped in a single transaction. If embedding fails midway (e.g. the provider
is down), the previous index is left intact and searchable rather than wiped.

### Paths & Unicode

`tbuk ingest`, `update`, and `delete` resolve their path argument to an
absolute, cleaned form, so a document ingested as `docs/a.md` is deleted just
the same via `./docs/a.md` or its full absolute path — no double-indexing under
different spellings. (Documents indexed before this behaviour existed are keyed
by their original relative path; re-ingest to re-key them absolutely.)

Chunk and search-preview boundaries snap to UTF-8 rune starts, so non-ASCII
text (accents, CJK) is never sliced mid-rune into invalid UTF-8.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `tbuk` command not found after install | Go bin dir not in PATH | Add `export PATH="$PATH:$(go env GOPATH)/bin"` to shell profile and restart terminal |
| `tbuk doctor` shows LLM or embedding unreachable | llama.cpp not running, or wrong port | Start llama.cpp; verify `llm.base_url` / `embedding.base_url` in `~/.tbuk/config.yaml` |
| `tbuk doctor` shows `hosted API — not probed` | Provider is `claude`/`openai` (no `/health` endpoint) | Expected — hosted APIs aren't probed; set `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` and use `tbuk ask` to verify connectivity |
| `tbuk ask` fails with `HTTP 4xx/5xx` | Provider rejected the request (unknown model, context too long, rate limit) | The error now includes the provider's own message — read it, then fix the model name or lower `--top` / `max_tokens` |
| `tbuk ingest` produces 0 chunks | File is empty or extension not supported | Check file has content; supported: `.md`, `.txt`, `.pdf`, `.html`, `.htm` |
| `tbuk ask` returns irrelevant or vague answers | Low retrieval quality or document not ingested | Run `tbuk search <query>` to inspect retrieved chunks; run `tbuk update <path>` if the file changed |
| `tbuk ask` is very slow | Large `--top` value, slow model, or large chunks | Reduce `--top`; use a faster LLM model; reduce `chunking.size` in config. Press `Ctrl-C` to cancel — retrieval and streaming are interrupted cleanly |
| Database error on start | DB file missing or corrupted | Check `database.path` in config; run `tbuk init` to recreate missing dirs (does not overwrite existing DB) |
| Embedding dimension mismatch error | Model changed since last ingest | Set `embedding.dimension` in config to match the new model; re-ingest all documents with `--force` |

## License

[MIT](./LICENSE)
