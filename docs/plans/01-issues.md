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

---

# Additions — long-term evolution assessment (2026-07-19)

Findings from an architecture review focused on how the repo will evolve —
schema growth, config growth, and the concurrency surface the roadmap
(`docs/plans/next-steps.md`) will add. Nothing here is broken *today*; each
item is a latent cost that gets paid the first time the relevant area changes.
Numbering continues from the reviews above.

## Medium priority

### 23. Migration runner is not ready for a second migration

**Problem:** The whole schema lives in one migration whose statements are all
`IF NOT EXISTS`, which masks three weaknesses in `RunMigrations` that will bite
as soon as migration v2 lands (the roadmap's embedding-provenance and
conversational-session work both imply schema changes):

1. The applied-version check swallows its error
   (`_ = db.QueryRow(...).Scan(&exists)`), so any transient failure reads as
   "not applied" and the migration is re-run. Harmless for idempotent
   `IF NOT EXISTS` DDL; a future `ALTER TABLE ... ADD COLUMN` re-applied this
   way fails hard or corrupts.
2. A migration's SQL and its `schema_migrations` record are executed as two
   separate `Exec` calls, not one transaction. A crash between them leaves the
   schema changed but unrecorded — the next run re-applies it (same hazard as
   above).
3. There is no guard for a database *newer* than the binary. An older `tbuk`
   opening a DB whose `schema_migrations` max version exceeds what it knows
   silently reads/writes a schema it doesn't understand. For a local-first
   tool where users up/downgrade standalone binaries against one long-lived
   `~/.tbuk/tbuk.sqlite`, this is the realistic failure mode.

**Evidence:** `internal/storage/migrate.go:79-94` (swallowed Scan at 81,
two-step apply at 85-93); no max-version check anywhere in `storage`.

**Fix:** Before the second migration is ever written: wrap each migration's
SQL + version record in one transaction; propagate the version-check error;
error out (with a clear "this database was created by a newer tbuk" message)
when the recorded max version exceeds the binary's. Optionally copy the DB
file aside before applying pending migrations — cheap insurance for personal
data with no other backup story until export/import ships.

### 24. Unknown config keys are silently ignored

**Problem:** `Load` uses plain `yaml.Unmarshal`, which drops unknown fields.
A typo (`chunk_size:` for `size:`, `baseurl:` for `base_url:`) or a key from a
newer tbuk version is silently ignored and the default silently wins — the
config *looks* applied but isn't. Issue 6 covers validating *values*; this is
the complementary gap: detecting keys that don't exist at all. It grows worse
as the config schema grows (collections, `prompts.dir`, retrieval settings are
all on the roadmap).

**Evidence:** `internal/config/config.go:100` (`yaml.Unmarshal(data, &cfg)`).

**Fix:** Decode via `yaml.Decoder` with `KnownFields(true)` and fail with the
offending key name. Fold into the same `Config.Validate()` effort as issue 6
so all config errors surface together at startup.

## Low priority

### 25. CI never runs the race detector

`make test-race` exists but no workflow invokes it — CI runs plain `go test`
(`.github/workflows/ci.yml`). The `llm` package is goroutine + channel heavy
(streaming adapters, `sendToken`/`ctx.Done()` selects), and the roadmap's
multi-turn `ask` adds more concurrency. Races regress silently until a user
hits them. Add `-race` to the CI test step (or a parallel job if runtime
matters).

---

# Additions — security & attack surface assessment (2026-07-19)

Findings from a security review of the repo's attack surface: untrusted
inputs (ingested documents), secrets handling, network calls, the SQLite
layer, and the release/CI pipeline. The core is in good shape — SQL is
parameterised throughout, FTS5 queries are sanitised
(`internal/search/keyword.go:49-56`), API keys come only from environment
variables and never touch config or logs, no external commands are executed,
and `~/.tbuk` content is created owner-only (dirs `0700`, config/DB/extracted
files `0600`, including a `chmod` on open in `internal/storage/db.go:37-43`).
The issues below are the remaining gaps. Numbering continues from above.

## Medium priority

### 26. A malformed PDF can panic and kill an entire ingest run

**Problem:** The primary untrusted-input surface is document ingestion — PDFs
are exactly the kind of file users download from elsewhere. PDF extraction
delegates to `github.com/ledongthuc/pdf`, a parser with known
index-out-of-range / nil-dereference panics on malformed or hostile files
(see its upstream issue tracker), and nothing recovers: a panic anywhere in
`Extract` unwinds past `IngestDir`'s per-file error handling and crashes the
whole `tbuk ingest` run, instead of recording one error `Result` and moving
on. The extractor also `io.ReadAll`s the entire file with no size cap
(`pdf.go:17`, and `plaintext.go`/`html.go` read unbounded too), so a
multi-GB stray file in an ingest directory exhausts memory before chunking
starts.

**Evidence:** `internal/preprocess/pdf.go:15-42` (no recover, unbounded
read); `internal/ingest/ingester.go:226-240` (per-file loop has no panic
isolation).

**Fix:** Wrap the extractor call (`Extractor.Extract` or
`preprocess.Extract`) in a `defer`/`recover` that converts a panic into an
error `Result` for that file. Add a configurable max-file-size guard (stat
before read; a generous default like 100 MB) so one oversized file can't OOM
the run. Both fold naturally into the error-surfacing work in issue 4.

### 27. API keys are attached to any configured `base_url`, including plain HTTP

**Problem:** The claude/openai adapters set `x-api-key` /
`Authorization: Bearer` on requests to whatever `base_url` the config
supplies, with no scheme or host check. Combined with issue 17 (defaults
hardcode `base_url: http://localhost:8080`, so switching `provider: llama` →
`provider: claude` without editing `base_url` is a realistic slip), the
failure mode is concrete: the user's real `ANTHROPIC_API_KEY` is sent in
cleartext to whatever answers on localhost:8080 — or, if they point
`base_url` at a remote `http://` host, across the network unencrypted.

**Evidence:** `internal/llm/claude.go:90-96`, `internal/llm/openai.go`,
`internal/embeddings/openai.go:50-58` — headers set unconditionally;
`internal/config/config.go:69,75` — non-empty default `base_url` makes the
misdirect reachable.

**Fix:** In the cloud-provider factories (claude, openai), reject — or at
minimum loudly warn on — a `base_url` that is non-HTTPS and not a loopback
address when an API key will be attached. Do this alongside the issue 17 fix
(empty default `base_url` resolved per provider), which removes the most
likely way to hit it.

### 28. No dependency vulnerability scanning, and the release toolchain is unpinned

**Problem:** The project ships standalone binaries but nothing watches its
dependency tree: no `govulncheck` in CI, no Dependabot/Renovate config, so a
CVE in `golang.org/x/net`, `modernc.org/sqlite`, or the PDF parser (the one
dependency that parses untrusted input, see issue 26) goes unnoticed until a
user is bitten. Meanwhile the release workflow runs
`goreleaser/goreleaser-action@v6` with `version: latest`, so every tagged
release builds with whatever GoReleaser happens to be current that day —
unreproducible, and exposed to a bad or compromised upstream release at the
worst moment (publish time).

**Evidence:** `.github/workflows/ci.yml`, `quality-check.yml` (no vuln
scanning); `.github/workflows/release.yml:24-27` (`version: latest`); no
`.github/dependabot.yml`.

**Fix:** Add a `govulncheck ./...` step to CI (it's fast and has no config
burden); add a minimal `dependabot.yml` for `gomod` and `github-actions`
ecosystems; pin the GoReleaser version in `release.yml`. Optionally pin
actions to commit SHAs — with Dependabot keeping them fresh, pinning costs
nothing.

## Low priority

### 29. Release artifacts have no provenance or signature

GoReleaser publishes `checksums.txt` alongside the binaries, but checksums
and binaries live in the same release — an attacker who can tamper with one
can tamper with both, so the checksum verifies download integrity, not
authenticity. GitHub's `actions/attest-build-provenance` gives signed
build-provenance attestations (verifiable with `gh attestation verify`) for
a few lines of workflow YAML; cosign keyless signing of `checksums.txt` is
the alternative. Cheap insurance for a tool users install by downloading a
binary. Evidence: `.goreleaser.yml` (`checksum:` only),
`.github/workflows/release.yml`.

### 30. Ingested documents can smuggle terminal escape sequences through `ask` output

`tbuk search` escapes previews with `%q` (`internal/cli/search.go:139`), but
`tbuk ask` streams LLM output to the terminal verbatim
(`internal/cli/ask.go:193-205`). Retrieved chunk text is placed in the
prompt, and models readily echo it — so a document a user ingested from
elsewhere can carry ANSI/OSC sequences that reach the terminal raw: OSC 52
writes to the clipboard, OSC 0 retitles the window, cursor/erase sequences
can hide or rewrite what the user thinks they read. Same applies to
doc-derived fields printed with `%s` elsewhere (citations, titles, metadata
values in `meta list`/`stats`). Fix: filter C0/C1 control characters (keep
`\n`, `\t`) from streamed `ask` output and doc-derived display strings — a
small writer wrapper in one place.

## Noted, no action needed (security review)

- **SQL injection:** all queries use placeholders; the one dynamically built
  query (`internal/search/metadata.go:16-31`) interpolates only generated
  aliases, never user input. Fine as-is.
- **FTS5 injection:** `sanitizeFTS5Query` quotes every term and doubles
  embedded quotes. Fine as-is.
- **Prompt injection via ingested documents** is inherent to RAG and the
  blast radius here is small by design: the LLM's output is text to a
  terminal, there is no tool-use or shell execution path. Issue 30 covers
  the one real escalation (terminal escapes). Revisit if the roadmap ever
  gives `ask` tool-calling abilities.
- **Secrets hygiene:** keys are read from env at construction, stored only
  in unexported struct fields, and never logged; `errorMessage` caps error
  bodies at 2 KB and echoes only the server response. Fine as-is.
- **Path traversal via `--template`** (`ask -t ../../x` escapes
  `~/.tbuk/prompts`): the attacker and victim are the same local user, so
  there is no trust boundary to cross. Not worth code.

---

# Additions — production readiness / SRE assessment (2026-07-19)

Findings from an SRE/platform review: release pipeline, failure recovery,
signal handling, and operational behaviour of the SQLite layer. Much is
already solid — WAL + `busy_timeout(5000)` + `foreign_keys` are set per
pooled connection in the DSN (`internal/storage/db.go:50`), partial ingest
failures exit non-zero with a per-file error report
(`internal/cli/ingest.go:121-124`), re-runs recover cheaply via SHA256
dedup, and CI gates coverage at 85%. Backup/export is already on the
roadmap (`next-steps.md` quick win 3), migration-runner hardening is
issue 23, and supply-chain gaps are issues 28–29. Numbering continues.

## Medium priority

### 31. Release workflow publishes without any test gate, and CI never builds the platforms it ships

**Problem:** Two related holes in the path from commit to published binary:

1. `release.yml` triggers on any `v*` tag push and goes straight to
   GoReleaser — no lint, no tests. The `make release-*` helpers enforce
   "clean main" locally, but nothing server-side does: a tag pushed by hand
   (or from a commit that never went through a PR) publishes untested
   binaries to the Releases page. GitHub tag pushes don't trigger the CI
   workflow either (`ci.yml` runs only on branch pushes/PRs to main), so a
   release can ship from a commit CI never saw.
2. Releases ship six platform builds (linux/darwin/windows × amd64/arm64)
   but CI only ever compiles linux/amd64. A change that breaks compilation
   on another GOOS (a `syscall` usage, a build-tag slip, path handling)
   is first discovered when the tagged release *fails at publish time* —
   the worst moment to find out.

**Evidence:** `.github/workflows/release.yml` (checkout → setup-go →
goreleaser, nothing else); `.github/workflows/ci.yml` (single
`ubuntu-latest` job, no GOOS matrix); `.goreleaser.yml:15-21` (six
targets).

**Fix:** In `release.yml`, add a job that runs `go test ./...` and
`go vet` (or reuse the CI steps) as a prerequisite of the goreleaser job.
In `ci.yml`, add a cheap cross-compile check — `GOOS=darwin`/`windows`
`go build ./...` in a small matrix, or a `goreleaser release --snapshot`
smoke job — so platform breakage surfaces on the PR, not at tag time.

### 32. No signal handling — Ctrl-C is not the clean cancel the README claims

**Problem:** The README/troubleshooting table says "Press `Ctrl-C` to
cancel — retrieval and streaming are interrupted cleanly", and the code is
carefully context-plumbed end to end (`cmd.Context()` flows into ingest
loops, embedding calls, and the SSE stream goroutines, whose
`ctx.Done()` selects exist precisely for this). But nothing ever creates a
signal-aware context: `main` calls `cli.Execute()` → cobra `Execute()`
with the default background context, and `signal.NotifyContext` appears
nowhere. On SIGINT the Go runtime's default handler simply kills the
process — the cancellation paths are dead code in production, deferred
cleanup (`db.Close`, in-flight chunk transaction rollback) never runs, and
a mid-directory `tbuk ingest` dies without printing the partial
"Done: N ingested" summary that would tell the user where it stopped.
Data is safe (WAL journal recovers on next open), but the documented
behaviour doesn't exist and the interrupted-run UX is worse than designed.

**Evidence:** `cmd/tbuk/main.go` (no signal setup),
`internal/cli/root.go:77-83` (`Execute()`, not `ExecuteContext`);
contrast the ctx plumbing it would activate:
`internal/ingest/ingester.go`, `internal/llm/stream.go:12`.

**Fix:** In `Execute`, wrap the root context with
`signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`
and run `root.ExecuteContext(ctx)`. On cancellation, let commands return
`ctx.Err()` and exit non-zero. For `IngestDir`, print the summary of
results accumulated so far before returning. A second Ctrl-C should
force-quit (the stdlib gives this for free: `NotifyContext` restores
default handling after the first signal cancels).

## Low priority

### 33. No retry on transient provider errors during bulk ingest

A bulk `tbuk ingest <dir>` against a hosted embedding provider will hit
rate limits (HTTP 429) and occasional 5xx/connection resets; each such
error fails that file permanently for the run. Recovery exists — re-running
skips completed files via SHA dedup and retries only the failures — so this
is friction, not data loss, but a large first-time ingest against OpenAI
degrades into several manual re-runs. Fix: a small bounded retry
(2–3 attempts, exponential backoff, honour `Retry-After`) on 429/5xx in
the embedding adapters — or once in `Ingester` around the `Embed` call.
Do not retry LLM streaming calls (`ask` is interactive; fail fast there).
Evidence: `internal/embeddings/openai.go`, `internal/embeddings/ollama.go`
(single-shot POSTs); `internal/ingest/ingester.go:126` (one `Embed` per
file, error recorded and loop moves on).

## Noted, no action needed (SRE review)

- **Concurrent `tbuk` processes:** WAL + `busy_timeout(5000)` handles the
  realistic case (a search while an ingest runs). Fine as-is.
- **Partial-failure exit codes:** dir ingest exits non-zero when any file
  fails and says how many. Fine as-is.
- **Backup story:** roadmap quick win 3 (export/import/backup) covers it;
  issue 23's pre-migration copy covers the upgrade path. No new issue.
- **Observability:** errors already carry provider response bodies (capped
  at 2 KB); for a local CLI that is adequate — no structured logging
  needed at this scale.
