# Measuring the deferred defaults

Two defaults in this repository are off, and both say in their own release
notes that the flip waits on the eval harness:

| Default | Where it says so | Issue |
|---|---|---|
| `retrieval.rewrite: condense` | `README.md`, Conversation threads | [#159](../../../../issues/159), roadmap #24 |
| `retrieval.expand: N` | `README.md`, Conversation threads | [#160](../../../../issues/160), roadmap #25 |

The harness now exists. This directory is where it gets pointed at a real
corpus so those two decisions stop being deferred.

**No number in this repository is produced by anything but a real run.** A
harness whose own evidence was invented is the one failure the whole plan
exists to prevent, so every table below is either filled from a committed JSON
report or left empty with the reason.

---

## The label set

`timbuktu-docs.yaml` — 24 cases over this repository's own documentation.
Eleven carry a thread and a `gold_query`; thirteen are single-shot.

The split is the design. A single-shot question makes `window`, `off` and
`condense` the same measurement — a rewrite is an identity on a question with
nothing behind it — so a set without threads scores every mode identically and
the default never flips. The single-shot half is the control: a rewrite that
helps follow-ups must not hurt questions that needed no help.

Every `gold_query` is the standalone question a competent person would have
typed in place of the follow-up. Retrieving on it is an ordinary search, so the
**ceiling costs no model call**, and the gap between it and `window` is the
room a rewrite has left to win. If `window` already sits near the ceiling,
`condense` cannot buy enough to justify a model call per question and #159's
default is settled without ever invoking a condenser.

## Reproducing

The corpus is eleven documents: `README.md`, `docs/user-guide.md`,
`docs/initial-context.md`, and `docs/plans/*.md`. `docs/archive/` is excluded on
purpose — an archived plan is a near-duplicate of the prose that replaced it,
so labelling one over the other would measure how the set was written rather
than how retrieval performs.

```bash
make eval-ingest      # init + ingest + doctor, into ~/.tbuk-eval
make eval-defaults    # the five sweeps, into docs/eval/results/*.json
```

Both targets use `~/.tbuk-eval` — a root of its own, so the measurement corpus
stays out of a working knowledge base — and both pass `--root` explicitly.
Override with `ROOT=~/.tbuk make eval-defaults` to use yours instead, and
`TBUK=./bin/tbuk` to point at a build rather than whatever is on `PATH`. Set
`ROOT` the same for both, or the sweeps score a corpus the labels were not
written against.

The root decides the config too, so `llm.model`, `embedding.model` and the base
URLs come from `$ROOT/config.yaml`. A sweep reading a different root than the
ingest is a measurement of another corpus through another model, and it looks
exactly like a measurement of this one.

It ends on `doctor`, which is not decoration. Read the **Eval** section before
sweeping: a label naming a document that was never ingested scores zero on
every run and reads on the report exactly like a retrieval failure.

`tbuk ingest` takes exactly one path per invocation, which is why this is a
script and not a one-liner.

**`FORCE=1 make eval-ingest`** re-ingests documents whose content has not
changed. Ingest skips those by SHA256, which is right for a working knowledge
base and wrong after a run that recorded the documents and then failed to embed
them — the rows exist, so every re-run reports "skipped (unchanged)" while the
corpus stays unsearchable and the sweeps keep scoring zero. If `doctor` shows
documents but `Embedding / stored` says none, that is the state, and `FORCE=1`
is the way out.

### Serving the models

**Two servers on two ports.** Chat and embeddings are separate endpoints, and
`mlx_lm.server` serves `/v1/chat/completions` only — pointing `embedding.base_url`
at it gives `HTTP 404: Not Found` on every query. Run whatever serves
`/v1/embeddings` on a port of its own and name it explicitly:

```yaml
# $ROOT/config.yaml — the root the sweeps run against
llm:
  base_url: http://localhost:8080
  model: mlx-community/Qwen2.5-3B-Instruct-4bit
embedding:
  base_url: http://localhost:8002
  model: mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ
  dimension: 1024
```

An empty `base_url` is not "unset" — it resolves to `http://localhost:8080` for
both, so leaving the embedding one blank silently aims it at the chat server.
`dimension` must match the embedding model, or ingest stores vectors search
will refuse to run against.

**Name the chat model too.** With `llm.model` empty the request carries an
empty `model` field and the server answers with whatever it has loaded — which
another client can change underneath you. It is also what the report records as
the instrument, and `modelLabel` has nothing to name when it is blank.

**Use a model that does not reason for the rewrite sweeps.** `condense` gets one
line to write and is capped at 20 seconds and 256 tokens
(`rewrite.CondenseTimeout`, `CondenseMaxTokens`); it cannot fail, it falls back
to the window. A reasoning model spends the whole budget thinking and never
reaches the question, so **every** case falls back — the report says so rather
than reporting the window's numbers as condense's. A large model fails the
other way, crossing the 20s deadline. A small instruct model does this in under
a second.

That is a finding as much as a workaround. #24 is decided on correctness **and**
latency (D10), and a rewrite that costs seconds a question, or that breaks
outright on the local reasoning models people actually run, does not become a
default whatever it does to MRR.

### What each row costs

| Sweep | Needs |
|---|---|
| `off`, `window` | nothing at eval time — the window is deterministic |
| `gold` (the ceiling) | nothing beyond the labels |
| `condense` | one model call per case |
| `expand3` | one model call per case, plus a search per wording |
| generation, `--judge` | one or two model calls per case |

Ingesting the corpus needs the embedding server whichever row you want, because
every chunk is embedded on the way in. "Free" means free of a *model* call at
eval time, not free of a server.

---

## Results

Recorded 2026-09-12 from `docs/eval/results/*.json`, committed alongside so
every number below can be re-derived rather than trusted.

| | |
|---|---|
| corpus | 11 documents, 345 chunks |
| embedding | `mlx/mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ`, 1024 dimensions |
| chat (rewrites) | `mlx/mlx-community/Qwen2.5-3B-Instruct-4bit` |
| host | `fritznew.local` (Apple silicon) |
| degraded / unindexed | 0 / 0 on every sweep |

`degraded: 0` matters: no rewrite fell back, so the `condense` row is a
condense measurement and not the window's numbers wearing its name.

### Retrieval, all 24 cases

| Run | hit@5 | recall@5 | P@5 | MRR | nDCG@5 | median | p95 |
|---|---|---|---|---|---|---|---|
| `off` | 0.333 | 0.312 | 0.075 | 0.229 | 0.248 | 252ms | 358ms |
| `window` | 0.375 | 0.354 | 0.083 | 0.238 | 0.264 | 259ms | 344ms |
| `condense` | 0.458 | 0.417 | 0.092 | 0.258 | 0.291 | 258ms | 369ms |
| `window --expand 3` | 0.333 | 0.312 | 0.067 | 0.279 | 0.285 | **1024ms** | 1146ms |
| `gold` (ceiling, 11 cases) | 0.455 | 0.409 | 0.091 | 0.280 | 0.306 | 264ms | 387ms |

`P@5` is bounded above by `labels/5`, which is a property of the label set and
not of the retriever. Most cases carry one label, so a precision near `0.20` is
the ceiling — these sit well under even that.

**The latencies above exclude the rewrite's model call**, and that is a defect
in the harness rather than a property of the runs: the planner used to run
before the timer started, so `condense` reads as 258ms while actually costing a
round trip per question on top. `expand3`'s 1024ms is its three extra searches,
which the timer did see; its model call is missing too.

Fixed since these numbers were recorded. Query planning is now timed and
reported separately — `planning median … p95 …` in the text report,
`plan_latency` in the JSON, and its own row in a `--baseline` diff — so a
re-run will show what the rewrites actually cost. It changes none of the
conclusions below, which turn on quality; it does mean **the latency column
here understates `condense` and `expand3`**, and that #28's kill criterion,
stated in latency, could not have been evaluated with the harness as it stood.

### The eleven follow-ups, against the ceiling

The table #24 turns on, scored per case over the follow-ups alone — the ceiling
skips every case with no `gold_query`, so its report average is over a
different denominator.

| Run | hit@5 | MRR | nDCG@5 | MRR gap to ceiling |
|---|---|---|---|---|
| `off` | 0.091 | 0.045 | 0.057 | −0.235 |
| `window` | 0.182 | 0.064 | 0.093 | −0.216 |
| `condense` | 0.182 | 0.076 | 0.085 | −0.204 |
| `window --expand 3` | **0.273** | **0.155** | **0.183** | −0.125 |
| `gold` (ceiling) | 0.455 | 0.280 | 0.306 | — |

### The thirteen single-shot cases, as a control

| Run | hit@5 | MRR | nDCG@5 |
|---|---|---|---|
| `off` | 0.538 | 0.385 | 0.410 |
| `window` | 0.538 | 0.385 | 0.410 |
| `condense` | **0.692** | **0.412** | **0.465** |
| `window --expand 3` | **0.385** | 0.385 | 0.371 |

`off` and `window` are identical here, which is the design working: a window is
an identity on a question with nothing behind it.

### Generation

Not run. `--stage both` costs a model call a case and `--judge` another, and
nothing about #24 or #25 turns on them. The retrieval half is what the two
deferred defaults are about.

---

## The decisions

### #24 / [#159](../../../../issues/159) — `condense` stays off

**It does not do the thing it was deferred for.** #159 deferred the flip until
the harness showed condensing "beats the window on follow-up turns". On the
follow-ups it ties the window on hit (0.182 both) and gains 0.012 MRR — inside
the noise of an 11-case set, where one case is ±0.09 on hit.

What it *does* do is improve the **single-shot control**, from 0.538 to 0.692
hit — the half of the set where a rewrite was supposed to be a no-op. A model
asked to restate a standalone question is apparently cleaning it up in ways
retrieval likes.

Both readings come from a single run, and `condense` is the one knob here whose
output is not deterministic. Sampling its variance is the obvious next thing to
do, and it is what would turn "stays off, and here is an unexplained gain
elsewhere" into a decision about the gain itself.

That is a real effect and a different feature from the one #159 describes.
Flipping the default on it would be flipping it for a reason nobody stated and
nobody measured against. So `condense` stays off, and the control-half gain is
worth an issue of its own rather than a silent reinterpretation of this one.

### #25 / [#160](../../../../issues/160) — `expand: N` stays off

**It helps exactly where it should and hurts everywhere else.** On the
follow-ups it is the best non-ceiling row by a clear margin — 0.273 hit and
0.155 MRR against the window's 0.182 and 0.064, closing about 40% of the gap to
the ceiling. On the single-shot control it *loses* hit, 0.538 down to 0.385.
And it costs 1024ms against 259ms: **4× the latency of every question**.

A default that makes most questions four times slower and less likely to find
the passage, in exchange for better follow-ups, is not a default. It stays off
and stays worth reaching for on threads — which is what `--expand N` already
is.

### What neither of them is: the actual problem

The ceiling is **0.455 hit@5**. The standalone question a competent person
would have typed finds the labelled passage, in the top five, less than half
the time. No query planner can beat that, because the ceiling *is* the query
being right.

So the headroom on this corpus is not in query planning at all. It is in
chunking, ranking, or fusion — 400-token chunks over 345 of them, cut at
sentence boundaries with no structural awareness, scored by RRF over a single
embedding leg. That is roadmap #8 (re-ranking), #27 (parent-child) and #29
(structural chunking), and this is the first evidence in the repository that
says so rather than assuming it.

### How much to trust these

Eleven and thirteen cases. One case moving is ±0.09 on hit and ±0.03 on MRR.
None of the above is a significance claim — with a set this size there is
nothing to be significant about, which the plan said before any of it ran.

**Within one ingest the numbers are exact.** `--baseline` over two `window`
runs three minutes apart returns a delta of **0 on hit, recall, precision, MRR
and nDCG** — every quality metric, to the last digit. Only latency moves
(median −5.5ms, p95 +30.9ms), which is why D10 reports it and never asserts it.
That is the "A/B'd with a diff of two JSON files" the plan is for, working.

**Across a re-ingest they are not comparable, and that is the corpus, not the
harness.** An earlier sweep of this same label set is on record with different
figures; it ran before the corpus was fully ingested, so it scored a different
knowledge base. Numbers from two ingests are two measurements, not two samples
of one — which is the whole reason the instrument block travels with the
report.

The practical consequence: **these decisions rest on one run each**, reproduced
exactly rather than corroborated by an independent sample. For a knob whose
output is deterministic (`window`, `off`, `gold`) that is the whole story. For
`condense`, whose rewrite comes from a model, it is not: a second and third run
would sample its variance, and nobody has. The direction is clear enough to act
on and the effect sizes are not.

---

### What the fixture already hints at, and why it decides nothing

The CI fixture (`internal/eval/testdata/corpus/`, 8 documents, 5 cases, scored
against frozen vectors) shows the window fold retrieving *worse* than the raw
follow-up on its two threaded cases, with the ceiling far above both:

| | window MRR | ceiling MRR | gap |
|---|---|---|---|
| keyword | 0.2500 | 0.7500 | +0.5000 |
| hybrid | 0.2500 | 1.0000 | +0.7500 |

A plausible mechanism: folding the thread in makes a two-topic query, and a
strong embedder commits to the dominant topic. It is the shape #24 predicts.

It is also five synthetic cases over eight short documents, three of which have
no thread at all. It is a smoke test for the instrument, not evidence about the
default. The tables above are what decide.
