# Subplan 32: Topics — tag documents, filter retrieval, digest, scoped export

Lands roadmap **#32** (topics) from `docs/plans/next-steps.md`. A *topic* is a
free-form tag attached to a document. Topics gate every retrieval surface
(`search`, `ask`), get their own management commands, power a "tell me
everything about X" digest, and scope `export` to a subset of the knowledge
base.

## Goal

Let the user say *which shelf a document sits on* and then work shelf-by-shelf:

```bash
tbuk ingest ./notes/go --topic go --topic programming   # tag at ingest time
tbuk ingest ./docs --infer-topics                       # derive topics from dir structure
                                                         # docs/food/recipe/soup.md → topics: food, recipe
tbuk topic add ./old-doc.md history                     # tag after the fact
tbuk search "generics" --topic go                       # retrieval scoped to topic(s)
tbuk ask "how do slices grow?" --topic go
tbuk topic list                                         # all topics + doc counts
tbuk topic show go                                      # documents under a topic
tbuk topic digest go                                    # LLM synthesis of everything tagged go
tbuk export ~/backups/go.tar --topic go                 # portable archive of just that slice
```

Success = filters compose with the existing metadata pre-filter, untagged
corpora behave exactly as today (no flag → no change), and a topic-scoped
export is a *valid KB archive* that `tbuk import` restores unchanged.

## Design decisions (and alternatives rejected)

1. **First-class tables, not metadata.** The `metadata` table has
   `PRIMARY KEY (document_id, key)` — one value per key. A document with
   topics `go` *and* `programming` cannot be represented without either
   changing metadata's upsert semantics everywhere (multi-value PK migration)
   or encoding hacks (comma-joined values, `topic:<name>` keys) that break the
   exact-match `find` UX. A `topics` + `document_topics` junction is the
   standard tags shape: multi-topic per doc, cheap counts, rename in one row,
   and the metadata table's single-value contract stays untouched.
2. **Multiple `--topic` values = OR (union).** "Limit to these shelves" is the
   tag expectation; AND-intersection is rare and can be added later behind a
   flag without breaking anything. Topics AND-compose with `Options.Metadata`
   (doc must match the metadata filter *and* carry at least one listed topic).
3. **Filter via `EXISTS` subquery, not JOIN.** A doc carrying two of the
   requested topics would duplicate its chunk rows under a JOIN — duplicate
   ids in the vector heap, duplicate FTS ranks. `EXISTS` filters without row
   multiplication and composes with the existing `metadataFilterJoins`.
4. **Digest = exhaustive fetch + map-reduce, not similarity search.** "All the
   knowledge about X" is a bounded corpus (the topic's chunks), so fetch *all*
   of it deterministically instead of hoping top-K covers it. When it exceeds
   the token budget, summarize in passes (the "multiple searches" the user
   anticipated — multiple LLM calls, not multiple retrievals).
5. **Topic-scoped export stays a KB archive.** Build a temp staging root with
   a *filtered copy* of the database plus only the matching docs'
   extracted/raw files, then reuse `export.Create` → `Verify`. The output is
   byte-format-identical to a full export, so `tbuk import` works unchanged.
   (Rejected: a JSONL/markdown dump — simpler, but not importable, and would
   fork the archive format.)
6. **Names normalized: `strings.ToLower(strings.TrimSpace(name))`**, must be
   non-empty and comma-free (commas are the flag's list separator). Normalize
   at every write and lookup so `Go` and `go` are one topic.
7. **Unknown topic in a filter = hard CLI error** naming the known topics.
   The search layer itself treats it as "matches nothing" (pure filter);
   validating in the CLI turns a silent empty result into a legible typo fix.
8. **Lifecycle:** links cascade with document deletion (FK `ON DELETE
   CASCADE`); a topic row survives at zero documents (visible in
   `topic list`) until `topic delete` removes it. No auto-GC — cheap rows,
   and auto-deleting would forget the vocabulary between re-ingests.
9. **Inferred topics from directory structure (opt-in).** When `--infer-topics`
   flag is passed with directory ingest, derive topic names from directory path
   components (all levels except the top-level root). For
   `docs/food/recipe/soup.md` → topics `food`, `recipe`; for
   `docs/food/nutrition/swede.md` → topics `food`, `nutrition`. Explicit
   `--topic` flags and inferred topics compose (union). Normalization applies
   (paths lowercased, whitespace trimmed). Matches user expectation: "organize
   by folder structure". Only active with flag; default (no `--infer-topics`) →
   no change to existing behavior.

## Schema (migration 002)

Appended to the `migrations` slice in `storage/migrate.go`, same style as 001:

```sql
CREATE TABLE IF NOT EXISTS topics (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT    NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS document_topics (
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    topic_id    INTEGER NOT NULL REFERENCES topics(id)    ON DELETE CASCADE,
    PRIMARY KEY (document_id, topic_id)
);

CREATE INDEX IF NOT EXISTS idx_document_topics_topic ON document_topics(topic_id);
```

`name` stores the normalized (lowercased, trimmed) form.

## Package changes

```
internal/storage/
  topics.go         ← TopicRepo (new)
  topics_test.go
  migrate.go        ← migration 002 appended

internal/search/
  search.go         ← Options.Topics []string
  filters.go        ← topicFilterSQL(topics, docAlias) (EXISTS fragment) beside metadataFilterJoins
  vector.go         ← apply in rankVector's WHERE
  keyword.go        ← apply in the FTS query's WHERE
  hybrid.go         ← copy Topics into the expanded Options

internal/retrieval/
  retriever.go      ← Retrieve takes Filters{Meta map[string]string; Topics []string}
                      (replaces the bare meta param; call sites: ask.go + tests)

internal/cli/
  topic.go          ← `tbuk topic` group: list, show, add, rm, rename, delete, digest
  ingest.go         ← --topic
  search.go         ← --topic
  ask.go            ← --topic; retrieverFn signature follows retrieval.Filters
  export.go         ← --topic (milestone 3)
  init.go           ← install builtin `digest` template

internal/export/
  filter.go         ← staging-root builder for topic-scoped export (milestone 3)
```

### TopicRepo

```go
type Topic struct { ID int64; Name string }
type TopicCount struct { Topic; Documents int }

func NewTopicRepo(db *sql.DB) *TopicRepo

func (r *TopicRepo) Ensure(ctx, name string) (int64, error)        // normalize, upsert by name
func (r *TopicRepo) Tag(ctx, docID int64, topicIDs ...int64) error // idempotent INSERT OR IGNORE
func (r *TopicRepo) Untag(ctx, docID int64, topicIDs ...int64) error
func (r *TopicRepo) ListAll(ctx) ([]TopicCount, error)             // name + doc count, ordered by name
func (r *TopicRepo) ListForDocument(ctx, docID int64) ([]Topic, error)
func (r *TopicRepo) IDsForNames(ctx, names []string) ([]int64, error) // storage.ErrNotFound naming the missing topic
func (r *TopicRepo) Rename(ctx, old, new string) error
func (r *TopicRepo) Delete(ctx, name string) error                 // row + links
func (r *TopicRepo) DocumentsFor(ctx, names []string) ([]Document, error) // union, for show/digest/export
```

Errors wrap `fmt.Errorf("TopicRepo.Method: %w", err)`; misses use
`storage.ErrNotFound`, matching the existing repos. `App` grows a memoized
`Topics()` accessor in the composition root.

### Search filter

```go
// Options gains:
Topics []string // OR-combined topic pre-filter, AND-composed with Metadata

// filters.go:
// topicFilterSQL returns an "AND EXISTS (...)" fragment restricting docAlias
// to documents carrying at least one of the named topics, plus bind args.
// ("", nil) when topics is empty.
func topicFilterSQL(topics []string, docAlias string) (string, []any)
```

Applied in `rankVector` (vector), the FTS query (keyword); `Hybrid` copies
`Topics` into its expanded `Options` exactly as it copies `Metadata` today.
The standalone `Searcher.Metadata` (the `find` command) is untouched.

## CLI surface

All `--topic` flags are `StringSlice` — repeatable and comma-splitting
(`--topic go,programming` ≡ `--topic go --topic programming`).

| Command | Behaviour |
|---|---|
| `ingest <path> [--topic x,y] [--infer-topics]` | after each successful (non-skipped, non-error) file: `Ensure` + `Tag`. Dir ingest tags every ingested file. Explicit `--topic` flags apply to all files. `--infer-topics` derives topics from directory path components (top-level root ignored; `docs/food/recipe/file.md` → `food`, `recipe`); composes with explicit topics (union). Re-ingest without flags leaves existing links alone (doc row is upserted, junction untouched). |
| `search <q> --topic x,y` | validate names via `IDsForNames` → `Options.Topics`. Works in all three modes. |
| `ask <q> --topic x,y` | validate → `retrieval.Filters.Topics`. Empty retrieval falls into the existing no-context warning / `--require-context` path. |
| `topic list [--format]` | names + document counts (text/json). |
| `topic show <name> [--format]` | the topic's documents: path, title, chunk count. |
| `topic add <path> <topic>...` | tag an already-ingested doc (path via `NormalizePath`, miss → `ErrNotFound` message). Creates topics as needed. |
| `topic rm <path> <topic>...` | remove links; topics themselves survive. |
| `topic rename <old> <new>` | one-row update; merge conflict (new exists) → error suggesting add+delete. |
| `topic delete <name> [--yes]` | remove topic + all its links; confirm prompt like `delete`. Documents untouched. |
| `topic digest <name> [...]` | milestone 2, below. |
| `export <path> --topic x,y` | milestone 3, below. |

## Milestone 2 — `topic digest`

"Fetch all the knowledge about topic X" as synthesized prose:

1. `DocumentsFor` + all their chunks in `(document, chunk_index)` order —
   exhaustive, no similarity ranking.
2. Budget = the digest template's `retrieval.max_tokens` (builtin default
   ~8000; `chunking.CountTokens` approximation, as elsewhere).
3. **Fits** → one LLM call: render builtin `digest` template with every chunk,
   stream the answer (existing `RunAsk`-style plumbing, `Sources:` footer
   listing the documents).
4. **Doesn't fit** → map-reduce: batch chunks per document (splitting oversize
   docs) under the budget; summarize each batch with the same template
   (buffered); feed the batch summaries back as pseudo-chunks
   (`Citation = path`) in a final reduce call, streamed. One level of
   reduction; if even the summaries blow the budget, error with counts and
   suggest `--top`-style narrowing rather than recursing silently.
5. Flags: `--template` (defaults `digest`), `--no-stream`, `--format` not
   needed (prose). Progress lines (`summarizing 3/7: <path>`) on stderr.

New builtin `digest` template installed by `init` beside `qa`/`brief`/`anki`:
system prompt asks for a structured overview (themes, key facts, gaps),
citations inline. Core logic lives in an exported `RunDigest(...)` seam with
injected retrieve/chat fns, unit-tested with fakes like `RunAsk`.

## Milestone 3 — `export --topic`

`RunExport` gains `topics []string`. When empty: current behaviour, zero
change. When set:

1. Validate names; resolve the union's document set.
2. Stage under `os.MkdirTemp`: open a fresh DB (`RunMigrations`), copy —
   verbatim ids, via the repos/SQL in Go, no ATTACH — the matching
   `documents`, their `chunks` (insert triggers rebuild `chunks_fts`),
   `metadata`, the referenced `topics` + `document_topics` rows.
3. Copy only the matching docs' `extracted/<sha256>.txt` and `raw/<sha256>.*`
   files; copy `prompts/` wholesale (templates aren't per-topic).
4. Point a staged config at the staging root, then the existing
   `export.Create` → temp file → `export.Verify` → rename pipeline. Cleanup
   via `defer os.RemoveAll`.

The archive is indistinguishable from a full export of a smaller KB, so
`import` (including `--merge` into an existing KB) needs no changes. Document
counts in the summary line (`exported 12 of 240 documents (topics: go)`).

## Testing (TDD, table-driven, ≥85% per package)

- **storage:** migration 002 applies over a v1 DB (and fresh); TopicRepo CRUD;
  normalization (`Go` ≡ `go`); `IDsForNames` miss → `ErrNotFound`; document
  delete cascades links; `Delete`/`Rename` edge cases. In-memory SQLite.
- **search:** topics filter on vector/keyword/hybrid; multi-topic doc returns
  no duplicate chunks; topics+metadata compose (AND); unmatched topic →
  empty; empty `Topics` ≡ today (regression parity).
- **retrieval:** `Filters` passthrough to `search.Options`.
- **cli:** `ingest --topic` links docs (dir + single file; skipped/error files
  untagged); `ingest --infer-topics` derives topics from dir structure
  (path components, top-level root ignored; `docs/food/recipe/file.md` →
  `food`, `recipe`); explicit and inferred topics compose (union); every
  `topic` subcommand; unknown-topic error text; `ask --topic` forwards
  filters (fake retriever asserts); `RunDigest` single-call and map-reduce
  paths with fake chat (batch boundaries, budget edge, reduce overflow error);
  `export --topic` archive contains exactly the filtered rows/files and
  round-trips through `Import`; extend `integration_test.go` (`ingest --topic
  → search --topic → topic list/show → export --topic`; also `ingest
  --infer-topics` on nested dir tree).

## Rollout (one PR per milestone)

1. **feat(topics): core** — migration, TopicRepo, search/retrieval filters,
   `--topic` on ingest/search/ask, `topic` group minus digest.
2. **feat(topics): digest** — `topic digest`, builtin `digest` template.
3. **feat(export): topic-scoped export** — staging-root filter + `--topic`.

Each PR updates `README.md` (quick start, schema, architecture),
`docs/initial-context.md` (schema, CLI list, search/retrieval sections — per
AGENTS.md, before merge), and `docs/user-guide.md`. This plan is archived to
`docs/archive/` in the milestone-3 PR.

## Out of scope (deliberate)

- AND-intersection filter mode (`--all-topics`) — later, additive.
- Hierarchical topics (`go/stdlib`) — names may contain `/` but no tree
  semantics.
- Auto-tagging via LLM classification — a separate idea entirely.
- `find` learning a `topic:` pseudo-key — `topic show` covers it.
- `import` changes — none needed by design.

## Open questions

- Digest template wording / output shape (themes vs. chronological vs. Q&A) —
  iterate on the builtin prompt once wired.
- Should `list`/`stats` surface topics per document / totals? Cheap
  follow-up, not gating.
