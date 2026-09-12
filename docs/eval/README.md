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
tbuk --root ~/.tbuk-eval init
tbuk --root ~/.tbuk-eval ingest README.md docs/user-guide.md docs/initial-context.md docs/plans/
tbuk --root ~/.tbuk-eval doctor          # the Eval section must find every labelled path
make eval-defaults                        # writes docs/eval/results/*.json
```

A separate root keeps the measurement corpus out of a working knowledge base;
drop `--root` if you would rather use your own.

`doctor` before sweeping is not optional. A label naming a document that was
never ingested scores zero on every run and reads on the report exactly like a
retrieval failure.

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
