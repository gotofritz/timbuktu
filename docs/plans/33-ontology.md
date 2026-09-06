# Subplan 33: One label substrate — topics, entities, and a knowledge graph

Lands roadmap **#33** (hybrid RAG: vector search for similarity, graph
traversal for exact relationships) from `docs/plans/next-steps.md`, and
**revises plan 32's storage design** so that topics and entities are one
mechanism rather than two. Roadmap **#34** (ontology-guided chunking) is not in
this plan.

> Numbering: the archived `2026-09-05-2317-c9baa8f-33-context-guard.md` reused
> "33" for a different item (roadmap #2 / issue [#141](../../../../issues/141)).
> This plan is the roadmap's #33.

This replaces the generic brief that previously sat at this path. That brief
surveyed the ontology/KG design space; what follows is scoped to *this*
codebase, *this* corpus size, and *one* user.

---

## Settled first: does the ontology replace topics? Do we need both?

**One system, two settings. Not two features, and not one feature pretending
to be the other.**

The two scenarios that prompted the question are the two ends of the same dial:

- **A — a Helix editor knowledge base.** The labels are `search`,
  `multi-cursor`, `keybindings`, `lsp`. Flat, a dozen of them, no relationships
  worth modelling. Filtering and "tell me everything about X" is the whole job.
- **B — an energy-industry knowledge base.** The labels are `PPA`, `Substation`,
  `Turbine`, `Operator`, `Grid` — typed, with real relations (`supplies`,
  `operated_by`, `located_in`) that a question actually traverses.

The mistake is to read these as *topics* and *ontology*. What actually differs
between them is two independent axes:

| | **Where the label attaches** | **Whether the label is typed** |
|---|---|---|
| **A (helix)** | document — "this page is about multi-cursor" | no — a flat name is enough |
| **B (energy)** | mention — "this chunk names Dogger Bank" | yes — class + relations |

Those axes are orthogonal, and the four cells are all reachable from one
model. A "topic" is **a label asserted at document grain with no type**. An
"entity" is **a label derived at mention grain with a type**. They are the same
row in the same table, differing in how they got there and what they hang off.

So the design is:

```
                     one vocabulary  (labels)
                            |
        +-------------------+-------------------+
        |                                       |
  asserted, document grain              derived, mention grain
  "this doc sits on this shelf"      "this chunk names this thing"
        |                                       |
   scenario A, and the filter          scenario B, and the graph
   for scenario B too                            |
                                          typed → triples → traversal
```

### Scenario A, concretely

Nothing beyond milestone 1. No seed file, no classes, no LLM:

```bash
tbuk ingest ./helix-docs --topic helix
tbuk topic add book/src/usage.md multi-cursor selections
tbuk search "add cursor below" --topic multi-cursor
tbuk digest --topic multi-cursor
```

Identical to plan 32's UX. The rows underneath are `labels` +
`label_documents` instead of `topics` + `document_topics`, which costs nothing
today and is the whole point (see "What this changes for plan 32").

**A gets sharper for free if it wants to.** Helix's docs are big pages covering
many features, so document-grain labels are coarse: `--topic multi-cursor`
admits all of `usage.md`, search sections included. Feed the same label names
to the gazetteer and you get mention grain over the identical vocabulary:

```bash
tbuk graph build              # gazetteer over existing label names, no LLM
tbuk entity show multi-cursor # the exact chunks, not the whole page
```

Opt-in, deterministic, reversible. The user never learns a second vocabulary.

### Scenario B, concretely

B is A plus a seed file plus two more tables' worth of behaviour:

```bash
tbuk graph build --llm                       # relations, opt-in
tbuk entity show "Dogger Bank" --hops 2
tbuk ask "who operates the turbines at X?" --expand-entities
```

And B still uses document-grain labels for the things that are not in the text
— `--topic internal`, `--topic regulator-filings` — because provenance is a
shelf, not a concept.

### Configuration comparison

| | A: helix | B: energy |
|---|---|---|
| Classes | none (untyped labels) | `Turbine`, `PPA`, `Substation`, `Operator`… |
| Predicates | none | `supplies`, `operated_by`, `located_in`… |
| `ontology.yaml` | not needed | required |
| Attachment | asserted, document grain | derived mentions + asserted shelves |
| LLM in the loop | none | optional, and only for triples |
| Commands | `topic add/list/show`, `search --topic`, `digest --topic` | + `graph build`, `entity show`, `--expand-entities` |
| Milestones | 1 (–3 for digest and export) | 1, then 4–6 |

### The three things that only document-grain assertion can do

Worth stating so the unified model is not mistaken for "entities can replace
everything":

1. **Labels that are not in the text.** "my work notes", "official docs",
   "stuff I disagree with". No extractor finds these, at any price.
2. **A deterministic, hand-editable document set** — what `export --topic` and
   `reindex --topic` need. A confidence-thresholded extraction changes when you
   re-run the build; an asserted row does not.
3. **`--infer-topics` from directory structure** — provenance about where a
   document came from, unrecoverable from content.

And the one thing only mention grain can do: connect two documents that never
mention each other, which is the entirety of scenario B's value.

---

## What this changes for plan 32

Plan 32 is **documentation only** — `625cd10` landed the markdown; there is no
`topics` table, no `TopicRepo`, no `--topic` flag in the tree today. That
timing is the reason this plan is worth reading before
[#115](../../../../issues/115) is built:

> **Build [#115](../../../../issues/115) on `labels` + `label_documents`, not on
> `topics` + `document_topics`.** The two schemas are the same size and the same
> work. Shipping the separate one costs a migration and a rewrite the day
> scenario B arrives; shipping the shared one costs nothing now.

Everything else in plan 32 stands unchanged — the `EXISTS` filter (its design
decision 3), OR semantics, normalization, lifecycle, digest map-reduce, scoped
export, `reindex --topic`. Only the table names and the repo name change:
`TopicRepo` becomes `LabelRepo`, and every call site in plan 32 passes `""` for
the label type.

### So does plan 32 go?

**No.** The topics feature is untouched, and plan 32 keeps everything this plan
does not restate: the digest map-reduce (budget, batching, the reduce-overflow
error), the export staging-root procedure, `--infer-topics` derivation, the
`reindex --topic` cross-plan obligation, its nine design decisions, its CLI
behaviour table and its tests. None of that has an equivalent here, and issues
[#115](../../../../issues/115)–[#117](../../../../issues/117) still point at it.

What goes is plan 32's **schema section** and its independence from this one.
It has been amended in place: `labels`/`label_documents`/`label_aliases`
instead of `topics`/`document_topics`, `LabelRepo` instead of `TopicRepo`,
`schemaSQL` in place instead of "migration version 3", and D10's rule for
`topic list`. Everything else in it reads exactly as before.

Ownership is therefore:

| | Owner | Issues |
|---|---|---|
| Vocabulary tables, document grain, filter, topic CLI, digest, scoped export | **plan 32** | #115, #116, #117 |
| Seed ontology, mention grain, triples, graph CLI, entity expansion | **plan 33** (here) | not yet filed |

The rollout table below shows both, so the sequence is readable in one place;
rows 1–3 are plan 32's to build and plan 32's to archive.

Two further corrections to plan 32 while someone is in there:

- Its "Schema (the next migration) … version 3" section predates the collapse
  to a single migration. New tables go into `schemaSQL` in place with a
  throwaway `scripts/` script (see D8).
- Its own open question — "are topics and metadata labels one thing or two?" —
  is answered by its **answer 1** and is unaffected by this plan. `metadata`
  states a fact *about* a document (`author`, `year`); a label says what the
  document is *about*. Different objects, and neither is this plan's business.

---

## Goal

Give the corpus a second index that is about *things* rather than about
similarity: which labels the documents carry, which entities their chunks
mention, how those entities relate, and which chunk is the evidence for each
claim.

Success = scenario A is served with no model in the loop and no vocabulary file,
scenario B rides the same tables and commands, and `--expand-entities` is only
wired into retrieval once it is measured to help.

**Reference corpora.** Milestones 1–3 are validated against the Helix editor
documentation — small, public, and made of large multi-feature pages, which is
exactly the shape that shows whether document-grain labels are too coarse.
Milestones 4–6 are validated against an energy-industry corpus (turbines,
operators, substations, PPAs), where the relations are real and a traversal has
something to find. Both are named in `docs/plans/next-steps.md` beside their
roadmap entries.

---

## Design decisions (and alternatives rejected)

### D1. One vocabulary table, two attachment tables

`labels` holds every name the user or the extractor knows. `label_documents`
attaches a label to a document (asserted). `label_mentions` attaches it to a
byte range in a chunk (derived). `triples` relates two labels and cites chunks
as evidence.

A label's `type` is empty for scenario A and a class name for scenario B.
Nothing about a scenario-A label is more expensive than plan 32's topic row.

**Every label in the vocabulary was named by a human.** The gazetteer only
matches names it was given, and the LLM extractor drops anything it cannot
resolve to an existing row (D4) — so no extractor ever invents vocabulary, and
the table needs no `source`/provenance column to tell hand-written names from
machine-invented ones. `type` therefore says exactly one thing: *which* human
input the name came from — `''` for one typed at the CLI, a class name for one
read from `ontology.yaml`. That is what makes it a sound discriminator for
`topic list` vs `entity list` (D10).

Rejected: separate `topics`/`entities` tables. Two vocabularies that neither
compose nor convert, two sets of management commands, and a user who has to
decide which one `multi-cursor` is. That decision has no right answer — it is
genuinely both.

### D2. The ontology is a hand-written seed file, not an induced artifact

The biggest cut against the original brief, and the one that makes the rest
affordable. The brief proposed inducing classes, relations, hierarchy,
domain/range and a `candidate → approved → deprecated` lifecycle from
aggregated corpus evidence. For a single-user proof of concept with no
evaluation harness, that is a large machine for producing a vocabulary nobody
asked for, and every one of its states is a place for a bad extraction to
become authoritative.

Invert it. The vocabulary is a small YAML file the user owns, living under the
data root like every other data path — and **absent entirely in scenario A**.

`tbuk init` does **not** scaffold it: an unexplained vocabulary file in the
root is noise for the majority of knowledge bases, which never type a label.
`tbuk graph init` writes a commented starter on request, and `graph suggest`
(D3) fills it from what the corpus actually says. A missing file is a valid
state, not an error.

```yaml
# ~/.tbuk/ontology.yaml — scenario B only; scenario A never creates this file
classes:
  - name: Turbine
    aliases: [wind turbine, WTG]
  - name: Operator
  - name: Substation
predicates:
  - name: operated_by
    domain: Turbine
    range: Operator
    aliases: [run by, managed by]
  - name: connects_to
    domain: Turbine
    range: Substation
labels:                      # optional seeds; the gazetteer matches these
  - name: Dogger Bank
    type: Turbine
    aliases: [Dogger Bank A, DBA]
```

Consequences, all good:

- The vocabulary is **closed and small**, so extraction *validates against it*
  and drops everything else. Junk never reaches the database.
- Diffable, hand-editable, versioned by whatever the user versions their root
  with. No lifecycle table, no approval states, no ontology-version column.
- "Induction" degrades to a **reporting** step (D3) that prints and writes
  nothing.
- `domain`/`range` are the only constraints kept, and only because they are a
  free validity check on an extracted triple. `inverse_of`, `transitive`,
  `symmetric`, `disjoint` and `subClassOf` reasoning: dropped. No OWL, no
  reasoner.

Rejected: `ontology_classes` / `ontology_properties` / `ontology_hierarchy`
tables. They buy nothing a file does not, and cost a schema change per idea.

### D3. `graph suggest` prints; it never writes

The useful half of induction is "here are the terms your corpus keeps using
that your vocabulary has no name for". That is a frequency report over unmatched
capitalised n-grams and unmatched predicate phrases, printed as a diff the user
can paste into `ontology.yaml` — or, in scenario A, as `tbuk topic add` lines:

```
$ tbuk graph suggest --min 25
# candidate labels (unmatched, ≥25 occurrences)
  labels:
    - name: Auto Scaling Group   # 817 occurrences, 41 documents
    - name: Launch Template      # 392 occurrences, 22 documents
# candidate predicates near known labels
    - name: member_of            # 183 occurrences
```

No approval workflow, because there is nothing to approve — the file is the
approval.

### D4. Extraction is deterministic first, LLM second and optional

`internal/llm.LLM` streams tokens and nothing else — no JSON mode, no
structured output, no tool calling — and the default provider is a local MLX
model. Making the feature *depend* on that model emitting valid JSON several
thousand times is not a foundation.

Two extractors behind one interface:

```go
type Extractor interface {
    Extract(ctx context.Context, chunk storage.Chunk) (Result, error)
}
```

1. **Gazetteer (default, no LLM).** Match label names and aliases against
   `chunks.text`, case-folded, on word boundaries. Shortlist candidate chunks
   with an FTS5 `MATCH` over the alias terms first, then exact-scan only the
   shortlist for byte offsets — the index is already there and already tuned.
   `searchtext.Reduce`'s split forms give code identifiers extra aliases for
   free. 100% precision on known names, zero cost, fully reproducible. **This is
   what makes scenario A's "sharper" mode free:** the label list *is* the
   gazetteer.
2. **LLM (opt-in, `graph build --llm`).** Buffers the token stream (the
   `--no-stream` path in `ask.go` already does this), parses JSON tolerantly,
   then **hard-validates every triple against the seed**: unknown predicate →
   drop; subject/object type violating `domain`/`range` → drop; label not
   resolvable to a seed or existing row → drop. Reports the drop rate at the end
   of the run. A model that cannot hold the format degrades to zero rows, not to
   garbage rows.

Rejected: LLM-only extraction — unusable offline, unreproducible, and the whole
feature dies when the local model has a bad day.

### D5. The retrieval filter reads asserted rows by default

`search --topic x` filters on `label_documents` with an `EXISTS` subquery
(plan 32's design decision 3, unchanged: a JOIN would multiply chunk rows).
Mention-grain rows do **not** silently widen a filter — a filter's value is that
it is exact and free, and a derived one is neither.

`--topic-mentions` opts into the union for a corpus where mention grain is the
better filter (scenario A sharpened). Off by default.

### D6. Graph expansion returns chunks, not a facts block (in v1)

The brief wants a structured `Entity / Type / Relationships` block handed to the
model alongside the retrieved text. That is a new prompt shape, a new
interaction with the context-window guard that just shipped
([#147](../../../../issues/147)), and a new groundedness question — a triple is
a claim; only its evidence chunk is a source.

v1 therefore expands the *chunk set*: detect labels in the question, traverse
≤`hops` in `triples`, collect the evidence chunks of the traversed triples, and
merge them into the retrieved set below the vector/keyword hits. They flow
through the existing citation, `squeeze` compaction and budget machinery
untouched, and every added chunk is a real quotable source.

Honest cost: v1 cannot answer a relation question whose answer is spread across
two chunks that never co-occur — it can only put both chunks in front of the
model, which is most but not all of the value. A `Facts:` template block is a v2
decision, taken after D7's measurement, not before.

### D7. Retrieval wiring is gated on measurement, with a kill criterion

Entity expansion adds chunks. Adding chunks trades precision for recall, and
this repo has no way to tell which way that trade lands — the retrieval eval
split (#30 / [#126](../../../../issues/126)) is still open.

Milestones 1–3 change **no** retrieval behaviour beyond the label filter and are
useful on their own (`entity show`, `graph stats`, `graph suggest` are
corpus-browsing tools). Milestone 4 wires expansion in behind an off-by-default
flag, and must land with either eval numbers or a documented manual A/B over ≥20
questions.

> **Kill criterion.** If expansion does not beat the baseline on recall@10
> without losing more than 5 points of precision@5, ship milestones 1–3, delete
> the expander, and record the result here. Scenario A is unaffected either way.
> That is an acceptable outcome, not a failure.

### D8. Schema is edited into `schemaSQL` in place, not a new migration

Per AGENTS.md ("proof of concept") and the standing comment in
`internal/storage/migrate.go`: `schemaVersion` stays 2, the new tables go into
`schemaSQL`, and an existing knowledge base is brought forward by a throwaway
script under `scripts/` — the route `search_text` and the FTS tokenizer both
took. Deleted once it has done its job.

### D9. SQLite, recursive CTE, no graph database

Consistent with the brief and with the repo: pure-Go `modernc.org/sqlite`, no
CGO. Traversal is a `WITH RECURSIVE` over `triples` with a depth cap and a
visited set. At this corpus scale — vector search is still an O(n) scan,
[#127](../../../../issues/127) — a dedicated graph store is unjustifiable.

---

### D10. `topic list` lists what `--topic` can filter on; `entity list` lists what the corpus knows

Two commands over one table, split by the question each answers rather than by
an arbitrary display preference:

- **`topic list` — "what can I pass to `--topic`?"** Untyped labels, plus any
  typed label carrying at least one document attachment. The second clause is
  the whole reason this is a rule and not just `WHERE type = ''`: a scenario-B
  user who tags documents with a typed label (`topic add contract.pdf "Dogger
  Bank"`) has made it a usable filter value, and a command whose job is to
  enumerate filter values must not hide it. Zero-attachment untyped labels stay
  listed — plan 32's decision 8 keeps the vocabulary between re-ingests.
- **`entity list` — "what does the corpus know about?"** Typed labels grouped by
  class, because a flat list of three thousand turbines is not a list.

In scenario A every label is untyped, so `topic list` prints all of them and
`entity list` is empty — indistinguishable from plan 32. In scenario B the
shelves and the entities separate on their own, with no flag to learn. The two
sets only overlap where the user deliberately made them overlap.

```
$ tbuk topic list                          # A, and B's shelves
TOPIC           DOCS  MENTIONS
helix             41         -
multi-cursor       3       117
search             5       284
scratch            0         -

$ tbuk entity list                         # B
Turbine (312)
  Dogger Bank A            88 mentions   12 docs
  Hornsea Two              51 mentions    9 docs
Operator (14)
  Ørsted                  203 mentions   31 docs
```

`MENTIONS` reads `-` when no extraction run has covered the corpus, so scenario
A never sees a column of confusing zeros, and scenario A *sharpened* (D4's
gazetteer over the same names) gets its payoff visible in the table it already
reads. A `TYPE` column appears only when a typed row is in the output.

`topic list --all` flattens the entire vocabulary into one table with `TYPE`
filled in — for audit and grep, not for daily use. `entity list --type T`
narrows to one class. `--format json` emits flat records in both, since the
grouping is a presentation choice and a script wants rows.

Rejected: `topic list` showing everything (three thousand rows in scenario B);
`topic list` showing untyped only (hides a filter value the user created);
splitting on attachment kind rather than type (a label with both grains has no
home, and scenario A sharpened puts every label in that state).

---

## What this plan drops from the brief, and why

| Brief section | Verdict | Why |
|---|---|---|
| §3 Stage B ontology induction | → `graph suggest`, print-only (D3) | The vocabulary is the user's (D2) |
| §11 candidate/approved/deprecated lifecycle | Dropped | No induced state to govern once D2 holds |
| §10 ontology versioning | Dropped | The YAML file is versioned by the user's VCS |
| §1 `inverse_of`, `transitive`, `symmetric`, `disjoint` | Dropped | No reasoner; `domain`/`range` kept as a free validity check |
| §1 `subClassOf` hierarchy | Deferred | Nothing in v1 traverses it; revisit if `entity show` wants roll-up |
| §14 structured facts block for the LLM | Deferred to v2 | Prompt shape + context-guard interaction (D6) |
| §6 `ontology_*` tables | Dropped | Replaced by the seed file (D2) |
| Roadmap #34 ontology-guided chunking | Out of scope | Its own plan, and it needs this one first |

Kept in full: entity/relation extraction, normalization and resolution,
provenance on every row, hybrid retrieval, incremental re-runs, replaceable LLM.

---

## Schema

Appended to `schemaSQL` in `internal/storage/migrate.go` (D8). Milestone 1
ships the first two tables — the same footprint plan 32 was going to spend on
`topics` + `document_topics`.

```sql
-- The one vocabulary. A scenario-A topic is a row with type=''.
CREATE TABLE IF NOT EXISTS labels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Normalized (lowercased, trimmed, whitespace-collapsed) match key.
    name       TEXT    NOT NULL,
    -- As the user or seed spells it; what the CLI prints.
    display    TEXT    NOT NULL,
    -- '' for an untyped topic, else a class name from ontology.yaml. Validated
    -- on write, not by FK: the vocabulary lives in a file, so a class the user
    -- deletes must leave its rows readable rather than orphaning them.
    type       TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    UNIQUE(name, type)
);

-- Asserted, document grain. This is plan 32's document_topics.
CREATE TABLE IF NOT EXISTS label_documents (
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    label_id    INTEGER NOT NULL REFERENCES labels(id)    ON DELETE CASCADE,
    PRIMARY KEY (document_id, label_id)
);

CREATE INDEX IF NOT EXISTS idx_label_documents_label ON label_documents(label_id);

CREATE TABLE IF NOT EXISTS label_aliases (
    label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    alias    TEXT    NOT NULL,          -- normalized
    PRIMARY KEY (alias, label_id)
);

-- Derived, mention grain. Milestone 1 (gazetteer) onward.
CREATE TABLE IF NOT EXISTS label_mentions (
    chunk_id   INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    label_id   INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    start_byte INTEGER NOT NULL,
    end_byte   INTEGER NOT NULL,
    run_id     INTEGER NOT NULL REFERENCES extraction_runs(id) ON DELETE CASCADE,
    PRIMARY KEY (chunk_id, label_id, start_byte)
);

CREATE INDEX IF NOT EXISTS idx_label_mentions_label ON label_mentions(label_id);

-- Scenario B. Milestone 3 onward.
CREATE TABLE IF NOT EXISTS triples (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    subject_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    predicate  TEXT    NOT NULL,         -- validated against ontology.yaml
    object_id  INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    UNIQUE(subject_id, predicate, object_id)
);

CREATE INDEX IF NOT EXISTS idx_triples_subject ON triples(subject_id);
CREATE INDEX IF NOT EXISTS idx_triples_object  ON triples(object_id);

-- Provenance. A triple whose last evidence row goes with a deleted chunk is
-- removed by the build, so a fact never outlives the text that said it.
CREATE TABLE IF NOT EXISTS triple_evidence (
    triple_id  INTEGER NOT NULL REFERENCES triples(id) ON DELETE CASCADE,
    chunk_id   INTEGER NOT NULL REFERENCES chunks(id)  ON DELETE CASCADE,
    confidence REAL    NOT NULL DEFAULT 1.0,
    run_id     INTEGER NOT NULL REFERENCES extraction_runs(id) ON DELETE CASCADE,
    PRIMARY KEY (triple_id, chunk_id)
);

CREATE TABLE IF NOT EXISTS extraction_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at  TEXT    NOT NULL,
    finished_at TEXT    NOT NULL DEFAULT '',
    extractor   TEXT    NOT NULL,          -- "gazetteer" | "llm:<provider>/<model>"
    chunks_seen INTEGER NOT NULL DEFAULT 0,
    dropped     INTEGER NOT NULL DEFAULT 0, -- failed seed validation (D4)
    notes       TEXT    NOT NULL DEFAULT ''
);
```

`type` and `predicate` are text validated against the seed at write time rather
than foreign keys, deliberately: the vocabulary is a file, and a user editing
that file must not corrupt the database. `graph stats` reports rows whose type
or predicate has left the seed, so the drift is visible.

Deleting a document already cascades to its chunks, which now cascades to
mentions and evidence — a fact dies with its source. A label row survives at
zero attachments until `topic delete` removes it (plan 32's decision 8,
unchanged).

---

## Package changes

```
internal/storage/
  labels.go          ← LabelRepo (vocabulary + both attachment grains)
  labels_test.go
  graph.go           ← TripleRepo, RunRepo; Traverse (recursive CTE)
  graph_test.go
  migrate.go         ← new tables appended to schemaSQL

internal/ontology/
  seed.go            ← Seed: Load(path), Validate(), class/predicate lookup, Normalize()
  seed_test.go
  gazetteer.go       ← gazetteer extractor: FTS shortlist → exact offset scan
  gazetteer_test.go
  llmextract.go      ← LLM extractor: buffered stream → tolerant JSON → seed validation
  llmextract_test.go
  suggest.go         ← frequency report over unmatched terms (print-only)
  suggest_test.go

internal/search/
  filters.go         ← labelFilterSQL(names, docAlias) EXISTS fragment (plan 32's shape)
  search.go          ← Options.Topics []string, TopicMentions bool

internal/retrieval/
  retrieval.go       ← Filters{Meta, Topics, Expand, Hops} — defined once (see below)
  expand.go          ← label detection in the query + traversal + chunk merge
  expand_test.go

internal/cli/
  topic.go           ← tbuk topic list|show|add|rm|rename|delete   (plan 32, on LabelRepo)
  graph.go           ← tbuk graph build|stats|suggest
  entity.go          ← tbuk entity list|show|alias|merge
  init.go            ← untouched: no ontology.yaml scaffolding (D2)
  search.go, ask.go  ← --topic, --expand-entities, --hops
  doctor.go          ← seed loads; rows whose type/predicate left the seed

internal/config/
  config.go          ← OntologyConfig; ResolvePaths covers ontology.path; Validate()

scripts/
  add-label-tables.sh  ← throwaway (D8); deleted once run
```

**`retrieval.Filters` is defined exactly once.** Plan 32 milestone 1 replaces
`Retrieve(ctx, query, topK, meta)` with a `Filters` struct; this plan adds
`Expand`/`Hops` to that same struct. Whichever lands first defines it.

### Repos

```go
type Label struct { ID int64; Name, Display, Type string }
type Triple struct { ID int64; Subject Label; Predicate string; Object Label }
type Evidence struct { ChunkID int64; Confidence float64; RunID int64 }

// Vocabulary
func (r *LabelRepo) Ensure(ctx context.Context, display, typ string) (int64, error)
func (r *LabelRepo) FindByName(ctx context.Context, name string) (Label, error) // ErrNotFound
func (r *LabelRepo) IDsForNames(ctx context.Context, names []string) ([]int64, error)
func (r *LabelRepo) List(ctx context.Context, typ string) ([]LabelCount, error)
func (r *LabelRepo) Rename(ctx context.Context, old, new string) error
func (r *LabelRepo) Delete(ctx context.Context, name string) error
func (r *LabelRepo) AddAlias(ctx context.Context, id int64, alias string) error
func (r *LabelRepo) Merge(ctx context.Context, keep, drop int64) error // re-points every attachment

// Document grain (asserted) — what `topic` and the filter use
func (r *LabelRepo) Tag(ctx context.Context, docID int64, labelIDs ...int64) error
func (r *LabelRepo) Untag(ctx context.Context, docID int64, labelIDs ...int64) error
func (r *LabelRepo) DocumentsFor(ctx context.Context, names []string) ([]Document, error)
func (r *LabelRepo) ForDocument(ctx context.Context, docID int64) ([]Label, error)

// Mention grain (derived) — what the gazetteer and `entity show` use
func (r *LabelRepo) AddMention(ctx context.Context, chunkID, labelID int64, start, end int, runID int64) error
func (r *LabelRepo) ChunksMentioning(ctx context.Context, labelIDs []int64) ([]int64, error)

func (r *TripleRepo) Add(ctx context.Context, t Triple, ev Evidence) error
func (r *TripleRepo) For(ctx context.Context, labelID int64) ([]Triple, error)
// Traverse walks up to maxHops from the seed labels, capped at maxLabels, and
// returns the reached triples with their evidence chunk ids.
func (r *TripleRepo) Traverse(ctx context.Context, from []int64, maxHops, maxLabels int) ([]Triple, []int64, error)
```

Errors wrap `fmt.Errorf("LabelRepo.Method: %w", err)`; misses use
`storage.ErrNotFound`, matching the existing repos. `App` grows memoized
`Labels()` / `Triples()` accessors in the composition root.

---

## Config

```yaml
ontology:
  path: ./ontology.yaml   # relative to root (rebased by ResolvePaths) or absolute;
                          # empty or missing = untyped labels only (scenario A)
  extract:
    llm: false            # gazetteer only unless true (D4)
    batch: 32
  retrieval:
    expand: false         # off until D7's measurement says otherwise
    max_hops: 1
    max_labels: 8
    max_added_chunks: 10
```

Scenario A needs none of this — an absent seed file means untyped labels, which
is the default. `Config.Validate()` (the existing fail-fast chokepoint) rejects
`max_hops` outside 1..3, non-positive caps, and a `path` that is set but
unreadable or invalid.

---

## CLI surface

`--topic` flags are `StringSlice` — repeatable and comma-splitting — per plan 32.

| Command | Behaviour |
|---|---|
| `ingest <path> [--topic x,y] [--infer-topics]` | Plan 32, unchanged, writing `label_documents`. |
| `search`/`ask` `<q> --topic x,y` | `EXISTS` pre-filter on asserted rows (D5). Unknown name → CLI error naming the known labels. |
| `search`/`ask` `--topic-mentions` | Widen the filter to mention grain. Off by default. |
| `topic list [--all] [--format]` | Untyped labels + any typed label with ≥1 document attachment — i.e. everything `--topic` accepts (D10). `DOCS` and `MENTIONS` counts; `--all` flattens the whole vocabulary with a `TYPE` column. |
| `topic show\|add\|rm\|rename\|delete` | Plan 32's group, unchanged, on `LabelRepo`. |
| `digest --topic x` | Milestone 2 (plan 32). |
| `digest --entity X [--hops N]` | Milestone 4 — same engine, second selector (below). |
| `graph build [--topic x,y] [--llm] [--force]` | Extraction run over stored chunks; skips chunks already covered by a run of the same extractor unless `--force`. Summary: chunks seen, mentions, triples, dropped. |
| `graph stats` | Labels per class, triples per predicate, chunk coverage %, rows whose type or predicate left the seed, last run. |
| `graph init` | Write a commented starter `ontology.yaml`. Not run by `tbuk init` (D2); refuses to overwrite an existing file. |
| `graph suggest [--min N]` | Print-only frequency report (D3). Writes nothing. |
| `entity list [--type T] [--format]` | Typed labels grouped by class, with mention and document counts (D10). Empty in scenario A. |
| `entity show <name> [--hops N]` | Type, aliases, triples in/out, evidence chunks with `path §index` citations. |
| `entity alias <name> <alias>...` | Add aliases — the user's half of entity resolution. |
| `entity merge <keep> <drop>` | Re-point every attachment, delete the dropped row. Confirm prompt like `delete`. |
| `export --topic x,y` / `reindex --topic x,y` | Plan 32, unchanged — asserted rows only, by design (D5). |

**One digest engine, two selectors.** Plan 32 milestone 2 builds `RunDigest`
(exhaustive fetch → token budget → single call or map-reduce) and the topic
selector. The entity selector lands in milestone 4 with the mention rows it
reads — the engine ships with the seam, not with both users of it. Make the
chunk-selection step injected:

```go
type ChunkSelector func(ctx context.Context) ([]retrieval.RetrievedChunk, error)
```

- `--topic`: every chunk of every document carrying the label.
- `--entity`: every chunk mentioning the label, plus the evidence chunks of its
  triples within `--hops`.

Rejected: a separate `entity digest` command — the same code twice, drifting.

Label names reaching the terminal go through the existing `cli.sanitize` path;
they are document-derived text.

---

## Export / import

`export.Create` snapshots config + data folders, so `ontology.yaml` rides along
with an export. `importer.Extract` ignores config and prompts and will ignore
the seed for the same reason — the importing knowledge base keeps its own
vocabulary. Asserted `label_documents` rows are carried by plan 32's
topic-scoped export as it already specifies; mention and triple rows are not,
because import re-ingests from the raw archive and `graph build` re-derives them
deterministically. Say so in the user guide so nobody expects otherwise.

---

## Testing (TDD, table-driven, ≥85% per package)

- **storage/labels:** vocabulary CRUD; normalization (`Multi-Cursor` ≡
  `multi-cursor`); `UNIQUE(name, type)` lets an untyped topic and a typed entity
  share a name; `IDsForNames` miss → `ErrNotFound`; document delete cascades
  document attachments; chunk delete cascades mentions; `Merge` re-points both
  grains and leaves no orphan; `Rename`/`Delete` edge cases. In-memory SQLite.
- **storage/graph:** `Add` idempotent; a triple whose last evidence row goes
  with a deleted chunk is removed; `Traverse` at 1/2/3 hops; a cycle (`A→B→A`)
  terminates; `maxLabels` cap honoured; a disconnected seed returns empty.
- **ontology/seed:** valid load; unknown class in a predicate's domain/range →
  error; duplicate class names; empty file; **missing file is not an error** (it
  is scenario A).
- **ontology/gazetteer:** exact and alias match; case folding; word boundaries
  (`Go` must not match `Google`); UTF-8 offsets land on rune starts; overlapping
  aliases prefer the longest match; code identifiers via `searchtext.Reduce`
  split forms; a chunk with no match writes no rows.
- **ontology/llmextract:** fake `chatFn` returning canned token channels (the
  `ask_run_test.go` pattern) — well-formed JSON; JSON wrapped in prose; a
  trailing comma; unparseable output → zero rows and a counted drop; unknown
  predicate dropped; `domain`/`range` violation dropped; stream error
  propagated; context cancellation mid-stream.
- **search:** label filter on vector/keyword/hybrid; a doc carrying two of the
  requested labels returns no duplicate chunks; labels and metadata compose
  (AND); unmatched label → empty; empty `Topics` ≡ today (regression parity);
  `TopicMentions` widens and only then.
- **retrieval/expand:** label detection in a question; expansion adds only
  evidence chunks; `max_added_chunks` cap; expansion respects the label and
  metadata filters; `Expand: false` is byte-identical to today's result — the
  important one.
- **cli/list views (D10):** `topic list` includes an untyped label, a typed
  label with a document attachment, and a zero-attachment untyped label, and
  excludes a mention-only typed label; `MENTIONS` reads `-` before any run and a
  count after; the `TYPE` column appears only when a typed row is present;
  `--all` flattens everything; `entity list` groups by class, is empty in
  scenario A, and `--type T` narrows; `--format json` is flat in both.
- **cli:** every subcommand; `graph build` skip/force; unknown-label error text;
  `entity merge` confirm prompt; `--expand-entities` forwards `Filters` (fake
  retriever asserts); `doctor` flags seed drift; extend `integration_test.go`
  with both scenarios — A (`ingest --topic → search --topic → digest --topic`,
  no seed file present) and B (`graph build → entity show → search
  --expand-entities`).

---

## Rollout (one PR per milestone)

| # | PR | Plan | Depends on | Serves |
|---|---|---|---|---|
| 1 | `feat(labels): vocabulary, document grain, filter` — `labels` + `label_documents` + aliases in `schemaSQL` + `scripts/` script, `LabelRepo`, search/retrieval filter, `--topic` on ingest/search/ask/reindex, `topic` group | 32 | — | **A, complete.** This *is* [#115](../../../../issues/115). |
| 2 | `feat(digest): engine and topic selector` — `RunDigest`, builtin `digest` template, injected `ChunkSelector` | 32 | 1 | A ([#116](../../../../issues/116)) |
| 3 | `feat(export): topic-scoped archive` | 32 | 1 | A ([#117](../../../../issues/117)) |
| 4 | `feat(graph): seed ontology, mentions, gazetteer build` — `ontology.yaml`, `label_mentions`, `extraction_runs`, `graph init`/`build`/`stats`/`suggest`, `entity list/show/alias/merge`, and `digest --entity` (the second selector, which needs the mentions this PR creates) | 33 | 1, 2 | A sharpened, B started |
| 5 | `feat(graph): opt-in LLM relation extraction` — `triples`, `triple_evidence`, `graph build --llm`, validation, run bookkeeping | 33 | 4 | B |
| 6 | `feat(retrieval): entity expansion behind a flag` — `Filters.Expand`, expander, `--expand-entities` | 33 | 5, D7 measurement | B |

Milestones 1–3 belong to plan 32 and are listed here only so the sequence reads
in one place — they are the whole of scenario A and carry no new concepts for a
user who never opens `ontology.yaml`. Milestones 4–6 are this plan's, are
scenario B, and are individually skippable.

Each PR updates `README.md`, `docs/initial-context.md` (schema, CLI list,
architecture — per AGENTS.md, before merge) and `docs/user-guide.md`. Plan 32 is
archived in its own milestone-3 PR; this plan is archived in the milestone-6 PR,
or in milestone 5 if D7's kill criterion fires. Milestone 1 belongs to plan 32
but creates the tables this plan's schema section specifies — whoever builds it
should read both.

---

## Risks

| Risk | Mitigation |
|---|---|
| Unifying the tables slows down scenario A | It does not: milestone 1 is the same two tables and the same filter plan 32 already specified, with two extra columns. If it starts growing scenario-B concepts, that is the signal to stop and split. |
| Local MLX model emits unusable JSON | The gazetteer is the default and needs no model; the LLM path validates hard and reports its drop rate (D4). A run that produces nothing is legible, not silent. |
| Extraction cost — one LLM call per chunk | `--llm` is opt-in and `--topic`-scopeable. Order of magnitude: a 5,000-chunk corpus at ~2 s/chunk on a local model is ~3 hours. Say so in the docs; do not bury it. |
| Expansion hurts answer quality | Off by default, gated on D7, with a stated kill criterion. Scenario A is unaffected. |
| Seed drift — user edits `ontology.yaml`, rows keep old types | `graph stats` and `doctor` both report it; `graph build --force` re-derives. |
| Scope creep back toward the brief | The drops table above is the contract. Reopening any row needs a reason written into this plan. |

---

## Out of scope (deliberate)

- Ontology-guided chunking (roadmap #34) — its own plan, and it needs this one.
- A `Facts:` block in the prompt (brief §14) — v2, after D7 (D6).
- Class hierarchy traversal / roll-up queries.
- Any reasoner, RDF store, SPARQL, or external graph database.
- Label embeddings — the vocabulary is small enough for exact matching, and
  `Dog is_a Animal` vs `Animal is_a Dog` is exactly what embeddings get wrong
  (brief §9).
- Cross-document coreference beyond alias matching.
- AND-intersection filter mode, hierarchical topic names — plan 32's out-of-scope
  list, unchanged.

---

## Open questions

- **Should `graph build` run at ingest?** A separate command keeps ingest fast
  and re-runnable, matching `reindex`. An ingest hook is a cheap follow-up once
  the gazetteer is proven fast; not gating.
- **Mention offsets on re-chunk.** `reindex` rewrites chunk rows, so mentions
  cascade away and must be rebuilt. Acceptable — re-running the gazetteer is
  cheap — but `reindex` should say so, or call `graph build` itself.
*(Settled and moved into the plan body: `tbuk init` does not scaffold
`ontology.yaml` — see D2 and the config section; `topic list` vs `entity list`
— see D10.)*
