# Subplan 07: Retrieval evaluation — measuring retrieval and generation apart

Lands roadmap **#7** (retrieval evaluation harness,
[#126](../../../../issues/126)) and the expansion `next-steps.md` files as
**#30** — *separate retrieval eval from generation eval* — as one harness with
two stages, because they are one command with two halves of an answer and
splitting them across two plans would give the second one nothing to attach to.

> Numbering: the archived `2026-07-18-1900-f4d346b-07-search.md` used "07" for
> the original search subplan, the way `05-conversational-context.md` reused
> "05". This plan is the roadmap's #7.

---

## Why now, and why this shape

Every lever in the **Retrieval Quality** cluster is currently tuned by anecdote.
Two of them have already shipped that way and say so in their own release notes:
`condense` ([#159](../../../../issues/159)) and `expand`
([#160](../../../../issues/160)) are both **off by default with the flip
deferred to "the eval split"** — a gate that does not exist. A third,
multi-hop ([#161](../../../../issues/161)), carries a written kill criterion
that cannot be evaluated at all:

> two hops must beat one on answer correctness at no worse than 2× median
> latency, or the loop does not ship

That sentence is the specification for this plan. The harness is not a nice
piece of infrastructure to have; it is the missing half of three decisions
already made.

So this plan is scoped by what those decisions need, not by what an eval harness
could theoretically report:

| Pending decision | What it needs from the harness |
|---|---|
| #24: is `condense` better than `window`? | Recall / MRR on **follow-up** queries, thread and all |
| #25: is `expand: N` worth a model call? | The same metrics under a swept `--expand` |
| #28: does hop 2 beat hop 1? | Answer **correctness**, and **median latency** |
| #8, #27, #29 (later) | The same numbers, unchanged, so a chunking change is comparable to last week's |

Median latency is therefore a first-class output of this harness and not a
footnote, and a labelled case can carry a **thread**, not only a question —
otherwise #24, the cheapest decision on the list, stays unmeasurable.

---

## Goal

```bash
tbuk eval                              run every label set under ~/.tbuk/eval
tbuk eval go-docs.yaml                 run one
tbuk eval go-docs.yaml --format json > before.json
tbuk eval go-docs.yaml --rewrite condense --baseline before.json
tbuk eval go-docs.yaml --stage generation --judge     # costs model calls
```

```
retrieval  24 cases   hit@5 0.83   recall@5 0.71   P@5 0.29   MRR 0.68   nDCG@5 0.74
           latency    median 84ms  p95 210ms
```

Success is three things:

1. A change to chunking, fusion, ranking or query planning can be **A/B'd with
   a diff of two JSON files**, on the user's own corpus.
2. The numbers are **attributable**: a generation regression that is really a
   retrieval regression shows up in the retrieval stage, which is the whole
   point of #30's split.
3. The suite itself is **regression-tested in CI with no server running** — a
   refactor that reshuffles a ranking fails a Go test, not a user's afternoon.

---

## Design decisions (and alternatives rejected)

### D1. A label names a **document and an optional passage anchor**, never a chunk id

`chunks.id` is not stable — `ReplaceForDocument` deletes and re-inserts on every
re-ingest and every `tbuk reindex` — and `path §index` is worse, because the
index moves whenever chunking changes. Labelling by chunk would make the harness
useless for the first thing it is wanted for: proving a chunking change helps.

So a label is a document path plus, optionally, `contains:` — a snippet of the
passage that ought to come back. A retrieved chunk satisfies the label when it
belongs to that document and (when an anchor is given) contains that text.

This is #126's "query→expected-doc pairs" and #30's "query→relevant-chunk sets"
reconciled: the *judgment* is at chunk granularity, the *label* is at a
granularity that survives re-chunking.

*Rejected:* labelling by chunk id and re-labelling after every reindex. That is
a corpus maintained by hand forever, and it silently rots.

### D2. Label paths match by unambiguous suffix

The knowledge base stores absolute, platform-normalised paths
(`/home/u/notes/go/slices.md`, `D:\notes\go\slices.md`). A labels file written by
hand cannot carry those and stay portable, so `path: go/slices.md` matches any
indexed document whose path ends on that separator boundary.

A suffix matching **two** indexed documents is an error at load, naming both.
Scoring silently against whichever one sorted first is how an eval harness
quietly starts lying, and a harness that lies is worse than no harness.

### D3. Text anchors match whitespace- and case-insensitively

A chunk boundary that reflows, or a preprocessor that collapses a line break,
must not turn a passing case into a failing one — that is noise, and noise in the
measuring instrument is indistinguishable from the regression it is there to
find. `contains` folds case and collapses runs of whitespace on both sides
before comparing. Nothing else is normalised: punctuation is signal.

### D4. Two stages, scored separately, and only the first one is free

**Retrieval** scores the ranked list against the labels: hit-rate@k, recall@k,
precision@k, MRR, nDCG@k. It costs one embedding call per query and no model
call at all.

**Generation** scores the answer: correctness against a reference, and
groundedness in the retrieved passages. It costs a model call per case to
produce the answer, and (with `--judge`) another to score it.

They run separately (`--stage retrieval|generation|both`, default `retrieval`)
because attribution is the point of #30 — an answer that got worse because
retrieval got worse is a different bug from an answer that got worse on the same
evidence — and because a free stage that people actually run beats a complete
one they do not.

### D5. Generation is scored deterministically first, and by a judge only if asked

Two scorers, in this order:

- **Deterministic**, always: `must_include` substrings present; every citation
  the answer emitted resolves to an indexed document; **groundedness by
  overlap** — what fraction of the answer's content words appear in the
  retrieved passages. Crude, free, has no opinions, and catches the failure that
  matters most (an answer that stopped being grounded) without a model.
- **LLM judge**, under `--judge`: correctness against `answer:` and
  faithfulness against the passages, each on a 0–2 scale with a reason.

*Rejected:* judge-only. A metric you cannot compute without a model and an API
key is a metric that never runs in CI, and one whose value drifts when the judge
model is updated underneath it.

### D6. The judge's prompt is Go source, not a user template

A built-in `judge` prompt template would be tunable, exportable, importable, and
overridable — every one of which is a way for two runs to be scored by two
different instruments and compared anyway. The judge prompt is a constant in
`internal/eval`, versioned with the code, and printed by `tbuk eval --judge
--verbose` so a number can be traced to the question that produced it.

*Rejected:* `eval.judge_template` in config. Revisit if anyone ever wants a
domain-specific rubric; the cost of adding it later is one key.

### D7. `tbuk eval` never ingests, and never writes to the knowledge base

It searches, and under `--stage generation` it asks. It creates no session, it
appends no turn, and it modifies no row. A corpus is put in place by
`tbuk ingest`, which already exists and is already tested; an eval command that
also ingested would be two commands wearing one name, and its `--force`
semantics alone would be a plan of their own.

### D8. CI regression-testing uses a deterministic embedder, not a server

`make check-ci` has no embedding server and no model, so the shipped fixture
corpus is scored inside a Go test against a **hashing embedder**: a stable
vector derived from the text's tokens.

It is not a semantic model and the plan does not pretend otherwise — the
absolute numbers are meaningless. What it pins is **rank stability**: a refactor
of `FuseRRF`, of the query parser, or of chunking that reshuffles the ranking
changes a metric and fails a test. That is the regression bar the extraction in
[#160](../../../../issues/160) had to assert by hand, made permanent.

Quality, as opposed to regression, is measured against a real embedder on a real
corpus, by the user, with `tbuk eval`.

### D9. The report is text for a person and JSON for a diff, and `--baseline` does the diff

`--format json` follows `search`, `stats`, `list` and `find`. `--baseline
before.json` reads a previous JSON report and prints deltas alongside the
current numbers, because "A/B'd with evidence" in #126's acceptance means
someone has to actually do the subtraction, and a human subtracting nDCG in their
head is a human who stops doing it by Thursday.

A baseline from a different label set, or a different set version, is an error —
comparing two corpora is not a comparison.

### D10. Latency is measured, reported, and part of the record

Median and p95 wall time per stage, in the text report and in the JSON. #28's
kill criterion is stated in latency and correctness together, so a harness that
reports only quality cannot decide it.

Latency is the noisiest number here — it moves with the machine, the server, and
what else is running — so it is reported, never asserted in a test, and the
report says which provider and model produced it.

### D11. A case may carry a thread

```yaml
  - id: maps-followup
    thread:
      - question: how do slices grow?
        answer: A slice grows when append finds len == cap …
    query: and maps?
```

Without this, #24 is unmeasurable: `window` and `condense` are identities on a
question with nothing behind it, so a single-shot label set scores every rewrite
mode the same and the default never flips. With it, the harness answers the
question plan 05's D6 deferred.

The thread is replayed to the planner exactly as `openThread` builds one, but it
is **never persisted** (D7): it is an input to the case, not a session.

### D12. Sweeping the knobs is the feature, not a flag

`--mode`, `--top`, `--rewrite`, `--expand` on `tbuk eval` mean what they mean on
`search` and `ask`, and each run reports which values it ran under. That is what
makes the harness the gate the roadmap says it is: the comparison is two runs of
one command, not two builds of the binary.

`--hops` joins them when [#161](../../../../issues/161) is picked back up, with
no change to this design — which is the point of doing this first.

### D13. Label sets live under the root, so `doctor` can check them

`eval.dir` (default `<root>/eval`) is where `tbuk eval` with no argument looks,
and it is what makes a `doctor` section possible: label sets found, cases
counted, and — the check that matters — **labels naming a document that is not
in the index**, which otherwise scores zero forever and reads as a retrieval
failure.

An explicit path argument bypasses the directory entirely.

---

## Label set format

YAML, versioned, one file per set:

```yaml
version: 1
name: go-docs
description: follow-up questions over the Go documentation corpus

cases:
  - id: slices-growth
    query: how do slices grow?
    relevant:
      - path: go/slices.md
        contains: capacity is doubled
        grade: 2                    # optional, default 1; nDCG uses it
      - path: go/internals.md
    answer: |                       # generation stage only
      append reallocates when len == cap, roughly doubling capacity.
    must_include: [append, cap]     # generation stage only

  - id: maps-followup
    thread:
      - question: how do slices grow?
        answer: A slice grows when append finds len == cap …
    query: and maps?
    relevant:
      - path: go/maps.md
```

Load-time errors, all raised before a single search runs: an unknown `version`,
a case with no `query`, a case with no `relevant` entries when the retrieval
stage is asked for, a duplicate `id`, a negative `grade`, an ambiguous path
(D2), and — as a **warning**, not an error — a path matching nothing in the
index, since a label set is often written before the corpus is complete.

---

## Metrics

Macro-averaged over cases; per-case numbers in `--verbose` and always in JSON.

| Metric | Definition |
|---|---|
| hit@k | 1 if any labelled passage is in the top k |
| recall@k | labelled passages found in top k ÷ labelled passages |
| precision@k | labelled passages found in top k ÷ retrieved |
| MRR | 1 ÷ rank of the first labelled passage |
| nDCG@k | Σ (2^grade − 1)/log₂(rank+1), over the ideal ordering |

Generation, all in 0–1:

| Metric | Definition |
|---|---|
| includes | `must_include` substrings present ÷ required |
| citations | emitted citations resolving to an indexed document ÷ emitted |
| groundedness | answer content words appearing in the retrieved passages |
| correctness | judge, 0–2 against `answer:`, rescaled (needs `--judge`) |
| faithfulness | judge, 0–2 against the passages, rescaled (needs `--judge`) |

Precision at a k larger than the number of labels is bounded above by
`labels/k`, which is a property of the label set and not of the retriever. The
report says so in a footnote rather than hiding it, because the first reaction
to `P@5 0.29` is otherwise to go looking for a bug.

---

## Package changes

```
internal/eval/           NEW — pure. LoadSet/Set/Case parsing and validation;
                         Match(chunk, label) (D1–D3); Score(ranked, labels, k)
                         → Metrics; Aggregate([]Metrics) → Report. No DB, no
                         LLM, no cobra, so every metric is table-testable
                         against a handwritten ranking.
internal/eval/generate.go   deterministic answer scoring; Judge behind a ChatFn
                         seam, the shape internal/rewrite already uses, so a
                         fake chat drives the tests.
internal/eval/report.go  text and JSON rendering; Diff(current, baseline).

internal/cli/eval.go     NEW — tbuk eval: resolve the set, build retriever and
                         (for --stage generation) the LLM, run the sweep, print.
internal/cli/doctor.go   new Eval section (D13).
internal/config/         EvalConfig{Dir}; FillMissingDefaults backfills it.
```

Nothing existing changes shape. The harness is a reader of `internal/retrieval`
and `internal/rewrite` as they already stand — which is deliberate: a measuring
instrument that required the thing it measures to be refactored first would have
measured the refactor.

---

## Config

```yaml
eval:
  dir: ~/.tbuk/eval    # where tbuk eval looks when given no path
```

`Config.Validate` has nothing to reject here — an empty dir means "no default
set", which is the state of every knowledge base until someone writes one.

---

## CLI surface

```
tbuk eval [set]                    a label set, by path or by name under eval.dir;
                                   with no argument, every set in eval.dir
  --stage retrieval|generation|both   default retrieval (D4)
  --judge                          add the LLM judge to the generation stage (D5)
  --mode vector|keyword|hybrid     as on search; default hybrid
  --top N                          retrieval depth the metrics are cut at (default 5)
  --rewrite off|window|condense    as on ask (D12)
  --expand N                       as on ask (D12)
  --case ID                        one case, for iterating
  --baseline FILE                  a previous --format json report, diffed (D9)
  --format text|json               default text
  --verbose                        per-case rows, and the judge's reasons
```

---

## Doctor

A new **Eval** section, after **Prompts**:

```
Eval
  label sets     2 (go-docs, ops) — 31 cases
  labelled docs  ✗ 3 labelled paths are not in the index (go/generics.md, …)
```

Absent `eval.dir`, one line saying so and naming where to put a set. The check
for a labelled document missing from the index is the one that earns the
section (D13).

---

## Testing (TDD, table-driven, ≥85% per package)

- **eval (parsing):** every load-time error above; an unknown key; a set with no
  cases; `version: 2`.
- **eval (matching):** suffix matching on a separator boundary and not
  mid-segment (`slices.md` must not match `go-slices.md`); ambiguity is an
  error; anchors folding case and whitespace; an anchor that spans what was one
  line in the source.
- **eval (metrics):** handwritten rankings with known answers — the labelled
  passage first, last, absent, several labels, graded labels, k larger than the
  result list, an empty result list, a case with one label and one result.
  nDCG against a worked example computed by hand in the test table.
- **eval (generation):** `must_include` present/absent/partial; a citation
  naming an unindexed document; groundedness at 0 and 1; the judge with a fake
  chat — a well-formed verdict, a malformed one, an error, a timeout — each
  scoring the case as unjudged rather than as zero, because a judge that failed
  is not evidence that the answer was wrong.
- **eval (report):** text and JSON goldens; `Diff` against an identical
  baseline (all deltas zero), an improved one, and a mismatched set name (error).
- **cli:** `tbuk eval` against a seeded in-memory KB with a fake embedder;
  `--case`; `--format json`; `--baseline`; `--stage generation` with a fake chat;
  a set whose labels match nothing warns and still reports; **no rows are
  written** (D7) — the assertion that keeps the command honest.
- **fixture corpus (D8):** `internal/eval/testdata/corpus/` ingested into an
  in-memory SQLite KB with the hashing embedder, scored against
  `testdata/corpus/labels.yaml`, asserting the metrics are exactly what the
  committed baseline says. This is the test that fails when a ranking refactor
  changes a ranking.
- **integration:** extend `internal/cli/integration_test.go` with
  `init → ingest → eval --format json`, embedding faked as it already is.

---

## Rollout (one PR per milestone)

| # | PR | Delivers | Depends on |
|---|---|---|---|
| 1 | `feat(eval): label sets and retrieval metrics` — `internal/eval` parsing, matching, metrics, aggregation, report + `Diff`. Pure package, no CLI | the instrument | — |
| 2 | `feat(eval): tbuk eval over the knowledge base` — the command, sweep flags, text/JSON, `--baseline`, config key, doctor section, fixture corpus + hashing-embedder regression test | #126's acceptance | 1 |
| 3 | `feat(eval): generation scoring and an LLM judge` — deterministic answer scoring, `--stage`, `--judge` | #30's split | 2 |
| 4 | `docs(eval): measure the deferred defaults` — a `docs/eval/` set over this repo's own docs, the numbers for `window` vs `condense` (#24) and `expand` (#25), and the resulting default decision; archive this plan | the payoff | 3 |

[#126](../../../../issues/126) closes with milestone 3 — the point at which both
stages exist and report. Milestone 4 is the roadmap's #30 in the sense that
matters: the numbers get used.

**Milestone 4 is the user's to run, not this session's.** Real numbers need a
real embedding server and a real model; a container with neither can produce the
label set, the procedure and the reporting, and cannot produce evidence. The PR
delivers the first three honestly and says plainly that the default flips wait
on a run the author has to do. Inventing numbers here would be the one failure
mode this whole plan exists to prevent.

---

## Risks

| Risk | Mitigation |
|---|---|
| The label set is small enough to overfit to | The report prints the case count next to every metric, and `--baseline` shows deltas rather than absolutes. A 24-case set moves 0.04 on one case; the docs say so. |
| Labelling is work nobody does, so the harness is never used | `session_turns.query` already records real queries from real threads ([#157](../../../../issues/157)); a case is a query plus the paths that should have come back, which is a two-minute job per case with `tbuk search` open in another pane. Milestone 4 seeds a set so nobody starts from an empty file. |
| The hashing embedder's numbers get mistaken for quality | It lives in `testdata`, it is never what `tbuk eval` uses, and the test that consumes it is named for regression. Stated in D8 and in the test's doc comment. |
| The judge becomes the thing being measured | `--judge` is opt-in, deterministic scoring always runs alongside it, the prompt is versioned code (D6), and the JSON records the judge model — so a judge upgrade is visible as a judge upgrade. |
| Latency numbers are compared across machines | The JSON records provider, model and host; `--baseline` from a different provider prints a warning next to the latency deltas. |
| The harness ossifies today's retrieval shape | It only reads `retrieval.RetrieveMany` and `rewrite.Planner`, both of which #160 already generalised. `--hops` is a flag, not a redesign. |

---

## Out of scope (deliberate)

- **Ingesting from the eval command** (D7). `tbuk ingest` exists.
- **A shipped public benchmark corpus.** The fixture corpus is a regression
  fixture, not a benchmark; comparing timbuktu to anything else is not a goal.
- **Per-chunk relevance labels** (D1), and the re-labelling treadmill they need.
- **Automatic label mining from threads.** Tempting, given `session_turns`, but
  a label set generated from the answers a system already gave is a system
  grading its own homework. Harvest by hand; the column is there.
- **Statistical significance testing.** With 20–50 cases on one machine there is
  nothing to be significant about. Report deltas, not p-values.
- **Tuning anything.** This plan measures. #24's flip, #25's default and #28's
  ship-or-kill are decisions taken *with* the harness, in their own PRs.

---

## Open questions

- **Should a case be able to pin `top_k` of its own?** A question with one
  relevant passage and a question with eight want different depths, and the
  macro-average currently blurs that. Per-case `k` is easy to add and hard to
  compare across cases, which is why it is not in milestone 1.
- **What is the right groundedness proxy without a model?** Content-word overlap
  is crude and rewards an answer that quotes. An entailment-ish signal without a
  model is probably not available; the judge covers it when asked. Revisit if
  the deterministic number turns out to be uninformative in practice.
- **Should `--baseline` live in the command, or should there be a `tbuk eval
  diff a.json b.json`?** The flag is fewer moving parts and covers the common
  case (compare to what I had before). A subcommand reads better for comparing
  two archived runs, neither of which is "now". Not gating.
