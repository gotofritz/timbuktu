# Plan 01 — Issues from Architecture Assessment (2026-07-19)

Findings from staff-level architecture & design review. Ordered by priority.
Each issue: problem, evidence, fix.

## High priority

### 1. `search.Options.Metadata` silent no-op

**Problem:** `Options.Metadata` documented as "AND-combined metadata
pre-filter"; `Retriever.Retrieve` accepts `meta` arg, forwards into
`search.Options`. But `Searcher.Hybrid` rebuilds options as
`Options{TopK: ..., MinScore: 0}` — drops Metadata. `Vector`/`Keyword` never
read it. Caller passing filter gets unfiltered results, no error.

**Evidence:** `internal/search/hybrid.go:12`, `internal/search/search.go:26`,
`internal/retrieval/retrieval.go:38-40`.

**Fix:** Implement metadata pre-filter in Vector/Keyword/Hybrid (JOIN against
`metadata` table before scoring), or remove field + `meta` parameter chain so
API stops advertising behaviour it lacks.

### 2. Embedding dimension mismatch fails silent

**Problem:** `cosineSimilarity` returns `0` when vector lengths differ. User
changes embedding model or `embedding.dimension` after ingest → every stored
vector scores 0: vector search silently returns nothing, hybrid degrades to
keyword-only. No dimension recorded in DB; nothing checks mismatch at ingest
or query time.

**Evidence:** `internal/search/vector.go:72-75`; schema in
`internal/storage/migrate.go` stores raw BLOBs, no dimension metadata.

**Fix:** Record embedding dimension (ideally provider/model) in DB. Error
loud on mismatch at query + ingest. Add `tbuk doctor` check.

### 3. Composition root scattered across CLI commands

**Problem:** Every command hand-wires own dependency graph — open DB, repos,
embedder, chunker, searcher — duplicated open/close + error wrapping. New
dependency = touch N command files.

**Evidence:** `internal/cli/ingest.go:29-53`, `internal/cli/ask.go:53-70`,
similar in `search.go`, `update.go`, `delete.go`, `stats.go`.

**Fix:** Shared builder, e.g. `cli.openApp(cfg) (*App, error)` returning
repos/searcher/ingester + closer. Commands consume App.

## Medium priority

### 4. `IngestDir` swallows walk errors

**Problem:** `filepath.WalkDir` return discarded; callback returns `nil` on
entry errors. Unreadable dir/file skipped invisibly — no `Result`, not in
"Done: N errors" summary.

**Evidence:** `internal/ingest/ingester.go:228-230`.

**Fix:** Record entry errors as error `Result`s → surface in output +
non-zero exit path.

### 5. `DefaultYAML` duplicates `Defaults()` by hand

**Problem:** Default config exists twice — struct literal in `Defaults()`,
hand-built YAML string in `DefaultYAML()`. Two sources of truth drift.

**Evidence:** `internal/config/config.go:58-85` vs `config.go:108-132`.

**Fix:** Generate YAML by marshalling `Defaults()` (template if comments
wanted).

### 6. Config has no validation

**Problem:** No check for `overlap >= size`, negative chunk sizes, nonsense
values; unknown providers fail deep inside factories.

**Evidence:** `internal/config/config.go` (no Validate), factories in
`internal/llm/llm.go` / `internal/embeddings/embeddings.go`.

**Fix:** Add `Config.Validate()` called from root `PersistentPreRunE` — every
command fails fast, clear message.

## Low priority

### 7. Prompts root hardcoded

`~/.tbuk/prompts` hardcoded in `internal/cli/ask.go:44-46` while DB path +
extracted dir configurable. Move to config (e.g. `prompts.dir`).

### 8. Storage queries in CLI package

`CountDocuments` / `CountChunks` in `internal/cli/ingest.go:128-139` are
storage concerns. Move to `internal/storage`.

### 9. Dead `sseScanner` wrapper

`internal/llm/stream.go:39-42` claims to strip trailing carriage returns but
just returns `bufio.NewScanner(r)`. Implement CR stripping or delete wrapper
+ comment.

### 10. Makefile `serve` target leftover cruft

`make serve` serves `output/` dir "for local feed testing" — no `output/`
exists. Remove target.

### 11. Stale architecture doc entry

`docs/initial-context.md` lists `internal/metadata/ STUB` — package not
exist. Remove line.

### 12. No committed golangci-lint config

AGENTS.md mandates `golangci-lint`, but no `.golangci.yml` in repo — lint
runs with local version defaults. Commit pinned config for reproducibility.

### 13. HTTP clients without timeouts

Provider clients built as `&http.Client{}`, no `Timeout`. Context
cancellation covers Ctrl-C, but hung embedding call during long ingest waits
forever. Add per-request deadlines for embedding calls (not LLM streams —
intentionally long-lived).

## Noted, no action needed

- Vector search = full table scan decoding all embeddings per query.
  Documented acceptable below ~100k chunks; `Searcher` API allows later
  sqlite-vec swap without interface change. Revisit only if scale grows.

---

# Additions — day-to-day correctness review (2026-07-19)

Correctness-focused code review. Numbering continues; existing issues keep
priority order.

## High priority

### 14. Chunker boundary search picks *earliest* separator → tiny chunks

**Problem:** `findBoundary` meant to return "best sentence break at or before
maxEnd" (closest to target size). Instead takes *minimum* over last
occurrences of each separator type
(`if candidate > minStart && candidate < best`). Window with one rare early
separator (single `"! "` or `"? "`) → chunk cut there.

**Reproduced:** `"Hello! " + 300×"This is a normal sentence. "` with
`Chunker{Size: 800, Overlap: 100}` yields first chunk of **7 bytes**
(`"Hello! "`) instead of ~3200. Any window mixing separator types cut at
earliest — real prose (paragraph breaks + full stops + odd question mark)
systematically produces undersized chunks — more embeddings, worse retrieval
context.

**Evidence:** `internal/chunking/chunker.go:85-102`.

**Fix:** Track *maximum* candidate (`candidate > best`, `best` initialised to
sentinel, fall back to `maxEnd` when none). Add table-driven tests, mixed
separators.

### 15. `Chunker.Split` hangs forever when `Size <= 0`

**Problem:** With `Size: 0` (hand-edited config — nothing validates, see
issue 6), `end == start`, boundary resolves to `start`, no-progress guard
sets `next = boundary = start`. Loop never advances, appends empty chunks
forever: `tbuk ingest` hangs, unbounded memory. Confirmed with
2-second-timeout repro test.

**Evidence:** `internal/chunking/chunker.go:37-78` (guard at line 74 assumes
`boundary > start`, fails when `sizeBytes == 0`).

**Fix:** Guard in `Split` itself (sane default or error for `Size <= 0`) —
belt-and-braces with `Config.Validate()` from issue 6, since `Chunker` also
constructed directly in code.

### 16. `tbuk meta set` / `meta list` don't normalize path argument

**Problem:** `ingest`/`update`/`delete` resolve CLI path via `NormalizePath`
(absolute + cleaned) before DB; documents keyed by canonical path.
`RunMetaSet`/`RunMetaList` pass raw arg straight to `GetByPath`, so
`tbuk ingest ./notes.md` then `tbuk meta set ./notes.md topic=x` fails
"document not found" — works only with exact absolute path.

Also both collapse *every* lookup error into "document not found"
(`meta.go:74,94`) — conflates real DB errors with missing row, contrary to
`storage.ErrNotFound` / `errors.Is` pattern elsewhere.

**Evidence:** `internal/cli/meta.go:71-75, 91-95` vs
`internal/cli/delete.go:69-72`, `internal/cli/update.go:61-64`.

**Fix:** `NormalizePath` in both commands; branch on
`errors.Is(err, storage.ErrNotFound)`, propagate other errors as-is.

## Medium priority

### 17. Provider `base_url` defaults unreachable — provider switch silently targets `localhost:8080`

**Problem:** Every adapter has in-code fallback base URL (claude →
`api.anthropic.com`, openai → `api.openai.com`, ollama → `:11434`) triggering
only when `cfg.BaseURL == ""`. But `config.Defaults()` (and `tbuk init` YAML)
hardcode `base_url: http://localhost:8080` for both `llm` and `embedding` —
`Load` never yields empty BaseURL. User edits `provider: llama` →
`provider: claude` without touching `base_url` → Anthropic-bound requests to
`http://localhost:8080`; `provider: ollama` never reaches documented `:11434`
default.

**Evidence:** `internal/config/config.go:65-76,113-123`;
`internal/llm/claude.go:27-30`, `internal/llm/ollama.go:22-25`,
`internal/embeddings/openai.go:28-31`.

**Fix:** Leave `base_url` empty in defaults/`DefaultYAML` (comment
per-provider defaults instead), resolve per provider in factories. Or have
`Config.Validate()` (issue 6) reject provider/base_url combos that look like
stale default.

### 18. Ollama LLM adapter silently ignores `max_tokens` + `temperature`

**Problem:** `ollamaProvider.Chat` reads only `Model` from `CallOptions`,
sends just `model`/`messages`/`stream`; provider's `maxTokens` field stored,
never used. Manifest `temperature`/`max_tokens` (forwarded by `RunAsk` via
`CallOptions`) and config `llm.max_tokens` = silent no-ops on ollama, while
working for claude/openai/llama.

**Evidence:** `internal/llm/ollama.go:34-56` (contrast
`internal/llm/claude.go:40-83`).

**Fix:** Map into ollama's `options` object (`num_predict`, `temperature`) in
request body.

### 19. Embedding count mismatch panics mid-ingest

**Problem:** Ingester indexes `vecs[j]` assuming embedder returned exactly
one vector per input text. Ollama + openai adapters return whatever server
sent (`result.Embeddings` / `result.Data`) without count check — partial or
malformed response crashes `tbuk ingest` with index-out-of-range panic, not
clean error.

**Evidence:** `internal/ingest/ingester.go:126-137`;
`internal/embeddings/ollama.go:78-85`, `internal/embeddings/openai.go:73-92`.

**Fix:** In each adapter (or once in `IngestFile`), verify
`len(vectors) == len(texts)`, descriptive error on mismatch.

### 20. Document row updated before chunks replaced — failed re-ingest strands stale chunks forever

**Problem:** On re-ingest, `IngestFile` writes new SHA256 to `documents` row
*before* `ReplaceForDocument` swaps chunks. If chunk replacement fails (disk
full, DB locked beyond busy_timeout), document records new hash while index
holds old chunks — every later `ingest`/`update` sees "SHA unchanged", skips.
Stale index repairable only with `--force`, nothing tells user. Undercuts
atomicity the chunk transaction was built for.

**Evidence:** `internal/ingest/ingester.go:143-172` (doc `Update` at 147,
`ReplaceForDocument` at 170).

**Fix:** Document upsert + `ReplaceForDocument` in same transaction, or
update document SHA256 only after chunks stored.

## Low priority

### 21. `CheckFTS5` leaks `sql.Rows`

`internal/search/search.go:48-54` discards `*sql.Rows` from `QueryContext`
without closing — holds pooled connection until GC. Use
`QueryRowContext(...).Scan(...)` (tolerate `sql.ErrNoRows`) or close rows.

### 22. `tbuk template edit` doesn't edit anything

Help ("Open a template's manifest in $EDITOR") and `docs/initial-context.md`
promise editor session; implementation prints `Edit: <path> (open with vi)`
and exits (`internal/cli/template.go:92-109`). Launch `$EDITOR` via
`exec.Command` with stdio attached, or rename/re-describe command + fix docs.

## Noted, no action needed (correctness review)

- `Searcher.Vector` silently skips chunks whose embedding blob fails decode
  (`vector.go:42-45`). Same "silent degrade" family as issue 2; doctor check
  there should count undecodable embeddings too.
- `tbuk stats --format banana` silently falls back to text while
  `search`/`find` validate flag. Cosmetic inconsistency.

---

# Additions — long-term evolution assessment (2026-07-19)

Review focused on how repo evolves — schema growth, config growth,
concurrency surface roadmap (`docs/plans/next-steps.md`) will add. Nothing
broken *today*; each item = latent cost paid when relevant area changes.
Numbering continues.

## Medium priority

### 23. Migration runner not ready for second migration

**Problem:** Whole schema = one migration, all statements `IF NOT EXISTS` —
masks three weaknesses in `RunMigrations` that bite when migration v2 lands
(roadmap's embedding-provenance + conversational-session work imply schema
changes):

1. Applied-version check swallows error
   (`_ = db.QueryRow(...).Scan(&exists)`) — transient failure reads as "not
   applied", migration re-runs. Harmless for idempotent `IF NOT EXISTS` DDL;
   future `ALTER TABLE ... ADD COLUMN` re-applied this way fails hard or
   corrupts.
2. Migration SQL + `schema_migrations` record = two separate `Exec` calls,
   not one transaction. Crash between → schema changed, unrecorded — next
   run re-applies (same hazard).
3. No guard for DB *newer* than binary. Older `tbuk` opening DB whose
   `schema_migrations` max exceeds what it knows silently reads/writes
   schema it doesn't understand. For local-first tool where users
   up/downgrade standalone binaries against one long-lived
   `~/.tbuk/tbuk.sqlite`, this = realistic failure mode.

**Evidence:** `internal/storage/migrate.go:79-94` (swallowed Scan at 81,
two-step apply at 85-93); no max-version check in `storage`.

**Fix:** Before second migration written: wrap each migration SQL + version
record in one transaction; propagate version-check error; error out (clear
"database created by newer tbuk" message) when recorded max version exceeds
binary's. Optionally copy DB file aside before applying pending migrations —
cheap insurance for personal data with no other backup story until
export/import ships.

### 24. Unknown config keys silently ignored

**Problem:** `Load` uses plain `yaml.Unmarshal` — drops unknown fields. Typo
(`chunk_size:` for `size:`, `baseurl:` for `base_url:`) or key from newer
tbuk silently ignored, default silently wins — config *looks* applied, isn't.
Issue 6 validates *values*; this = complementary gap: keys that don't exist.
Grows worse as config schema grows (collections, `prompts.dir`, retrieval
settings all on roadmap).

**Evidence:** `internal/config/config.go:100` (`yaml.Unmarshal(data, &cfg)`).

**Fix:** Decode via `yaml.Decoder` with `KnownFields(true)`, fail with
offending key name. Fold into same `Config.Validate()` effort as issue 6 —
all config errors surface together at startup.

## Low priority

### 25. CI never runs race detector

`make test-race` exists but no workflow invokes it — CI runs plain `go test`
(`.github/workflows/ci.yml`). `llm` package goroutine + channel heavy
(streaming adapters, `sendToken`/`ctx.Done()` selects); roadmap multi-turn
`ask` adds more concurrency. Races regress silent until user hits them. Add
`-race` to CI test step (or parallel job if runtime matters).

---

# Additions — security & attack surface assessment (2026-07-19)

Security review of attack surface: untrusted inputs (ingested documents),
secrets, network calls, SQLite layer, release/CI pipeline. Core in good
shape — SQL parameterised throughout, FTS5 queries sanitised
(`internal/search/keyword.go:49-56`), API keys env-only, never in config or
logs, no external commands executed, `~/.tbuk` owner-only (dirs `0700`,
config/DB/extracted files `0600`, incl. `chmod` on open in
`internal/storage/db.go:37-43`). Below = remaining gaps. Numbering continues.

## Medium priority

### 26. Malformed PDF can panic + kill entire ingest run

**Problem:** Primary untrusted-input surface = document ingestion — PDFs
exactly what users download from elsewhere. PDF extraction delegates to
`github.com/ledongthuc/pdf` — parser with known index-out-of-range /
nil-dereference panics on malformed/hostile files (see upstream tracker),
nothing recovers: panic in `Extract` unwinds past `IngestDir` per-file error
handling, crashes whole `tbuk ingest` run instead of one error `Result`.
Extractor also `io.ReadAll`s entire file, no size cap (`pdf.go:17`;
`plaintext.go`/`html.go` unbounded too) — multi-GB stray file exhausts memory
before chunking.

**Evidence:** `internal/preprocess/pdf.go:15-42` (no recover, unbounded
read); `internal/ingest/ingester.go:226-240` (per-file loop no panic
isolation).

**Fix:** Wrap extractor call (`Extractor.Extract` or `preprocess.Extract`) in
`defer`/`recover` → panic becomes error `Result` for that file. Add
configurable max-file-size guard (stat before read; generous default like
100 MB) so one oversized file can't OOM run. Both fold into error-surfacing
work, issue 4.

### 27. API keys attached to any configured `base_url`, incl. plain HTTP

**Problem:** claude/openai adapters set `x-api-key` /
`Authorization: Bearer` on requests to whatever `base_url` config supplies,
no scheme/host check. Combined with issue 17 (defaults hardcode
`base_url: http://localhost:8080` — switching `provider: llama` →
`provider: claude` without editing `base_url` = realistic slip), failure mode
concrete: real `ANTHROPIC_API_KEY` sent cleartext to whatever answers on
localhost:8080 — or, remote `http://` host, unencrypted across network.

**Evidence:** `internal/llm/claude.go:90-96`, `internal/llm/openai.go`,
`internal/embeddings/openai.go:50-58` — headers unconditional;
`internal/config/config.go:69,75` — non-empty default `base_url` makes
misdirect reachable.

**Fix:** In cloud-provider factories (claude, openai), reject — or minimum
loud warn on — non-HTTPS non-loopback `base_url` when API key attached. Do
alongside issue 17 fix (empty default `base_url` resolved per provider) —
removes most likely trigger.

### 28. No dependency vuln scanning; release toolchain unpinned

**Problem:** Project ships standalone binaries but nothing watches dependency
tree: no `govulncheck` in CI, no Dependabot/Renovate — CVE in
`golang.org/x/net`, `modernc.org/sqlite`, or PDF parser (the one dep parsing
untrusted input, issue 26) unnoticed until user bitten. Release workflow runs
`goreleaser/goreleaser-action@v6` with `version: latest` — every tagged
release builds with whatever GoReleaser is current that day — unreproducible,
exposed to bad/compromised upstream release at publish time.

**Evidence:** `.github/workflows/ci.yml`, `quality-check.yml` (no vuln
scanning); `.github/workflows/release.yml:24-27` (`version: latest`); no
`.github/dependabot.yml`.

**Fix:** Add `govulncheck ./...` step to CI (fast, no config burden); minimal
`dependabot.yml` for `gomod` + `github-actions`; pin GoReleaser version in
`release.yml`. Optionally pin actions to commit SHAs — with Dependabot
keeping fresh, pinning costs nothing.

## Low priority

### 29. Release artifacts lack provenance / signature

GoReleaser publishes `checksums.txt` alongside binaries, but checksums +
binaries live in same release — attacker who tampers one tampers both;
checksum verifies download integrity, not authenticity. GitHub
`actions/attest-build-provenance` gives signed build-provenance attestations
(verifiable `gh attestation verify`) for few lines of workflow YAML; cosign
keyless signing of `checksums.txt` = alternative. Cheap insurance for
binary-download install path. Evidence: `.goreleaser.yml` (`checksum:` only),
`.github/workflows/release.yml`.

### 30. Ingested documents can smuggle terminal escapes through `ask` output

`tbuk search` escapes previews with `%q` (`internal/cli/search.go:139`), but
`tbuk ask` streams LLM output verbatim (`internal/cli/ask.go:193-205`).
Retrieved chunk text goes into prompt; models echo it — document ingested
from elsewhere can carry ANSI/OSC sequences reaching terminal raw: OSC 52
writes clipboard, OSC 0 retitles window, cursor/erase sequences hide/rewrite
what user reads. Same for doc-derived fields printed `%s` elsewhere
(citations, titles, metadata values in `meta list`/`stats`). Fix: filter
C0/C1 control chars (keep `\n`, `\t`) from streamed `ask` output +
doc-derived display strings — small writer wrapper, one place.

## Noted, no action needed (security review)

- **SQL injection:** all queries use placeholders; one dynamic query
  (`internal/search/metadata.go:16-31`) interpolates only generated aliases,
  never user input. Fine.
- **FTS5 injection:** `sanitizeFTS5Query` quotes every term, doubles embedded
  quotes. Fine.
- **Prompt injection via ingested docs** inherent to RAG; blast radius small
  by design: LLM output = text to terminal, no tool-use or shell path.
  Issue 30 covers the one real escalation (terminal escapes). Revisit if
  roadmap gives `ask` tool-calling.
- **Secrets hygiene:** keys read from env at construction, unexported struct
  fields only, never logged; `errorMessage` caps error bodies 2 KB, echoes
  only server response. Fine.
- **Path traversal via `--template`** (`ask -t ../../x` escapes
  `~/.tbuk/prompts`): attacker = victim = same local user, no trust boundary.
  Not worth code.

---

# Additions — production readiness / SRE assessment (2026-07-19)

SRE/platform review: release pipeline, failure recovery, signal handling,
SQLite operational behaviour. Much solid — WAL + `busy_timeout(5000)` +
`foreign_keys` set per pooled connection in DSN
(`internal/storage/db.go:50`), partial ingest failures exit non-zero with
per-file report (`internal/cli/ingest.go:121-124`), re-runs recover cheap via
SHA256 dedup, CI gates coverage 85%. Backup/export on roadmap
(`next-steps.md` quick win 3), migration hardening = issue 23, supply-chain =
issues 28–29. Numbering continues.

## Medium priority

### 31. Release workflow publishes without test gate; CI never builds shipped platforms

**Problem:** Two holes in commit → published binary path:

1. `release.yml` triggers on any `v*` tag push, straight to GoReleaser — no
   lint, no tests. `make release-*` helpers enforce "clean main" locally;
   nothing server-side does: hand-pushed tag (or commit that never saw a PR)
   publishes untested binaries. Tag pushes don't trigger CI either (`ci.yml`
   runs only branch pushes/PRs to main) — release can ship from commit CI
   never saw.
2. Releases ship six platform builds (linux/darwin/windows × amd64/arm64);
   CI compiles only linux/amd64. Compile break on another GOOS (`syscall`
   usage, build-tag slip, path handling) first discovered when tagged
   release *fails at publish time* — worst moment.

**Evidence:** `.github/workflows/release.yml` (checkout → setup-go →
goreleaser, nothing else); `.github/workflows/ci.yml` (single
`ubuntu-latest` job, no GOOS matrix); `.goreleaser.yml:15-21` (six targets).

**Fix:** In `release.yml`, add job running `go test ./...` + `go vet` (or
reuse CI steps) as prerequisite of goreleaser job. In `ci.yml`, cheap
cross-compile check — `GOOS=darwin`/`windows` `go build ./...` small matrix,
or `goreleaser release --snapshot` smoke job — platform breakage surfaces on
PR, not at tag time.

### 32. No signal handling — Ctrl-C not the clean cancel README claims

**Problem:** README/troubleshooting says "Press `Ctrl-C` to cancel —
retrieval and streaming are interrupted cleanly"; code carefully
context-plumbed end to end (`cmd.Context()` flows into ingest loops,
embedding calls, SSE stream goroutines with `ctx.Done()` selects). But
nothing creates signal-aware context: `main` → `cli.Execute()` → cobra
`Execute()` with default background context; `signal.NotifyContext` appears
nowhere. On SIGINT Go runtime default handler kills process — cancellation
paths = dead code in production, deferred cleanup (`db.Close`, in-flight
chunk transaction rollback) never runs, mid-directory `tbuk ingest` dies
without partial "Done: N ingested" summary. Data safe (WAL recovers on next
open), but documented behaviour not exist, interrupted-run UX worse than
designed.

**Evidence:** `cmd/tbuk/main.go` (no signal setup),
`internal/cli/root.go:77-83` (`Execute()`, not `ExecuteContext`); contrast
ctx plumbing it would activate: `internal/ingest/ingester.go`,
`internal/llm/stream.go:12`.

**Fix:** In `Execute`, wrap root context with
`signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`,
run `root.ExecuteContext(ctx)`. On cancellation, commands return `ctx.Err()`,
exit non-zero. `IngestDir`: print summary accumulated so far before return.
Second Ctrl-C force-quits (stdlib free: `NotifyContext` restores default
handling after first signal).

## Low priority

### 33. No retry on transient provider errors during bulk ingest

Bulk `tbuk ingest <dir>` against hosted embedding provider hits rate limits
(HTTP 429) + occasional 5xx/connection resets; each error fails that file for
the run. Recovery exists — re-run skips completed files via SHA dedup,
retries failures — friction, not data loss, but large first-time ingest
against OpenAI degrades into several manual re-runs. Fix: small bounded retry
(2–3 attempts, exponential backoff, honour `Retry-After`) on 429/5xx in
embedding adapters — or once in `Ingester` around `Embed` call. Don't retry
LLM streaming (`ask` interactive; fail fast). Evidence:
`internal/embeddings/openai.go`, `internal/embeddings/ollama.go` (single-shot
POSTs); `internal/ingest/ingester.go:126` (one `Embed` per file, error
recorded, loop moves on).

## Noted, no action needed (SRE review)

- **Concurrent `tbuk` processes:** WAL + `busy_timeout(5000)` handles
  realistic case (search during ingest). Fine.
- **Partial-failure exit codes:** dir ingest exits non-zero on any failure,
  says how many. Fine.
- **Backup story:** roadmap quick win 3 covers it; issue 23 pre-migration
  copy covers upgrade path. No new issue.
- **Observability:** errors carry provider response bodies (2 KB cap);
  adequate for local CLI — no structured logging needed at this scale.

---

# Additions — test & QA assessment (2026-07-19)

QA-lead review of test suite: what tests exercise, where gates drift, which
bug classes suite structurally cannot catch. Suite in good shape — providers
mocked `net/http/httptest`, storage on in-memory SQLite, UTF-8 boundary
property tests, goroutine-leak test for stream cancellation,
FTS5-trigger-sync assertions, generated minimal-PDF fixture. All packages
pass ≥85% coverage. Two gaps remain. Numbering continues.

## Medium priority

### 34. CI enforces only *total* coverage — per-package ≥85% rule local-only

**Problem:** AGENTS.md mandates coverage ≥85% *per package*. `make check-ci`
enforces (comment says it "mirrors the quality-check CI jobs"), but actual CI
coverage job checks only total: PR can drop one package to 40% while total
stays above 85% — CI green; per-package rule fires only for contributors who
remember `make check-ci` locally. Not hypothetical headroom: `cli` (85.8%),
`storage` (85.6%), `llm` (86.1%), `embeddings` (86.0%) all within ~1% of line
today — silent per-package drift = expected failure mode. Two gates
maintained as duplicated shell/awk → keep diverging (divergence = proof).

**Evidence:** `.github/workflows/quality-check.yml:44-56` (total-only) vs
`Makefile:56-72` (`check-ci` with per-package awk pass).

**Fix:** CI runs same per-package check — simplest: coverage job runs
`make check-ci` (or extract coverage-gate script to one file both invoke) so
local + CI gates can't drift again.

### 35. No test exercises real wiring — production extractor, root command, exit codes at 0%

**Problem:** Every test injects fakes at package seams; nothing runs
assembled pipeline. Concretely: `DefaultFileExtractor.ExtractFile` — only
production `FileExtractor`, used by every real `tbuk ingest` — 0% coverage
(ingester tests use mock extractor; CLI ingest tests cover only missing-arg +
nonexistent-path failures). `cli.Execute` + `cmd/tbuk/main` also 0% — error →
exit-code-1 path untested. Matters because bug classes other reviews found
are precisely wiring-level, invisible to per-package unit tests at any
coverage: metadata filter dropped between retrieval + search (issue 1),
`meta set` skipping path normalization siblings apply (issue 16), signal
context plumbed everywhere but never created (issue 32).

**Evidence:** coverage run 2026-07-19: `internal/ingest/ingester.go:247`
(`ExtractFile` 0%), `internal/cli/root.go:78` (`Execute` 0%), `cmd/tbuk` (no
test files); `internal/cli/ingest_test.go` (failure paths only).

**Fix:** One CLI-level happy-path integration test: temp `HOME`,
`tbuk init` → `ingest` real `.md` fixture (embedder backed by `httptest`) →
`search` finds it → `meta set`/`meta list` → `delete`. Drive through root
command (`SetArgs` + `Execute`) so flag parsing, config loading, composition
wiring all exercised — also pins exit-code behaviour before issue 32 signal
work changes it; same test in small OS matrix gives issue 31 platform gap
runtime (not just compile-time) coverage.

## Noted, no action needed (QA review)

- **Chunker test gaps** (uniform single-separator fixtures = why issues 14/15
  slipped): fixes for those issues already specify missing mixed-separator +
  `Size <= 0` tests. No separate issue.
- **Goroutine-leak test** polls `runtime.NumGoroutine`, 2 s deadline; bounded,
  package-sequential, not flake risk. Fine.
- **`printFileResult`/`printDirResults` at 0%**: one-line stdout wrappers
  around fully tested exported variants. Not worth code.

---

# Additions — performance & efficiency assessment (2026-07-19)

Performance review of hot paths: vector scan, ingest pipeline, SQLite layer,
per-query allocation. Earlier acceptance of O(n) vector scan ("Noted" under
architecture assessment) stands — neither issue re-litigates it. Numbering
continues.

## Medium priority

### 36. Vector search materialises full text of every chunk per query + full-sorts all candidates

**Problem:** Accepted design = O(n) scan over embeddings; implementation does
strictly more. Scan query selects `c.text`, `d.path`, `d.title` for *every*
chunk; every row passing `MinScore` (default `0` — almost all cosine scores
clear it) appended to `candidates` — peak per-query memory ≈ entire corpus
text + all decoded embeddings held simultaneously, then all but K thrown
away. Final ranking = `sort.Slice` over all n (O(n log n)) when only top K
needed. `Hybrid` runs this with `TopK*2`; `ask` runs per question. At
documented ~100k-chunk comfort zone, ~1–3 KB chunks = hundreds of MB
transient allocations per query — GC churn + latency the design doesn't ask
for.

**Evidence:** `internal/search/vector.go:19-24` (SELECT includes
text/path/title for all rows), `vector.go:34-52` (unbounded `candidates`
append), `vector.go:57-59` (full sort).

**Fix:** Two-phase within same scan design: first pass selects only
`id, embedding`, bounded min-heap of top K (O(n log k) time, O(k) memory, one
blob live at a time); second query hydrates text/path/title for just those K
chunk IDs. No `Searcher` API change; later sqlite-vec swap gets cheaper.
Benchmark before/after with synthetic 50–100k-chunk DB (`go test -bench`).

### 37. Bulk ingest fully serial — embedding round-trip latency = wall clock

**Problem:** `IngestDir` processes one file at a time; within file, embed
loop = one blocking `Embed` call per 16-chunk batch. Ollama adapter splits
each into sequential batches of 8 (`ollamaBatchSize`) — every ingester batch
= *two* serial HTTP round-trips. Nothing overlaps network wait with
extraction/chunking/storage; never two embedding requests in flight. For
primary bulk op — first-time `tbuk ingest <dir>` against network-backed
embedder — wall clock ≈ (batches) × (RTT), provider + machine idle in
alternation. ~600 batches at 300 ms RTT = ~3 min pure sequential waiting;
modest concurrency → tens of seconds.

**Evidence:** `internal/ingest/ingester.go:226-240` (serial per-file walk),
`ingester.go:116-138` (serial batch loop, `embedBatchSize = 16` at line 81),
`internal/embeddings/ollama.go:13,33-47` (sequential split into 8s).

**Fix:** Bounded concurrency at one level — simplest: small worker pool (2–4
workers, semaphore-bounded) over embed batches within file, per-file DB
writes stay serial so `ReplaceForDocument` atomicity untouched; concurrency
across files = alternative if per-file chunk counts typically small. Align or
make configurable ingester/adapter batch sizes so one batch = one request.
Keep limit low + configurable — must compose with 429/5xx retry work
(issue 33), not amplify rate-limit pressure. Local providers (ollama/llama)
benefit too when serving parallel requests.

## Noted, no action needed (performance review)

- **Hybrid runs vector + keyword legs sequentially:** vector leg dominated by
  query-embedding network call; parallelising saves single-digit ms. Not
  worth goroutines.
- **No prepared-statement reuse:** each CLI invocation runs handful of
  queries once; per-call prepare overhead = noise.
- **`stats` aggregates in single grouped query** — already efficient shape;
  no N+1 anywhere in CLI.
- **FTS5 external-content index never gets `optimize`:** segment buildup from
  re-ingests negligible at personal-corpus scale; revisit only if keyword
  latency degrades.
- **Redundant second SHA256 on auto-preprocess path** (`IngestFile` hashes,
  `preprocess.Extract` hashes again): one extra sequential file read per
  *new* file, dwarfed by embedding calls. Not worth code.

---

# Additions — developer experience / new-contributor assessment (2026-07-19)

Walk-through as first-time contributor: clone, read docs, install tooling,
build, test, commit. Much DX good — `make` self-documenting with `help`
default goal, `go build ./...` + full test suite work first try, zero setup
(~14 s, all green), user guide excellent, README covers
install/quick-start/releasing. Below = remaining gaps; issues 10 (stale
`serve` target), 12 (no committed golangci-lint config), 34 (local/CI
coverage-gate drift) cover other DX friction from same walk-through, not
repeated. Numbering continues.

## Medium priority

### 38. README golangci-lint install = v1 while CI runs v2 — contributors lint with different major version

**Problem:** README from-source prerequisites say "golangci-lint v2" but give
`go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`.
That module path = **v1** — latest v1.64.8 — while v2 lives at `/v2` path. CI
pins v2.5.0 (`quality-check.yml`). Contributor following README gets v1.64.8;
`make lint` / `make check` / `make check-ci` run different major than CI —
different default linters, incompatible config format — green locally can
fail on PR and vice versa. (golangci-lint project also explicitly doesn't
support `go install` as install method.) Compounds issue 12: no committed
`.golangci.yml` *and* unpinned wrong-major local install — local lint and CI
lint share almost nothing.

**Evidence:** `README.md:42` (v1 module path, `@latest`);
`.github/workflows/quality-check.yml:23-25` (action pins `v2.5.0`); verified
`go list -m -versions`: `github.com/golangci/golangci-lint` tops out v1.64.8,
v2 releases under `github.com/golangci/golangci-lint/v2`.

**Fix:** Replace README command with supported install (binary install script
or `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0`),
pinned to CI version; note golangci-lint needed only for
`make lint`/`check`/`check-ci`, not build/test. Best with issue 12 so version
stated in exactly one place.

### 39. Commit-time gates (pre-commit + commitizen) enforced but undocumented — first commit fails on missing tools

**Problem:** AGENTS.md declares "Pre-commit enabled"; `.pre-commit-config.yaml`
wires commit-msg hook running `cz check` (commitizen, Python tool,
`language: system` — must be on PATH) + Go hooks needing `goimports`. None
documented for humans: README Development section never mentions pre-commit,
no CONTRIBUTING.md, AGENTS.md Environment says "No virtualenv. Standard Go
toolchain" — actively misleading about Python tooling hooks require. README
presents Conventional Commits as optional nicety for release notes when
commit-msg hook makes them mandatory. New contributor either hits
`cz: command not found` / hook failures on first commit, or — more likely —
never runs `pre-commit install`, commits unchecked, discovers conventions via
CI or review.

**Evidence:** `.pre-commit-config.yaml:40-48` (commitizen commit-msg hook,
`language: system`), `:53-57` (go-imports, golangci-lint hooks); `AGENTS.md`
Environment + Enforcement sections; `README.md` Development section (no
mention); no `CONTRIBUTING.md`.

**Fix:** Short "Contributing" section in README (or CONTRIBUTING.md linked
from it) listing one-time setup — `pipx install pre-commit commitizen` (or
equivalent), `pre-commit install --install-hooks`,
`go install .../goimports` — stating plainly commit subjects must pass
`cz check`. Correct AGENTS.md Environment section re Python hook tooling.
Alternatively, if commitizen gate unwanted, drop hook rather than leave
half-enforced.

## Low priority

### 40. Contributor docs disagree on Go version; still advertise nonexistent `metadata` package

Two small drifts, each costs new contributor double-take: AGENTS.md tooling
says "go (1.24+)" while README requires "Go 1.25+" and `go.mod` pins
`go 1.25.0` (1.24 wrong — toolchain auto-download masks, only for
contributors with auto-download enabled); README Architecture lists
`internal/metadata/  stub (not yet active)` — same stale entry issue 11 flags
in `docs/initial-context.md:26` — package not exist. Fix: state Go
requirement in one place (README), AGENTS.md defers to it; when fixing
issue 11, remove stale line from `README.md:206` same pass.

## Noted, no action needed (DX review)

- **`make` UX**: default goal `help`, every target documented, README embeds
  output. Model self-documenting Makefile.
- **Zero-setup build/test**: fresh clone → `go build ./...` →
  `go test ./...` all green ~14 s, no services/env vars/fixtures. In-memory
  SQLite + `httptest` mocks keep it that way — preserve this.
- **`make test`'s `-v -count=1`** = long output, defeats test cache, but CI
  same and suite fast; not worth churn.
- **No issue/feature templates** under `.github/ISSUE_TEMPLATE`: fine for
  single-maintainer project at this stage.

---

# Additions — user impact / product assessment (2026-07-19)

Product-engineering pass over CLI + docs from real user's view: first-run
experience, everyday workflows, docs promising behaviour tool not deliver.
Overlaps from same pass not repeated — broken `template edit` = issue 22,
`meta set`/`meta list` path-normalization = issue 16, hardcoded prompts dir =
issue 7 (note: also affects `internal/cli/template.go:111-114`, not just
`ask`). Roadmap items in `docs/plans/next-steps.md` excluded. Numbering
continues.

## Medium priority

### 41. `tbuk delete` confirmation hangs on plain Enter — advertised `[y/N]` default unreachable

**Problem:** Prompt = `Delete <path> (N chunks)? [y/N]` — convention: plain
Enter = No. But reply read with `fmt.Fscan(os.Stdin, &answer)`; `Fscan` skips
newlines waiting for token — Enter does nothing, command looks frozen until
user types non-whitespace. Every interactive delete hits this.

**Evidence:** `internal/cli/delete.go:49-54`.

**Fix:** Read full line (`bufio.NewReader(os.Stdin).ReadString('\n')`), trim,
empty line = default No.

### 42. No way to list documents in knowledge base

**Problem:** No `tbuk list` (or `tbuk docs`). `tbuk stats` = counts only;
`tbuk find` requires metadata filter — "show me everything indexed" has no
answer; user guide "Checking Your Knowledge Base" can only offer stats. Users
need constantly: verify ingest worked, spot renamed/stale files, decide what
to delete. (`tbuk find extension=md` = closest workaround, per-extension
only.)

**Evidence:** `internal/cli/root.go:47-59` (command roster);
`docs/user-guide.md` §8.

**Fix:** Add `tbuk list [--format text|json]` reading `documents` table
(path, title, chunk count, updated_at) with usual `--limit`. Low effort —
repos + text/JSON printing patterns exist. Complements richer-doctor roadmap
item, not duplicates (doctor diagnoses; this = everyday query).

### 43. Single-file `tbuk ingest` succeeds silent — user guide shows output that not exist

**Problem:** Single file: `PrintFileResult` prints nothing on success; dedup
skip prints nothing without `--verbose`. Guide's first ingest walkthrough
promises `Ingesting … chunks: 1 … Done.`, says repeat run prints
`Skipped … (unchanged)` — real command returns zero output all three cases
(ingested, skipped, `--verbose` absent). First-time user can't tell success
from no-op; guide-vs-reality gap kills trust at most sensitive funnel point
(first ingest). Directory ingest already prints per-file progress + summary;
single-file = inconsistent case.

**Evidence:** `internal/cli/ingest.go:84-93`; `docs/user-guide.md` §6
(promised output blocks).

**Fix:** Print one-line result unconditionally for single-file ingest
(`<path> → N chunks embedded` / `<path> → skipped (unchanged)`), align guide
sample output with actual.

### 44. Guide's `--min-score` advice returns zero results in default search mode

**Problem:** `docs/user-guide.md` §10: "Scores range from 0 (unrelated) to 1
(identical). A threshold of 0.6–0.7 is a reasonable starting point" —
directly under examples using **default hybrid mode**. Hybrid scores = RRF
sums (max ≈ 2/61 ≈ 0.033, k=60, two lists), so
`tbuk search --top 10 --min-score 0.7 "project deadlines"` — guide's own
example — filters every result, prints "No results found.", no hint why.
README documents scale difference; guide, aimed at non-experts, actively
misleads. Also poisons guide's own debugging advice ("run `tbuk search` to
inspect retrieval").

**Evidence:** `docs/user-guide.md` §10 ("Controlling result count and minimum
score"); `internal/search/hybrid.go` (RRF k=60); `README.md:57` (documents
differing scale).

**Fix:** Fix guide first (scope 0–1 advice to `--mode vector`; hybrid-scale
example or omit `--min-score` from hybrid examples). Then make flag usable
without reading internals: normalize hybrid scores to 0–1 before `MinScore`
applied, or warn when `--min-score` exceeds achievable hybrid max for given
`--top`.

### 45. `tbuk ask` silently answers from model priors when retrieval returns nothing

**Problem:** Retrieval yields zero chunks — empty knowledge base (default
first-run state) or no matches — `RunAsk` renders template with no context,
streams whatever LLM says. Guide promises "the model cannot invent facts that
are not in your documents — its answer is grounded in what you have written";
empty context = pure model prior, identical confidence, only clue = absent
`Sources:` section. Exactly the user who most needs signal (forgot to ingest,
or query missed).

**Evidence:** `internal/cli/ask.go:150-176` (no empty-chunks branch);
`internal/cli/ask.go:207-212` (`Sources:` only when chunks exist);
`docs/user-guide.md` §9.

**Fix:** On zero retrieved chunks, clear warning to stderr before (or instead
of) LLM call — e.g. "no relevant chunks found in your knowledge base; the
answer below is NOT grounded in your documents. Check `tbuk stats` or try
`tbuk search '<query>'`" — consider `--require-context` flag (or
template-manifest option) that aborts instead. Keep qa template's "say so
clearly" instruction as backstop, not only defence.

## Low priority

### 46. `tbuk update` fails obscure on directories

`ingest` accepts file or directory; `update` calls `IngestFile`
unconditionally — natural `tbuk update ~/notes/` fails with low-level
extraction error instead of working or explaining. Either support directories
(delegate to `IngestDir` loop — `update` is already "ingest unless
unchanged") or stat path, return "update takes a single file; use
`tbuk ingest <dir>` for folders". Evidence: `internal/cli/update.go:60-77` vs
`internal/cli/ingest.go:65-70`.

## Noted, no action needed (product review)

- **`tbuk preprocess` single-file only**: acceptable — `ingest` handles
  directories, auto-preprocesses; two-step flow exists for inspecting
  individual extractions.
- **Guide §6 says preprocess "splits it into chunks"**: chunking actually
  happens at ingest; one-word docs fix when §6 touched for issue 43, not
  separate issue.
- **`stats` guide output matches implementation** (modulo issue 43
  neighbouring sections). Fine.

---

# Additions — project health / maintainer assessment (2026-07-19)

OSS-maintainer pass: licensing, releases, install funnel, disclosure policy,
backlog hygiene, community files. Much healthy — MIT license committed,
release automation works end to end (v0.1.0 + v0.1.1 published with grouped
changelog, version stamped via ldflags, `tbuk version` wired), plan-archive
discipline followed (20 archived plans, naming convention respected), PR
template with TDD checklist in place, CI runs on PRs. Release-pipeline gaps =
issues 28/29/31, contributor-setup gaps = issues 38/39/40, not repeated.
Numbering continues.

## Medium priority

### 47. README recommended install downloads a 404 — asset name has no `v` prefix

**Problem:** README "Pre-built binary (recommended)" example sets
`VERSION=v0.1.0` and builds
`tbuk_${VERSION}_${OS}_${ARCH}.tar.gz` → `tbuk_v0.1.0_linux_amd64.tar.gz`.
GoReleaser `{{ .Version }}` strips the leading `v`: real assets are
`tbuk_0.1.1_linux_amd64.tar.gz` (verified against published v0.1.1 release
assets). Copy-pasting the recommended install = 404 for every user on every
release — same trust-killing funnel class as issue 43, but earlier: before
first run. Minor sibling: README says Windows = `_windows_amd64.zip` only;
`_windows_arm64.zip` also published.

**Evidence:** `README.md:22-30` (`VERSION=v0.1.0` used in asset filename);
`.goreleaser.yml:27-29` (`name_template` uses `{{ .Version }}`, no `v`);
live v0.1.1 asset list (`tbuk_0.1.1_*`).

**Fix:** Use `${VERSION#v}` in the filename (keep `v` in the
`/releases/download/${VERSION}/` path segment, which *does* need it), or set
tag and version as separate vars. Mention arm64 Windows asset. Ideally verify
the snippet once against a real release after edit.

## Low priority

### 48. No SECURITY.md / vulnerability disclosure channel

Issues 28/29 cover *detecting* vulns (govulncheck) and *signing* releases;
nothing tells an outside reporter where to send one. Project parses untrusted
input (PDFs, issue 26) and ships binaries — the case where private disclosure
matters. GitHub private vulnerability reporting works best advertised via
`SECURITY.md` (supported versions + "use GitHub advisories / email"). Few
lines, standard template. Evidence: no `SECURITY.md` in repo root or
`.github/`.

### 49. Findings backlog has no lifecycle — nothing marks issues resolved

This file now holds 49 findings from ten assessment passes; GitHub Issues
never used (0 issues total, tracker idle). No status marker per finding — as
fixes land in PRs, nothing records which findings are done; file drifts
stale, and every future assessment pass re-spends effort manually
dedup-checking against all of it. Also `01-issues.original.md` (pre-compress
backup) sits in `docs/plans/` — convention says active plans only. Fix:
triage high/medium findings into GitHub issues (free, closable by PR
keywords) keeping this file as source index, or add per-finding status
markers and archive resolved sections per the existing plan-archive
convention; move the `.original.md` backup to `docs/archive/` or delete it.

## Noted, no action needed (maintainer review)

- **License:** MIT, committed, copyright line present. Fine.
- **Release cadence/automation:** two releases first two days, changelog
  grouped by type, conventional commits feeding it. Healthy.
- **CODE_OF_CONDUCT / issue templates:** single-maintainer stage; already
  noted under DX review. Skip until outside contributors appear.
- **Docs volume:** README + user guide + initial-context + roadmap all
  current-ish (drift tracked as issues 11/40/43/44). Unusually good for
  project age.

---

## Engineering Sustainability Assessment — 2026-07-19 (Technical Debt Auditor)

### ES-1: CI coverage gate weaker than local `make check-ci` (per-package rule unenforced)

- **Where:** `.github/workflows/quality-check.yml` (coverage job) vs `Makefile` `check-ci`
- **Problem:** AGENTS.md and `make check-ci` require ≥85% coverage *per package* (and fail on
  packages with no test files). The CI coverage job only checks the *total* (currently 86.7%).
  A PR can drop a single package well below 85% (or add an untested package), pass CI, and merge —
  then every developer's local `make check-ci` fails on an inherited problem.
- **Fix:** Make the CI coverage step run the same per-package check as the Makefile — simplest is
  to have CI invoke `make check-ci` (or extract the awk gate into a script both call), so local
  and CI gates cannot drift again.
- **Effort:** Small.

### ES-2: Race detector never runs in CI

- **Where:** `.github/workflows/ci.yml`; `internal/llm/claude.go:113`, `ollama.go:79`, `openai.go:126`
- **Problem:** The LLM streaming adapters spawn goroutines, but `go test -race` exists only as a
  local `make test-race` target nobody is forced to run. Data races in the streaming path would
  ship undetected.
- **Fix:** Run tests with `-race` in the CI test step (suite takes ~3s, race overhead is trivial),
  or add a dedicated race job.
- **Effort:** Trivial.

### ES-3: Lint config not committed + README installs the wrong golangci-lint major version

- **Where:** repo root (no `.golangci.yml`); `README.md` "From source" section;
  `.github/workflows/quality-check.yml`
- **Problem:** Two related drifts:
  1. No `.golangci.yml` is committed, so lint behavior is whatever the tool's defaults are for the
     version that happens to run — it can change silently when CI bumps the pinned version.
  2. README tells contributors to `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`,
     which installs **v1** (the v2 module path is `github.com/golangci/golangci-lint/v2/cmd/golangci-lint`).
     CI pins v2.5.0. Local lint and CI lint therefore run different major versions with
     incompatible configs and different default linters — "passes locally, fails in CI" (or worse,
     the reverse).
- **Fix:** Commit a minimal `.golangci.yml` (`version: "2"` plus the intended linter set); fix the
  README install command to the `/v2/` path pinned to the same version CI uses.
- **Effort:** Small.

### ES-4: No automated dependency updates

- **Where:** `.github/` (no `dependabot.yml`, no renovate config)
- **Problem:** Five direct deps (cobra, yaml.v3, modernc sqlite, x/net, ledongthuc/pdf) plus four
  GitHub Actions go stale silently. `x/net` in particular carries regular security fixes, and the
  pdf lib is a pseudo-versioned snapshot. For a low-touch personal project, unpatched deps are the
  main way the repo rots between active periods.
- **Fix:** Add `.github/dependabot.yml` with `gomod` + `github-actions` ecosystems, weekly or
  monthly interval. CI (tidy-diff, tests, lint) already gates the update PRs.
- **Effort:** Trivial.

### ES-5: Architecture docs describe a package that doesn't exist

- **Where:** `README.md` (Architecture section) and `docs/initial-context.md` both list
  `internal/metadata/ — stub (not yet active)`
- **Problem:** There is no `internal/metadata/` directory. Metadata functionality actually lives in
  `internal/storage/metadata.go` (repo) and `internal/search/metadata.go` (search). AGENTS.md
  designates `docs/initial-context.md` as the architecture source of truth agents read first —
  a phantom package sends every new session/contributor looking for code that isn't there.
- **Fix:** Delete the `metadata/` line from both files (or note where metadata actually lives).
- **Effort:** Trivial.

### ES-6: Dead `make serve` target from another project

- **Where:** `Makefile` (`serve: cd output && python3 -m http.server 8080`)
- **Problem:** References an `output/` directory that doesn't exist and "local feed testing" that
  has no meaning in this codebase — copy-paste leftover. It appears in `make help` as if it were a
  real workflow, fails when run, and adds a python3 implication to a pure-Go project.
- **Fix:** Delete the target (and its `.PHONY` entry).
- **Effort:** Trivial.

### ES-7: Contributor toolchain undocumented (pre-commit + commitizen)

- **Where:** `.pre-commit-config.yaml`; `README.md` Development section
- **Problem:** AGENTS.md states "Pre-commit enabled" and the commit-msg hook runs `cz check`
  (commitizen, a Python tool, `language: system` — must be preinstalled). Neither README nor any
  CONTRIBUTING.md mentions installing `pre-commit` or `commitizen`, and no commitizen config is
  committed. A fresh clone either silently skips the hooks (never ran `pre-commit install`) or
  fails commits with a missing-`cz` error. Conventional-commit discipline also feeds the
  GoReleaser changelog grouping, so unenforced commits degrade release notes.
- **Fix:** Add a short "Contributing / setup" subsection to README: `pip install pre-commit
  commitizen && pre-commit install --install-hooks`. Optionally commit a `.cz.toml` to pin the
  convention explicitly.
- **Effort:** Small.

### ES-8: Go version stated inconsistently across docs

- **Where:** `go.mod` (`go 1.25.0`), `README.md` ("Go 1.25+"), `AGENTS.md` ("go (1.24+)")
- **Problem:** AGENTS.md promises Go 1.24+ works, but `go.mod` demands 1.25.0 — a 1.24 toolchain
  will refuse to build (or auto-download 1.25, surprising offline/CI environments). Minor, but
  AGENTS.md is the instructions file agents follow verbatim.
- **Fix:** Update AGENTS.md to `go (1.25+)`.
- **Effort:** Trivial.

### ES-9: Test suite runs twice on every push/PR

- **Where:** `.github/workflows/ci.yml` (Test step, `-v`) and
  `.github/workflows/quality-check.yml` (coverage job)
- **Problem:** Both workflows trigger on the same events and both run the full test suite; ci.yml
  additionally uses `-v`, which buries failures in per-test noise. Low cost today (fast suite) but
  it's structural duplication that grows with the suite, and two workflows must now be kept in
  sync with any test-invocation change (see ES-1 for the same drift pattern).
- **Fix:** Drop the Test step from ci.yml (coverage job already runs everything with `-count=1`),
  or merge the two workflows; drop `-v` either way.
- **Effort:** Small. Priority: low.

## Ecosystem Health Assessment (Dependency Manager, 2026-07-19)

### ECO-1: No automated dependency updates (Dependabot/Renovate absent)
- **What**: `.github/` has no `dependabot.yml` or Renovate config. Direct deps are currently fresh (cobra v1.10.2, x/net v0.57.0, modernc.org/sqlite v1.54.0), but nothing keeps them that way; indirect updates are already pending (`spf13/pflag v1.0.10`, `modernc.org/libc v1.74.3`).
- **Why it matters**: security patches in x/net and sqlite land silently; without automation the module drifts stale between manual audits. GitHub Actions (`actions/checkout@v4`, `setup-go@v5`, `golangci-lint-action@v7`, `goreleaser-action@v6`) also get no update PRs.
- **Fix**: add `.github/dependabot.yml` with `gomod` (weekly) and `github-actions` (weekly) ecosystems. Optionally group indirect `modernc.org/*` bumps to cut PR noise.

### ECO-2: No vulnerability scanning in CI
- **What**: neither `ci.yml` nor `quality-check.yml` runs `govulncheck`. (Verified locally too: govulncheck cannot run in this sandbox — vuln DB fetch blocked — so no scan has been recorded anywhere.)
- **Why it matters**: the module ships an HTTP-adjacent stack (`golang.org/x/net`) and a C-transpiled SQLite (`modernc.org/sqlite`); both have had CVEs historically. Symbol-level `govulncheck` is cheap and low-noise.
- **Fix**: add a CI step `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` (or `golang/govulncheck-action`) to quality-check.yml.

### ECO-3: golangci-lint runs with no config; local vs CI version drift
- **What**: no `.golangci.yml` in repo. CI pins golangci-lint v2.5.0 via `golangci-lint-action@v7`; `make lint` and the pre-commit `golangci-lint` hook run whatever version is on PATH with default linters.
- **Why it matters**: default linter set changes across golangci-lint releases, so the CI gate can shift silently on action bumps, and local `make check-ci` can pass while CI fails (or vice versa).
- **Fix**: commit a minimal `.golangci.yml` (version: "2", explicit linter list) and pin the same golangci-lint version expectation in the Makefile comment or a tools file.

### ECO-4: `ledongthuc/pdf` is an unreleased, panic-prone dependency on the untrusted-input path
- **What**: `internal/preprocess/pdf.go` parses arbitrary PDFs with `github.com/ledongthuc/pdf`, pinned to a pseudo-version (`v0.0.0-20250511090121`) — the project has no tagged releases and minimal maintenance. The library panics (rather than returning errors) on many malformed inputs, and `pdfExtractor.Extract` has no `recover`.
- **Why it matters**: one corrupt/hostile PDF crashes the whole `tbuk` run instead of failing that single document.
- **Fix**: wrap `Extract` in `defer func() { if p := recover(); p != nil { err = fmt.Errorf("pdf: panic: %v", p) } }()` (test with a malformed fixture). Longer term, evaluate a maintained alternative (e.g. `pdfcpu`) if PDF coverage grows.

### ECO-5: Release builds use unpinned goreleaser (`version: latest`)
- **What**: `release.yml` runs `goreleaser/goreleaser-action@v6` with `version: latest`.
- **Why it matters**: a goreleaser major bump can change archive layout/changelog behavior or break the v2 config exactly at release time — the worst moment to discover it.
- **Fix**: pin `version: '~> v2'` so releases stay on the tested major.

### Healthy — no action needed
- Direct dependencies all current as of 2026-07-19; dep tree is small (5 direct, ~15 indirect).
- `go mod tidy` drift is CI-enforced (`git diff --exit-code go.mod go.sum`).
- Go toolchain pinned via `go-version-file: go.mod` (1.25.0) consistently across all three workflows.
- CGO disabled + pure-Go SQLite keeps cross-compile matrix (6 targets) dependency-free.

## Agent-Readiness Assessment (2026-07-19)

Findings from evaluating how well this repo supports AI coding agents. Baseline is good: `make check-ci` passes clean in a fresh remote container (lint 0 issues, total coverage 86.8%, every package >= 85%), AGENTS.md exists and is loaded via CLAUDE.md, and hooks enforce workflow. Issues below are the gaps.

### AR-1: AGENTS.md mandates `gh` CLI, but remote agent sessions have no `gh`

AGENTS.md says "Use `gh` for all GitHub operations" and the branch workflow requires `gh pr list --state open` before creating a branch. Remote Claude Code sessions (web/mobile) have no `gh` binary — GitHub access goes through MCP tools. An agent following AGENTS.md literally wastes turns on failing `gh` calls or concludes it cannot check PRs.

Fix: reword to "use `gh` when available; in environments without it (e.g. remote agent sessions), use the GitHub MCP tools instead."

### AR-2: TDD hook blocks all edits to `cmd/tbuk/main.go`

`.claude/hooks/check-tdd.sh` blocks Write/Edit of any non-test `.go` file when the directory has no `*_test.go`. `cmd/tbuk/` contains only `main.go` and no test file (by design — thin main, excluded from coverage). Result: an agent cannot make even a trivial edit to `main.go` without first creating a throwaway `cmd/tbuk/*_test.go`, which contradicts "minimal diffs".

Fix: allowlist `cmd/` (or files named `main.go`) in the hook, or add a minimal smoke test to `cmd/tbuk/`.

### AR-3: Session-start "ask which branch" hook stalls autonomous sessions

The SessionStart hook demands the agent ask the user for a branch and wait before any work. In autonomous/remote runs the user is not watching, and task prompts often already name the branch. The instruction as written forces either a stalled session or a rule violation.

Fix: amend hook text to "if the user's prompt already specifies a branch, use it without asking; otherwise ask."

### AR-4: README's golangci-lint install command installs v1, not v2

README says "requires `golangci-lint` v2" but the given command is `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`, which resolves the pre-v2 module path and installs v1.x. v2 lives at `github.com/golangci/golangci-lint/v2/cmd/golangci-lint`. An agent (or human) bootstrapping a machine from the README gets a linter whose results diverge from CI (which pins v2.5.0).

Fix: correct the module path and pin the version to match CI (`@v2.5.0`).

### AR-5: No `.golangci.yml`; linter version pinned only inside the CI workflow

Lint runs with implicit defaults, and the only version pin (v2.5.0) is buried in `.github/workflows/quality-check.yml`. Local `make lint` uses whatever version happens to be installed, so local-vs-CI lint drift is possible and silent.

Fix: commit a minimal `.golangci.yml` (even just documenting the default set) and state the required linter version in README/AGENTS.md, or manage the linter via a pinned Go tool dependency.

### AR-6: Commit-message convention undocumented in AGENTS.md and unenforced in CI

The repo actually uses Conventional Commits (`feat:`, `fix:`, `docs:` throughout `git log`) and pre-commit runs `cz check` on commit messages. But AGENTS.md's Commits section only says "imperative present tense, <= 72 chars" — no mention of the conventional prefix. Additionally, `cz`/`pre-commit` are not installed in remote agent containers and CI has no commit-message check, so agent commits bypass the convention entirely.

Fix: document Conventional Commits explicitly in AGENTS.md; optionally add a commit-lint step to CI so enforcement doesn't depend on locally installed hooks.

### AR-7: CI does not enforce per-package coverage, but AGENTS.md and `make check-ci` do

AGENTS.md requires >= 85% per package and `make check-ci` fails on any package below it, but the quality-check workflow only checks the total. A change that drops one package below 85% while the total stays above passes CI and only fails for whoever runs `make check-ci` locally — the gates disagree.

Fix: add the per-package check (same awk logic as the Makefile) to the coverage job, or have CI simply run `make check-ci`.

### AR-8: Stale `make serve` target references nonexistent `output/` directory

`serve` does `cd output && python3 -m http.server 8080` and its help text mentions "local feed testing" — there is no `output/` dir and no feed anywhere in this project; the target looks copy-pasted from another repo. Self-documenting `make help` is a primary agent affordance here, so a dead target actively misleads.

Fix: delete the target.

### AR-9: Broken reference and version drift in AGENTS.md

Two small doc bugs that misdirect agents:
- AGENTS.md points to `.claude/skills/tdd.md`; the actual path is `.claude/skills/tdd/SKILL.md`.
- AGENTS.md says Go "1.24+" while `go.mod` requires 1.25.0 and README says 1.25+.

Fix: correct the path and version.

### AR-10: `make test` runs verbose, flooding agent context

`test` runs `go test ./... -v -count=1`; `-v` prints every test name across the whole suite. For agents (and CI logs) this is thousands of lines of noise per run for no diagnostic gain — failures print regardless. CI's Test step also uses `-v`.

Fix: drop `-v` from the default target; keep a `test-verbose` target for when it's wanted.

### AR-11: TDD enforcement weaker than AGENTS.md claims

AGENTS.md states "MANDATORY TDD — no exceptions. A PreToolUse hook enforces this," but the hook only matches Write/Edit tools and only checks that *some* `_test.go` exists in the directory. File writes via Bash (heredoc, `cat >`, `sed -i`) bypass it, and adding one trivial test file unlocks unlimited implementation files in that package. Not fixable in full, but the doc overstates the guarantee.

Fix: note in AGENTS.md that the hook is a guardrail, not proof of TDD; optionally extend the matcher to Bash write patterns.
