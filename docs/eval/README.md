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
make eval-ingest           # init + ingest + doctor, into ~/.tbuk-eval
make eval-defaults         # the five sweeps, into docs/eval/results/*.json
make eval-condense-spread  # condense ×3, and window as the control (#173)
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
| generation, `--judge` | one or two model calls per case; needs `answer` / `must_include` on the cases |

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

Not run at the time those numbers were recorded, and it could not have been:
the set carried **no reference answers at all**. `--stage both --judge` over it
scored the retrieval half normally and reported no generation block — which on
the page is indistinguishable from a model that answered nothing, and which is
exactly how one `--hops 0` baseline run was spent finding out.

Two things changed after that. Every case now carries an `answer` (the
reference the judge marks correctness against) and a `must_include` list (short
substrings a correct answer has to contain, scored without a model at all), and
`tbuk eval` **refuses** a generation run over a set with nothing to mark against
rather than reporting an empty half. A set where only some cases are scorable
still runs — that is a partial set, and the skipped list records it.

So the generation half is now runnable. Nothing about #24 or #25 turns on it —
those were settled on retrieval — but [#161](../../../../issues/161)'s kill
criterion is stated in answer correctness, and this is what it needs.

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

Both readings came from a single run. Sampling that run's variance was the
obvious next thing to do, and it has been done: three runs, spread zero — the
rewrite is deterministic here — and the gain, read case by case, turns out to be
one synonym swap and one capital letter. It is not the model cleaning up a
question. See [Sampling `condense`'s variance](#sampling-condenses-variance-173).

That looked like a real effect and a different feature from the one #159
describes. Flipping the default on it would have been flipping it for a reason
nobody stated and nobody measured against. So `condense` stays off — and the
control-half gain got an issue of its own ([#173](../../../../issues/173))
rather than a silent reinterpretation of this one. That issue is now answered
below, and the answer is that the gain is two cases of surface-form luck.

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
`condense`, whose rewrite comes from a model, it was not — until it was
sampled. It has been now, three times: the spread is **zero**, because the model
rewrites deterministically at this temperature. See [Sampling `condense`'s
variance](#sampling-condenses-variance-173) below, which also explains the
single-shot gain and finds it is not what anybody assumed.

---

## Sampling `condense`'s variance ([#173](../../../../issues/173))

The tables above rest on one run each. For `off`, `window` and `gold` that is
the whole story — they are deterministic, and a second run returns the same
digits. `condense` is the one row a model wrote, and its variance had never been
sampled. It has now.

Thirteen single-shot cases means one case moving is ±0.077 on hit. The gain the
decision above notes — 0.538 to 0.692 on the control half — is two cases, so
"the rewrite helps standalone questions" and "two cases landed differently"
were the same observation until something told them apart.

Two things do. The **spread** says whether the two cases land the same way
twice. The **queries** say what the rewrite did to them. Both are below, and
they do not agree with what the issue expected.

### Running it

```bash
make eval-ingest           # once, if ~/.tbuk-eval is not already built
make eval-condense-spread  # RUNS=3 by default
```

It writes four files into `results/`: `condense-spread.{json,txt}` and
`window-spread.{json,txt}`. No ingest happens between the runs — `tbuk eval`
only reads — so the corpus they disagree about is one corpus, and the report's
instrument block is what says so.

**`window-spread` is the control, and it is read first.** The window is
deterministic, so its spread must be exactly zero on hit, recall, precision,
MRR and nDCG. Anything else means the corpus or the index moved underneath the
runs, and the `condense` column measures that rather than the planner.

The JSON carries a row per case — the question, the distinct queries it
actually ran on, and its hit in each run — so the single-shot half can be
re-split out of it afterwards, and so a case that moved can be read against the
query it moved on. That is what answers the second half of #173: whether the
rewrite is dropping interrogative framing and leaving noun phrases closer to
the prose being searched, or doing something else entirely.

### Results

Recorded 2026-09-12 from `results/condense-spread.json` and
`results/window-spread.json`, three runs each, no ingest between them. Same
instrument as the tables above — `Qwen3-Embedding-0.6B-4bit-DWQ`,
`Qwen2.5-3B-Instruct-4bit`, `fritznew.local` — and the single-run figures
reproduce exactly, which is the first thing the spread says.

**`condense`, 24 cases, three runs:**

| metric | mean | min | max | stddev | span |
|---|---|---|---|---|---|
| hit@5 | 0.458 | 0.458 | 0.458 | 0.000 | **0.000** |
| recall@5 | 0.417 | 0.417 | 0.417 | 0.000 | **0.000** |
| P@5 | 0.092 | 0.092 | 0.092 | 0.000 | **0.000** |
| MRR | 0.258 | 0.258 | 0.258 | 0.000 | **0.000** |
| nDCG@5 | 0.291 | 0.291 | 0.291 | 0.000 | **0.000** |
| latency median | 261ms | 260ms | 262ms | 1.4 | 4ms |
| planning median | 457ms | 455ms | 460ms | 3.0 | 5ms |

`degraded_per_run: [0, 0, 0]` — no case fell back, so this is a `condense`
measurement and not the window's numbers wearing its name. **Zero cases moved
between runs, and all 24 produced exactly one distinct plan across the three.**
The model wrote the identical rewrite every time.

**The control, `window`, three runs:** span `0.000` on hit, recall, precision,
MRR and nDCG, as it must be. Only latency moves (median 252–258ms), which is
the machine and not the retriever.

**The two halves**, re-split from the per-case rows. Valid to read off the
single-run reports because the spread is zero and the instrument block matches:

| | `window` | `condense` | delta |
|---|---|---|---|
| single-shot (13), hit@5 | 0.538 | **0.692** | **+0.154** |
| single-shot, MRR | 0.385 | 0.412 | +0.027 |
| single-shot, nDCG@5 | 0.410 | 0.465 | +0.055 |
| follow-ups (11), hit@5 | 0.182 | 0.182 | 0.000 |
| follow-ups, MRR | 0.064 | 0.076 | +0.012 |

### What it means

**The variance is not small. It is zero.** Greedy decoding at the template's
temperature, on this model and this hardware, is deterministic: the same
question produces the same rewrite, three times out of three, for every case in
the set. So the question #173 asked — *does the gain survive resampling?* — has
an answer, and it is that there was never anything to resample. The +0.154 on
the control half is exactly as reproducible as the deterministic rows are.

That closes item 1. **Item 2 is where it gets interesting, and the guess in the
issue is wrong.**

The guess was that `condense` "drops interrogative framing and leaves noun
phrases closer to the prose being searched". It does not. Every one of the 13
single-shot rewrites is still a question, and **4 of the 13 differ from what was
asked only in capitalisation.** The two cases that flip from miss to hit:

| case | asked | ran on |
|---|---|---|
| `context-budget` | what happens when the prompt is too **big** for the model? | What happens when the prompt is too **large** for the model? |
| `kebab-case-queries` | **w**hy does searching for a hyphenated word behave oddly? | **W**hy does searching for a hyphenated word behave oddly? |

The second one is the whole finding. Nothing changed but a capital letter.
`chunks_fts` is built with `unicode61`, which case-folds, so the keyword leg
returned the identical ranking — **the flip came from the vector leg reacting to
a capital W.** The first is a one-word synonym swap that happens to match the
corpus's own wording.

So the "single-shot gain" is two cases: one lexical luck, one surface-form
perturbation of the embedding. Not a model cleaning up a question.

**What that settles.** #173 item 3 asked whether `condense` is really a
different feature — "clean the question" rather than "resolve the follow-up" —
deserving its own name and its own default. On this evidence, no. Cleaning is
not what it is doing. What it is doing on standalone questions is re-rolling the
vector leg with a slightly different surface form, at **457ms of planning per
question**, which is where `--rewrite condense` costs 2.7× the search it
precedes. A knob that perturbs the query and sometimes lands better is not a
feature; it is a coin with a latency bill.

`condense` stays off, and the unexplained gain recorded above is now explained
rather than outstanding. #24 and #25 are unaffected — both were already settled
on the follow-up numbers, where `condense` ties the window exactly.

### The one run that would confirm it

Re-run both sweeps at `--mode keyword`. The keyword leg case-folds, so:

- `kebab-case-queries` **must** score identically under both modes. If it still
  flips, the mechanism is not what is written above.
- `context-budget` may still flip — `big` and `large` are genuinely different
  tokens to BM25.

That is two model-free runs for `window` and two cheap ones for `condense`, and
it separates "the embedder is surface-form sensitive" from "the rewrite found
better words" without arguing about it.

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
