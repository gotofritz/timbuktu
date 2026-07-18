# Plan 10: User Guide

## Goal

Write a friendly, comprehensive user guide at `docs/user-guide.md` for
non-technical users who can run terminal commands but have no background
in AI, machine learning, or search systems.

The guide teaches users to build and query a personal knowledge base with
timbuktu + llama.cpp, starting from "what is a RAG?" and ending with
advanced query patterns.

Out of scope: installation steps for timbuktu, llama.cpp, or any other
third-party tool. A troubleshooting section belongs in `README.md`, not
in the user guide.

---

## Deliverables

- `docs/user-guide.md` — the guide itself
- `README.md` — Status section replaced with a link to the guide; new
  Troubleshooting section added

---

## Output File

`docs/user-guide.md`

Tone: clear, friendly, no jargon without explanation. Every technical term
introduced once, in plain English, before first use. No assumed knowledge
beyond "I can open a terminal and type commands."

---

## Guide Structure

### 1. Introduction — What Is This and Why Should I Care?

Cover:
- Personal documents (notes, PDFs, saved articles, meeting notes) pile up
  and become hard to search
- Keyword search finds words, not meaning; misses synonyms, context,
  paraphrasing
- Timbuktu lets you ask questions in natural language and get answers
  drawn from your own files
- Everything runs locally — no cloud, no data leaves the machine

### 2. Background — RAG Pipelines in Plain English

Avoid the acronym until it has been explained. Use an analogy:

> Imagine you have a huge folder of notes. A very patient assistant reads
> every page, marks the most useful passages for any question you might
> ask, and when you ask something, fetches those passages and hands them
> to a second, smarter assistant who writes you a proper answer.

Concepts to define (one paragraph each, no bullet soup):

| Term | Plain-English definition |
|------|--------------------------|
| Embedding | A way of turning text into a list of numbers that captures its *meaning*, so similar ideas end up with similar numbers |
| Vector database | A store for those number-lists that can find "meaning neighbours" quickly |
| Chunk | A short excerpt (a few paragraphs) of a larger document; we split documents because models handle short passages better than whole books |
| Retrieval | Finding the chunks most relevant to a question |
| Augmented generation | Giving the retrieved chunks to an LLM as context before it writes an answer |
| RAG | Retrieval-Augmented Generation — the full pipeline: split → embed → retrieve → generate |

Diagram (ASCII, inline in the guide):

```
Your documents
     │
     ▼
┌──────────────┐
│  Preprocess  │  extract text, split into chunks
└──────────────┘
     │
     ▼
┌──────────────┐
│    Embed     │  turn each chunk into a meaning-vector (llama.cpp)
└──────────────┘
     │
     ▼
┌──────────────┐
│   Database   │  store vectors + original text
└──────────────┘
     │        ▲
     │  ask   │ retrieve top chunks
     ▼        │
┌──────────────┐
│     LLM      │  read chunks, write your answer (llama.cpp)
└──────────────┘
```

### 3. What Timbuktu Is Designed to Do

Cover:
- Single command-line tool (`tbuk`) for personal, local knowledge bases
- SQLite database — one file, easy to back up, no server to run
- Works with Markdown, plain text, PDF, and HTML files
- Provider-agnostic: llama.cpp by default; Ollama, OpenAI, Claude also supported
- Not a search engine, not a chat bot — a *knowledge base* you control
- What it does *not* do: web crawling, real-time indexing, multi-user access

### 4. Before You Start

State assumptions:

- Timbuktu is already installed (`tbuk version` prints a version number)
- llama.cpp is running locally with an embedding model and a chat model
  loaded (guide does not cover how to do this — see llama.cpp docs)
- You have some documents you want to query

Quick sanity check:

```bash
tbuk version
tbuk doctor
```

Explain each line of `tbuk doctor` output. Show what healthy output looks
like vs. what broken output looks like (provider unreachable, DB missing,
FTS5 unavailable). Reference README Troubleshooting for fixes.

### 5. First-Time Setup

```bash
tbuk init
```

Explain:
- Creates `~/.tbuk/` with `config.yaml` and default prompt templates
- Does not touch any of your documents
- Safe to re-run (idempotent)

Walk through `~/.tbuk/config.yaml`:
- What each section means (`llm`, `embedding`, `chunking`)
- Which settings most users never need to touch
- The one setting they might: `llm.model` and `embedding.model` (show how
  to set the model name to match what is loaded in llama.cpp)

### 6. Adding Your First Document

Use a single short Markdown file as the example throughout this section.
Tell the reader to create `~/notes/first-note.md` with a few lines about
a topic they care about, so the later query examples are meaningful.

#### Step 6a — Preprocess

```bash
tbuk preprocess ~/notes/first-note.md
```

Explain:
- Extracts plain text from the file
- Splits it into chunks (typically 200–800 words each)
- Saves normalised text to `~/.tbuk/extracted/`
- Does *not* talk to the LLM or embedding model yet
- Use `--dry-run` to see what would happen without saving anything

Show sample output.

#### Step 6b — Ingest

```bash
tbuk ingest ~/notes/first-note.md
```

Explain:
- Reads the extracted text produced in the previous step
- Sends each chunk to llama.cpp for embedding
- Stores chunk text + embedding in the database
- SHA256 deduplication: re-running on an unchanged file is a no-op

Show sample output including chunk count and timing.

#### Combining both steps

```bash
tbuk ingest ~/notes/first-note.md   # auto-preprocesses if needed
```

Explain that `ingest` calls preprocess automatically when the extracted
file is missing; the two-step flow exists for users who want to inspect
or edit extracted text before indexing.

### 7. Adding a Folder of Documents

```bash
tbuk ingest ~/notes/
```

Cover:
- Recursively processes `.md`, `.txt`, `.pdf`, `.html` files
- Skips unchanged files automatically
- Good for a first bulk import; re-run any time to pick up new/changed files

Practical advice: start with a small folder (10–20 files) to verify
everything is working before ingesting hundreds of documents.

### 8. Checking What Is in Your Knowledge Base

```bash
tbuk stats
```

Walk through each output field. Introduce `--format json` as useful for
scripting.

```bash
tbuk find                    # not valid — show what happens
tbuk find topic=cooking      # metadata filter example (explain metadata concept briefly)
```

Note: metadata filters become more useful once users attach tags (covered
in §11).

### 9. Asking Your First Questions

This is the payoff section. Build up from trivial to real.

#### 9a — The simplest query

```bash
tbuk ask "What did I write about X?"
```

Walk through what happens:
1. Question is embedded (same model, same vector space)
2. Database returns the most similar chunks
3. Chunks + question sent to the LLM
4. Answer streams to the terminal

Explain that the answer is *grounded in your documents* — the LLM cannot
invent facts not present in the retrieved chunks (within reason).

#### 9b — More specific questions

Show 3–4 example questions that go from vague to precise:

```bash
tbuk ask "Summarise my notes on project Alpha"
tbuk ask "What were the action items from the meeting on 12 March?"
tbuk ask "What did I decide about the database schema?"
```

Explain: more specific = better retrieval = better answer.

#### 9c — Controlling how many sources are used

```bash
tbuk ask --top 10 "What do I know about machine learning?"
```

Explain `--top`: fetches more chunks, useful for broad summary questions;
slower and uses more LLM context.

#### 9d — Non-streaming output

```bash
tbuk ask --no-stream "When did I last update the budget?"
```

Useful when piping output to another command or a file.

### 10. Searching Without the LLM

Sometimes you want to find passages, not generate answers.

#### Hybrid search (default, recommended)

```bash
tbuk search "machine learning fundamentals"
```

Explain hybrid: combines semantic (meaning) search with keyword (exact
word) search; generally the best results.

#### Keyword-only search

```bash
tbuk search --mode keyword "API rate limit"
```

Best when you remember the exact phrase. Faster, no embedding step.

#### Semantic search

```bash
tbuk search --mode vector "concepts related to cost reduction"
```

Best for conceptual queries where exact words are not known.

#### Controlling result count and score threshold

```bash
tbuk search --top 10 --min-score 0.7 "project deadlines"
```

Explain score: 0 = unrelated, 1 = identical. Start with 0.6–0.7 and
adjust.

#### JSON output for scripting

```bash
tbuk search --format json "budget 2025" | jq '.[].text'
```

### 11. Prompt Templates (Customising Answers)

```bash
tbuk template list
tbuk template show qa
```

Explain:
- Templates live in `~/.tbuk/prompts/`
- Default `qa` template is a general question-answer format
- Users can create custom templates for specific tasks

Walk through creating a simple custom template for a common personal use
case (e.g., a "meeting-notes" template that always formats its answer as
bullet action items).

```bash
tbuk ask --template meeting-notes "What were the decisions in last week's meetings?"
```

Show how template variables work:

```bash
tbuk ask --var style=concise "Summarise my notes on budgeting"
```

### 12. Keeping Your Knowledge Base Up to Date

#### When a document changes

```bash
tbuk update ~/notes/first-note.md
```

Explain: compares SHA256; re-ingests only if content changed. Safe to run
often.

#### When you delete a document

```bash
tbuk delete ~/notes/old-note.md
```

Explain: removes document and all its chunks from the database; does not
touch the file on disk.

#### Bulk refresh

```bash
tbuk ingest ~/notes/       # re-run any time; skips unchanged files
```

Practical schedule suggestion: run after any batch of edits, or on a
schedule (e.g., via cron once a day).

### 13. More Complex Use Cases

Each sub-section is a short scenario with commands:

#### 13a — Building a research archive

Scenario: user has saved 50 PDF papers on a topic. Walk through:
1. Create a dedicated folder
2. `tbuk ingest ~/papers/` (bulk import)
3. Ask synthesis questions across many papers
4. Use `--top 15` for broad synthesis
5. Use `--mode keyword` when looking for a specific citation

#### 13b — Personal diary / journal

Scenario: folder of daily `.md` notes. Cover:
- Entry naming conventions that help retrieval (date in filename)
- Querying across time: "what was I thinking about in March?"
- Summarising emotional themes across entries (show example prompt)

#### 13c — Work documentation

Scenario: team wikis, meeting notes, decision logs saved locally. Cover:
- Mixing `.md`, `.html`, `.pdf` sources
- Querying for decisions: "what did we decide about X?"
- Finding action items: "what tasks are assigned to me?"
- Using `tbuk find` with metadata (if user tagged docs)

#### 13d — Cooking / hobby knowledge base

Light, accessible scenario to show the tool is general-purpose. A folder
of recipe files, technique notes, ingredient lists. Questions like:
- "What can I make with chicken and lemon?"
- "How do I make a roux?"

### 14. Tips and Limitations

Honest section on what the tool can and cannot do.

**Works well:**
- Finding relevant passages you definitely wrote
- Synthesising across multiple documents
- Natural-language queries that keyword search would miss

**Does not work well:**
- Questions about things not in your knowledge base (LLM may hallucinate
  if retrieval fails; answer quality degrades gracefully)
- Very long documents with dense information density (chunking may split
  context; use smaller `chunking.size` in config)
- Real-time information (it knows only what you ingested)

**Chunk size tuning:**
- Smaller chunks (400–600) = more precise retrieval, less context per
  chunk
- Larger chunks (800–1200) = more context per chunk, retrieval less
  precise
- Start with defaults; adjust if answers feel too narrow or too vague

**When answers are wrong:**
- Try `tbuk search` on the same query to see what was retrieved
- Add `--top 10` to pull in more context
- Check that the relevant document is ingested (`tbuk stats`, `tbuk find`)
- Re-ingest after editing source documents (`tbuk update`)

---

## README Changes

Replace the Status table (tracks implementation subplans, not useful to
end users post-completion) with a single line linking to the user guide:

```markdown
## Documentation

See [User Guide](docs/user-guide.md) for a full walkthrough.
```

Add a Troubleshooting section to `README.md` covering the most common
failure modes:

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `tbuk doctor` shows LLM/embedding unreachable | llama.cpp not running, or wrong port | Check llama.cpp is started; verify `base_url` in config |
| `ingest` produces 0 chunks | File is empty or unsupported type | Check file has content; check extension is `.md/.txt/.pdf/.html` |
| `ask` gives irrelevant answers | Low retrieval quality or document not ingested | Run `tbuk search` to inspect retrieved chunks; run `tbuk update` |
| `ask` is very slow | Large `--top` value, slow model, or big chunks | Reduce `--top`; use a smaller/faster LLM model |
| Database error on start | DB file corrupted or wrong path | Check `database.path` in config; try `tbuk doctor` |
| `tbuk` command not found | Go bin dir not in PATH | Add `export PATH="$PATH:$(go env GOPATH)/bin"` to shell profile |

---

## PR Scope

One PR. Changes:
- `docs/plans/10-user-guide.md` (this file)
- `docs/user-guide.md` (the guide)
- `README.md` (Status → Documentation link + Troubleshooting)

Archive this plan in the same PR using the standard filename format.

## QA Checklist

- [ ] All links in `docs/user-guide.md` resolve
- [ ] Every command in the guide exists in `tbuk --help` output
- [ ] README renders correctly on GitHub (check Markdown)
- [ ] No `make check-ci` regressions (guide is docs only; no Go changes)
