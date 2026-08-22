# Plan 130: raw archive as source of truth — `tbuk reindex`, `tbuk restore`, re-encoding `tbuk import`

Lands issue [#130](https://github.com/gotofritz/timbuktu/issues/130): three
related changes, all variations on one idea — the `raw/` archive, not a live
filesystem path or a trusted-verbatim database, is the durable source of
truth for a document's content, and embeddings should always be derivable
locally from it.

1. **`tbuk reindex`** (new command) — re-embed every already-indexed
   document, reading from `raw/`.
2. **`tbuk restore`** (new command) — today's `tbuk import` behavior,
   renamed and otherwise unchanged: verbatim archive restore, trusting the
   archive's embeddings exactly as exported.
3. **`tbuk import`** (repurposed) — restore, then always re-encode every
   restored document from the *local* machine's embedding config. Becomes
   the safe default for moving a knowledge base between machines with
   different embedding setups; `tbuk restore` is the explicit opt-in for
   "I know source and target configs match, skip re-embedding."

This is a PoC with a single user — no backward compatibility constraint.
`tbuk import`'s behavior changes outright; nothing preserves the old
behavior under the `import` name.

## Goal

```bash
# fixing an existing KB after switching embedding.provider/model:
tbuk reindex                    # re-embed every document, reading from raw/
tbuk reindex --topic go         # only documents tagged "go" (optional — see below)
tbuk reindex --source-dir /mnt/backup/raw   # resolve raw copies from elsewhere
tbuk reindex --dry-run          # list what would be re-embedded, do nothing

# moving a KB to a new machine:
tbuk restore backup.tar         # verbatim — trusts the archive's embeddings exactly
tbuk import backup.tar          # restore, then re-encode every document from
                                 # THIS machine's embedding config (the default choice)
```

Success = a knowledge base with a mixed-dimension corpus — whether from
switching providers on an existing KB, or from importing an archive built
under a different config — becomes consistent again in one command, without
requiring the document's original file to still exist on this machine.

**Topics status:** verified against the current codebase (no `TopicRepo`,
no `Topic` type, no `topics.go` anywhere under `internal/`) — the topics
feature described in `docs/plans/32-topics.md` is **not implemented yet**,
despite that plan sitting in `docs/plans/` alongside a git history commit
titled "topics: core schema and filtering" (that commit touches none of
`internal/storage`, `internal/search`, or `internal/cli` — whatever it
landed isn't the plan-32 implementation, and plan 32 itself is still
unarchived, which per this repo's convention means its work isn't done).
`--topic` is therefore **optional scope** for this plan, not a v1
requirement — see Design decision 6.

## Why now

Discussed while building the `mlx` embedding provider (#106): switching
`embedding.provider`/`embedding.model` changes the output dimension (e.g.
768 → 1024). Existing chunks stay at the old dimension; nothing bulk
re-embeds them. `internal/search/vector.go`'s `rankVector` already detects
this and fails loud (`"query embedding has %d dimensions but stored vectors
have %d"`), and `tbuk doctor`'s `CheckEmbeddingDimension` flags it — but nothing
*fixes* it in one step:

- `tbuk update <path>` — single file only (`cobra.ExactArgs(1)`), and reads
  from the file's *live* path via `Ingester.IngestFile`, not `raw/`.
- `tbuk ingest <dir> --force` — bulk, but requires the user to still have,
  and correctly point at, the original directory tree.

Neither covers a document restored via `tbuk import` from a `.tar`
snapshot: `documents.path` holds the *exporting* machine's original absolute
path (import re-homes the config-declared component directories — db,
extracted, raw, prompts — but does not rewrite the `path` column), so it
typically resolves to nothing on the importing machine. The bytes are right
there in `raw/<sha256><ext>`, content-addressed and untouched — `reindex`
reads from there.

The same mismatch happens *at import time*, not just after: today's
`tbuk import` restores the archive's `chunks` table verbatim, embeddings and
all. Move a knowledge base from a machine running `embedding.provider: llama`
(768-dim) to one running `embedding.provider: mlx` (1024-dim) and the
restored KB is broken on arrival — the exact dimension-mismatch failure mode
described above, just reached via `import` instead of a config edit. Once
`reindex` exists (reading from `raw/`, which import already restores), the
fix is mechanical: run it against whatever `import` just wrote.

## Design decisions (and alternatives rejected)

1. **New command, not an extended `update`.** `update`'s contract is "one
   required file path, skip if SHA256 unchanged." Bolting a no-args
   whole-KB mode onto that means conditional cobra arg validation and two
   unrelated operations sharing one command's help text and flags.
   `reindex` is also semantically different at the core: it doesn't ask "did
   the source change?" (there's nothing to compare against — we *know* the
   content is unchanged, we're deliberately re-deriving embeddings for it
   under a new model). Bundling that into `update` fights its existing
   change-detection framing. (Rejected: `update --all` / `update` with no
   args — see above.)
2. **Default read source is `raw/<sha256><ext>`, not `documents.path`.**
   That is what the raw archive exists for (per `docs/initial-context.md`'s
   Ingestion section: "archive a copy of each ingested source"). Reading
   from it instead of the live path is what makes `reindex` work for
   imported/relocated knowledge bases, which is the actual gap `update`/
   `ingest` leave. Content-addressed by SHA256, already stored on the
   `documents` row — no path-matching or rediscovery logic needed.
3. **Fallback to `documents.path` when no raw copy exists.** The raw archive
   is optional (`ingest.raw_dir` can be empty, or `--no-raw` skips it for a
   given ingest) — a document ingested that way has nothing under `raw/`.
   Fall back to its stored live path (the `ingest`/`update` behaviour today)
   so `reindex` still helps knowledge bases that never enabled the archive,
   rather than refusing to touch them.
4. **Per-document failure is reported, not fatal.** If neither the raw copy
   nor the live path resolves (raw disabled *and* the original file is
   gone), skip that document and note it in the run summary — same pattern
   `IngestDir` already uses for partial-failure resilience (continues past
   one bad file, prints a summary at the end). A single missing document
   must not abort re-embedding the other 999.
5. **`--source-dir DIR` overrides where the raw archive is resolved from —
   not the live-source tree.** Content-addressed lookup
   (`DIR/<sha256><ext>`) needs no path-matching logic and mirrors exactly
   what `raw_dir` already is, just pointed somewhere else for one run (e.g.
   a `raw/` copied in from a backup, or a non-default `raw_dir` from another
   machine's export). Remapping the *live* tree (`~/old-notes` →
   `~/notes`, matching by relative path or basename) was considered and
   rejected: no precedent for that kind of fuzzy remapping anywhere in the
   codebase, and it's ambiguous the moment directory structure changed at
   all. Defaults to `cfg.Ingest.RawDir`.
6. **`--topic` is optional scope, ship `reindex` without it if topics still
   doesn't exist.** Topics (`docs/plans/32-topics.md`) is not implemented in
   the current codebase (verified — see "Topics status" above), so
   `reindex`'s core value (re-embed from `raw/`) must not be blocked on it
   landing first. Two valid sequences, either is fine:
   - `reindex` ships first, without `--topic`; whoever implements topics
     later adds it as a fast-follow (plan 32 now has a section flagging this
     — see "Cross-plan note" there), reusing the same filter semantics
     `search --topic`/`ask --topic` land with.
   - Topics ships first; then `reindex --topic` is a small addition here,
     reusing whatever `TopicRepo`/filter API plan 32 actually produces (not
     re-derived in this plan, to avoid documenting an interface that doesn't
     exist yet and may not match this description once written).
   Either way, `--topic` is additive to `reindex` — the command's initial
   scope is "every document" plus `--source-dir`/`--dry-run` only.
7. **No skip-if-unchanged check.** Unlike `ingest`/`update`, `reindex` always
   re-embeds every document it targets. There is nothing meaningful to
   compare against — the source content is presumed unchanged; the trigger
   is a *provider/model/config* change, which `reindex` cannot see on the
   stored chunk itself (embeddings don't carry provenance). Running it is
   already an explicit, deliberate choice.
8. **`--dry-run` lists targeted documents and their resolved source (raw vs.
   fallback vs. unresolvable) without embedding anything** — cheap
   confidence check before spending API calls/compute on a potentially
   large corpus, same rationale as `export`'s and `import`'s existing
   confirm-before-acting patterns elsewhere in the CLI.

### Import & restore

9. **Today's verbatim-restore behavior becomes `tbuk restore`, unchanged.**
   Same `internal/importer.Import` / `RunImport` logic, same
   `--merge`/`--force-config`/`--force-data` flags, same everything — just
   reached by a different command name. No flag on `import` preserves this;
   it moves to its own command outright (no backward-compat constraint —
   see the plan header).
10. **`tbuk import` (new) = `tbuk restore`'s exact restore step, then
    reindex only the documents that step just wrote.** No new
    partial-restore mode in `internal/importer` — reuse the full verbatim
    restore unchanged (it already writes `raw/`, `extracted/`, and every DB
    row including chunks), then run the reindex-per-document primitive
    (from decision 1) against just those documents. `ChunkRepo
    .ReplaceForDocument`'s existing per-document atomicity means the
    freshly-restored (possibly wrong-dimension) chunks are replaced one
    document at a time — no bulk "wipe chunks" step needed. (Rejected: a
    new narrower "restore identity rows only, skip chunks" mode in
    `importer` — strictly more new code than composing two operations that
    already exist, for the same end state.)
11. **The reindex step is scoped to just-restored documents, not the whole
    KB.** Needed so `tbuk import --merge` into a large existing KB doesn't
    silently re-embed everything already there — only what the archive just
    added/overwrote. Requires `importer.Import`'s result to report which
    document IDs it wrote, and the ingest-package reindex primitive to
    accept an explicit document list as a third calling shape alongside
    "all" and "by topic" (`tbuk reindex` the CLI command keeps defaulting to
    "all" — this is an additional internal caller, not a new CLI flag).
12. **`extracted/` restores as part of the verbatim step; the reindex step
    leaves it alone.** Extraction is deterministic given the same source
    bytes and extractor version, so re-running it on every import buys
    nothing. If the archived `extracted/<sha256>.txt` is present, reindex's
    existing "use the cache if present, else auto-preprocess from `raw/`"
    behavior (decision 2/3's territory) picks it up for free.
13. **`tbuk import` re-encodes unconditionally — no opt-out flag.**
    Considered and rejected a flag (e.g. `--trust-embeddings`) to skip the
    reindex step and keep the archive's chunks as-is: the expensive part of
    either path is the embedding API calls, and a flag that skips them only
    saves anything when source and target already share identical embedding
    config — at which point `tbuk restore` already does exactly that,
    faster, with no reindex logic involved at all. A flag on `import`
    duplicating that case is pure surface area for no new capability.

## Package changes (sketch — confirm exact signatures when implementing)

```
internal/ingest/
  reindex.go          ← new: Reindexer (or a method on Ingester) that, given
                         a Document (path, sha256) and a resolved source file,
                         extracts → chunks → embeds → ReplaceForDocument,
                         bypassing the live-path SHA256-recompute/dedup logic
                         IngestFile uses (we already know the sha256; we are
                         not detecting a change)
  reindex_test.go

internal/cli/
  reindex.go           ← `tbuk reindex` command: resolves target documents
                         (all, or via --topic), resolves each one's source
                         file (raw/<sha256><ext> under --source-dir or
                         cfg.Ingest.RawDir, falling back to documents.path),
                         calls the ingest-package reindex operation per
                         document, prints a per-document summary (ok /
                         skipped-unresolvable / error) and a final count
  reindex_test.go

  restore.go            ← renamed from today's import.go (RunImport /
                         newImportCmd); behavior unchanged
  restore_test.go        ← renamed from import_test.go; assertions unchanged

  import.go              ← new: tbuk import — calls the same restore logic
                         as restore.go, collects the document IDs it wrote,
                         then calls the ingest-package reindex primitive
                         (document-list shape) scoped to exactly those IDs
  import_test.go

internal/importer/
  importer.go            ← Import's result gains a field reporting which
                         document IDs/rows were written, so the CLI layer
                         can scope the reindex step correctly under --merge
```

Source resolution needs the file's original extension to pick the right
extractor (`preprocess.DetectMIME` keys off extension) — `raw/<sha256><ext>`
already keeps it (per the existing raw-archive naming), so this is a
non-issue, not a gap to design around.

## CLI surface

| Flag | Behaviour |
|---|---|
| `tbuk reindex` | re-embed every document in the KB |
| `--topic x,y` | **optional scope** — only documents carrying at least one listed topic, OR/union, matching whatever semantics `search --topic`/`ask --topic` ship with. Add only once topics (`docs/plans/32-topics.md`) actually exists; omit from v1 if it doesn't yet — see Design decision 6 |
| `--source-dir DIR` | resolve `<sha256><ext>` under `DIR` instead of `cfg.Ingest.RawDir` |
| `--dry-run` | print what would happen; touch nothing |

Output: one line per document (`re-embedded: <path> (N chunks)` /
`skipped: <path> — no raw copy and live path unreadable` /
`error: <path> — <err>`), then a summary count, matching the
style of `IngestDir`'s existing per-file + summary output.

| Command | Behaviour |
|---|---|
| `tbuk restore <archive>` | verbatim archive restore — today's `import` behavior, unchanged. `--merge`/`--force-config`/`--force-data` as before. |
| `tbuk import <archive>` | restore (identical to `restore`), then reindex only the documents that restore step just wrote, from the local embedding config. Same restore-step flags as `restore`; no flag opts out of the reindex step. |

## Testing (TDD, table-driven, ≥85% per package)

- **ingest:** reindex-from-raw happy path (fake raw dir with a `<sha256><ext>`
  file, asserts extraction/chunking/embedding runs against *that* file, not
  any live path); fallback to `documents.path` when no raw copy exists;
  unresolvable document (no raw copy, live path also gone) returns a typed
  per-document error rather than aborting; re-embed replaces existing chunks
  atomically (reuse `ChunkRepo.ReplaceForDocument`, same atomicity guarantee
  `update`/`ingest --force` already rely on).
- **cli:** `reindex` with no flags targets every document; `--topic` narrows
  the set (fake/seeded topic links); `--source-dir` overrides resolution
  path; `--dry-run` performs no writes and no embed calls (fake embedder
  asserts zero invocations); per-document failure doesn't stop the run
  (seed one resolvable + one unresolvable document, assert both are
  reported and the resolvable one still succeeds); summary line counts are
  correct. Extend `integration_test.go` if a real end-to-end
  ingest → switch dimension → reindex → search round-trip is cheap to add.
- **importer:** `Import`'s result reports the document IDs/rows it wrote —
  assert this directly (new or extended test) since `tbuk import`'s
  reindex-scoping depends on it.
- **cli (restore):** today's `import_test.go` assertions, renamed and
  retargeted at `restore` — behavior must be byte-identical to what `import`
  does today.
- **cli (import, new):** fresh target — restore then reindex touches every
  restored document (fake embedder call count == restored doc count;
  stored chunk embeddings end up at the *local* config's dimension even
  though the archive was built under a different one — this is the named
  motivating scenario, worth its own test); `--merge` into an existing
  non-empty KB reindexes only the newly-written/overwritten documents, not
  pre-existing ones (fake embedder call count excludes pre-existing docs);
  a failure during the restore step aborts before any reindexing starts
  (fail fast — never partially reindex a broken restore).
- `make check-ci` before the PR.

## Rollout

One PR, branch `feature/reindex-command` (already created off `main`;
this plan is its first commit). Roughly:

1. `feat(ingest): add reindex operation reading from raw archive`
2. `feat(cli): add tbuk reindex command (--source-dir, --dry-run)` — plus
   `--topic` in the same commit only if topics has landed by then (see
   Design decision 6); otherwise a separate fast-follow once it does
3. `feat(cli)!: rename tbuk import's verbatim restore to tbuk restore` —
   breaking change, no back-compat shim (PoC, single user, explicit
   instruction — see plan header)
4. `feat(importer): report written document IDs from Import`
5. `feat(cli): tbuk import restores then re-encodes from local config`
6. `docs: document tbuk reindex, tbuk restore, and the new tbuk import`
   (README quick-start table, `docs/initial-context.md` CLI list and Import
   section, `docs/user-guide.md` — including the embedding-dimension-mismatch
   troubleshooting row, which currently points at `--force` re-ingest
   without mentioning any of these three commands) + archive this plan.

Commits 1–2 (reindex) and 3–5 (restore/import) are independently useful and
could ship as separate PRs if that proves easier to review — noted here as
one PR for now since they share the same underlying primitive and this is a
single small branch.

## Out of scope (deliberate)

- Auto-detecting a dimension mismatch and prompting to run `reindex` —
  `tbuk doctor` already surfaces the mismatch; wiring an automatic
  suggestion is a separate, smaller follow-up once `reindex` exists.
- Re-deriving a moved live-source tree (`--source-dir` pointing at a new
  document root rather than a raw archive) — see Design decision 5.
- Changing what `ingest.raw_dir` / `--no-raw` do — `reindex` is a consumer
  of the existing archive, not a change to how it's populated.
- Any change to `update` or `ingest <dir>` — they keep their current,
  narrower semantics; `reindex` is additive.
- A partial-restore opt-out flag on `tbuk import` — see Design decision 13;
  `tbuk restore` already covers the "trust the archive" case.

## Open questions

1. **Whether `--topic` ships in the first PR at all.** Depends entirely on
   whether topics has landed by the time this is implemented — check
   `internal/storage` for `TopicRepo`/`Topic` fresh at implementation time
   (this plan's "not implemented yet" is current as of the date this plan
   was written, not a permanent fact). If it has landed, confirm its actual
   method names/signatures and filter-layer shape (`search.Options` /
   `retrieval.Filters`) before wiring `--topic` in, rather than trusting this
   plan's earlier (and already-corrected-once) assumptions about an
   interface documented in a different plan.
2. **Should `reindex` also refresh automatic metadata** (`filename`,
   `extension`, `mime`, `dir` — normally refreshed on every ingest per
   `docs/initial-context.md`)? Likely yes, for consistency with `ingest`/
   `update`, but confirm it doesn't clobber anything topic/user-metadata
   related that shouldn't move.
3. **Progress output for large corpora.** A KB with thousands of documents
   re-embedding serially could take a while — worth a progress line (`3/500`)
   from the start, or is the final summary enough? Lean toward a progress
   line given `ingest`'s existing per-file verbose output precedent, but not
   gating.
4. **Exact shape of `importer.Import`'s written-document-IDs report.** An
   additive field on whatever its current result type is, versus a repo
   query keyed on restore-start timestamp — check the current type at
   implementation time and pick whichever needs less new plumbing.
5. **Copy-rename vs. refactor for `restore.go`/`restore_test.go`.**
   Implementation-sequencing detail, not a design fork — likely just a
   straight rename in one commit (decision 9), with the new `import.go`
   building on top in a later commit.
