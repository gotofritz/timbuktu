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

---

# Additions — day-to-day correctness review (2026-07-19)

Findings from a correctness-focused code review. Numbering continues from the
architecture assessment above; the existing issues keep their priority order.

## High priority

### 14. Chunker boundary search picks the *earliest* separator, producing tiny chunks

**Problem:** `findBoundary` is meant to return "the best sentence break at or
before maxEnd", i.e. the boundary closest to the target size. It instead takes
the *minimum* over the last occurrences of each separator type
(`if candidate > minStart && candidate < best`). If a window contains one rare
separator early on (a single `"! "` or `"? "`), the chunk is cut there.

**Reproduced:** `"Hello! " + 300×"This is a normal sentence. "` with
`Chunker{Size: 800, Overlap: 100}` yields a first chunk of **7 bytes**
(`"Hello! "`) instead of ~3200. Any window mixing separator types is cut at
the earliest one, so real prose (paragraph breaks + full stops + the odd
question mark) systematically produces undersized chunks — more embeddings,
worse retrieval context.

**Evidence:** `internal/chunking/chunker.go:85-102`.

**Fix:** Track the *maximum* candidate (`candidate > best` with `best`
initialised to a sentinel, falling back to `maxEnd` when none found). Add
table-driven tests with mixed separators.

### 15. `Chunker.Split` hangs forever when `Size <= 0`

**Problem:** With `Size: 0` (e.g. `chunking: {size: 0}` in a hand-edited
config — nothing validates it, see issue 6), `end == start`, the boundary
resolves to `start`, and the no-progress guard sets `next = boundary = start`.
The loop never advances and appends empty chunks forever: `tbuk ingest` hangs
and consumes unbounded memory. Confirmed with a 2-second-timeout repro test.

**Evidence:** `internal/chunking/chunker.go:37-78` (guard at line 74 assumes
`boundary > start`, which fails when `sizeBytes == 0`).

**Fix:** Guard in `Split` itself (fall back to a sane default or return an
error for `Size <= 0`) — belt-and-braces with the `Config.Validate()` proposed
in issue 6, since `Chunker` is also constructed directly in code.

### 16. `tbuk meta set` / `meta list` don't normalize the path argument

**Problem:** `ingest`, `update`, and `delete` all resolve the CLI path through
`NormalizePath` (absolute + cleaned) before touching the DB, and documents are
keyed by that canonical path. `RunMetaSet` and `RunMetaList` pass the raw
argument straight to `GetByPath`, so `tbuk ingest ./notes.md` followed by
`tbuk meta set ./notes.md topic=x` fails with "document not found" — the
command only works when given the exact absolute path.

Additionally, both functions collapse *every* lookup error into
"document not found" (`meta.go:74,94`), conflating real DB errors with a
missing row — contrary to the `storage.ErrNotFound` / `errors.Is` pattern the
rest of the codebase follows.

**Evidence:** `internal/cli/meta.go:71-75, 91-95` vs
`internal/cli/delete.go:69-72`, `internal/cli/update.go:61-64`.

**Fix:** Call `NormalizePath` on the path in both commands; branch on
`errors.Is(err, storage.ErrNotFound)` and propagate other errors as-is.

## Medium priority

### 17. Provider-specific `base_url` defaults are unreachable — switching provider silently targets `localhost:8080`

**Problem:** Every adapter has an in-code fallback base URL (claude →
`api.anthropic.com`, openai → `api.openai.com`, ollama → `:11434`) that
triggers only when `cfg.BaseURL == ""`. But `config.Defaults()` (and the
`tbuk init` YAML) hardcode `base_url: http://localhost:8080` for both `llm`
and `embedding`, so `Load` never yields an empty BaseURL. A user who edits
`provider: llama` → `provider: claude` and doesn't think to touch `base_url`
sends Anthropic-bound requests to `http://localhost:8080`; likewise
`provider: ollama` never reaches its documented `:11434` default.

**Evidence:** `internal/config/config.go:65-76,113-123`;
`internal/llm/claude.go:27-30`, `internal/llm/ollama.go:22-25`,
`internal/embeddings/openai.go:28-31`.

**Fix:** Leave `base_url` empty in defaults/`DefaultYAML` (comment the
per-provider defaults instead) and resolve it per provider in the factories.
Alternatively have `Config.Validate()` (issue 6) reject provider/base_url
combinations that look like the stale default.

### 18. Ollama LLM adapter silently ignores `max_tokens` and `temperature`

**Problem:** `ollamaProvider.Chat` reads only `Model` from `CallOptions` and
sends just `model`/`messages`/`stream`; the provider's `maxTokens` field is
stored but never used. A prompt manifest's `temperature`/`max_tokens` (which
`RunAsk` forwards via `CallOptions`) and config `llm.max_tokens` are silent
no-ops on ollama, while working for claude/openai/llama.

**Evidence:** `internal/llm/ollama.go:34-56` (contrast
`internal/llm/claude.go:40-83`).

**Fix:** Map them into ollama's `options` object (`num_predict`,
`temperature`) in the request body.

### 19. Embedding count mismatch panics mid-ingest

**Problem:** The ingester indexes `vecs[j]` assuming the embedder returned
exactly one vector per input text. The ollama and openai adapters return
whatever the server sent (`result.Embeddings` / `result.Data`) without
checking the count, so a partial or malformed provider response crashes
`tbuk ingest` with an index-out-of-range panic instead of a clean error.

**Evidence:** `internal/ingest/ingester.go:126-137`;
`internal/embeddings/ollama.go:78-85`, `internal/embeddings/openai.go:73-92`.

**Fix:** In each adapter (or once in `IngestFile`), verify
`len(vectors) == len(texts)` and return a descriptive error on mismatch.

### 20. Document row is updated before chunks are replaced — a failed re-ingest strands stale chunks forever

**Problem:** On re-ingest, `IngestFile` writes the new SHA256 to the
`documents` row *before* `ReplaceForDocument` swaps the chunks. If the chunk
replacement fails (disk full, DB locked beyond busy_timeout), the document
records the new hash while the index still holds the old chunks — and every
subsequent `ingest`/`update` sees "SHA unchanged" and skips the file. The
stale index can then only be repaired with `--force`, and nothing tells the
user. This undercuts the atomicity the chunk transaction was built for.

**Evidence:** `internal/ingest/ingester.go:143-172` (doc `Update` at 147,
`ReplaceForDocument` at 170).

**Fix:** Perform the document upsert and `ReplaceForDocument` in the same
transaction, or update the document's SHA256 only after chunks are stored.

## Low priority

### 21. `CheckFTS5` leaks the `sql.Rows` it opens

`internal/search/search.go:48-54` discards the `*sql.Rows` from
`QueryContext` without closing it, holding a pooled connection until GC.
Use `QueryRowContext(...).Scan(...)` (tolerating `sql.ErrNoRows`) or close
the rows.

### 22. `tbuk template edit` doesn't edit anything

The command's help ("Open a template's manifest in $EDITOR") and
`docs/initial-context.md` promise an editor session, but the implementation
prints `Edit: <path> (open with vi)` and exits
(`internal/cli/template.go:92-109`). Either launch `$EDITOR` via
`exec.Command` with stdio attached, or rename/re-describe the command and fix
the docs.

## Noted, no action needed (correctness review)

- `Searcher.Vector` silently skips chunks whose embedding blob fails to
  decode (`vector.go:42-45`). Same "silent degrade" family as issue 2; the
  doctor check proposed there should count undecodable embeddings too.
- `tbuk stats --format banana` silently falls back to text output while
  `search`/`find` validate the flag. Cosmetic inconsistency.
