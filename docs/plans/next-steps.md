# Next Steps — Roadmap

Ideas for what comes after the POC + hardening work. This is a **direction
sketch**, not an implementation plan: each item is one or two lines. When one
is picked up, it gets its own numbered subplan under `docs/plans/` with real
detail (package layout, tests, acceptance).

Everything below is ranked on an **Impact / Effort matrix**. Impact = how much
it improves the product for a local-first single-user RAG CLI. Effort = rough
engineering cost against the current architecture.

---

## Impact / Effort Matrix

```
              LOW EFFORT                      HIGH EFFORT
           ┌──────────────────────────┬──────────────────────────┐
           │  QUICK WINS              │  BIG BETS                │
   HIGH    │  do first                │  plan deliberately       │
  IMPACT   │                          │                          │
           │  • DB switching (--db /  │  • Conversational context│
           │    collections)          │    / multi-turn ask      │
           │  • Context-window budget │  • sqlite-vec ANN index  │
           │    guard on ask          │  • Retrieval eval harness│
           │  • Export / import /     │  • Re-ranking stage       │
           │    backup                │  • Embedding-model        │
           │  • More extractors       │    migration / re-embed   │
           │    (docx, epub, code)    │                          │
           ├──────────────────────────┼──────────────────────────┤
           │  FILL-INS                │  MONEY PITS              │
   LOW     │  spare-time polish       │  avoid / defer           │
  IMPACT   │                          │                          │
           │  • Shell completions     │  • Web / server mode      │
           │  • Richer doctor output  │  • Plugin system          │
           │  • Colorized / paged     │  • Encryption at rest     │
           │    output                │  • Multi-user / sync      │
           │  • Config validation     │                          │
           └──────────────────────────┴──────────────────────────┘
```

---

## Quick Wins — high impact, low effort

1. **Database management / seamless switching.** *(user-requested)*
   Today `database.path` is a single value in `config.yaml`. Add first-class
   support for multiple knowledge bases ("collections" / "workspaces"): a
   `--db <path>` global flag and/or `TBUK_DB` env var overriding config, plus
   optional named collections resolved from config so `tbuk --db work ask ...`
   just works. No schema change — each DB is already self-contained.

2. **Context-window budget guard on `ask`.** *(user-requested: context management)*
   `retrieval.max_tokens` already trims retrieved chunks, but nothing bounds
   the *whole* prompt (system + template + chunks + question) against the
   model's context window. Add a single budget check that trims/warns before
   the call so `ask` never fails with "context length exceeded". Low effort,
   removes a sharp edge.

3. **Export / import / backup.** `tbuk export <file>` and `tbuk import <file>`
   to snapshot a knowledge base (DB + extracted cache, or a portable dump).
   High value for a local-first tool; mostly plumbing over existing repos.

4. **More extractors.** Add `docx`, `epub`, and source-code files to the
   `Extractor` backends. The interface already exists — each backend is small
   and independently testable.

---

## Big Bets — high impact, high effort

5. **Conversational context / multi-turn `ask`.** *(user-requested: context management)*
   `ask` is single-shot today. Introduce a session concept: keep a bounded
   conversation history, feed prior turns back into the prompt within the
   context budget, and let retrieval consider the running thread. This is the
   deep version of "context management" and touches `cli`, `retrieval`,
   `prompts`, and possibly storage (session persistence).

6. **`sqlite-vec` ANN index.** Vector search is a full table scan (fine below
   ~100k chunks). Swapping in `sqlite-vec` for approximate nearest-neighbour
   keeps search fast at scale. The `Searcher` interface was designed for this
   swap, but it's still real integration + benchmarking work.

7. **Retrieval evaluation harness.** A repeatable way to measure retrieval
   quality (labelled query→chunk sets, recall/MRR/nDCG). Unlocks confident
   tuning of chunking, hybrid weights, and re-ranking instead of guessing.

8. **Re-ranking stage.** Insert an optional cross-encoder / LLM re-rank between
   hybrid retrieval and prompt assembly to lift top-k precision. Pairs
   naturally with the eval harness (7) to prove it helps.

9. **Embedding-model migration.** Changing embedding model/dimension currently
   forces a full `--force` re-ingest. Support re-embedding from the cached
   extracted text (skip re-extraction), track the embedding model per document,
   and detect/repair dimension mismatches gracefully.

---

## Fill-ins — low impact, low effort

10. **Shell completions** (`tbuk completion bash|zsh|fish`) via cobra.
11. **Richer `doctor`** — surface index size, orphaned extracted files, stale
    documents (source changed on disk since last ingest).
12. **Output polish** — colorized/paged search + ask output, quiet/JSON-first
    modes for scripting.
13. **Config validation** — friendlier errors for bad provider/model/dimension
    combinations at load time rather than at call time.

---

## Money Pits — defer unless the product goal changes

14. **Web / server mode** — contradicts the local-first, no-web-UI constraint
    in `docs/initial-context.md`. Only revisit on a deliberate scope change.
15. **Plugin system** — large surface for little near-term gain; the
    `Extractor` / `Embedder` / `LLM` interfaces already give extension points.
16. **Encryption at rest** — files are already `0o600` and single-user;
    marginal benefit for large effort and key-management complexity.
17. **Multi-user / sync** — out of scope for a personal, single-machine tool.

---

## Suggested near-term order

Ship the four **Quick Wins** first (DB switching and the context-budget guard
directly answer the requested gaps and are cheap), then invest in the
**Conversational context** big bet, backed by the **eval harness** so retrieval
changes are measurable before re-ranking and `sqlite-vec` land.
