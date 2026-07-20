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
              LOW EFFORT                              HIGH EFFORT
           ┌────────────────────────────────┬────────────────────────────────┐
           │  QUICK WINS                    │  BIG BETS                      │
   HIGH    │  do first                      │  plan deliberately             │
  IMPACT   │                                │                                │
           │  • DB switching (--db /        │  • Conversational context /    │
           │    collections)                │    multi-turn ask              │
           │  • Context-window budget       │  • sqlite-vec ANN index        │
           │    guard on ask                │  • Ingestor framework +        │
           │  • Export / import / backup    │    Joplin / Evernote / YouTube │
           │  • More extractors (docx,      │  • Retrieval-quality cluster   │
           │    epub, code)                 │    (eval, HyDE, re-rank,       │
           │  • Document cleaning pass      │    parent-child, agentic)      │
           │  • Query rewrite / expand      │  • Embedding-model migration   │
           │  • Lineage: source_uri         │  • Structural chunking         │
           │    + inline citations          │                                │
           ├────────────────────────────────┼────────────────────────────────┤
           │  FILL-INS                      │  MONEY PITS                    │
   LOW     │  spare-time polish             │  avoid / defer                 │
  IMPACT   │                                │                                │
           │  • Shell completions           │  • Web / server mode           │
           │  • Richer doctor output        │  • Plugin system               │
           │  • Colorized / paged output    │  • Encryption at rest          │
           │  • Config validation           │  • Multi-user / sync           │
           └────────────────────────────────┴────────────────────────────────┘
```

The last three sections (**Ingestion & Sources**, **Retrieval Quality**,
**Evaluation**) came out of a later planning pass. They are *clusters* with
internal dependency ordering, so they live below the flat list rather than
scattered across it; the matrix above points at them by group.

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
   Expanded as **#30** (retrieval eval split from generation eval); it gates
   the whole **Retrieval Quality** cluster below.

8. **Re-ranking stage.** Insert an optional cross-encoder / LLM re-rank between
   hybrid retrieval and prompt assembly to lift top-k precision. Pairs
   naturally with the eval harness (7) to prove it helps. Part of the
   **Retrieval Quality** cluster below.

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

## Ingestion & Sources — new inputs, cleaner data, provenance

"Where does the knowledge come from, and can I trust it?" These share one
structural unlock, **#18**. Today ingestion is filesystem-path-centric —
`Ingester.IngestFile(path)`, `FileExtractor.ExtractFile(path)`, and a hardcoded
`supportedExts` map. The sources below are *not* plain files on disk (a local
HTTP API, mail archives, remote transcripts), so they need a seam *upstream* of
the extractor.

18. **Ingestor / `Source` abstraction.** *(foundational — unblocks 19–21)*
    Generalize ingestion beyond a local path. Define a `Source` that yields
    documents — a stable source id, a content stream, native metadata, and a
    `source_uri` — so non-file origins plug in without pretending to be files.
    `DefaultFileExtractor` becomes one `Source` among several; the chunk →
    embed → store tail is reused unchanged. Medium-high effort, but every
    ingestor below is small once this exists.

19. **Joplin ingestor.** *(user-requested)* Pull notes via the local Joplin
    Data API (`http://localhost:41184`, token auth). Note id → `source_uri`;
    notebook + tags → metadata. Joplin notes are Markdown, so this rides the
    Markdown-aware cleaning/chunking paths (#22, #29).

20. **Evernote-archive ingestor.** *(user-requested)* Parse `.enex` export
    files (XML: a `<note>` per entry). One note = one document; note title →
    title, note GUID/title → `source_uri`, `<created>`/`<updated>` + `<tag>`s →
    metadata. Note bodies are ENML (an HTML-ish markup), so this rides the
    HTML/Markdown cleaning path (#22) to reach plain text; attachments
    (`<resource>`) are out of scope for a first cut.

21. **YouTube-transcript ingestor.** *(user-requested)* Fetch transcripts
    (timedtext / `yt-dlp --write-auto-subs`). Video URL → `source_uri`; keep
    timestamps so a citation can deep-link `…&t=<sec>`. Chunk on transcript
    cadence, not sentence punctuation — auto-captions rarely carry `. ` breaks.

22. **Document cleaning / normalization.** *(user-asked: "clean up before
    ingesting?")* **Current:** preprocess only *extracts* text (strips Markdown
    fences/headings, HTML tags) — there is no cleaning stage. Add an optional
    normalization pass before chunking: collapse whitespace, NFC-normalize
    Unicode, drop boilerplate (email signatures + quoted replies, PDF running
    headers/footers, web nav chrome), and de-duplicate repeated blocks.
    Improves every downstream stage; pair with eval (#30) to prove it helps.

23. **Lineage / provenance.** *(user-asked: "how can I tell the source of a
    piece of information?")* **Current:** each retrieved chunk already carries
    `Citation = "path §chunkIndex"`; the `qa` template injects it as `Source:`
    per chunk and `ask` prints a numbered source list — but the *answer text*
    itself doesn't cite, and provenance is only a local file path. Two layers:
    - **Storage:** `source_type` + `source_uri` columns on `documents` so a
      citation points at the Joplin note / message / video, not a temp path.
    - **Answer-level:** ask the model to emit inline `[n]` markers keyed to the
      printed source list, and optionally a groundedness check that flags
      answer sentences with no supporting chunk.

---

## Retrieval Quality — better answers from the same corpus

Coupled levers that raise answer quality without adding documents. Ordered by
**dependency**, not just impact: **build the eval split (#30) first** so each
lever is measured, not guessed. Cheap query-side wins (#24–#26) come before the
heavier retrieval-shape changes (#27), the structural chunking they lean on
(#29), and the agentic loop (#28). Re-ranking (#8, above) belongs to this
cluster too.

24. **Query rewriting.** Clean the raw question before retrieval — expand
    pronouns, fix spelling, drop chit-chat — via one cheap LLM call. Low
    effort; most valuable once `ask` is multi-turn (#5), where the live
    question depends on prior turns.

25. **Query expansion (multi-query).** Retrieve on several paraphrases /
    synonym sets and fuse the hit-lists with the existing RRF. Cheap — reuses
    the hybrid + RRF plumbing already in `search`.

26. **Hypothetical Document Embeddings (HyDE).** Generate a hypothetical
    answer, embed *that*, and search by it — helps when the question's wording
    is far from the corpus's. One extra LLM call; gate it behind eval (#30), it
    doesn't always win.

27. **Parent-child retrieval.** Embed and search *small* child chunks for
    precision, but feed the enclosing *parent* (section or document) to the LLM
    for context. Needs a parent link in storage and pairs with structural
    chunking (#29). Strong precision-plus-context win.

28. **Iterative / agentic retrieval.** Let `ask` run multi-hop: retrieve →
    reason → issue follow-up queries until it has enough, instead of one shot.
    Highest effort here; builds on query rewriting (#24) and conversational
    context (#5). A deliberate big bet.

29. **Better chunking strategies.** *(user-asked: "is chunking fixed-size?")*
    **Current: yes — fixed-size.** `Chunker.Split` cuts every `Size*4` bytes,
    then snaps back to the nearest sentence separator (`. `, `\n\n`, `! `,
    `? `) and UTF-8 rune boundary, with token overlap. It is structure-blind.
    Add: Markdown-heading + code-block-aware splitting, recursive splitting, and
    optional semantic (embedding-distance) chunking, selectable per source.
    Feeds parent-child (#27); measure against eval (#30).

---

## Evaluation — measure before tuning

30. **Separate retrieval eval from generation eval.** *(user-asked; expands
    #7)* Score the two stages independently so a regression is attributable:
    - **Retrieval:** recall / precision / MRR / nDCG on labelled
      query→relevant-chunk sets.
    - **Generation:** answer quality — faithfulness / groundedness and
      correctness — via an LLM-judge or reference answers.
    This is the gate for the whole **Retrieval Quality** cluster: land it
    before #24–#29 (and before #8) so every change is proved, not hoped.

---

## Suggested near-term order

1. Ship the four original **Quick Wins** (1–4) — cheap, and they close the
   gaps requested earlier.
2. **Trust the answers:** document cleaning (#22) + lineage `source_uri` /
   inline cites (#23). Small, high-value, and they make every later change
   legible.
3. **Feed real work:** the `Source` abstraction (#18), then the Joplin
   ingestor (#19) — user-blocking; Evernote `.enex` (#20) and YouTube (#21)
   follow the same seam.
4. **Then measure, then tune:** land the eval split (#30) before touching the
   **Retrieval Quality** cluster, and take that cluster in dependency order —
   cheap query-side wins (#24–#26) → re-ranking (#8) / parent-child (#27) →
   agentic (#28), with structural chunking (#29) slotted in where parent-child
   needs it.

The **Conversational context** big bet (#5) pairs naturally with query
rewriting (#24) and agentic retrieval (#28); do it alongside that cluster
rather than before it. Back all retrieval changes with the **eval split (#30)**
so they are measurable before re-ranking and `sqlite-vec` land.
