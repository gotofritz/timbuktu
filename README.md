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

#### Verifying the download (optional)

Every release archive carries a signed build-provenance attestation binding
it to the workflow run that built it. With the [GitHub CLI](https://cli.github.com):

```bash
gh attestation verify tbuk.tar.gz --repo gotofritz/timbuktu
```

A successful check proves the archive was produced by this repository's
release workflow and has not been altered since.

### From source

Requires:

- Go 1.26+
- `golangci-lint` v2 — needed only for `make lint` / `check` / `check-ci`, and
  installed automatically the first time you run any of them (`make lint`
  depends on `make lint-install`). To install it up front, run `make
  lint-install`. It builds the pinned version from source **with this module's
  Go toolchain** — a linter built with an older Go refuses to lint a newer
  module — pinning the version from `.github/workflows/quality-check.yml` (the
  single source of truth) so local and CI never drift. Re-runs are a no-op once
  the right binary is present.

> **Local vs CI — why they install the linter differently.** CI does *not* run
> `make lint`. Its lint job uses [`golangci-lint-action`](https://github.com/golangci/golangci-lint-action)
> in `goinstall` mode, which builds the linter with the job's Go (1.26, from
> `go.mod`) and adds inline PR annotations and caching. That path is already
> correct, so CI is left on the action. The `make lint-install` script exists
> for **local** builds only: on a machine whose base Go is older than 1.26,
> `GOTOOLCHAIN=auto` would otherwise build `golangci-lint` with that older Go
> and it would then refuse to lint this module. The version pin is shared (the
> script reads it from the same workflow file), so the two paths can't drift on
> version.

```bash
make install             # installs to $(go env GOPATH)/bin — defaults to ~/go/bin
```

## Quick start

```bash
tbuk init                # create ~/.tbuk/ with default config and prompt dirs
tbuk version
tbuk doctor              # check config, database, LLM connectivity, and extractors
tbuk preprocess <path>   # extract text from document → save to ~/.tbuk/extracted/ (--dry-run, --output-dir)
tbuk ingest <path>       # read extracted text → chunk → embed → store in DB (--force, --verbose, --no-raw)
                         #   copies each source into ~/.tbuk/raw unless --no-raw is passed
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
tbuk update <file>       # re-ingest a single file if SHA256 changed (--force); use `tbuk ingest <dir>` for folders
tbuk stats               # knowledge base summary: doc/chunk counts, size (--format text|json)
tbuk list                # list indexed documents: path, title, chunk count, updated (--limit, --format)
tbuk export <path>       # bundle config + all data folders into a portable .tar (--root, --force)
                         #   <path> dir → timestamped file inside it; <path> file → that file (prompts before overwrite)
tbuk import <archive>    # restore a knowledge base from a .tar snapshot (--root, --config, --merge, --force)
                         #   default: adopt archive config, re-home folders under root; --merge: keep target config
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

### Automatic releases

Merging to `main` triggers an automatic release when the commits since the last
tag include at least one releasable type. The
[Auto Release workflow](.github/workflows/auto-release.yml) runs after CI passes
and applies these bump rules:

| Commit prefix | Version bump |
|---------------|-------------|
| `feat!:` | major |
| `feat:` / `fix!:` / `refactor!:` / `perf!:` | minor |
| `fix:` / `style:` / `refactor:` / `perf:` | patch |
| `docs:` / `chore:` / `ci:` / `test:` | — (no release) |

The workflow creates the next `vX.Y.Z` tag, which fires the release pipeline
(GoReleaser). If no releasable commits exist since the last tag, no tag is
created.

### Cutting a release manually

The `make` helpers remain available when you need to cut a release by hand
(hotfix, dry-run, or automation skip):

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
  path: ./tbuk.sqlite

llm:
  provider: llama    # llama | ollama | claude | openai
  model: ""          # provider default when empty

embedding:
  provider: llama    # llama | ollama | openai
  model: ""
  dimension: 768

chunking:
  size: 400          # tokens (approximated as chars/4); keep ≤ llama.cpp batch size (default 512)
  overlap: 50

ingest:
  embed_concurrency: 4   # embed batches in flight per file (>=1; 1 = serial)
  raw_dir: ./raw         # archive a copy of each ingested source (empty to disable)

preprocess:
  output_dir: ./extracted  # extracted-text cache

prompts:
  dir: ./prompts       # root directory holding prompt template folders
```

Each data path (`database.path`, `preprocess.output_dir`, `ingest.raw_dir`,
`prompts.dir`) may be **relative to the data root** — as written above, so the
config is portable — or an **absolute path** to pin one component elsewhere (e.g.
`raw_dir: /mnt/big/raw` to keep the archive on a larger disk). Relative paths
resolve against the root; the pipeline's parts can therefore live in different
places.

Override config file: `tbuk --config /path/to/config.yaml <cmd>`

### Data root (`--root`)

By default all data — database, extracted-text cache, raw archive, prompt
templates, and `config.yaml` — lives under `~/.tbuk`. The global `--root DIR`
flag relocates that whole directory for a single invocation:

```bash
tbuk init  --root /data/work-kb    # scaffold config + templates under /data/work-kb
tbuk ingest --root /data/work-kb ./docs
tbuk ask   --root /data/work-kb "…"
```

`init --root DIR` writes a portable `config.yaml` — its data paths are relative
to `DIR`, so relocating the directory moves every component with it — and later
commands need only the same `--root DIR`. Pointing `--root` at different
directories keeps several independent knowledge bases side by side.

The config always lives directly under its root, so `--config DIR/config.yaml`
(with no `--root`) is equivalent to `--root DIR`: the config file's own directory
becomes the data root. Passing both lets `--root` set the root while `--config`
names a specific file within it.

Re-running `tbuk init` on a directory that already has a `config.yaml` fills in
any default keys the file is missing (preserving your own values) rather than
overwriting it; a config that already has every key is left untouched.

## Backup, export, and import

`tbuk export <path>` writes a portable `.tar` snapshot of a knowledge base — the
config plus every data folder (database with its SQLite WAL sidecars,
extracted-text cache, raw archive, and prompt templates):

```bash
tbuk export ~/backups          # writes ~/backups/tbuk-export-<timestamp>.tar
tbuk export ~/backups/kb.tar   # exact filename; prompts before overwrite (--force skips the prompt)
tbuk export --root /data/work-kb ~/backups/work.tar
```

An existing **directory** target gets a timestamped filename inside it; a
**file** target is used as-is. The archived `config.yaml` has its data-folder
paths commented out, so `tbuk import` re-homes each folder under the target
root instead of pinning it to the exporting machine's absolute paths (provider,
model, and chunking settings are preserved). Components stored outside the root
are archived by basename so the archive is always self-contained.

`tbuk import <archive>` restores a snapshot. `--root` / `--config` choose where
it lands, exactly as for every other command:

```bash
tbuk import ~/backups/kb.tar                    # restore into ~/.tbuk
tbuk import --root /data/work-kb ~/backups/kb.tar   # restore into a specific root
tbuk import --merge ~/backups/kb.tar            # merge into an existing KB's folders
tbuk import --force ~/backups/kb.tar            # overwrite existing files
```

By default import **adopts the archive's config** and re-homes each data folder
at the target root's default locations. With `--merge` it keeps the target's
existing `config.yaml` (or the defaults when there is none) and copies the
imported folders into the paths that config names — use it to fold a snapshot
into an existing knowledge base. Existing files are left untouched unless
`--force` is passed, so an import never clobbers a live database by accident.

Export re-reads the archive before putting it in place, so a damaged snapshot
fails at export time rather than on restore, and import checks the same thing
before writing anything — an incomplete archive is refused rather than restoring
part of a knowledge base. If import rejects a file, the error says why — the file is empty, truncated part-way (an incomplete copy or a
cloud folder that has not finished syncing), corrupt at a given entry, or not a
tar at all. A `.tar.gz`/`.tgz` must be decompressed first: `tbuk export` writes
an uncompressed `.tar`.
Archive entries that would escape the target via an absolute path or `..` are
rejected.

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
  export/           Create — tar snapshot of config + data folders (portable, path-commented config)
  importer/         Import — restore a tar snapshot under a target root (re-home or --merge)
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

A template can also declare how its output should be repaired. Models hold a
requested format for a while and then slide back into markdown lists, so
`normalize` re-imposes the shape after the call instead of only asking for it:

```yaml
normalize:
  filters: [strip_preamble, strip_fences, strip_list_markers, collapse_blank_lines]
  records:
    separator: "----"
    fields: [lead, note, body]
```

`filters` are line-level cleanups (`strip_fences`, `strip_headings`,
`strip_list_markers`, `strip_preamble`, `collapse_blank_lines`,
`trim_trailing_space`); the optional `records` block rebuilds the output as
separator-delimited records. `fields` names the positional line roles: `lead`
(the first line), an optional `note` (a parenthesised second line, kept even
when empty), and `body` (the rest, one item per line). Omit `fields` for the
`[lead, body]` default. The builtin `anki` template uses both parts — its cards
are records of `[lead, note, body]`. An unknown filter or field name fails when
the template loads. Declaring a pipeline turns
streaming off for that template, since the whole completion is needed before
anything can be rewritten.

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
