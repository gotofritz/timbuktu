# Subplan 05: Conversational context — multi-turn `ask`, query planning, iterative retrieval

Lands roadmap **#5** (conversational context / multi-turn `ask`,
[#145](../../../../issues/145)) from `docs/plans/next-steps.md`, together with
the cluster-mates that roadmap entry names as belonging with it — **#24 query
rewriting** and **#28 iterative / agentic retrieval** — plus **#25 query
expansion**, which falls out of the same seam for the price of a loop.

> Numbering: the archived `2026-07-18-1748-ccdb278-05-llm-providers.md` used
> "05" for the original LLM-provider subplan, the way
> `2026-09-05-2317-c9baa8f-33-context-guard.md` reused "33". This plan is the
> roadmap's #5.

---

## Why these are one plan and not four

`next-steps.md` already says it — "the **Conversational context** big bet (#5)
pairs naturally with query rewriting (#24) and agentic retrieval (#28); do it
alongside that cluster rather than before it" — but the reason is mechanical,
not thematic.

A follow-up question is not a query:

```
> how do slices grow?
  … answers from three chunks of the Go docs …
> and maps?
```

`and maps?` retrieved on its own reaches nothing. The model would see the
thread and the retriever would not, so the second answer comes from the first
answer's chunks plus the model's priors — a chat that has quietly stopped being
a RAG system, and says nothing about it. So:

- **#5 without #24** is a conversation whose evidence is one turn stale.
- **#24 without #5** is a spell-checker: there is no thread to resolve a pronoun
  against.
- **#28** is the same loop again with the model, rather than the user, writing
  the follow-up question.
- **#25** is that loop unrolled once, with no model in it at all.

One seam serves all four: something that turns *(thread, question)* into *the
queries retrieval actually runs*.

```
                     (thread, question)
                             │
                   ┌─────────▼─────────┐
                   │   query planner   │   internal/rewrite
                   └─────────┬─────────┘
                             │
      window │  condense  │  expand ×N  │  hop ×N (from the answer)
        #5       #24           #25              #28
                             │
              retrieval (hybrid + RRF, unchanged) ── #145 budget guard
```

Everything below the planner already exists. Everything above it is this plan.

---

## Goal

```bash
tbuk ask --session go "how do slices grow?"
tbuk ask --session go "and maps?"            # retrieval knows this means Go maps
tbuk ask -c "show me the second one again"   # -c = most recently used session
tbuk chat --session go                       # the same thread, interactively
tbuk session list                            # name, turns, last used
tbuk session show go                         # the thread, with its citations
tbuk session delete go --yes
```

Success is three things:

1. A follow-up retrieves what a standalone rephrasing of it would have
   retrieved — measurable, and measured (see **Evaluation gate**).
2. The thread survives the process, a `tbuk reindex`, and a new terminal.
3. `tbuk ask` with no `--session` behaves **exactly** as today — same prompt,
   same tokens, same output. No flag, no change; that is the regression bar the
   test suite pins.

---

## Design decisions (and alternatives rejected)

### D1. Sessions live in the knowledge base's SQLite database

A conversation is *about a corpus*. `--root` (and, when
[#140](../../../../issues/140) finishes, `--db`) already switches corpora; a
thread that followed the user across that switch would replay questions whose
evidence is not in the index any more. Putting sessions in the DB makes the
scope automatic: switch the knowledge base, switch the threads.

*Rejected:* a JSON file per session under the root. Simpler to write, but it
needs its own scoping rule, its own listing code, its own locking, and it makes
`session show` a file parser rather than a query. The DB is already open, WAL,
`0o600`, and cascade-deleting.

### D2. Two commands, one store

`tbuk ask --session NAME` is the stateful one-shot: every invocation appends a
turn to a named thread. `tbuk chat` is a REPL loop that calls the same code
path in the same process.

Ordering matters and is deliberate: **`ask --session` ships first**, because
persistence is the hard half and it is the half that survives a pipe, a script
and a Ctrl-C. `chat` is then a `bufio.Scanner` around `RunAsk` — a small
milestone rather than a new subsystem.

*Rejected:* REPL-first. It would put the thread in process memory, and the
persistence layer would arrive as a rewrite rather than as a foundation.

### D3. History replays as messages, not as rendered prompt text

`llm.Message` is already a role-tagged slice and every adapter already sends
one. Prior turns go back as `{RoleUser, question}` / `{RoleAssistant, answer}`
pairs ahead of the current turn's rendered user message.

*Rejected:* a `{{ .History }}` block inside `user.tmpl`. It would need every
template edited, it hides the turn structure from providers that use it (Claude,
OpenAI both do), and it makes prior turns typographically indistinguishable from
the current question — the exact confusion that makes a model answer the
turn-before-last.

### D4. A stored turn is question + answer + citations, never the rendered prompt

The obvious implementation — remember the messages we sent — is wrong here,
because the user message of turn *n* contains turn *n*'s retrieved chunks. Replay
it and the prompt carries every chunk the thread ever saw: the cost grows
quadratically, and stale evidence outranks the passages retrieved for the
question actually being asked.

So a turn stores the question **as typed**, the query **as planned** (D5), the
answer text, and the citation strings. Retrieved chunk text is rendered into the
*current* turn only. The thread is cheap, and the freshest evidence is the only
evidence.

### D5. The retrieval query for a follow-up is planned, not typed

Three planners behind one interface, shipped in this order:

| Planner | Cost | Milestone | Roadmap |
|---|---|---|---|
| `window` | none | 1 (default) | #5 |
| `condense` | one cheap LLM call | 3 | #24 |
| `expand` | one call, N queries out | 4 | #25 |

`window` concatenates the last *n* user turns with the current question and
retrieves on that string — no model, no latency, and it already fixes `and
maps?`, because the words `slices grow` are still in the query. It is the
default precisely because it costs nothing and cannot fail.

`condense` is the real #24: one call rewriting *(thread, question)* into one
standalone question, which also picks up the spelling fixes and chit-chat
stripping the roadmap entry asks for. It is opt-in until the eval says
otherwise (D6).

*Rejected:* embedding the whole thread. Averaging a conversation's topics is
how a retriever ends up equidistant from all of them.

### D6. The condenser is gated, and its failure mode is the window

An LLM in the retrieval path is a new way for `ask` to be slow, wrong or down.
Two guards:

- **Fallback, not failure.** A condense call that errors, times out, or returns
  something empty or absurd (longer than the thread it condensed, say) falls back
  to `window` with a warning on the diagnostics stream. `ask` never fails
  because the *rewriter* failed.
- **Measured before default.** `condense` becomes the default only if the
  retrieval eval ([#126](../../../../issues/126) / roadmap #30) says it beats
  `window` on follow-up turns. Until then the manifest opts in.

### D7. The budget ladder gains a rung, above compaction

The context guard ([#141](../../../../issues/141), shipped) climbs: compact
retrieved text → drop lowest-ranked chunks → fail. History adds a rung
**above** compaction:

```
1. drop the oldest history turns, one pair at a time   ← new
2. compact the retrieved text (internal/squeeze)
3. drop the lowest-ranked chunks
4. fail, naming the knobs
```

History goes first because grounded evidence for the question in front of you
beats a transcript of the questions behind it, and because the user can restate
what the thread forgot but cannot restate what the corpus was not asked for.

The floor is the **most recent pair**: pronoun resolution dies without it, so it
is dropped only if the prompt still does not fit with nothing else left, and a
prompt that reaches that state warns that the thread was dropped entirely.

`fitToContext` therefore stops taking `(system, user)` and starts taking the
whole `[]llm.Message`; `promptTokens` sums the slice. Same estimator
(`chunking.CountTokens`), same warnings, one more rung.

### D8. Citations are stored as text, not as chunk ids

`chunks.id` is not stable: `ReplaceForDocument` deletes and re-inserts on every
re-ingest and every `reindex`. A foreign key would either cascade a thread's
provenance away or forbid the re-index. `RetrievedChunk.Citation` is already the
display form (`path §index`), so store that string. A session survives a full
re-embed with its provenance intact and no referential integrity to maintain.

### D9. Schema is edited into `schemaSQL` in place

Per AGENTS.md ("proof of concept") and the storage section of
`docs/initial-context.md`: a new knowledge base gets the tables; an existing one
gets a throwaway `scripts/` script named in the PR and deleted once it has run.
`storage.HasSessionTables` joins `HasSearchTextColumn` / `HasPunctuationTokenizer`
so `tbuk doctor` names the script instead of `ask --session` failing with a raw
`no such table`.

### D10. A turn is appended only when the answer completes

Ctrl-C mid-stream, a provider error, a normalize failure: nothing is written.
A thread never contains half an answer, because half an answer replayed as
context is worse than a thread that lost a turn — the model reads a truncated
assistant message as a statement it finished making.

The streaming path therefore tees into a `strings.Builder` **only when a
session is active**, so the single-shot path allocates exactly what it does
today.

### D11. Names are normalized; `--continue` is the ergonomic path

`--session NAME` is explicit and reproducible: same rule as topics —
`strings.ToLower(strings.TrimSpace(name))`, non-empty. An unknown name
**creates** the session (a thread is a shell history file, not a resource to
provision); an unknown name to `session show/rename/delete` is an error naming
the known sessions.

`--continue` / `-c` targets the most recently updated session, because that is
what a person actually types on the second question. It is an error when no
session exists — never a silent fallback to single-shot, which would look
identical and answer differently.

### D12. Iterative retrieval is off by default, behind a written kill criterion

`--hops N` (manifest `retrieval.max_hops`, default `0` = today's single shot)
runs retrieve → let the model name what is still missing → retrieve again →
fuse, bounded by `N` and by the context budget, which the loop re-checks on
every hop rather than at the end.

**Kill criterion, stated before the work starts:** if, on the eval split, two
hops do not beat one on answer correctness at no worse than 2× median latency,
the loop does not ship. The planner interface and the fusion stay — they are
milestones 3 and 4's — and milestone 5 is closed as "measured, not worth it".
Same discipline as plan 33's D7.

**Measured 2026-09-12. The criterion fired, and the loop does not ship.**

Built, run against the eval split at `--hops 0` and `--hops 2`
(`--stage both --judge`, `Qwen2.5-3B-Instruct-4bit` answering and judging,
`fritznew.local`), and removed:

| | `--hops 0` | `--hops 2` | |
|---|---|---|---|
| correctness | 0.783 | 0.78 | **+0.00 — did not beat one** |
| latency median | 412ms | 1273ms | **3.1× — the cap was 2×** |
| hit@5 | 0.375 | 0.29 | −0.08 |
| recall@5 | 0.354 | 0.27 | −0.08 |
| nDCG@5 | 0.264 | 0.23 | −0.04 |
| includes | 0.50 | 0.46 | −0.04 |

No case degraded, so every hop really ran; the +861ms is two model calls a
question, which is what it cost. It failed both limbs of the criterion and was
not close on either, and it made retrieval **worse** rather than leaving it
alone — which is the design working exactly as written and being wrong for this
corpus. Every round re-runs all queries and fuses them, so the follow-up query
brings a ranked list of its own and RRF averages the two; a passage that was
first on the original question is pushed below the cutoff by agreement with a
query that was never the question. Where the ceiling is 0.455 hit@5 there is
not enough signal for a second opinion to add to, so it only dilutes the first.

`rewrite.Hop`, `cli.RetrieveWithHops`, `--hops` and `retrieval.max_hops` came
back out. `search.FuseRRF` and the `Planner` interface stay — they are
milestones 3 and 4's, and neither depended on the loop. Numbers:
`docs/eval/README.md`.

Two things the milestone learned on its way out, both kept:

- `tbuk eval` **refuses** a generation run over a label set with nothing to mark
  an answer against. The first `--hops 0` baseline was spent discovering that
  `docs/eval/timbuktu-docs.yaml` carried no reference answers at all, so
  `--stage both --judge` scored retrieval and reported no generation half —
  indistinguishable, on the page, from a model that answered nothing.
- That set now carries an `answer` and a `must_include` per case, and a test
  loads it and asserts every case is scorable by both stages. Nothing had ever
  loaded it, which is how it lost a stage unnoticed.

**Roadmap #28 is answered on evidence, not abandoned.** What was measured is
this loop, on this corpus, with this model: retrieve → name what is missing →
retrieve → fuse. A different shape — reranking the union rather than fusing it,
or a corpus whose ceiling leaves room for a second opinion — is a different
question, and would need its own criterion written before the work.

### D13. Appends are transactional

`UNIQUE(session_id, turn_index)` plus `MAX(turn_index)+1` computed inside the
insert's transaction. Two `tbuk ask --session work` running at once get a
constraint error on the loser rather than a silently overwritten turn — the
right trade for a single-user tool where the concurrent case is a mistake.

### D14. Thread text is sanitized in both directions

Answers echo document text, and document text is untrusted (ANSI/OSC escapes —
`internal/cli/sanitize.go`). `session show` and the `chat` REPL are
document-derived output and go through `sanitizeWriter` / `stripControl` like
`search` and `ask` already do. Stored questions are user input and are stored
verbatim; they are sanitized on the way *out*, not on the way in, so the store
stays a faithful record.

---

## Schema

Appended to `schemaSQL` (D9):

```sql
CREATE TABLE IF NOT EXISTS sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,      -- normalized: lowercased, trimmed
    template   TEXT    NOT NULL DEFAULT '',  -- template the thread was opened with
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL              -- what --continue orders by
);

CREATE TABLE IF NOT EXISTS session_turns (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL,
    question   TEXT    NOT NULL,             -- as typed
    query      TEXT    NOT NULL DEFAULT '',  -- what retrieval actually ran (D5)
    answer     TEXT    NOT NULL,
    citations  TEXT    NOT NULL DEFAULT '',  -- newline-joined citation strings (D8)
    created_at TEXT    NOT NULL,
    UNIQUE(session_id, turn_index)
);

CREATE INDEX IF NOT EXISTS idx_session_turns_session
    ON session_turns(session_id, turn_index);
```

`template` is recorded, not enforced: a thread opened under `qa` and continued
under `brief` is legal and occasionally what you want, but `session show` says
which template each turn ran under, so a surprising answer is explicable.

`query` exists for the same reason: when a rewritten query retrieves the wrong
thing, the user has to be able to see the query that ran, not the question they
typed. It is what `session show --verbose` prints and what the eval harness
reads.

---

## Package changes

```
internal/conversation/   NEW — Thread, Turn; Replay(turns, limit) and
                         Messages(system, user, turns) assemble the prompt.
                         Pure: no DB, no LLM, no cobra — so the budget
                         arithmetic is table-testable without a fake of anything.

internal/rewrite/        NEW (M3) — Planner interface:
                             Queries(ctx, thread []Turn, question string) ([]string, error)
                         Window (deterministic, M1 lives here too), Condense (M3),
                         Expand (M4). Each is a struct; the LLM ones take a
                         llm.LLM and CallOptions, so a fake chat drives the tests.

internal/storage/        SessionRepo — Create, GetByName, MostRecent, List,
                         Rename, Delete, AppendTurn, Turns. Same shape as the
                         existing repos: takes *sql.DB, wraps errors as
                         "SessionRepo.Method: %w", returns ErrNotFound on a miss.
                         HasSessionTables for doctor.

internal/search/         FuseRRF([][]SearchResult, k) exported — the fusion
                         Hybrid already does internally, lifted so multi-query
                         (M4) and multi-hop (M5) reuse it rather than reimplement
                         it. Hybrid calls the exported function; behaviour
                         unchanged, and a test pins that.

internal/retrieval/      Retriever.RetrieveMany(ctx, queries []string, topK, meta)
                         — runs each query and fuses. Retrieve stays as the
                         single-query wrapper, so every existing caller is
                         untouched.

internal/cli/ask.go      RunAsk takes an optional thread + planner; fitToContext
                         becomes []llm.Message-aware (D7); the streaming path
                         tees only under a session (D10).
internal/cli/chat.go     NEW (M2) — REPL over RunAsk.
internal/cli/session.go  NEW (M2) — session list / show / rename / delete.
internal/cli/app.go      App.Sessions() alongside Docs()/Ingester()/LLM().
```

`RunAsk`'s parameter list is already at the edge of readable and this plan adds
three more things to it. **Milestone 1 folds the optional parameters into the
existing `AskOption` chain** — `WithSession(thread, repo)`, `WithPlanner(p)` —
rather than growing the positional signature. That is the pattern
`WithErrOut` / `WithContextBudget` / `WithRequireContext` already set.

---

## Config and manifest

Thread bounds are a property of the *user's* setup and go in `config.yaml`:

```yaml
session:
  history_turns: 6   # prior Q/A pairs replayed into the prompt; 0 = sessions off
  max_turns: 0       # stored turns kept per session; 0 = keep everything
```

Query planning is a property of the *template* — it spends the template's model
at the template's temperature — so it goes in the manifest's existing
`retrieval:` block, next to `top_k` and `max_tokens`:

```yaml
retrieval:
  top_k: 5
  max_tokens: 0
  rewrite: window     # off | window | condense           (M1 / M3)
  window_turns: 2     # turns folded into the query under `window`
  expand: 0           # extra paraphrases, fused by RRF    (M4)
  max_hops: 0         # follow-up retrieval rounds         (M5)
```

`Config.Validate` rejects a negative `history_turns` / `max_turns`;
`loadManifest` rejects an unknown `rewrite` value and a negative `expand` /
`max_hops`, so a typo fails at template load rather than after a model call —
the rule `normalize` already follows. `tbuk init`'s `FillMissingDefaults`
backfills the `session:` block into an existing config; no migration.

Flags override for one run: `--rewrite`, `--expand`, `--hops`, each landing in
the milestone that introduces the knob.

---

## CLI surface

```
tbuk ask <question> --session NAME     append to (and create) a named thread
tbuk ask <question> --continue | -c    the most recently used thread
tbuk ask <question> --rewrite MODE     off | window | condense        (M3)
tbuk ask <question> --expand N         extra paraphrases              (M4)
tbuk ask <question> --hops N           follow-up retrieval rounds     (M5)

tbuk chat [--session NAME]             REPL; no --session = in-memory, unsaved
tbuk session list                      name, turns, last used
tbuk session show <name> [--verbose]   the thread; --verbose adds planned queries
tbuk session rename <old> <new>
tbuk session delete <name> [--yes]     reuses ConfirmYes, like tbuk delete
```

`chat` REPL commands, kept to the few that a session actually needs:
`/exit`, `/sources` (the last turn's citations), `/new [name]`, `/forget` (drop
the replayed history without deleting the thread), `/help`. No command
language beyond that — anything larger is a shell, and this is a CLI.

An unsaved `chat` (no `--session`) is deliberate: most conversations are not
worth keeping, and a tool that silently accumulates every idle question makes
`session list` useless within a week.

---

## Evaluation gate

Milestones 3–5 are retrieval changes and follow the rule the roadmap already
sets for the whole Retrieval Quality cluster: **measured, not hoped**. The
harness is [#126](../../../../issues/126) (roadmap #30).

What this plan needs from it, and contributes to it:

- A labelled set of **follow-up** queries — thread + follow-up question →
  relevant chunks — which does not exist for a single-shot corpus and is this
  plan's to add. The `session_turns.query` column makes real threads
  harvestable into that set.
- Recall / MRR on `window` vs `condense` vs the standalone rephrasing a human
  would have typed (the ceiling).
- Answer correctness for M5's kill criterion (D12).

Milestone 1 and 2 are **not** gated: persistence and a REPL are plumbing whose
correctness is a unit test, not a metric. If #126 has not landed when
milestone 3 is picked up, milestone 3 ships `condense` opt-in and non-default —
which is what D6 says anyway — and the default flip waits for the numbers.

---

## Interaction with what is already shipped

| Shipped | Interaction |
|---|---|
| Context guard ([#141](../../../../issues/141)) | Gains a rung and a message-slice signature (D7). Its warnings and `--require-context` semantics are unchanged. |
| `internal/squeeze` | Unchanged. Compaction still runs on retrieved text only; history is dropped whole, never squeezed — a compacted question changes what was asked. |
| `internal/normalize` | A records template buffers its output; the stored answer is the **normalized** text, which is what the user saw. |
| `reindex` / re-ingest | Sessions survive by D8. Nothing to do. |
| Export / import | `export` copies the DB whole, so threads ride along inside it; `import` reads that DB as a *document manifest* and never looks at other tables, so threads never land on the importing machine. Correct by construction — no flag, no filter. `session delete` before exporting is the answer for a thread you would rather not ship. |
| `doctor` | New line under **Database**: sessions present / tables missing (naming the script), plus the session count. |

---

## Testing (TDD, table-driven, ≥85% per package)

- **conversation:** replay limits (`history_turns`, fewer turns than the limit,
  zero); message assembly order (system, pairs oldest-first, current user last);
  dropping oldest pairs one at a time; the most-recent-pair floor; a thread of
  one turn.
- **storage:** tables created on a fresh KB and by the `scripts/` script over an
  existing one; name normalization (`Work` ≡ `work`); `GetByName` miss →
  `ErrNotFound`; `AppendTurn` assigns consecutive indexes and a second appender
  in the same transaction conflicts; `MostRecent` orders by `updated_at`;
  session delete cascades turns; document delete does **not** touch sessions.
  In-memory SQLite.
- **rewrite:** `window` output for 0/1/n turns; `condense` with a fake chat —
  happy path, error, timeout, empty completion, absurdly long completion — each
  falling back to `window` with a warning; `expand` returning N distinct queries
  and de-duplicating identical ones.
- **search:** `FuseRRF` parity — `Hybrid` before and after the extraction return
  identical results for the same inputs (the regression bar for a refactor of
  ranked output); fusion of three lists; a chunk in all three ranks above one in
  one.
- **retrieval:** `RetrieveMany` fuses and de-duplicates by chunk id; single
  query ≡ `Retrieve` (parity).
- **cli:** `ask --session` creates, appends, and replays (fake retriever asserts
  the query it was handed; fake chat asserts the message slice); `--continue`
  with no sessions is an error; a cancelled stream appends nothing (D10); the
  budget ladder drops history before chunks and warns (D7); **no `--session` ⇒
  byte-identical prompt and output to today** — the regression bar; every
  `session` subcommand; `chat` REPL driven by a scripted stdin, including
  `/forget` and `/exit`; control characters in a stored answer are stripped by
  `session show`.
- **integration:** extend `internal/cli/integration_test.go` with
  `init → ingest → ask --session → ask -c → session show → session delete`,
  embedding server and chat faked as it already does.

---

## Rollout (one PR per milestone)

| # | PR | Roadmap | Depends on | Issue |
|---|---|---|---|---|
| 1 | `feat(session): thread store and ask --session` — schema + `scripts/` script, `SessionRepo`, `internal/conversation`, `window` planner, `AskOption` wiring, budget rung, doctor line | #5 | — | [#157](../../../../issues/157) |
| 2 | `feat(chat): interactive REPL and session commands` — `tbuk chat`, `session list/show/rename/delete` | #5 | 1 | [#158](../../../../issues/158) |
| 3 | `feat(retrieval): query rewriting — condense a follow-up into a question` — `internal/rewrite` Condense, `--rewrite`, fallback path | #24 | 1 | [#159](../../../../issues/159) |
| 4 | `feat(retrieval): query expansion with multi-query RRF` — `FuseRRF` extraction, `RetrieveMany`, `--expand` | #25 | 3 | [#160](../../../../issues/160) |
| 5 | `feat(retrieval): iterative multi-hop ask behind a flag` — bounded loop, `--hops`, kill criterion | #28 | 3, 4, [#126](../../../../issues/126) | [#161](../../../../issues/161) |

[#145](../../../../issues/145) is the umbrella and closes with milestone 2 —
the point at which "conversational context" is a feature a user can use.
Milestones 3–5 are the cluster and close on their own issues.

Each PR updates `README.md`, `docs/initial-context.md` (schema, CLI list,
RAG section — per AGENTS.md, before merge) and `docs/user-guide.md`. This plan
is archived in the milestone-5 PR, or in milestone 4 if D12's kill criterion
fires.

---

## Risks

| Risk | Mitigation |
|---|---|
| Multi-turn quietly stops being RAG — the model answers from the thread, not the corpus | Every turn retrieves; the `Sources:` footer prints per turn; `--require-context` keeps working, per turn. A turn whose retrieval came back empty says so, as today. |
| History crowds out evidence in the context window | D7 drops history first, and warns when it does. `session.history_turns` defaults to 6, not "everything". |
| The condenser makes `ask` slower and occasionally wronger | Opt-in until measured (D6), falls back to `window` on any failure, and the planned query is stored so a bad rewrite is visible rather than mysterious. |
| Threads accumulate and nobody prunes them | `session list` shows counts and last-used; `session.max_turns` caps a thread; `chat` without `--session` saves nothing (the common case saves nothing by default). |
| The multi-hop loop is a token furnace | Off by default, bounded by `max_hops`, budget re-checked per hop, and a kill criterion that closes the milestone rather than shipping it (D12). |
| `RunAsk` grows into an unreadable function | Assembly moves to `internal/conversation` and planning to `internal/rewrite`; `RunAsk` gains options, not parameters. If milestone 3 cannot be added without another 100 lines in `ask.go`, that is the signal to split the command's core out first. |

---

## Out of scope (deliberate)

- **HyDE (#26)** — same query-side cluster, but it is orthogonal to threading and
  gated on the same eval. Its own issue when #126 lands.
- **Re-ranking (#8) and parent-child retrieval (#27)** — retrieval shape, not
  query planning. Different plan.
- **A per-template "this template is single-shot" knob.** `--session` works with
  any template. A records template (`anki`) in a thread is user error, not a
  configuration error; the docs say so and nothing enforces it.
- **Sharing, syncing or exporting a thread as a transcript.** `session show`
  writes to stdout and the shell has a `>`.
- **Summarizing dropped history into a running précis.** The standard trick, and
  a real one — but it is a second LLM call in the hot path to fix a problem
  `history_turns` mostly does not have at this corpus size. Revisit if the budget
  rung fires often in practice.
- **Tool use / function calling in the loop.** #28 as scoped here is
  retrieve-reason-retrieve, not an agent with tools.

---

## Open questions

- **Should `chat` retrieve once per thread or once per turn?** Per turn is
  specified above and is obviously right for a topic change; it is also N times
  the embedding calls for a thread that never leaves one topic. A cheap
  "is this a follow-up or a new question" check is possible, but it is another
  model in the path — measure the cost before adding one.
- **Does `--continue` deserve a scope?** Most-recently-used is global to the
  knowledge base. If threads get numerous, per-directory or per-template
  recency might read better. Not gating; the flag can gain a rule later without
  changing its spelling.
- **`session.max_turns` pruning policy.** Drop-oldest is assumed. Whether a
  pruned thread should keep a summary of what it dropped is the same question as
  the précis above, and gets the same answer for now: not yet.
