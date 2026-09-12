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

> **Not yet run.** Every table below is empty because no sweep has been
> recorded against this label set. Fill them from `docs/eval/results/*.json`
> after `make eval-defaults`, and commit the JSON alongside so the numbers can
> be re-derived rather than trusted.

### Retrieval, all 24 cases

| Run | hit@5 | recall@5 | P@5 | MRR | nDCG@5 | median | p95 |
|---|---|---|---|---|---|---|---|
| `off` | | | | | | | |
| `window` | | | | | | | |
| `condense` | | | | | | | |
| `window --expand 3` | | | | | | | |

`P@5` is bounded above by `labels/5`, which is a property of the label set and
not of the retriever. Most cases carry one label, so a precision near `0.20` is
the ceiling, not a finding.

**Check the `degraded` count before reading any rewrite row.** `condense`
degrades instead of failing, so a run where every model call timed out still
reports as a condense run — carrying the window's numbers under the condense
name. The report counts the cases whose planning fell back, prints
`! query planning fell back on N of M cases` above the metrics, and records
`degraded` in the JSON; `--baseline` warns when either side has any. Anything
above zero means the row measures a mixture, and the fix is a faster rewrite
model, not a footnote.

### The eleven follow-ups, against the ceiling

This is the table #24 turns on. The ceiling is scored over the follow-up cases
only, so compare per case — the ceiling skips every case with no `gold_query`,
and its report average is over a different denominator.

| Run | hit@5 | MRR | nDCG@5 | gap to ceiling (MRR) |
|---|---|---|---|---|
| `off` | | | | |
| `window` | | | | |
| `condense` | | | | |
| `gold` (ceiling) | | | — | — |

### The thirteen single-shot cases, as a control

| Run | hit@5 | MRR | nDCG@5 |
|---|---|---|---|
| `off` | | | |
| `window` | | | |
| `condense` | | | |

### Generation

| Run | includes | citations | groundedness | correctness | faithfulness |
|---|---|---|---|---|---|
| `--stage both` | | | | | |
| `--stage both --judge` | | | | — | — |

### Instrument

| | |
|---|---|
| embedding model | |
| chat model | |
| judge model | |
| host | |
| recorded | |

Latency moves with the machine as much as with the code, which is why the host
is part of the record and why `--baseline` warns when it changes.

---

## The decisions

> **Pending the numbers above.** Each needs a stated outcome, a link to the
> report it came from, and the corresponding edit to `README.md` and
> `docs/plans/next-steps.md` — including "stays off", which is a result and not
> a failure to decide.

- **#24 / [#159](../../../../issues/159) — should `condense` become the default?**
- **#25 / [#160](../../../../issues/160) — should `expand: N` become the default?**

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
