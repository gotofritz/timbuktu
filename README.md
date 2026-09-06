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
tbuk ingest <path>       # read extracted text → chunk → embed → store in DB (--force, -v/--verbose, --no-raw)
                         #   copies each source into ~/.tbuk/raw unless --no-raw is passed
tbuk search <query>      # search chunks by vector/keyword/hybrid (--mode, --top, --min-score, --format)
                         #   query is an expression: "a phrase", -exclude, main_consumption as one term
                         #   --min-score filters hybrid on fused RRF sums (different scale from cosine)
tbuk find <key=value>... # find documents by metadata filters (--limit, --format)
tbuk meta set <path> k=v # attach metadata to a document (one value per key; distinct keys per call)
tbuk meta list <path>    # list all metadata for a document
tbuk ask <question>      # RAG: retrieve relevant chunks, render prompt template, stream LLM answer
                         #   (--top, --template, --no-stream, --require-context to abort when no context matches)
tbuk template list       # list prompt templates in ~/.tbuk/prompts/
tbuk template show <n>   # print manifest + template files
tbuk template edit <n>   # open template manifest in $EDITOR
tbuk delete <path>       # remove a document, its chunks, and its extracted-text cache (--yes skips prompt)
tbuk update <file>       # re-ingest a single file if SHA256 changed (--force); use `tbuk ingest <dir>` for folders
tbuk reindex             # re-embed every indexed document, reading from ~/.tbuk/raw (--source-dir, --dry-run, -v)
                         #   the fix after changing embedding.provider/model; originals need not still exist
tbuk stats               # knowledge base summary: doc/chunk counts, size (--format text|json)
tbuk list                # list indexed documents: path, title, chunk count, updated (--limit, --format)
tbuk export <path>       # bundle config + all data folders into a portable .tar (--root, --force)
                         #   <path> dir → timestamped file inside it; <path> file → that file (prompts before overwrite)
tbuk import <archive>    # import an archive's documents and prompt templates, indexed with this machine's config (-v)
                         #   `import data` / `import templates` do one half each; --on-conflict skip|overwrite|ask, --dry-run, --yes
                         #   never reads the archive's config or its embeddings
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
  provider: mlx      # mlx | llama | ollama | claude | openai
  model: ""          # provider default when empty; mlx: HF repo id served
  max_tokens: 4096   # output budget for one answer
  base_url: ""       # empty = provider default: mlx/llama http://localhost:8080,
                     # ollama :11434, claude api.anthropic.com, openai api.openai.com
  context_tokens: 8192   # the model's whole window (prompt + reply); 0 disables the ask budget guard

embedding:
  provider: mlx      # mlx | llama | ollama | openai
  model: ""
  dimension: 768
  base_url: ""       # empty = provider default (see llm above)

chunking:
  size: 400          # tokens (script-aware estimate, see Paths & Unicode); keep ≤ llama.cpp batch size (default 512)
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
**file** target is used as-is. Components stored outside the root are archived
by basename so the archive is always self-contained.

`tbuk import <archive>` reads a snapshot back into a knowledge base. It takes
from the archive the things that are *yours* rather than the exporting
machine's: the **source files** under `raw/`, the **index** naming what those
files were (paths, titles, and the metadata you attached with `tbuk meta set`),
and your **prompt templates**. The sources are copied into this machine's raw
archive and embedded with this machine's config:

```bash
tbuk import ~/backups/kb.tar                        # documents and templates
tbuk import data ~/backups/kb.tar                   # documents only
tbuk import templates ~/backups/kb.tar              # templates only
tbuk import --root /data/work-kb ~/backups/kb.tar   # into a specific root
tbuk import --dry-run ~/backups/kb.tar              # list what would be imported
tbuk import --on-conflict overwrite ~/backups/kb.tar
```

All three take the same flags. Templates are imported first: they are quick and
need no embedding provider, so a slow or failing embed step never costs you
your templates. `tbuk import templates` needs no reachable provider at all.

Nothing else in the archive is used, deliberately:

- **The config is never read.** It describes the machine it came from — its
  paths, its providers, its models — and adopting another machine's setup is
  never what you want. Your `config.yaml` is left exactly as it is.
- **The embeddings are never trusted.** Vectors made by another machine's model
  cannot be searched here, and nothing in an archive proves which model made
  them. Matching `embedding.dimension` is not proof: two different 768-dim
  models produce vectors that fail silently rather than loudly. Every document
  is embedded afresh from the archived bytes.
- **The extracted-text cache is skipped.** Extraction is deterministic and
  re-derivable from the raw bytes.

A document already indexed at the **same path**, or a template already
installed under the **same name**, is left alone — so re-importing the same
archive is a no-op. `--on-conflict overwrite` replaces it with the archive's
copy; `--on-conflict ask` decides one at a time. A template is replaced whole
rather than merged, so what lands is always a template that loads.

The built-in templates (`qa`, `brief`, `anki`) exist on every machine and so
always collide: under the default `skip`, only genuinely custom templates
travel. If you edited a built-in on the other machine, `--on-conflict
overwrite` brings your version across.

A document the archive indexes but has no `raw/` copy of (ingested with
`--no-raw`) cannot be imported at all — it is reported and skipped, and the
rest of the run continues. If a document fails to embed, it is rolled back
rather than left as an empty row, so re-running the import picks it up.

Importing into a directory with **no `config.yaml`** sets one up first, exactly
as `tbuk init` would, then shows the embedding settings it will use and asks
before spending anything on them. `--yes` skips that prompt for scripts, and a
templates-only import never asks, having nothing to spend.

### Rewinding your own knowledge base

There is no "restore" command, and deliberately so: trusting an archive's
embeddings is only safe when the two machines share an embedding model, which
tbuk cannot verify. To roll your *own* machine back to a snapshot — where the
config has not changed and the vectors are yours — untar it over the root:

```bash
tar -xf ~/backups/kb.tar -C ~/.tbuk    # config.yaml included; check it first
```

Export re-reads the archive before putting it in place, so a damaged snapshot
fails at export time rather than on import, and import checks the same thing
before writing anything — an incomplete archive is refused rather than importing
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
  embeddings/       Embedder interface; MLX, llama.cpp, Ollama, OpenAI adapters
  ingest/           Ingester: SHA256 dedup, extract → chunk → embed → store pipeline; Reindex* — re-embed from raw/
  llm/              LLM interface; MLX, Claude, OpenAI, Ollama adapters (SSE + JSON-lines streaming)
  search/           Searcher: Vector (cosine), Keyword (FTS5 BM25), Metadata, Hybrid (RRF); query parser (phrases, exclusions)
  searchtext/       Reduce — the encoding stored in chunks.search_text and handed to the embedder
  retrieval/        Retriever: hybrid search → RetrievedChunk with Citation string
  prompts/          TemplateDir, Manifest, Template.Render — disk-based text/template system
  export/           Create — tar snapshot of config + data folders (portable, path-commented config)
  importer/         Extract — take a tar snapshot's raw sources, templates and index; ignores config and extracted cache
```

Dependencies point inward. Providers depend only on shared interfaces defined in `internal/llm` and `internal/embeddings`.

## Storage schema

```sql
documents   — path, sha256, title, mime_type, raw_path, timestamps
chunks      — document_id, chunk_index, text, search_text, token_count, embedding BLOB
metadata    — document_id, key, value  (key/value per document)
chunks_fts  — FTS5 virtual table over chunks.search_text, tokenize="unicode61 tokenchars '_'" (auto-synced via triggers)
```

Embeddings stored as little-endian `[]float32` BLOBs. Cascade delete on document removal.

A chunk is stored in two encodings. `text` is the chunk as written — what
`tbuk search` prints and what `tbuk ask` gives the model, code fences and all.
`search_text` is the same chunk reduced for retrieval, and it is what the FTS5
index is built from and what the embedding was taken of. See
[Encoding code for search](#encoding-code-for-search).

`raw_path` records where a document's archived copy lives, relative to
`ingest.raw_dir` (e.g. `<sha256>.md`). Relative, so a knowledge base stays
portable when its folders move or are imported under another root. Empty means
no copy is known — ingested with `--no-raw`, with archiving disabled, or before
the column existed.

### Metadata

The cache filename carries `preprocess.ExtractorVersion`, so improving an
extractor stops its old output from being reused for content that has not
changed — bump the constant and the next ingest or reindex re-extracts. Text
cached by an older version is read only when the document cannot be
re-extracted from anything at all, where older text beats no document; reindex
says so on the line when that happens.

Ingestion writes automatic metadata for every document: `filename`,
`extension` (lowercased, no leading dot), `mime`, and `dir`. These refresh on
re-ingest, so `tbuk find filename=README.md` or `tbuk find extension=md` work
after a plain `tbuk ingest`. A document holds **one value per key** — the
table's primary key is `(document_id, key)` — so `meta set` refuses a command
that gives the same key twice rather than silently keeping the last. Attach
your own labels with
`tbuk meta set <path> tag=design` (user-set keys survive re-ingest) and inspect
them with `tbuk meta list <path>`.

### Prompt templates

A template's `manifest.yaml` drives the LLM call: `model`, `temperature`, and
`max_tokens` are passed through to the provider on every `tbuk ask`. Omit
`temperature` to use the provider default; set `temperature: 0` for a
deterministic answer (an explicit `0` is honored, not treated as "unset"). A
template that pins a `model` with a different context window can pin the window
too, with a top-level `context_tokens:` — it overrides `llm.context_tokens` for
that template (see [Context budget](#context-budget)).

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

When a template declares `records`, its output is meant for another program, so
`tbuk ask` keeps stdout to the records alone — the `Sources:` footer is printed
on stderr instead of being dropped, which means `tbuk ask -t anki … > cards.txt`
gives a clean file while the citations still show up in the terminal.

### Context budget

`tbuk ask` bounds the whole prompt — system prompt, template, retrieved chunks
and question — against the model's context window before calling the provider,
so an oversized prompt fails locally with an actionable message rather than
remotely as `HTTP 4xx: context length exceeded`.

The window is `llm.context_tokens` (default `8192`; `0` turns the guard off),
overridable per template by a top-level `context_tokens` in `manifest.yaml`.
What is left for the prompt is the window minus the answer's budget — the
template's `max_tokens`, else `llm.max_tokens` — so the reply always has room.
Token counts come from the same script-aware estimate the chunker uses:
approximate, so leave a little headroom rather than setting the window to the
model's exact maximum.

Over budget, `ask` climbs a ladder and says on stderr what it did:

1. **compacts the retrieved text** — repeated whitespace and blank lines
   collapsed, English articles and filler words dropped. Fenced and indented
   code, identifiers, URLs and inline code spans are left byte-exact, and the
   question and template are never touched.
2. **drops the lowest-ranked chunks**, one at a time, naming how many of how
   many went. `Sources:` then lists only what the model actually saw.
3. **fails** — if even a chunk-free prompt does not fit, before any HTTP call,
   naming the knobs to change.

Compaction comes before dropping because text the model can still read beats a
passage it can no longer cite. With `--require-context`, a budget that leaves
room for no chunks at all aborts instead of answering from the model's priors.
`tbuk doctor` reports the window, what it leaves for the prompt, and any
template whose own `max_tokens` swallows it.

### Encoding code for search

Timbuktu is not a code search tool. What it has to answer about a fenced block
is *this topic is referenced in that bit of code*, and what carries that is the
comments, the identifiers, and the strings — not the syntax.

One string cannot serve the reader, the model and the index at once, so a chunk
is stored twice. `chunks.text` stays faithful. `chunks.search_text` holds a
reduced encoding (`internal/searchtext`): prose passes through as it stands,
and a code region becomes its comments, its identifiers, and the contents of
its string literals, with keywords, operators, punctuation and numeric literals
dropped. Every identifier is emitted alongside its split words —
`main_consumption main consumption`, `readMeter read meter` — so the exact term
and the words it is made of both reach the chunk. An inline `` `span` `` in
ordinary prose gets the same treatment.

There is no parser and no per-language support: terms split on `_ - . /` and at
camelCase boundaries, and a small cross-language keyword list is dropped, which
a fence's language tag extends when it carries one.

The embedding follows `search_text` too, which is the other half of the win: a
code chunk embeds as what it is about rather than as a wall of syntax. Changing
that changes the vectors, so `tbuk reindex` is what applies it to a knowledge
base indexed by an earlier version.

### Query semantics

The FTS5 index keeps `_` inside a token, so `main_consumption` is a single
term rather than the words it is made of. `-` is a separator: it was a token
character too, until that turned out to hold ordinary hyphenated English
together as well, so `long-term` answered to neither `long` nor `long term`
(issue #143). A kebab-case name is therefore indexed as its words, and
`check-ci` matches them adjacent and in order. `tbuk search` reads its query as
an expression to match:

```bash
tbuk search 'main consumption'                    # either form
tbuk search 'main_consumption'                    # that term only
tbuk search 'main consumption -main_consumption'  # the words apart, not the identifier
tbuk search '"main consumption"'                  # that phrase
```

A leading `-` excludes; anywhere else a dash is part of the term, which the
index reads as the words it joins, next to each other. An exclusion needs
something to exclude from, so a query of nothing but exclusions returns
nothing. Common English words are dropped from bare terms and kept inside a
quoted phrase.

`tbuk ask` does not read operators: it sends a natural-language question, and
reading one as an expression is how its keyword leg comes back empty.

A loose query reaches an identifier because `search_text` carries its split
form, not because the index breaks the identifier apart — so it reaches one
inside a fence or `` `backticks` ``, and not one written bare in prose. On a
knowledge base built before this, `tbuk doctor` says so and names the script
that rebuilds the index.

### Re-ingesting

`tbuk ingest --force` and `tbuk update` replace a document's chunks atomically:
text extraction and embedding run first, then the old and new chunks are
swapped in a single transaction. If embedding fails midway (e.g. the provider
is down), the previous index is left intact and searchable rather than wiped.

### Re-embedding the whole knowledge base

Changing `embedding.provider` or `embedding.model` changes the vector
dimension, and the chunks already indexed stay at the old one — a knowledge
base that `tbuk doctor` flags and that searches refuse to run against.
`tbuk reindex` repairs it in one command:

```bash
tbuk reindex                            # re-embed every document
tbuk reindex -v                         # ...and print a line per document
tbuk reindex --dry-run                  # list what would be re-embedded, and from where
tbuk reindex --source-dir /mnt/backup/raw   # resolve archived copies from elsewhere
```

`ingest`, `reindex` and `import` print only what needs acting on — documents
they could not process, and the closing counts — so one failure among hundreds
is visible rather than buried. `-v`/`--verbose` prints a line per item; a
`--dry-run` lists regardless, that being its purpose.

The text comes from whichever of three places still has it: the extracted-text
cache (`extracted/<sha256>.v<N>.txt`), the archived copy the document records
(`raw_path`), or `documents.path`. So a document whose original is gone —
imported from a snapshot, or since moved — is re-embedded like any other, and
one whose files have all gone is still re-embedded from text already extracted.
A document with none of the three is reported and skipped, naming each place it
looked. A knowledge base indexed before
that was recorded falls back to the name a copy would have been written under,
`<sha256><ext>`, and the location is recorded when it resolves, so the next run
is a plain lookup. A document with no archived
copy (`ingest.raw_dir` empty, or ingested with `--no-raw`) falls back to its
stored path; one with neither is reported and skipped, and the rest of the run
carries on. Every targeted document is re-embedded: there is no
unchanged-file check, because the content is presumed unchanged and it is the
embedding configuration that moved. Chunks are replaced per document, with the
same atomicity as `tbuk update`.

### Paths & Unicode

`tbuk ingest`, `update`, and `delete` resolve their path argument to an
absolute, cleaned form, so a document ingested as `docs/a.md` is deleted just
the same via `./docs/a.md` or its full absolute path — no double-indexing under
different spellings. (Documents indexed before this behaviour existed are keyed
by their original relative path; re-ingest to re-key them absolutely.)

Chunk and search-preview boundaries snap to UTF-8 rune starts, so non-ASCII
text (accents, CJK) is never sliced mid-rune into invalid UTF-8.

Token counts are estimated per rune, weighted by script, rather than by dividing
the byte length by four. A byte count reads multi-byte text as *cheaper* per
character than it is — a han ideograph is three bytes and roughly one token — so
`chunking.size` used to buy about three times the intended tokens on a CJK
corpus, overrunning the embedding server's batch (HTTP 500) and leaving
`retrieval.max_tokens` under-trimmed. The weights, per rune:

| Text | Estimate |
|------|----------|
| ASCII | 4 characters per token — the original calibration, unchanged |
| Accented Latin, Greek, Cyrillic, Hebrew, Arabic, Devanagari, Thai | 2 characters per token |
| Han, kana, hangul; non-ASCII punctuation and symbols | 1 token per character |
| Emoji and other runes beyond the BMP | 2 tokens per character |

This is a heuristic, not a tokenizer: no vocabulary, no allocation, and any real
BPE model will differ. The point is to stop being wrong by a factor of three on
dense scripts. `chunking.size` and `chunking.overlap`, `retrieval.max_tokens`,
and the `ask` context guard all measure with it, so a CJK chunk holds fewer
bytes than an English one at the same token budget.

A knowledge base indexed before this change still holds chunks sized in bytes.
`tbuk doctor` re-measures the largest of them and says so under **Chunking**;
`tbuk reindex` re-chunks and re-embeds from `raw/`.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `tbuk` command not found after install | Go bin dir not in PATH | Add `export PATH="$PATH:$(go env GOPATH)/bin"` to shell profile and restart terminal |
| `tbuk doctor` shows LLM or embedding unreachable | Local server (MLX or llama.cpp) not running, or wrong port | Start your MLX server / llama.cpp; verify `llm.base_url` / `embedding.base_url` in `~/.tbuk/config.yaml` (every key is in the sample under [Configuration](#configuration)) |
| `tbuk doctor` shows `hosted API — not probed` | Provider is `claude`/`openai` (no `/health` endpoint) | Expected — hosted APIs aren't probed; set `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` and use `tbuk ask` to verify connectivity |
| `tbuk ask` fails with `HTTP 4xx/5xx` | Provider rejected the request (unknown model, rate limit; a too-long prompt is normally caught locally first) | The error now includes the provider's own message — read it, then fix the model name or lower `--top` / `max_tokens`. If it *is* a context-length rejection, `llm.context_tokens` is set higher than the model's real window (or `0`) |
| `tbuk ask` warns `compacted the retrieved text` or `dropped N of M retrieved chunks` | The rendered prompt exceeded `llm.context_tokens` minus the answer's `max_tokens` | Expected when the budget is tight — raise `llm.context_tokens` to your model's real window, lower `--top`, or lower the template's `max_tokens` |
| `tbuk ask` fails with `prompt needs ~N tokens but only M are available` | Even a prompt with no retrieved context does not fit the budget | Shorten the question, raise `llm.context_tokens`, or lower the template's `max_tokens`; `tbuk doctor` shows both numbers |
| `tbuk ingest` produces 0 chunks | File is empty or extension not supported | Check file has content; supported: `.md`, `.txt`, `.pdf`, `.html`, `.htm` |
| Embedding server returns `HTTP 500` on a CJK/non-Latin corpus | Chunks stored before token estimation became script-aware are sized in bytes, so they hold ~3× the tokens `chunking.size` allowed | `tbuk doctor` reports it on the **Chunking / stored** line; run `tbuk reindex` to re-chunk and re-embed. If freshly indexed chunks still overrun, the server's batch is smaller than `chunking.size` — raise it (`-b 1024 -ub 1024`) or lower the size |
| `tbuk ask` returns irrelevant or vague answers | Low retrieval quality or document not ingested | Run `tbuk search <query>` to inspect retrieved chunks; run `tbuk update <path>` if the file changed |
| `tbuk ask` is very slow | Large `--top` value, slow model, or large chunks | Reduce `--top`; use a faster LLM model; reduce `chunking.size` in config. Press `Ctrl-C` to cancel — retrieval and streaming are interrupted cleanly |
| Database error on start | DB file missing or corrupted | Check `database.path` in config; run `tbuk init` to recreate missing dirs (does not overwrite existing DB) |
| Embedding dimension mismatch error | `embedding.provider`/`embedding.model` changed since last ingest | Set `embedding.dimension` in config to match the new model, then run `tbuk reindex` — it re-embeds every document from `~/.tbuk/raw`, so the originals need not still exist |
| Search fails right after untarring a snapshot over the root | The snapshot's vectors came from a different embedding model | Run `tbuk reindex`, or import the archive with `tbuk import` instead, which embeds locally from the start |
| `tbuk reindex` skips documents with `no extracted text …, no archived copy …` | None of the three places holds the document's text any more | The line names each path it tried. If copies are in `ingest.raw_dir` under their *original* names (a raw folder assembled by hand, or by an older version), re-ingest that folder as an ordinary source folder: `tbuk ingest <that-folder>` |

## License

[MIT](./LICENSE)
