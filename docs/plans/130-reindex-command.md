# Plan 130: `tbuk reindex` — re-embed documents from the raw archive

Lands issue [#130](https://github.com/gotofritz/timbuktu/issues/130): a new
command that re-embeds every already-indexed document using whatever content
the knowledge base actually still has custody of — the `raw/` archive by
default, not the document's live filesystem path.

## Goal

```bash
# after switching embedding.provider/model to a different dimension:
tbuk reindex                    # re-embed every document, reading from raw/
tbuk reindex --topic go         # only documents tagged "go"
tbuk reindex --source-dir /mnt/backup/raw   # resolve raw copies from elsewhere
tbuk reindex --dry-run          # list what would be re-embedded, do nothing
```

Success = a knowledge base with a mixed-dimension corpus (see "Why now"
below) becomes consistent again in one command, including for documents
whose original file no longer exists on this machine — the exact case a
`tbuk import`-restored knowledge base is in.

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
6. **`--topic` filters which documents are targeted**, reusing the topics
   feature (#111, merged) rather than inventing a second filter mechanism.
   Exact call site to be confirmed against `internal/storage`'s current
   topic-repo API when implementing (verify method names/signatures then —
   not re-derived here to avoid documenting an interface that has since
   moved).
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
```

Source resolution needs the file's original extension to pick the right
extractor (`preprocess.DetectMIME` keys off extension) — `raw/<sha256><ext>`
already keeps it (per the existing raw-archive naming), so this is a
non-issue, not a gap to design around.

## CLI surface

| Flag | Behaviour |
|---|---|
| `tbuk reindex` | re-embed every document in the KB |
| `--topic x,y` | only documents carrying at least one listed topic (reuses the existing topic-filter semantics — OR/union, matching `search --topic`) |
| `--source-dir DIR` | resolve `<sha256><ext>` under `DIR` instead of `cfg.Ingest.RawDir` |
| `--dry-run` | print what would happen; touch nothing |

Output: one line per document (`re-embedded: <path> (N chunks)` /
`skipped: <path> — no raw copy and live path unreadable` /
`error: <path> — <err>`), then a summary count, matching the
style of `IngestDir`'s existing per-file + summary output.

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
- `make check-ci` before the PR.

## Rollout

One PR, branch `feature/reindex-command` (already created off `main`;
this plan is its first commit). Roughly:

1. `feat(ingest): add reindex operation reading from raw archive`
2. `feat(cli): add tbuk reindex command (--topic, --source-dir, --dry-run)`
3. `docs: document tbuk reindex` (README quick-start table,
   `docs/initial-context.md` CLI list, `docs/user-guide.md` — including the
   embedding-dimension-mismatch troubleshooting row, which currently points
   at `--force` re-ingest without mentioning this command) + archive this
   plan.

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

## Open questions

1. **Exact `TopicRepo`/filter API to call for `--topic`.** Confirm current
   method names/signatures in `internal/storage` (and `search.Options` /
   `retrieval.Filters` if those are the right layer to reuse instead of a
   direct repo call) at implementation time rather than trusting this plan's
   description of a feature that landed separately.
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
