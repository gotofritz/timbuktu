# Plan 01 — Consolidated Issues from Assessment Reviews (2026-07-19)

All findings from the 2026-07-19 assessment passes, merged into a single list
**ordered by impact** (P0 → P3), deduplicated, and labelled with the reviewer
that raised each one. Original issue numbers kept as `orig #N` for
traceability; duplicates note every reviewer that independently found them.

## Reviewer legend

| Tag   | Review pass                                      | Orig. IDs |
|-------|--------------------------------------------------|-----------|
| ARCH  | Staff-level architecture & design                | 1–13      |
| CORR  | Day-to-day correctness                           | 14–22     |
| EVOL  | Long-term evolution                              | 23–25     |
| SEC   | Security & attack surface                        | 26–30     |
| SRE   | Production readiness / SRE                       | 31–33     |
| QA    | Test & QA                                        | 34–35     |
| PERF  | Performance & efficiency                         | 36–37     |
| DX    | Developer experience / new contributor           | 38–40     |
| PROD  | User impact / product                            | 41–46     |
| MAINT | Project health / maintainer                      | 47–49     |
| DEBT  | Engineering sustainability (tech-debt auditor)   | ES-1–9    |
| ECO   | Ecosystem health (dependency manager)            | ECO-1–5   |
| AGENT | Agent-readiness                                  | AR-1–11   |

---

## P0 — Alpha blockers

Broken install funnel, silent corruption of the core retrieval pipeline,
hangs, and the one realistic key-leak path. Fix all of these before alpha.

### 1. [MAINT] README recommended install downloads a 404 (orig #47) — ✅ DONE

**Problem:** README "Pre-built binary (recommended)" sets `VERSION=v0.1.0` and
builds `tbuk_${VERSION}_${OS}_${ARCH}.tar.gz` → `tbuk_v0.1.0_linux_amd64.tar.gz`.
GoReleaser `{{ .Version }}` strips the leading `v`: real assets are
`tbuk_0.1.1_linux_amd64.tar.gz` (verified against published v0.1.1). Copy-pasting
the recommended install = 404 for every user on every release — trust killed
before first run. Minor sibling: README lists only `_windows_amd64.zip`;
`_windows_arm64.zip` also published.

**Evidence:** `README.md:22-30`; `.goreleaser.yml:27-29`; live v0.1.1 assets.

**Fix:** Use `${VERSION#v}` in the filename (keep `v` in the
`/releases/download/${VERSION}/` path segment). Mention arm64 Windows asset.
Verify snippet once against a real release after edit.

### 2. [CORR] Chunker boundary search picks *earliest* separator → tiny chunks (orig #14) — ✅ DONE

**Problem:** `findBoundary` should return best sentence break at or before
`maxEnd`; instead takes *minimum* over last occurrences of each separator type
(`if candidate > minStart && candidate < best`). Reproduced:
`"Hello! " + 300×"This is a normal sentence. "` with
`Chunker{Size: 800, Overlap: 100}` yields a first chunk of **7 bytes** instead
of ~3200. Real prose mixing separator types systematically produces undersized
chunks — more embeddings, worse retrieval context. Silent quality corruption of
the core pipeline.

**Evidence:** `internal/chunking/chunker.go:85-102`.

**Fix:** Track *maximum* candidate (`candidate > best`, sentinel init, fall
back to `maxEnd` when none). Table-driven tests with mixed separators.

### 3. [CORR] `Chunker.Split` hangs forever when `Size <= 0` (orig #15) — ✅ DONE

**Problem:** With `Size: 0` (hand-edited config — nothing validates, see
issue 17), `end == start`, boundary resolves to `start`, no-progress guard sets
`next = boundary = start`. Loop never advances, appends empty chunks forever:
`tbuk ingest` hangs with unbounded memory. Confirmed with 2-second-timeout
repro test.

**Evidence:** `internal/chunking/chunker.go:37-78` (guard at line 74 assumes
`boundary > start`).

**Fix:** Guard in `Split` itself (sane default or error for `Size <= 0`) —
belt-and-braces with `Config.Validate()` (issue 17), since `Chunker` is also
constructed directly in code.

### 4. [ARCH] Embedding dimension mismatch fails silent (orig #2) — ✅ DONE

**Problem:** `cosineSimilarity` returns `0` when vector lengths differ. User
changes embedding model or `embedding.dimension` after ingest → every stored
vector scores 0: vector search silently returns nothing, hybrid degrades to
keyword-only. No dimension recorded in DB; nothing checks mismatch at ingest
or query time.

**Evidence:** `internal/search/vector.go:72-75`; schema in
`internal/storage/migrate.go` stores raw BLOBs, no dimension metadata.

**Fix:** Record embedding dimension (ideally provider/model) in DB. Error loud
on mismatch at query + ingest. Add `tbuk doctor` check.

### 5. [ARCH] `search.Options.Metadata` silent no-op (orig #1) — ✅ DONE

**Problem:** `Options.Metadata` documented as "AND-combined metadata
pre-filter"; `Retriever.Retrieve` accepts `meta` arg, forwards into
`search.Options`. But `Searcher.Hybrid` rebuilds options as
`Options{TopK: ..., MinScore: 0}` — drops Metadata. `Vector`/`Keyword` never
read it. Caller passing filter gets unfiltered results, no error.

**Evidence:** `internal/search/hybrid.go:12`, `internal/search/search.go:26`,
`internal/retrieval/retrieval.go:38-40`.

**Fix:** Implement metadata pre-filter in Vector/Keyword/Hybrid (JOIN against
`metadata` table before scoring), or remove field + `meta` parameter chain so
the API stops advertising behaviour it lacks.

### 6. [PROD] `tbuk delete` confirmation hangs on plain Enter (orig #41) — ✅ DONE

**Problem:** Prompt is `Delete <path> (N chunks)? [y/N]` — convention: plain
Enter = No. But reply read with `fmt.Fscan(os.Stdin, &answer)`; `Fscan` skips
newlines waiting for a token — Enter does nothing, command looks frozen until
user types non-whitespace. Every interactive delete hits this.

**Evidence:** `internal/cli/delete.go:49-54`.

**Fix:** Read full line (`bufio.NewReader(os.Stdin).ReadString('\n')`), trim,
empty line = default No.

### 7. [CORR] `tbuk meta set` / `meta list` don't normalize path argument (orig #16) — ✅ DONE

**Problem:** `ingest`/`update`/`delete` resolve CLI paths via `NormalizePath`
before DB; documents keyed by canonical path. `RunMetaSet`/`RunMetaList` pass
the raw arg straight to `GetByPath`, so `tbuk ingest ./notes.md` then
`tbuk meta set ./notes.md topic=x` fails "document not found" — works only
with the exact absolute path. Both also collapse *every* lookup error into
"document not found" (`meta.go:74,94`), conflating real DB errors with a
missing row, contrary to the `storage.ErrNotFound` / `errors.Is` pattern
elsewhere.

**Evidence:** `internal/cli/meta.go:71-75, 91-95` vs
`internal/cli/delete.go:69-72`, `internal/cli/update.go:61-64`.

**Fix:** `NormalizePath` in both commands; branch on
`errors.Is(err, storage.ErrNotFound)`, propagate other errors as-is.

### 8. [PROD] `tbuk ask` silently answers from model priors when retrieval returns nothing (orig #45) — ✅ DONE

**Problem:** Retrieval yields zero chunks — empty knowledge base (default
first-run state) or no matches — `RunAsk` renders the template with no context
and streams whatever the LLM says. Guide promises "the model cannot invent
facts that are not in your documents"; empty context = pure model prior with
identical confidence, only clue = absent `Sources:` section. Hits exactly the
user who most needs the signal.

**Evidence:** `internal/cli/ask.go:150-176` (no empty-chunks branch),
`ask.go:207-212` (`Sources:` only when chunks exist); `docs/user-guide.md` §9.

**Fix:** On zero retrieved chunks, clear warning to stderr before (or instead
of) LLM call; consider `--require-context` flag (or template-manifest option)
that aborts instead. Keep qa template's "say so clearly" instruction as
backstop, not only defence.

### 9. [SEC] Malformed PDF can panic + kill entire ingest run (orig #26; dup: ECO-4) — ✅ DONE

**Problem:** Primary untrusted-input surface = ingestion, and PDFs are exactly
what users download from elsewhere. Extraction delegates to
`github.com/ledongthuc/pdf` — unreleased, minimally maintained, pinned to a
pseudo-version, with known index-out-of-range / nil-dereference panics on
malformed files. Nothing recovers: a panic in `Extract` unwinds past
`IngestDir` per-file error handling and crashes the whole `tbuk ingest` run.
Extractor also `io.ReadAll`s the entire file with no size cap
(`plaintext.go`/`html.go` unbounded too) — a multi-GB stray file exhausts
memory before chunking.

**Evidence:** `internal/preprocess/pdf.go:15-42` (no recover, unbounded read);
`internal/ingest/ingester.go:226-240` (per-file loop, no panic isolation).

**Fix:** Wrap the extractor call in `defer`/`recover` → panic becomes an error
`Result` for that file (test with a malformed fixture). Add configurable
max-file-size guard (stat before read; generous default ~100 MB). Folds into
error-surfacing work, issue 15. Longer term, evaluate a maintained alternative
(e.g. `pdfcpu`) if PDF coverage grows.

### 10. [CORR] Provider `base_url` defaults unreachable — provider switch silently targets `localhost:8080` (orig #17) — ✅ DONE

**Problem:** Every adapter has an in-code fallback base URL (claude →
`api.anthropic.com`, openai → `api.openai.com`, ollama → `:11434`) triggering
only when `cfg.BaseURL == ""`. But `config.Defaults()` (and `tbuk init` YAML)
hardcode `base_url: http://localhost:8080` for both `llm` and `embedding` —
`Load` never yields empty BaseURL. User edits `provider: llama` →
`provider: claude` without touching `base_url` → Anthropic-bound requests go
to `http://localhost:8080`; `provider: ollama` never reaches the documented
`:11434` default.

**Evidence:** `internal/config/config.go:65-76,113-123`;
`internal/llm/claude.go:27-30`, `internal/llm/ollama.go:22-25`,
`internal/embeddings/openai.go:28-31`.

**Fix:** Leave `base_url` empty in defaults/`DefaultYAML` (comment
per-provider defaults instead), resolve per provider in factories. Or have
`Config.Validate()` (issue 17) reject provider/base_url combos that look like
a stale default. Do together with issue 11.

### 11. [SEC] API keys attached to any configured `base_url`, incl. plain HTTP (orig #27) — ✅ DONE

**Problem:** claude/openai adapters set `x-api-key` / `Authorization: Bearer`
on requests to whatever `base_url` config supplies, no scheme/host check.
Combined with issue 10 (defaults hardcode `http://localhost:8080`; switching
provider without editing `base_url` = realistic slip), the failure mode is
concrete: a real `ANTHROPIC_API_KEY` sent cleartext to whatever answers on
localhost:8080 — or a remote `http://` host, unencrypted across the network.

**Evidence:** `internal/llm/claude.go:90-96`, `internal/llm/openai.go`,
`internal/embeddings/openai.go:50-58` (unconditional headers);
`internal/config/config.go:69,75`.

**Fix:** In cloud-provider factories (claude, openai), reject — or minimum
loud warn on — non-HTTPS non-loopback `base_url` when an API key is attached.
Do alongside the issue 10 fix, which removes the most likely trigger.

### 12. [PROD] Single-file `tbuk ingest` succeeds silent — user guide shows output that doesn't exist (orig #43) — ✅ DONE

**Problem:** Single file: `PrintFileResult` prints nothing on success; dedup
skip prints nothing without `--verbose`. Guide's first ingest walkthrough
promises `Ingesting … chunks: 1 … Done.` and `Skipped … (unchanged)` on
repeat — the real command returns zero output in all three cases. First-time
user can't tell success from no-op; guide-vs-reality gap kills trust at the
most sensitive funnel point (first ingest). Directory ingest already prints
per-file progress + summary; single-file is the inconsistent case.

**Evidence:** `internal/cli/ingest.go:84-93`; `docs/user-guide.md` §6.

**Fix:** Print a one-line result unconditionally for single-file ingest
(`<path> → N chunks embedded` / `<path> → skipped (unchanged)`), align guide
sample output with actual.

---

## P1 — High (fix before or immediately after alpha)

### 13. [CORR] Document row updated before chunks replaced — failed re-ingest strands stale chunks forever (orig #20) — ✅ DONE

**Problem:** On re-ingest, `IngestFile` writes the new SHA256 to `documents`
*before* `ReplaceForDocument` swaps chunks. If chunk replacement fails (disk
full, DB locked beyond busy_timeout), the document records the new hash while
the index holds old chunks — every later `ingest`/`update` sees "SHA
unchanged", skips. Stale index repairable only with `--force`, nothing tells
the user. Undercuts the atomicity the chunk transaction was built for.

**Evidence:** `internal/ingest/ingester.go:143-172` (doc `Update` at 147,
`ReplaceForDocument` at 170).

**Fix:** Document upsert + `ReplaceForDocument` in the same transaction, or
update document SHA256 only after chunks are stored.

### 14. [CORR] Embedding count mismatch panics mid-ingest (orig #19) — ✅ DONE

**Problem:** Ingester indexes `vecs[j]` assuming the embedder returned exactly
one vector per input text. Ollama + openai adapters return whatever the server
sent without a count check — partial or malformed response crashes
`tbuk ingest` with an index-out-of-range panic, not a clean error.

**Evidence:** `internal/ingest/ingester.go:126-137`;
`internal/embeddings/ollama.go:78-85`, `internal/embeddings/openai.go:73-92`.

**Fix:** In each adapter (or once in `IngestFile`), verify
`len(vectors) == len(texts)`, descriptive error on mismatch.

### 15. [ARCH] `IngestDir` swallows walk errors (orig #4) — ✅ DONE

**Problem:** `filepath.WalkDir` return discarded; callback returns `nil` on
entry errors. Unreadable dir/file skipped invisibly — no `Result`, not in
"Done: N errors" summary.

**Evidence:** `internal/ingest/ingester.go:228-230`.

**Fix:** Record entry errors as error `Result`s → surface in output + non-zero
exit path.

### 16. [PROD] Guide's `--min-score` advice returns zero results in default search mode (orig #44) — ✅ DONE

**Problem:** `docs/user-guide.md` §10 recommends thresholds of 0.6–0.7
directly under examples using **default hybrid mode**. Hybrid scores are RRF
sums (max ≈ 2/61 ≈ 0.033, k=60), so the guide's own example
(`--min-score 0.7`) filters every result and prints "No results found." with
no hint why. Also poisons the guide's own debugging advice ("run `tbuk search`
to inspect retrieval").

**Evidence:** `docs/user-guide.md` §10; `internal/search/hybrid.go` (RRF
k=60); `README.md:57`.

**Fix:** Fix guide first (scope 0–1 advice to `--mode vector`). Then make the
flag usable without reading internals: normalize hybrid scores to 0–1 before
`MinScore` applies, or warn when `--min-score` exceeds the achievable hybrid
max for the given `--top`.

### 17. [ARCH] Config has no validation (orig #6) — ✅ DONE

**Problem:** No check for `overlap >= size`, negative chunk sizes, nonsense
values; unknown providers fail deep inside factories. Root cause enabler for
issues 3 and 10.

**Evidence:** `internal/config/config.go` (no Validate), factories in
`internal/llm/llm.go` / `internal/embeddings/embeddings.go`.

**Fix:** Add `Config.Validate()` called from root `PersistentPreRunE` — every
command fails fast, clear message.

### 18. [EVOL] Unknown config keys silently ignored (orig #24) — ✅ DONE

**Problem:** `Load` uses plain `yaml.Unmarshal` — drops unknown fields. Typo
(`chunk_size:` for `size:`, `baseurl:` for `base_url:`) or key from a newer
tbuk silently ignored, default silently wins — config *looks* applied, isn't.
Complementary to issue 17 (which validates *values*); grows worse as the
config schema grows.

**Evidence:** `internal/config/config.go:100`.

**Fix:** Decode via `yaml.Decoder` with `KnownFields(true)`, fail with the
offending key name. Fold into the same `Config.Validate()` effort as issue 17.

### 19. [SRE] No signal handling — Ctrl-C not the clean cancel README claims (orig #32) — ✅ DONE

**Problem:** README says "Press `Ctrl-C` to cancel — retrieval and streaming
are interrupted cleanly"; code is carefully context-plumbed end to end, but
nothing creates a signal-aware context: `main` → `cli.Execute()` → cobra
`Execute()` with default background context; `signal.NotifyContext` appears
nowhere. On SIGINT the Go runtime default handler kills the process —
cancellation paths are dead code, deferred cleanup (`db.Close`, in-flight
chunk transaction rollback) never runs, mid-directory ingest dies without the
partial summary. Data safe (WAL recovers), but documented behaviour doesn't
exist.

**Evidence:** `cmd/tbuk/main.go` (no signal setup),
`internal/cli/root.go:77-83` (`Execute()`, not `ExecuteContext`); contrast ctx
plumbing in `internal/ingest/ingester.go`, `internal/llm/stream.go:12`.

**Fix:** In `Execute`, wrap root context with
`signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`,
run `root.ExecuteContext(ctx)`. On cancellation, return `ctx.Err()`, exit
non-zero. `IngestDir`: print summary accumulated so far before return. Second
Ctrl-C force-quits (`NotifyContext` restores default handling).

### 20. [SRE] Release workflow publishes without test gate; CI never builds shipped platforms (orig #31) — ✅ DONE

**Problem:** Two holes in the commit → published binary path:
1. `release.yml` triggers on any `v*` tag push straight to GoReleaser — no
   lint, no tests; nothing server-side enforces "clean main". Tag pushes don't
   trigger CI either — a release can ship from a commit CI never saw.
2. Releases ship six platform builds (linux/darwin/windows × amd64/arm64); CI
   compiles only linux/amd64. Compile break on another GOOS first discovered
   when the tagged release *fails at publish time*.

**Evidence:** `.github/workflows/release.yml`; `.github/workflows/ci.yml`
(single ubuntu job); `.goreleaser.yml:15-21`.

**Fix:** In `release.yml`, add a job running `go test ./...` + `go vet` as a
prerequisite of the goreleaser job. In `ci.yml`, cheap cross-compile check
(`GOOS=darwin`/`windows` `go build ./...` matrix, or
`goreleaser release --snapshot` smoke job).

### 21. [CORR] Ollama LLM adapter silently ignores `max_tokens` + `temperature` (orig #18) — ✅ DONE

**Problem:** `ollamaProvider.Chat` reads only `Model` from `CallOptions`,
sends just `model`/`messages`/`stream`; the provider's `maxTokens` field is
stored, never used. Manifest `temperature`/`max_tokens` and config
`llm.max_tokens` are silent no-ops on ollama, while working for
claude/openai/llama.

**Evidence:** `internal/llm/ollama.go:34-56` (contrast
`internal/llm/claude.go:40-83`).

**Fix:** Map into ollama's `options` object (`num_predict`, `temperature`) in
the request body.

### 22. [EVOL] Migration runner not ready for second migration (orig #23) — ✅ DONE

**Problem:** Whole schema = one migration, all `IF NOT EXISTS` — masks three
weaknesses in `RunMigrations` that bite when migration v2 lands:
1. Applied-version check swallows error (`_ = db.QueryRow(...).Scan(&exists)`)
   — transient failure reads as "not applied", migration re-runs; a future
   `ALTER TABLE ... ADD COLUMN` re-applied fails hard or corrupts.
2. Migration SQL + `schema_migrations` record = two separate `Exec` calls, not
   one transaction. Crash between → schema changed, unrecorded → re-applied.
3. No guard for DB *newer* than binary. Older `tbuk` opening a newer DB
   silently reads/writes schema it doesn't understand — realistic for a
   local-first tool with standalone binaries against one long-lived
   `~/.tbuk/tbuk.sqlite`.

**Evidence:** `internal/storage/migrate.go:79-94` (swallowed Scan at 81,
two-step apply at 85-93); no max-version check in `storage`.

**Fix:** Before the second migration is written: wrap each migration SQL +
version record in one transaction; propagate the version-check error; error
out with a clear "database created by newer tbuk" message when recorded max
version exceeds the binary's. Optionally copy the DB file aside before
applying pending migrations.

### 23. [PROD] No way to list documents in the knowledge base (orig #42) — ✅ DONE

**Problem:** No `tbuk list` (or `tbuk docs`). `tbuk stats` = counts only;
`tbuk find` requires a metadata filter — "show me everything indexed" has no
answer. Users need it constantly: verify ingest worked, spot renamed/stale
files, decide what to delete.

**Evidence:** `internal/cli/root.go:47-59` (command roster);
`docs/user-guide.md` §8.

**Fix:** Add `tbuk list [--format text|json]` reading the `documents` table
(path, title, chunk count, updated_at) with the usual `--limit`. Low effort —
repos + text/JSON printing patterns exist.

---

## P2 — Medium

### 24. [SEC] No dependency vuln scanning; release toolchain unpinned (orig #28; dup: ES-4, ECO-1, ECO-2, ECO-5) — ✅ DONE

**Problem:** Project ships standalone binaries but nothing watches the
dependency tree: no `govulncheck` in CI, no Dependabot/Renovate — a CVE in
`golang.org/x/net`, `modernc.org/sqlite`, or the PDF parser (the one dep
parsing untrusted input, issue 9) goes unnoticed. GitHub Actions
(`checkout@v4`, `setup-go@v5`, `golangci-lint-action@v7`,
`goreleaser-action@v6`) also get no update PRs. Release workflow runs
`goreleaser-action@v6` with `version: latest` — every tagged release builds
with whatever GoReleaser is current that day: unreproducible, exposed to a
bad/compromised upstream release at publish time (a goreleaser major bump can
break the v2 config exactly at release time). Indirect updates already
pending (`spf13/pflag v1.0.10`, `modernc.org/libc v1.74.3`).

**Evidence:** `.github/workflows/ci.yml`, `quality-check.yml` (no vuln
scanning); `.github/workflows/release.yml:24-27` (`version: latest`); no
`.github/dependabot.yml`.

**Fix:** Add `govulncheck ./...` step to CI; minimal `dependabot.yml` for
`gomod` + `github-actions` (weekly; optionally group `modernc.org/*` bumps);
pin GoReleaser (`version: '~> v2'`). Optionally pin actions to commit SHAs —
with Dependabot keeping them fresh, pinning costs nothing.

### 25. [QA] CI enforces only *total* coverage — per-package ≥85% rule local-only (orig #34; dup: ES-1, AR-7) — ✅ DONE

**Problem:** AGENTS.md mandates coverage ≥85% *per package*; `make check-ci`
enforces it, but the actual CI coverage job checks only the total (86.7%): a
PR can drop one package to 40% while the total stays above 85% — CI green;
the rule fires only for contributors who remember `make check-ci` locally.
Not hypothetical headroom: `cli` (85.8%), `storage` (85.6%), `llm` (86.1%),
`embeddings` (86.0%) all within ~1% of the line today. Two gates maintained as
duplicated shell/awk → keep diverging.

**Evidence:** `.github/workflows/quality-check.yml:44-56` (total-only) vs
`Makefile:56-72` (`check-ci` with per-package awk pass).

**Fix:** CI runs the same per-package check — simplest: coverage job runs
`make check-ci` (or extract the coverage-gate script to one file both invoke)
so local + CI gates can't drift again.

### 26. [ARCH] Composition root scattered across CLI commands (orig #3) — ✅ DONE

**Problem:** Every command hand-wires its own dependency graph — open DB,
repos, embedder, chunker, searcher — duplicated open/close + error wrapping.
New dependency = touch N command files.

**Evidence:** `internal/cli/ingest.go:29-53`, `internal/cli/ask.go:53-70`,
similar in `search.go`, `update.go`, `delete.go`, `stats.go`.

**Fix:** Shared builder, e.g. `cli.openApp(cfg) (*App, error)` returning
repos/searcher/ingester + closer. Commands consume App.

### 27. [PERF] Vector search materialises full text of every chunk per query + full-sorts all candidates (orig #36) — ✅ DONE

**Problem:** Accepted design = O(n) scan over embeddings; implementation does
strictly more. Scan query selects `c.text`, `d.path`, `d.title` for *every*
chunk; every row passing `MinScore` (default 0 — almost all scores clear it)
is appended to `candidates` — peak per-query memory ≈ entire corpus text + all
decoded embeddings, then all but K thrown away. Final ranking = `sort.Slice`
over all n (O(n log n)) when only top K needed. At the documented ~100k-chunk
comfort zone, hundreds of MB transient allocations per query.

**Evidence:** `internal/search/vector.go:19-24`, `vector.go:34-52`,
`vector.go:57-59`.

**Fix:** Two-phase within the same scan design: first pass selects only
`id, embedding` with a bounded min-heap of top K (O(n log k) time, O(k)
memory); second query hydrates text/path/title for just those K chunk IDs. No
`Searcher` API change. Benchmark before/after with a synthetic 50–100k-chunk
DB.

### 28. [PERF] Bulk ingest fully serial — embedding round-trip latency = wall clock (orig #37) — ✅ DONE

**Problem:** `IngestDir` processes one file at a time; within a file, the
embed loop is one blocking `Embed` call per 16-chunk batch. The Ollama adapter
splits each into sequential batches of 8 — every ingester batch = *two* serial
HTTP round-trips. Nothing overlaps network wait with
extraction/chunking/storage. ~600 batches at 300 ms RTT = ~3 min pure
sequential waiting; modest concurrency → tens of seconds.

**Evidence:** `internal/ingest/ingester.go:226-240`, `ingester.go:116-138`
(`embedBatchSize = 16` at line 81), `internal/embeddings/ollama.go:13,33-47`.

**Fix:** Bounded concurrency at one level — simplest: small worker pool (2–4
workers) over embed batches within a file; per-file DB writes stay serial so
`ReplaceForDocument` atomicity is untouched. Align or make configurable
ingester/adapter batch sizes so one batch = one request. Keep the limit low +
configurable — must compose with 429/5xx retry work (issue 33).

### 29. [QA] No test exercises real wiring — production extractor, root command, exit codes at 0% (orig #35) — ✅ DONE

**Problem:** Every test injects fakes at package seams; nothing runs the
assembled pipeline. `DefaultFileExtractor.ExtractFile` — the only production
`FileExtractor` — 0% coverage; `cli.Execute` + `cmd/tbuk/main` also 0% —
error → exit-code-1 path untested. The bug classes other reviews found are
precisely wiring-level, invisible to per-package unit tests at any coverage:
metadata filter dropped (issue 5), `meta set` skipping path normalization
(issue 7), signal context plumbed but never created (issue 19).

**Evidence:** coverage run 2026-07-19: `internal/ingest/ingester.go:247`
(`ExtractFile` 0%), `internal/cli/root.go:78` (`Execute` 0%), `cmd/tbuk` (no
test files).

**Fix:** One CLI-level happy-path integration test: temp `HOME`, `tbuk init` →
`ingest` real `.md` fixture (embedder backed by `httptest`) → `search` →
`meta set`/`meta list` → `delete`, driven through the root command (`SetArgs`
+ `Execute`). Also pins exit-code behaviour before issue 19 signal work; same
test in a small OS matrix gives issue 20's platform gap runtime coverage.

### 30. [ARCH+DX] No committed golangci-lint config; README installs wrong lint major version (orig #12 + #38; dup: ES-3, ECO-3, AR-4, AR-5) — ✅ DONE

**Problem:** Two related drifts that together mean local lint and CI lint
share almost nothing:
1. No `.golangci.yml` committed — lint behaviour is whatever the running
   version's defaults are; the CI gate shifts silently when the pinned action
   version bumps. The only version pin (v2.5.0) is buried in
   `quality-check.yml`; `make lint` and the pre-commit hook run whatever is on
   PATH.
2. README says "golangci-lint v2" but gives
   `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` —
   that module path is **v1** (tops out v1.64.8); v2 lives under `/v2`. CI
   pins v2.5.0. A contributor following README gets a different major version
   with incompatible config format and different default linters — green
   locally can fail on PR and vice versa. (golangci-lint also explicitly
   doesn't support `go install` as an install method.)

**Evidence:** repo root (no `.golangci.yml`); `README.md:42` (v1 module path);
`.github/workflows/quality-check.yml:23-25` (pins v2.5.0); verified
`go list -m -versions`.

**Fix:** Commit a minimal `.golangci.yml` (`version: "2"` + intended linter
set); replace the README command with a supported install pinned to the CI
version (binary install script or
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0`);
note it's needed only for `make lint`/`check`/`check-ci`. State the version in
exactly one place.

### 31. [DX] Commit-time gates (pre-commit + commitizen) enforced but undocumented (orig #39; dup: ES-7, AR-6) — ✅ DONE

**Problem:** AGENTS.md declares "Pre-commit enabled"; `.pre-commit-config.yaml`
wires a commit-msg hook running `cz check` (commitizen, Python,
`language: system` — must be on PATH) + Go hooks needing `goimports`. None
documented for humans: README never mentions pre-commit, no CONTRIBUTING.md,
AGENTS.md says "No virtualenv. Standard Go toolchain" — actively misleading.
The repo actually uses Conventional Commits (feeding GoReleaser changelog
grouping) but AGENTS.md's Commits section doesn't mention the prefix
convention, no commitizen config is committed, and CI has no commit-message
check — so commits from environments without the hooks bypass the convention
entirely. A new contributor either hits `cz: command not found` on first
commit or never runs `pre-commit install` and discovers conventions via CI or
review.

**Evidence:** `.pre-commit-config.yaml:40-48, 53-57`; `AGENTS.md` Environment
+ Enforcement + Commits sections; `README.md` Development section; no
`CONTRIBUTING.md`, no `.cz.toml`.

**Fix:** Short "Contributing" section in README (or CONTRIBUTING.md) with
one-time setup — `pipx install pre-commit commitizen`,
`pre-commit install --install-hooks`, `go install .../goimports` — stating
commit subjects must pass `cz check`. Document Conventional Commits in
AGENTS.md; optionally commit `.cz.toml` and/or add a commit-lint CI step.
Alternatively, if the commitizen gate is unwanted, drop the hook rather than
leave it half-enforced.

### 32. [EVOL] CI never runs race detector (orig #25; dup: ES-2) — ✅ DONE

**Problem:** `make test-race` exists but no workflow invokes it — CI runs
plain `go test`. The `llm` package is goroutine + channel heavy (streaming
adapters, `sendToken`/`ctx.Done()` selects); roadmap multi-turn `ask` adds
more concurrency. Races regress silently until a user hits them.

**Evidence:** `.github/workflows/ci.yml`; `internal/llm/claude.go:113`,
`ollama.go:79`, `openai.go:126`.

**Fix:** Add `-race` to the CI test step (suite ~3s, race overhead trivial) or
a parallel job.

### 33. [SRE] No retry on transient provider errors during bulk ingest (orig #33) — ✅ DONE

**Problem:** Bulk `tbuk ingest <dir>` against a hosted embedding provider hits
rate limits (429) + occasional 5xx/connection resets; each error fails that
file for the run. Recovery exists (re-run skips completed files via SHA
dedup) — friction, not data loss, but a large first-time ingest against
OpenAI degrades into several manual re-runs.

**Evidence:** `internal/embeddings/openai.go`,
`internal/embeddings/ollama.go` (single-shot POSTs);
`internal/ingest/ingester.go:126`.

**Fix:** Small bounded retry (2–3 attempts, exponential backoff, honour
`Retry-After`) on 429/5xx in embedding adapters — or once in `Ingester`
around the `Embed` call. Don't retry LLM streaming (`ask` interactive; fail
fast).

### 34. [ARCH] `DefaultYAML` duplicates `Defaults()` by hand (orig #5) — ✅ DONE

**Problem:** Default config exists twice — struct literal in `Defaults()`,
hand-built YAML string in `DefaultYAML()`. Two sources of truth drift.

**Evidence:** `internal/config/config.go:58-85` vs `config.go:108-132`.

**Fix:** Generate YAML by marshalling `Defaults()` (template if comments
wanted).

---

## P3 — Low

### 35. [CORR] `CheckFTS5` leaks `sql.Rows` (orig #21)

`internal/search/search.go:48-54` discards `*sql.Rows` from `QueryContext`
without closing — holds a pooled connection until GC. Use
`QueryRowContext(...).Scan(...)` (tolerate `sql.ErrNoRows`) or close rows.

### 36. [CORR] `tbuk template edit` doesn't edit anything (orig #22)

Help ("Open a template's manifest in $EDITOR") and `docs/initial-context.md`
promise an editor session; implementation prints `Edit: <path> (open with vi)`
and exits (`internal/cli/template.go:92-109`). Launch `$EDITOR` via
`exec.Command` with stdio attached, or rename/re-describe command + fix docs.

### 37. [SEC] Ingested documents can smuggle terminal escapes through `ask` output (orig #30)

`tbuk search` escapes previews with `%q` (`internal/cli/search.go:139`), but
`tbuk ask` streams LLM output verbatim (`internal/cli/ask.go:193-205`).
Retrieved chunk text goes into the prompt; models echo it — a document
ingested from elsewhere can carry ANSI/OSC sequences reaching the terminal
raw: OSC 52 writes clipboard, OSC 0 retitles window, cursor/erase sequences
rewrite what the user reads. Same for doc-derived fields printed `%s`
elsewhere (citations, titles, metadata in `meta list`/`stats`). Fix: filter
C0/C1 control chars (keep `\n`, `\t`) from streamed `ask` output +
doc-derived display strings — small writer wrapper, one place.

### 38. [SEC] Release artifacts lack provenance / signature (orig #29)

Checksums + binaries live in the same release — attacker who tampers one
tampers both. GitHub `actions/attest-build-provenance` gives signed
build-provenance attestations (verifiable `gh attestation verify`) for a few
lines of workflow YAML; cosign keyless signing of `checksums.txt` is an
alternative. Evidence: `.goreleaser.yml` (`checksum:` only),
`.github/workflows/release.yml`.

### 39. [PROD] `tbuk update` fails obscure on directories (orig #46)

`ingest` accepts file or directory; `update` calls `IngestFile`
unconditionally — natural `tbuk update ~/notes/` fails with a low-level
extraction error. Either support directories (delegate to `IngestDir`) or
stat the path and return "update takes a single file; use `tbuk ingest <dir>`
for folders". Evidence: `internal/cli/update.go:60-77` vs
`internal/cli/ingest.go:65-70`.

### 40. [ARCH] Prompts root hardcoded (orig #7)

`~/.tbuk/prompts` hardcoded in `internal/cli/ask.go:44-46` (also affects
`internal/cli/template.go:111-114`, per PROD review) while DB path +
extracted dir are configurable. Move to config (e.g. `prompts.dir`).

### 41. [ARCH] Storage queries in CLI package (orig #8)

`CountDocuments` / `CountChunks` in `internal/cli/ingest.go:128-139` are
storage concerns. Move to `internal/storage`.

### 42. [ARCH] Dead `sseScanner` wrapper (orig #9)

`internal/llm/stream.go:39-42` claims to strip trailing carriage returns but
just returns `bufio.NewScanner(r)`. Implement CR stripping or delete wrapper
+ comment.

### 43. [ARCH] Makefile `serve` target leftover cruft (orig #10; dup: ES-6, AR-8)

`make serve` does `cd output && python3 -m http.server 8080` "for local feed
testing" — no `output/` exists, no feed anywhere in this project; copy-paste
leftover from another repo. Appears in `make help` as if real, fails when
run, adds a python3 implication to a pure-Go project. Delete the target (and
its `.PHONY` entry).

### 44. [ARCH] Stale architecture docs advertise nonexistent `metadata` package (orig #11; dup: ES-5, part of #40)

`docs/initial-context.md` **and** `README.md:206` both list
`internal/metadata/ — stub (not yet active)` — the package doesn't exist;
metadata actually lives in `internal/storage/metadata.go` and
`internal/search/metadata.go`. AGENTS.md designates `initial-context.md` as
the architecture source of truth — a phantom package misdirects every new
session/contributor. Delete the line from both files (or note where metadata
actually lives).

### 45. [DX] Contributor docs disagree on Go version (part of orig #40; dup: ES-8, part of AR-9)

AGENTS.md tooling says "go (1.24+)" while README requires "Go 1.25+" and
`go.mod` pins `go 1.25.0` — a 1.24 toolchain refuses to build (or
auto-downloads 1.25, surprising offline/CI environments). State the Go
requirement in one place (README); AGENTS.md defers to it.

### 46. [DEBT] Test suite runs twice on every push/PR; verbose output floods logs (ES-9; dup: AR-10)

Both `ci.yml` (Test step, `-v`) and `quality-check.yml` (coverage job) trigger
on the same events and run the full suite; `-v` prints every test name,
burying failures in noise (and flooding agent context). Drop the Test step
from ci.yml (coverage job already runs everything with `-count=1`) or merge
the workflows; drop `-v` either way (keep a `test-verbose` target).

### 47. [MAINT] No SECURITY.md / vulnerability disclosure channel (orig #48)

Issues 24/38 cover *detecting* vulns and *signing* releases; nothing tells an
outside reporter where to send one. Project parses untrusted input (PDFs,
issue 9) and ships binaries — the case where private disclosure matters. Add
`SECURITY.md` advertising GitHub private vulnerability reporting (supported
versions + "use GitHub advisories / email").

### 48. [MAINT] Findings backlog has no lifecycle — nothing marks issues resolved (orig #49)

This file holds all findings from the assessment passes; GitHub Issues never
used (0 issues, tracker idle). No status marker per finding — as fixes land,
nothing records which findings are done; every future assessment pass
re-spends effort dedup-checking. Also `01-issues.original.md` (pre-compress
backup) sits in `docs/plans/` — convention says active plans only. Fix:
triage high/medium findings into GitHub issues (closable by PR keywords)
keeping this file as source index, or add per-finding status markers and
archive resolved sections per the plan-archive convention; move the
`.original.md` backup to `docs/archive/` or delete it.

### 49. [AGENT] AGENTS.md mandates `gh` CLI, but remote agent sessions have no `gh` (AR-1)

AGENTS.md says "Use `gh` for all GitHub operations" and the branch workflow
requires `gh pr list --state open`. Remote Claude Code sessions have no `gh`
binary — GitHub access goes through MCP tools. Reword to "use `gh` when
available; in environments without it, use the GitHub MCP tools instead."

### 50. [AGENT] TDD hook blocks all edits to `cmd/tbuk/main.go` (AR-2)

`.claude/hooks/check-tdd.sh` blocks Write/Edit of any non-test `.go` file when
the directory has no `*_test.go`. `cmd/tbuk/` contains only `main.go` (by
design). An agent cannot make even a trivial edit without creating a
throwaway test file, contradicting "minimal diffs". Allowlist `cmd/` (or
`main.go`) in the hook, or add a minimal smoke test to `cmd/tbuk/`.

### 51. [AGENT] Session-start "ask which branch" hook stalls autonomous sessions (AR-3)

The SessionStart hook demands the agent ask the user for a branch and wait. In
autonomous/remote runs the user isn't watching, and task prompts often already
name the branch — forcing either a stalled session or a rule violation. Amend
to "if the user's prompt already specifies a branch, use it without asking;
otherwise ask."

### 52. [AGENT] Broken skill reference in AGENTS.md (part of AR-9)

AGENTS.md points to `.claude/skills/tdd.md`; actual path is
`.claude/skills/tdd/SKILL.md`. Correct the path. (Go-version half of AR-9 is
issue 45.)

### 53. [AGENT] TDD enforcement weaker than AGENTS.md claims (AR-11)

AGENTS.md states "MANDATORY TDD — no exceptions. A PreToolUse hook enforces
this," but the hook only matches Write/Edit and only checks that *some*
`_test.go` exists in the directory. Bash file writes bypass it; one trivial
test file unlocks unlimited implementation files. Note in AGENTS.md the hook
is a guardrail, not proof of TDD; optionally extend the matcher to Bash write
patterns.

---

## Noted, no action needed (retained from reviews)

- **[ARCH]** Vector search = full table scan decoding all embeddings per
  query. Documented acceptable below ~100k chunks; `Searcher` API allows later
  sqlite-vec swap without interface change. Revisit only if scale grows.
  (Issue 27 tightens constants within this accepted design.)
- **[CORR]** `Searcher.Vector` silently skips chunks whose embedding blob
  fails decode (`vector.go:42-45`). Same "silent degrade" family as issue 4;
  the doctor check there should count undecodable embeddings too.
- **[CORR]** `tbuk stats --format banana` silently falls back to text while
  `search`/`find` validate the flag. Cosmetic inconsistency.
- **[SEC]** SQL injection: all queries parameterised; the one dynamic query
  (`internal/search/metadata.go:16-31`) interpolates only generated aliases.
  Fine.
- **[SEC]** FTS5 injection: `sanitizeFTS5Query` quotes every term, doubles
  embedded quotes. Fine.
- **[SEC]** Prompt injection via ingested docs inherent to RAG; blast radius
  small by design (LLM output = text to terminal, no tool-use). Issue 37
  covers the one real escalation. Revisit if roadmap gives `ask` tool-calling.
- **[SEC]** Secrets hygiene: keys env-only, unexported fields, never logged;
  error bodies capped 2 KB. Fine.
- **[SEC]** Path traversal via `--template`: attacker = victim = same local
  user, no trust boundary. Not worth code.
- **[SRE]** Concurrent `tbuk` processes: WAL + `busy_timeout(5000)` handles
  the realistic case. Fine.
- **[SRE]** Partial-failure exit codes: dir ingest exits non-zero on any
  failure, says how many. Fine.
- **[SRE]** Backup story: roadmap quick win 3 covers it; issue 22
  pre-migration copy covers the upgrade path.
- **[SRE]** Observability: errors carry provider response bodies (2 KB cap);
  adequate for a local CLI.
- **[QA]** Chunker test gaps (why issues 2/3 slipped): fixes for those issues
  already specify the missing mixed-separator + `Size <= 0` tests.
- **[QA]** Goroutine-leak test bounded, not flake risk. Fine.
- **[QA]** `printFileResult`/`printDirResults` at 0%: one-line stdout wrappers
  around tested variants. Not worth code.
- **[PERF]** Hybrid runs vector + keyword legs sequentially: parallelising
  saves single-digit ms. Not worth goroutines.
- **[PERF]** No prepared-statement reuse: per-call prepare overhead = noise
  for a CLI.
- **[PERF]** `stats` aggregates in a single grouped query; no N+1 anywhere.
- **[PERF]** FTS5 index never gets `optimize`: negligible at personal-corpus
  scale.
- **[PERF]** Redundant second SHA256 on auto-preprocess path: dwarfed by
  embedding calls. Not worth code.
- **[DX]** `make` UX: self-documenting, model Makefile. Preserve.
- **[DX]** Zero-setup build/test (~14 s, all green, no services). Preserve.
- **[DX]** No issue/feature templates: fine at single-maintainer stage.
- **[PROD]** `tbuk preprocess` single-file only: acceptable.
- **[PROD]** Guide §6 says preprocess "splits it into chunks": one-word docs
  fix when §6 is touched for issue 12.
- **[MAINT]** License (MIT), release cadence/automation, docs volume all
  healthy.
- **[ECO]** Direct deps all current as of 2026-07-19; `go mod tidy` drift
  CI-enforced; Go toolchain pinned via `go-version-file` consistently; CGO
  disabled keeps the cross-compile matrix dependency-free.
