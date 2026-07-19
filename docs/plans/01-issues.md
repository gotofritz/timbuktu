# Plan 01 — Issues from Architecture Assessment (2026-07-19)

Findings from a staff-level architecture & design review of the codebase.
Ordered by priority. Each issue lists the problem, evidence, and proposed fix.

## High priority

### 1. `search.Options.Metadata` is a silent no-op

**Problem:** `Options.Metadata` is documented as an "AND-combined metadata
pre-filter", and `Retriever.Retrieve` accepts a `meta` argument and forwards it
into `search.Options`. But `Searcher.Hybrid` rebuilds its options as
`Options{TopK: ..., MinScore: 0}`, dropping the Metadata field entirely, and
`Vector`/`Keyword` never read it. A caller passing a filter gets unfiltered
results with no error.

**Evidence:** `internal/search/hybrid.go:12`, `internal/search/search.go:26`,
`internal/retrieval/retrieval.go:38-40`.

**Fix:** Either implement the metadata pre-filter in Vector/Keyword/Hybrid
(JOIN against the `metadata` table before scoring), or remove the field and the
`meta` parameter chain so the API stops advertising behaviour it doesn't have.

### 2. Embedding dimension mismatch fails silently

**Problem:** `cosineSimilarity` returns `0` when vector lengths differ. If the
user changes the embedding model or `embedding.dimension` in config after
ingesting, every stored vector scores 0: vector search silently returns
nothing and hybrid quietly degrades to keyword-only. No dimension is recorded
in the database and nothing checks for a mismatch at ingest or query time.

**Evidence:** `internal/search/vector.go:72-75`; schema in
`internal/storage/migrate.go` stores raw BLOBs with no dimension metadata.

**Fix:** Record the embedding dimension (and ideally provider/model) in the
database. Error loudly on mismatch at query and ingest time. Add a check to
`tbuk doctor`.

### 3. Composition root scattered across CLI commands

**Problem:** Every command hand-wires its own dependency graph — open DB,
construct repos, embedder, chunker, searcher — with duplicated open/close and
error wrapping. Adding a dependency means touching N command files.

**Evidence:** `internal/cli/ingest.go:29-53`, `internal/cli/ask.go:53-70`,
plus similar blocks in `search.go`, `update.go`, `delete.go`, `stats.go`.

**Fix:** Extract a shared builder, e.g. `cli.openApp(cfg) (*App, error)`
returning repos/searcher/ingester plus a closer. Commands consume the App.

## Medium priority

### 4. `IngestDir` swallows walk errors

**Problem:** `filepath.WalkDir`'s return value is discarded and the callback
returns `nil` on entry errors, so an unreadable directory or file is skipped
invisibly — no `Result` recorded, not counted in the "Done: N errors" summary.

**Evidence:** `internal/ingest/ingester.go:228-230`.

**Fix:** Record entry errors as error `Result`s so they surface in output and
the non-zero exit path.

### 5. `DefaultYAML` duplicates `Defaults()` by hand

**Problem:** Default config exists twice — as a struct literal in `Defaults()`
and as a hand-built YAML string in `DefaultYAML()`. Two sources of truth will
drift.

**Evidence:** `internal/config/config.go:58-85` vs `config.go:108-132`.

**Fix:** Generate the YAML by marshalling `Defaults()` (keep comments via a
template if desired).

### 6. Config has no validation

**Problem:** No check for `overlap >= size`, negative chunk sizes, or
nonsensical values; unknown providers only fail deep inside the factories.

**Evidence:** `internal/config/config.go` (no Validate), factories in
`internal/llm/llm.go` / `internal/embeddings/embeddings.go`.

**Fix:** Add `Config.Validate()` called from the root `PersistentPreRunE` so
every command fails fast with a clear message.

## Low priority

### 7. Prompts root hardcoded

`~/.tbuk/prompts` is hardcoded in `internal/cli/ask.go:44-46` while DB path
and extracted dir are configurable. Move to config (e.g. `prompts.dir`).

### 8. Storage queries living in the CLI package

`CountDocuments` / `CountChunks` in `internal/cli/ingest.go:128-139` are
storage concerns. Move into `internal/storage`.

### 9. Dead `sseScanner` wrapper

`internal/llm/stream.go:39-42` claims to strip trailing carriage returns but
just returns `bufio.NewScanner(r)`. Either implement CR stripping or delete
the wrapper and its comment.

### 10. Makefile `serve` target is leftover cruft

`make serve` serves an `output/` directory "for local feed testing" —
no `output/` exists in this project. Remove the target.

### 11. Stale architecture doc entry

`docs/initial-context.md` lists `internal/metadata/ STUB` but no such package
exists. Remove the line.

### 12. No committed golangci-lint config

AGENTS.md mandates `golangci-lint`, but there is no `.golangci.yml` in the
repo, so lint runs with whatever defaults the local version ships. Commit a
pinned config for reproducibility.

### 13. HTTP clients without timeouts

Provider clients are built as `&http.Client{}` with no `Timeout`. Context
cancellation covers Ctrl-C, but a hung embedding call during a long ingest
waits forever. Add per-request deadlines for embedding calls (not for LLM
streams, which are intentionally long-lived).

## Noted, no action needed

- Vector search is a full table scan decoding all embeddings per query.
  Documented as acceptable below ~100k chunks; the `Searcher` API allows a
  later sqlite-vec swap without interface change. Revisit only if scale grows.
