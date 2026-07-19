# Subplan 11: POC Hardening — Correctness & Requirement Gaps

## Context

Post-implementation review of subplans 00–10 (all archived in `docs/archive/`).
The POC is structurally sound: clean package boundaries, dependencies point
inward, providers sit behind interfaces, table-driven tests throughout, and
every package ships with tests. However, the review found several correctness
bugs (one proven by a failing probe test), a handful of unmet plan
requirements, and smaller robustness/maintainability issues. This subplan
closes them.

Findings are ordered by severity. Each phase is one reviewable PR.

---

## P0 — Correctness bugs

### 1. Foreign-key cascade is broken across pooled connections (proven)

> **DONE (PR 1).** Archived to
> `docs/archive/2026-07-18-2242-f41a86c-11-p0-1-fk-cascade.md`. Repair
> migration 002 dropped — the app was never used, so no damaged DBs exist.

`storage.Open` runs `PRAGMA foreign_keys=ON` (and `journal_mode=WAL`) via
`db.Exec`. Both pragmas are **per-connection** in SQLite, but `database/sql`
maintains a pool: the pragma applies only to the one connection that happened
to execute it. Every other pooled connection has `foreign_keys = 0`.

Proven by a probe test: after forcing the pool past one connection,
`DocumentRepo.Delete` left an orphan chunk behind (`PRAGMA foreign_keys`
returned `0` on all pooled connections). Consequences:

- `tbuk delete` can silently leave orphan chunks and metadata.
- Orphan chunks keep their `chunks_fts` rows (the FTS delete trigger fires on
  chunk deletion, which never happens), so deleted documents keep appearing
  in keyword/hybrid search.

**Fix**: apply pragmas in the DSN so every pooled connection gets them
(`modernc.org/sqlite` supports this):

```go
dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
db, err := sql.Open("sqlite", dsn)
```

Note `:memory:` needs care: a pooled second connection to `:memory:` opens a
*different* empty database. Storage tests currently mask the pool problem for
exactly this reason. Either keep `:memory:` with
`db.SetMaxOpenConns(1)` inside `Open` when the path is `:memory:`, or switch
tests to `t.TempDir()` files.

**Migration/repair**: add migration 002 to clean up databases already damaged:

```sql
DELETE FROM chunks   WHERE document_id NOT IN (SELECT id FROM documents);
DELETE FROM metadata WHERE document_id NOT IN (SELECT id FROM documents);
INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild');
```

**Tests** (write first):
- Regression: open file-backed DB, `SetMaxOpenConns(4)`, churn connections,
  delete a document, assert zero orphan chunks and zero stale FTS rows.
- Migration 002 removes pre-seeded orphans and rebuilds FTS.

### 2. Default config cannot run `tbuk ask` — no `llama` LLM provider

> **DONE (PR 2).** Archived to
> `docs/archive/2026-07-18-2257-aab6dfa-11-p0-2-llama-provider.md`. `llama` is backed by the
> existing OpenAI adapter (optional bearer token); README/`initial-context.md`
> docs aligned (P1-13, `voyage` dropped).

`config.Defaults()` sets `llm.provider: llama`, but `llm.NewLLM` only accepts
`claude | openai | ollama`. Out of the box, `tbuk ask` fails with
`llm: unknown provider "llama"`. The original POC plan explicitly required a
llama.cpp provider; subplan 05 dropped it without reconciling the default
config or README (which documents `llama | ollama | claude | openai`).

**Fix**: add a `llama` case. llama.cpp's server exposes an OpenAI-compatible
`/v1/chat/completions` endpoint, so implement it as the OpenAI provider with
no API-key requirement and `base_url` defaulting to `http://localhost:8080`
(refactor `openAIProvider` to take an optional bearer token rather than
duplicating the SSE loop).

**Tests**: factory returns a provider for `llama` without env vars; streaming
against a mock OpenAI-format SSE server; README/config comment matches the
factory's accepted providers.

### 3. Metadata is never written — `tbuk find` always returns nothing

> **DONE (PR 3).** Archived to
> `docs/archive/2026-07-18-2315-9ed8d2f-11-p0-3-metadata.md`. Ingest writes
> automatic `filename`/`extension`/`mime`/`dir` metadata (refreshed on
> re-ingest, user keys preserved); added `tbuk meta set` / `tbuk meta list`
> backed by `MetadataRepo.List`.

`Ingester` receives a `MetadataRepo` but never calls it. No code path writes
metadata rows, so `tbuk find key=value` (a headline POC feature:
`tbuk find tag=design`, `tbuk find filename=README.md`) can never match.

**Fix** (two parts):
1. During ingest, write automatic metadata per document: `filename`,
   `extension`, `mime`, `dir`. This makes `tbuk find filename=README.md` work
   as the POC plan promised.
2. Add `tbuk meta set <path> <key>=<value>...` and
   `tbuk meta list <path>` subcommands so users can attach tags
   (`tbuk meta set notes.md tag=design`). Small surface: `MetadataRepo`
   already has Set/Get/Delete.

**Tests**: ingest writes the automatic keys; re-ingest refreshes them;
`find filename=...` returns the document's chunks; `meta set` + `find tag=...`
round-trip.

### 4. Template manifest `model` / `temperature` / `max_tokens` never reach the LLM

> **DONE (PR 4).** Archived to
> `docs/archive/2026-07-18-2359-a83a9c6-11-p0-4-5-callopts-reingest.md`. `RunAsk` forwards
> `llm.CallOptions{Model, Temperature, MaxTokens}`; `CallOptions.Temperature`
> and `Manifest.Temperature` are now `*float64` (explicit `0` honored,
> omitted from the request body when nil); dead `llmCfg` block removed.

`ask.go` builds `llmCfg` with the manifest's model override — then never uses
it (dead assignment). `RunAsk` calls `chat(ctx, messages)` with no
`CallOptions`, so manifest `temperature: 0.2`, `max_tokens: 2048`, and `model`
are all silently ignored. This breaks the "Template Capabilities" requirement
of the POC plan (templates configure model, temperature, output).

**Fix**: pass `llm.CallOptions{Model: manifest.Model, Temperature:
manifest.Temperature, MaxTokens: manifest.MaxTokens}` through `RunAsk` into
the chat call. Remove the dead `llmCfg` block. Note the providers treat
`Temperature == 0` as "unset"; switch `CallOptions.Temperature` to `*float64`
(or add a `HasTemperature` bool) so an explicit `0` is expressible.

**Tests**: `RunAsk` forwards manifest options into the injected `chatFn`
(assert received `CallOptions`); explicit temperature 0 is honored.

### 5. Re-ingest deletes old chunks before the new ones exist

> **DONE (PR 4).** Archived to
> `docs/archive/2026-07-18-2359-a83a9c6-11-p0-4-5-callopts-reingest.md`. Early `DeleteByDocument` dropped; extraction and
> embedding run first, then `ChunkRepo.ReplaceForDocument` does delete +
> bulk-insert in one transaction. A failed re-ingest keeps the old index.

`IngestFile` calls `chunks.DeleteByDocument` *before* extraction and
embedding. If embedding fails (provider down, 429), the old chunks are
already gone — the document's index is destroyed rather than left stale.
Subplan 06 required: "Each file is wrapped in a single DB transaction: all
chunks committed together or none".

**Fix**: reorder — extract, chunk, embed first; then delete-old + insert-new
inside one transaction. Simplest shape: add
`ChunkRepo.ReplaceForDocument(ctx, docID, chunks)` that does
delete + bulk-insert in a single tx, and drop the early delete from
`IngestFile`.

**Tests**: embed error on a previously-ingested doc leaves the old chunks
intact and searchable; success path replaces atomically.

### 6. Hybrid search ignores `MinScore`; keyword search swallows all errors

> **DONE (PR 5).** Archived to
> `docs/archive/11-p0-6-hybrid-minscore-fts5.md`. `Hybrid` filters the fused
> RRF scores by `opts.MinScore` before truncating to TopK; `Keyword` sanitizes
> user input into double-quoted FTS5 phrases (via `sanitizeFTS5Query`) and
> propagates real query errors instead of swallowing them with `nilerr`.

- `Hybrid` runs both legs with `MinScore: 0` and never filters the fused
  results, so `tbuk search --min-score 0.3` (default mode: hybrid) silently
  ignores the flag.
- `Keyword` returns `nil, nil` on *any* query error (`//nolint:nilerr`), to
  paper over FTS5 syntax errors from special characters. This also hides
  genuine DB failures, and inside `Hybrid` it means keyword results silently
  vanish.

**Fix**:
- Apply `opts.MinScore` to the fused RRF scores before truncating to TopK
  (document that hybrid scores are RRF sums, not cosine values).
- In `Keyword`, sanitize the user query for FTS5 instead of swallowing errors:
  wrap each whitespace-separated term in double quotes (escaping embedded
  quotes), which neutralizes FTS5 operators. Propagate real query errors.

**Tests**: hybrid respects MinScore; keyword search with `"foo AND ("` input
returns results/empty without error; closed-DB error propagates.

---

## P1 — Robustness

### 7. Chunker can split multi-byte UTF-8 runes

> **DONE (PR 6).** Archived to
> `docs/archive/2026-07-19-0000-0b3ae32-11-p1-7-11-utf8-paths.md`. `Split` snaps
> boundary and overlap offsets to a rune start (`snapRuneStart`); the search
> preview truncates on runes via `TruncatePreview`.

`Chunker.Split` slices at byte offsets (`sizeBytes`, `overlapBytes`,
`findBoundary` fallback `maxEnd`). For non-ASCII text (accents, CJK), chunk
boundaries can land mid-rune, producing invalid UTF-8 that is stored, embedded,
and displayed. Same class of bug in `cli/search.go` result preview
(`text[:120]`).

**Fix**: snap boundaries back to a rune start (`utf8.RuneStart`) in `Split`,
and truncate the preview on runes (`[]rune` or `utf8`-aware walk).

**Tests**: chunk a CJK/accented document; assert `utf8.ValidString` on every
chunk; preview truncation of multi-byte text stays valid.

### 8. LLM stream goroutines can leak

Provider goroutines send on an unbuffered channel with no `select` on
`ctx.Done()`. If the consumer stops reading early (as `RunAsk` does on a
mid-stream error via `return` — it never drains), the goroutine blocks on send
forever, holding the HTTP body open.

**Fix**: every `ch <- tok` becomes `select { case ch <- tok: case <-ctx.Done(): return }`
in claude/openai/ollama stream loops. `RunAsk` should also cancel/drain on
early exit and use a cancellable context (wire `cmd.Context()` through instead
of `context.Background()` so Ctrl-C interrupts retrieval and streaming).

**Tests**: consumer abandons channel after first token with cancelled ctx →
goroutine exits (assert with a done-signal or `goleak`-style check using
runtime.NumGoroutine delta).

### 9. Provider HTTP errors discard the response body

All five HTTP adapters map non-200 to `StatusText(code)` (e.g. literally
"Bad Request"), throwing away the API's error message ("model not found",
"context length exceeded", rate-limit details). Painful to debug.

**Fix**: read up to ~2 KB of the body and include it in
`LLMError.Message` / `EmbedError.Message`.

**Tests**: mock 400 with JSON error body → error string contains the body.

### 10. "Not found" conflated with real DB errors

`Ingester.IngestFile` and `RunDelete` treat *any* `GetByPath` error as "does
not exist". A transient DB error during ingest routes into the create path
and fails later with a confusing UNIQUE violation; in delete it reports
"document not found" for a real I/O failure.

**Fix**: `GetByPath`/`GetBySHA256` return a sentinel (`storage.ErrNotFound`
wrapping `sql.ErrNoRows`); callers branch with `errors.Is`.

**Tests**: closed DB → ingest/delete surface the real error, not
not-found/create.

### 11. Paths are not normalized

> **DONE (PR 6).** Archived to
> `docs/archive/2026-07-19-0000-0b3ae32-11-p1-7-11-utf8-paths.md`. `NormalizePath`
> (`filepath.Abs`) is applied at the ingest/update/delete boundary; existing
> relative-path rows won't match (POC-acceptable) — re-ingest re-keys them.

Documents are keyed by the path string exactly as typed. `tbuk ingest docs/`
then `tbuk delete ./docs/a.md` or an absolute path misses; the same file can
be indexed twice under two spellings.

**Fix**: normalize to `filepath.Abs` (+ `filepath.Clean`) at the CLI boundary
for ingest/update/delete. Migration note: existing relative-path rows won't
match; document this in the changelog (acceptable for a POC; optionally add a
one-shot migration that absolutizes rows resolvable from CWD — skip if
ambiguous).

**Tests**: ingest via relative path, delete via absolute → document removed.

### 12. Doctor inaccuracies

- The FTS5 check is gated on the *embedding server's* health flag (`ok` from
  the previous section), so a down embedding server hides FTS corruption and
  prints `fts5: ✓ available` unchecked.
- `/health` and `/v1/models` probes are llama.cpp/ollama conventions; for
  `claude`/`openai` providers the probe hits the wrong path on a hosted API
  (or an empty BaseURL) and reports misleading status.
- `tbuk delete` leaves the extracted-cache file
  (`~/.tbuk/extracted/<sha>.txt`) behind forever (lineage cleanup gap —
  fold into the doctor PR or the delete path).

**Fix**: track DB health in its own variable; skip HTTP probes (print
`hosted API — not probed; set ANTHROPIC_API_KEY/OPENAI_API_KEY`) for hosted
providers; delete the extracted file (by stored SHA) in `RunDelete`.

**Tests**: doctor with unreachable embedder still exercises the FTS check;
delete removes the cache file.

### 13. Docs promise providers that don't exist

README config comment says embedding `provider: llama | ollama | openai |
voyage` — there is no voyage embedder. LLM list includes `llama` (see P0-2).
`initial-context.md` architecture section is otherwise accurate — keep it in
sync with whatever P0-2 lands.

**Fix**: after P0-2, make README/`initial-context.md`/`DefaultYAML` comments
match the factories exactly (drop voyage or implement it — recommend drop for
now; it's listed under POC "possible implementations", not required).

---

## P2 — Maintainability / hygiene

### 14. Dead and vestigial code

- `chunking.splitSentences` returns `[]string{text}` — vestigial; the "greedy
  sentence accumulation" described in docs doesn't exist (boundary search
  does the work). Delete the function and fix the doc comment.
- `search/metadata.go`: unused `alias` + `_ = alias`; alias generation
  `rune('0'+i)` produces non-alphanumeric aliases past 10 filters — use
  `fmt.Sprintf("m%d", i)`.
- `internal/metadata` is an empty stub package with no test files (drags
  the "no test files" line through every coverage run). Delete it until
  something needs it; re-adding a package is cheap.
- `cli/ask.go` dead `llmCfg` block (removed in P0-4).

### 15. Per-package coverage below the stated bar

AGENTS.md demands ≥ 85% per package; `cli` is at 84.2% and `preprocess` at
82.2%, and `make check-ci` only gates the *total*. Either raise the two
packages above 85% (the P0/P1 tests above will mostly do it) or amend
AGENTS.md to match the total-coverage gate. Recommend: add the tests, and
extend `check-ci` to check per-package numbers so the rule is enforced, not
aspirational.

### 16. Misc

- `cli` package keeps config in package-level mutable vars (`cfg`,
  `cfgFile`) — AGENTS.md says no global mutable state. Thread config through
  cobra's context or a small struct.
- `stats` sizes via `LENGTH(GROUP_CONCAT(c.text))` counts separator commas
  and risks the group_concat length limit; `COALESCE(SUM(LENGTH(c.text)),0)`
  is simpler and exact.
- `retrieval.max_tokens` (context budget) in the manifest is parsed but
  unenforced — either trim retrieved chunks to the budget in `RunAsk` or
  remove the field from the built-in manifest until implemented.
- Security hardening (no exploitable findings; local-first CLI, parameterized
  SQL throughout, keys from env only — good): create `~/.tbuk` with `0o700`
  and extracted text/DB files with `0o600`; knowledge-base content is
  personal data and need not be group/world-readable.

---

## Phasing (one PR each)

| PR | Contents | Depends on |
|----|----------|-----------|
| 1 | P0-1 pragma DSN + regression test ✅ done (migration dropped — app unused) | — |
| 2 | P0-2 llama LLM provider + docs alignment (P1-13) ✅ done | — |
| 3 | P0-3 automatic metadata + `tbuk meta` commands ✅ done | — |
| 4 | P0-4 manifest CallOptions + P0-5 transactional re-ingest ✅ done | — |
| 5 | P0-6 hybrid MinScore + FTS5 query sanitizing ✅ done | — |
| 6 | P1-7 UTF-8 chunking + P1-11 path normalization ✅ done | 1 |
| 7 | P1-8 stream cancellation + P1-9 error bodies + P1-10 ErrNotFound | — |
| 8 | P1-12 doctor fixes + delete cache cleanup | — |
| 9 | P2 cleanups + per-package coverage gate + perms hardening | 1–8 |

Every PR: failing test first (TDD hook enforces), `make check-ci` green,
update `docs/initial-context.md` where behavior changes (P0-2, P0-3, P1-11).
Archive this plan per AGENTS.md convention when complete.

## Acceptance (end state)

- Fresh install: `tbuk init && tbuk ingest docs/ && tbuk ask "..."` works with
  the default config against a local llama.cpp server.
- `tbuk delete` leaves no orphan chunks, no stale FTS rows, no cache file —
  verified under a multi-connection pool.
- `tbuk find filename=README.md` returns results after plain ingestion.
- Template manifest model/temperature/max_tokens observably change LLM calls.
- A failed re-ingest never destroys the previous index.
- All packages ≥ 85% coverage, enforced by `make check-ci`.
