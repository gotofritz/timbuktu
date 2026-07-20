# Timbuktu User Guide

A practical guide to building and querying your own personal knowledge base —
no AI background required.

---

## Contents

1. [Introduction — What Is This and Why Should I Care?](#1-introduction)
2. [Background — RAG Pipelines in Plain English](#2-background)
3. [What Timbuktu Is Designed to Do](#3-what-timbuktu-does)
4. [Before You Start](#4-before-you-start)
5. [First-Time Setup](#5-first-time-setup)
6. [Adding Your First Document](#6-adding-your-first-document)
7. [Adding a Folder of Documents](#7-adding-a-folder-of-documents)
8. [Checking What Is in Your Knowledge Base](#8-checking-your-knowledge-base)
9. [Asking Your First Questions](#9-asking-questions)
10. [Searching Without the LLM](#10-searching)
11. [Prompt Templates](#11-prompt-templates)
12. [Keeping Your Knowledge Base Up to Date](#12-keeping-up-to-date)
13. [More Complex Use Cases](#13-complex-use-cases)
14. [Tips and Limitations](#14-tips-and-limitations)

---

## 1. Introduction

You probably have documents scattered across your computer — notes, PDFs, saved
web pages, meeting summaries, research papers. Over time they pile up and become
hard to search. Regular search tools find exact words; they miss synonyms,
context, and meaning.

Timbuktu lets you ask questions in natural language and get answers drawn from
your own files:

```
tbuk ask "What did I decide about the database schema?"
tbuk ask "What can I make with chicken and lemon?"
tbuk ask "What were the action items from last week's meetings?"
```

Everything runs on your own machine. No data is sent to the cloud. No
subscription. No internet connection required once set up.

---

## 2. Background

### How search usually works — and why it fails

When you search a folder for the word "cost", your computer looks for files
containing the letters c-o-s-t. It will miss a file that talks about "expenses"
or "budget overrun" even though those mean the same thing.

### A better approach: meaning-based search

A different approach is to convert text into a kind of fingerprint that captures
*meaning* rather than exact words. Two sentences that mean similar things end up
with similar fingerprints, even if they share no words.

Those fingerprints are called **embeddings** — lists of numbers that represent
what a piece of text is *about*. You do not need to know how they work; just
know that they allow searching by concept rather than by exact word.

### The pipeline

Timbuktu uses a four-stage pipeline called **RAG** (Retrieval-Augmented
Generation). Here is what each stage does:

```
Your documents
     │
     ▼
┌──────────────────┐
│   Preprocess     │  Extract plain text from each file. Split it into
│                  │  short passages called chunks (a few paragraphs each).
└──────────────────┘
     │
     ▼
┌──────────────────┐
│   Embed          │  Convert each chunk into a meaning-fingerprint
│                  │  (embedding) using a local AI model (llama.cpp).
└──────────────────┘
     │
     ▼
┌──────────────────┐
│   Store          │  Save the chunks and their embeddings in a local
│                  │  database (a single file on your machine).
└──────────────────┘
     │        ▲
     │  ask   │ retrieve top matching chunks
     ▼        │
┌──────────────────┐
│   Generate       │  When you ask a question, find the most relevant
│                  │  chunks, hand them to a language model (llama.cpp),
│                  │  and get a written answer back.
└──────────────────┘
```

**Key terms at a glance:**

| Term | What it means |
|------|---------------|
| Embedding | A fingerprint of a piece of text that captures its meaning |
| Chunk | A short excerpt of a larger document (a few paragraphs) |
| Vector database | A store for embeddings that can find meaning-neighbours quickly |
| Retrieval | Finding the chunks most relevant to your question |
| Generation | Writing an answer based on those chunks |
| RAG | The full pipeline: split → embed → retrieve → generate |

### Why split into chunks?

Language models work best with short, focused passages. Feeding an entire
100-page PDF into a model is impractical. Splitting into chunks means each
embedding is focused on one idea, retrieval is more precise, and answers are
more accurate.

---

## 3. What Timbuktu Is Designed to Do

Timbuktu (`tbuk`) is a single command-line tool for building and querying a
personal knowledge base from your own files.

**What it does:**
- Extracts text from Markdown, plain text, PDF, and HTML files
- Splits documents into chunks and computes embeddings using a local AI model
- Stores everything in a single SQLite database file — easy to back up, no
  server required
- Searches by meaning (vector search), by exact words (keyword search), or
  both at once (hybrid search)
- Retrieves relevant passages and sends them to a language model to generate
  natural-language answers
- Works with llama.cpp (default), Ollama, OpenAI, or Claude as the AI backend

**What it does not do:**
- Web crawling or real-time indexing
- Multi-user access or sharing
- Syncing across devices (though you can back up the database file yourself)
- Answering questions about things not in your knowledge base — it can only
  use what you have given it

---

## 4. Before You Start

This guide assumes:

1. **Timbuktu is installed.** The easiest way is to grab a pre-built binary
   from the [Releases page](https://github.com/gotofritz/timbuktu/releases):
   download the archive for your OS/architecture, extract the `tbuk` binary,
   and move it onto your `PATH` (e.g. `/usr/local/bin` on macOS/Linux). No Go
   toolchain is needed — the binary is self-contained. If you prefer to build
   from source, see the [README](../README.md#install). Then run `tbuk version`
   — it should print a version number. If you get "command not found", the
   binary is not on your `PATH`.

2. **An AI backend is available.** Timbuktu needs two things: an *embedding*
   model (to fingerprint your text) and a *chat* model (to write answers). The
   next section shows two ways to provide them — a fully local setup with
   llama.cpp, or a hybrid setup that uses Claude for the answers.

3. **You have some documents** to index — notes, PDFs, saved articles, anything
   in the supported formats.

### Choosing an AI backend

Timbuktu always needs a **local embedding model** (embeddings are what make
meaning-based search work, and there is no hosted embedding provider for
Claude). For *generating answers* you can either keep everything local with
llama.cpp, or hand the writing off to Claude. Pick one of the two paths below.

| | Fully local (llama.cpp) | Hybrid (llama.cpp embeddings + Claude) |
|---|---|---|
| Answer quality | Depends on local model size | Strongest |
| Privacy | Nothing leaves your machine | Retrieved chunks are sent to Anthropic |
| Cost | Free (uses your hardware) | Pay-per-use Anthropic API |
| Needs internet | No (after models downloaded) | Yes, for `tbuk ask` |
| Needs a GPU | Recommended for speed | Only for the embedding model |

Embeddings run locally in **both** paths, so start by setting up llama.cpp for
embeddings, then choose your generation backend.

#### Path A — Fully local with llama.cpp

llama.cpp runs GGUF models on your own machine and exposes an HTTP server.
Install it from the [llama.cpp
project](https://github.com/ggml-org/llama.cpp) (Homebrew: `brew install
llama.cpp`; or build from source). You get the `llama-server` command.

**Finding and downloading a model.** GGUF models live on
[Hugging Face](https://huggingface.co/models?library=gguf). Browse or search
there, open a repository (e.g.
[`unsloth/Qwen3-8B-GGUF`](https://huggingface.co)), and note two things: the
**repo id** (`user/name-GGUF`) and the **quantisation** you want (a file such
as `UD-Q4_K_XL` — smaller = faster and less memory, larger = higher quality).
You do **not** download the file by hand: `llama-server`'s `-hf` flag pulls it
straight from Hugging Face and caches it locally the first time you run it.

Timbuktu talks to embeddings and chat through **separate** `base_url`s, so run
**two** servers on two ports.

Embedding server (port 8080). Pick an embedding model whose output size matches
`embedding.dimension` in your config (default `768`); `nomic-embed-text` is
768-dimensional, so it fits the default without any config change:

```bash
llama-server -hf nomic-ai/nomic-embed-text-v1.5-GGUF:Q4_K_M \
  --embeddings --port 8080 -ngl 99
```

Chat server (port 8081). Any instruct/chat GGUF works here; a Qwen3 model is a
good default. Compared with a bare `llama-server -hf …` command, this adds what
RAG benefits from — a larger context window (`-c 8192`) so retrieved chunks
fit, and GPU offload (`-ngl 99`; drop it if you have no GPU):

```bash
llama-server -hf unsloth/Qwen3-8B-GGUF:UD-Q4_K_XL \
  --port 8081 -c 8192 -ngl 99
```

You can drop `--temp` / `--top-p` / `--repeat-penalty` from the original
command: Timbuktu sets sampling per request from the prompt template's
`manifest.yaml` (see section 11), so server-side sampling flags are just
defaults it overrides.

Then point the config at both servers (see section 5 for the full file):

```yaml
llm:
  provider: llama
  base_url: http://localhost:8081

embedding:
  provider: llama
  base_url: http://localhost:8080
  dimension: 768
```

#### Path B — Hybrid: local embeddings + Claude for answers

Keep the llama.cpp **embedding** server from Path A (Claude has no embedding
API, so this stays local), and let Claude write the answers. The `claude`
provider is the same Anthropic API that powers Claude Code.

1. Get an API key from the [Anthropic Console](https://console.anthropic.com)
   and export it:

   ```bash
   export ANTHROPIC_API_KEY=sk-ant-...
   ```

2. Set the LLM provider to `claude` and name a model explicitly (unlike llama,
   Claude has no "currently loaded" model to fall back on):

   ```yaml
   llm:
     provider: claude
     model: claude-sonnet-5    # any current Claude model id

   embedding:
     provider: llama            # still local
     base_url: http://localhost:8080
     dimension: 768
   ```

`tbuk doctor` won't probe the Claude endpoint (hosted APIs have no health
check) — it prints `hosted API — not probed`. That is expected; run a real
`tbuk ask` to confirm the key and model work.

### Quick sanity check

Run these two commands before doing anything else:

```bash
tbuk version
tbuk doctor
```

`tbuk doctor` checks that your configuration is valid, the database is
accessible, and the AI models are reachable. A healthy output looks like this:

```
Config
  path:        ~/.tbuk/config.yaml
  status:      ✓ valid

Database
  path:        ~/.tbuk/tbuk.sqlite
  status:      ✓ open
  documents:   0
  chunks:      0

LLM (llama)
  url:         http://localhost:8080
  status:      ✓ healthy

Embedding (llama)
  url:         http://localhost:8080
  status:      ✓ healthy
  dimension:   768

Preprocessing
  extractors:  markdown, text, html, pdf

Search
  fts5:        ✓ available
  vector:      ✓ available
  hybrid:      ✓ available

Prompts
  dir:         ~/.tbuk/prompts/
  templates:   qa
```

If any line shows an error (✗ or "unreachable"), check the Troubleshooting
section in the README before continuing.

---

## 5. First-Time Setup

```bash
tbuk init
```

This creates `~/.tbuk/` with:
- `config.yaml` — your configuration
- `prompts/` — templates that control how the AI formats its answers

It is safe to run more than once. It will not overwrite anything that already
exists.

### Understanding the configuration

Open `~/.tbuk/config.yaml` in any text editor. It looks like this:

```yaml
database:
  path: ~/.tbuk/tbuk.sqlite

llm:
  provider: llama    # llama | ollama | claude | openai
  model: ""          # leave empty to use the model currently loaded in llama.cpp
  base_url: http://localhost:8080

embedding:
  provider: llama    # llama | ollama | openai
  model: ""          # leave empty to use the model currently loaded in llama.cpp
  base_url: http://localhost:8080
  dimension: 768

chunking:
  size: 800          # how large each chunk is (measured in approximate word-equivalents)
  overlap: 100       # how much consecutive chunks overlap
```

**Settings most users never need to change:** `database.path`, `chunking.size`,
`chunking.overlap`.

**Settings you set once, per backend** (see [section 4](#4-before-you-start)
for the two backend paths):

- `llm.base_url` / `embedding.base_url` — the address of each llama.cpp server.
  If you run separate embedding and chat servers, give them different ports
  (e.g. `8080` for embeddings, `8081` for chat).
- `llm.provider` — `llama` for the fully local path, or `claude` for the hybrid
  path. With `claude`, also set `llm.model` (Claude has no loaded-model default)
  and export `ANTHROPIC_API_KEY`. `embedding.provider` stays `llama` either way.
- `llm.model` / `embedding.model` — leave empty for llama.cpp when only one
  model is loaded per server; set them if the server needs an explicit name.

---

## 6. Adding Your First Document

Let's walk through indexing a single file. If you do not have a file handy,
create one:

```bash
mkdir -p ~/notes
cat > ~/notes/first-note.md << 'EOF'
# Project Alpha Notes

## Overview
Project Alpha is our initiative to migrate the customer database to PostgreSQL.
The deadline is end of Q3. The main risks are data loss during migration and
downtime for the payment service.

## Decisions
- We will use a blue-green deployment to avoid downtime.
- The migration will happen on a weekend to reduce customer impact.
- Maria will lead the technical work; James handles stakeholder communication.

## Action items
- Maria: write migration scripts by July 15
- James: send update email to stakeholders by July 8
- Everyone: review rollback plan before July 20
EOF
```

Now index it.

### Step 1 — Preprocess

```bash
tbuk preprocess ~/notes/first-note.md
```

This extracts plain text from the file and saves the result to
`~/.tbuk/extracted/`. It does **not** chunk, embed, or talk to the AI model
yet — it is just preparing the text (chunking happens during ingest).

Use `--dry-run` to see what would happen without saving anything:

```bash
tbuk preprocess --dry-run ~/notes/first-note.md
```

### Step 2 — Ingest

```bash
tbuk ingest ~/notes/first-note.md
```

This reads the extracted text, sends each chunk to llama.cpp to compute its
embedding, and stores everything in the database. You will see a one-line
result:

```
/home/you/notes/first-note.md → 1 chunks embedded
```

### Shortcut: skip the preprocess step

You can run `tbuk ingest` directly without running `tbuk preprocess` first.
Ingest will call preprocess automatically if the extracted file is missing.

```bash
tbuk ingest ~/notes/first-note.md
```

The two-step flow exists for users who want to inspect or edit the extracted
text before indexing — for example, to remove boilerplate headers from a PDF.
For most purposes, running `tbuk ingest` directly is fine.

### SHA256 deduplication

If you run `tbuk ingest` on the same file twice without changing it, the second
run does nothing:

```
/home/you/notes/first-note.md → skipped (unchanged)
```

The file's content is fingerprinted; only changed files are re-indexed.

---

## 7. Adding a Folder of Documents

```bash
tbuk ingest ~/notes/
```

This processes every supported file (`.md`, `.txt`, `.pdf`, `.html`, `.htm`)
in the folder, including subfolders. Files that have not changed since the last
ingest are skipped automatically.

**Practical advice:** Start with a small folder (10–20 files) to confirm
everything is working before indexing hundreds of documents. A large ingest can
take several minutes depending on your hardware and model.

---

## 8. Checking Your Knowledge Base

### Summary statistics

```bash
tbuk stats
```

Output:

```
Knowledge Base Stats
────────────────────
Documents   : 1
Chunks      : 1
Embedded    : 1 / 1 (100%)
Approx size : 1 KB
DB path     : ~/.tbuk/tbuk.sqlite
DB size     : 0.2 MB
```

This tells you how many files are indexed, how many chunks they produced, and
whether all chunks have embeddings. If "Embedded" is less than "Chunks", some
chunks are missing embeddings — re-run `tbuk ingest` to fix that.

### Finding documents by metadata

```bash
tbuk find topic=cooking
```

This finds documents tagged with specific metadata. Metadata tagging is an
advanced feature covered in section 13. For now, `tbuk stats` is the quickest
way to check what is in the database.

---

## 9. Asking Questions

This is where the knowledge base pays off. Run:

```bash
tbuk ask "What did I write about Project Alpha?"
```

The answer streams to your terminal in real time, drawing on the content of
your indexed documents.

### What happens under the hood

1. Your question is converted to an embedding (same model, same meaning-space
   as your documents)
2. The database finds the chunks with the most similar embeddings
3. Those chunks, together with your question, are sent to the language model
4. The model writes an answer based only on the retrieved content
5. The answer streams to your terminal

When relevant chunks are found, the answer is grounded in what you have
written. If retrieval finds **nothing** — an empty knowledge base, or no
matching passages — `tbuk ask` prints a warning to stderr and answers from the
model's general knowledge instead (the response then reflects the model's
priors, not your documents, and no `Sources:` section is shown). Pass
`--require-context` to abort in that case rather than answer ungrounded.

### Building up from simple to specific

**Vague questions** work, but give vague answers:

```bash
tbuk ask "What do I know about Project Alpha?"
```

**Specific questions** give sharper answers:

```bash
tbuk ask "Who is leading the technical work on Project Alpha?"
tbuk ask "What are the action items for the Project Alpha migration?"
tbuk ask "What is the deadline for Project Alpha?"
```

**Synthesis questions** work well when you have many related documents:

```bash
tbuk ask "Summarise all the decisions I have made about Project Alpha"
tbuk ask "What risks have I noted across all my project notes?"
```

### Pulling in more context

By default, the retrieval step fetches the 5 most relevant chunks. For broad
synthesis questions, you can fetch more:

```bash
tbuk ask --top 10 "What do I know about machine learning?"
```

More chunks = more context = better synthesis, but also slower and uses more of
the model's capacity. Start with the default and increase only if answers feel
incomplete.

### Saving output to a file

Turn off streaming and redirect to a file:

```bash
tbuk ask --no-stream "Summarise my notes on budgeting" > summary.txt
```

---

## 10. Searching Without the LLM

Sometimes you want to find passages rather than generate an answer. `tbuk
search` skips the language model entirely and returns raw matching chunks.

### Hybrid search (recommended)

```bash
tbuk search "machine learning fundamentals"
```

Hybrid mode combines meaning-based search with exact-word search and generally
gives the best results. It is the default.

### Keyword-only search

```bash
tbuk search --mode keyword "API rate limit"
```

Best when you remember the exact phrase. Faster because no embedding is
computed.

### Semantic (meaning) search

```bash
tbuk search --mode vector "concepts related to cost reduction"
```

Best for conceptual queries where you do not know the exact words used in the
document.

### Controlling result count and minimum score

Use `--top` to cap how many results come back:

```bash
tbuk search --top 10 "project deadlines"
```

`--min-score` filters out low-scoring results, but the score scale depends on
the search mode — **only `--mode vector` uses a 0–1 scale**:

```bash
tbuk search --mode vector --min-score 0.7 "project deadlines"
```

In vector mode, scores range from 0 (unrelated) to 1 (identical), and a
threshold of 0.6–0.7 is a reasonable starting point; lower it if you get too
few results.

> **Do not use a 0–1 threshold in hybrid or keyword mode.** Hybrid (the
> default) fuses results with Reciprocal Rank Fusion, whose scores are tiny
> sums (roughly 0.03 at most), and keyword mode uses full-text ranks on their
> own scale. A value like `--min-score 0.7` in these modes filters out
> *every* result and prints "No results found." The command warns you if you
> try. To threshold on a 0–1 scale, switch to `--mode vector`.

### JSON output for scripting

```bash
tbuk search --format json "budget 2025" | jq '.[].text'
```

---

## 11. Prompt Templates

Templates control how the AI formats its answers. The default template (`qa`)
works well for general questions. You can create custom templates for specific
tasks.

### Listing and inspecting templates

```bash
tbuk template list
tbuk template show qa
```

Templates live in `~/.tbuk/prompts/`. Each template is a folder containing:
- `manifest.yaml` — settings (temperature, how many chunks to retrieve, etc.)
- `system.tmpl` — instructions to the AI model
- `user.tmpl` — the question format sent to the model

### Creating a custom template

To create a template that always formats answers as bullet action items:

```bash
mkdir -p ~/.tbuk/prompts/actions
```

Create `~/.tbuk/prompts/actions/manifest.yaml`:

```yaml
name: actions
description: Extract action items from retrieved notes
retrieval:
  top_k: 8
temperature: 0.3
```

Create `~/.tbuk/prompts/actions/system.tmpl`:

```
You are an assistant that extracts action items from notes.
Given the following excerpts, list only concrete action items as bullet points.
Each bullet should name who is responsible and what they need to do.
If no action items are present, say so.
```

Create `~/.tbuk/prompts/actions/user.tmpl`:

```
Notes:
{{range .Chunks}}
---
{{.Text}}
{{end}}

Question: {{.Question}}

Action items:
```

Now use it:

```bash
tbuk ask --template actions "What are all the things I need to do this week?"
```

### Passing variables to templates

```bash
tbuk ask --var style=concise "Summarise my notes on budgeting"
```

Variables are available in templates as `{{.Variables.style}}`. This lets you
adjust behaviour without creating separate template files.

---

## 12. Keeping Your Knowledge Base Up to Date

### When a document changes

```bash
tbuk update ~/notes/first-note.md
```

This checks whether the file has changed since it was last indexed. If so, it
re-indexes it. If not, it does nothing.

```
Updated ~/notes/first-note.md (4 chunks → 6 chunks)
```

or:

```
Skipped ~/notes/first-note.md (unchanged)
```

Use `--force` to re-index even if the file has not changed:

```bash
tbuk update --force ~/notes/first-note.md
```

### When you want to remove a document

```bash
tbuk delete ~/notes/old-note.md
```

This removes the document and all its chunks from the database. It does
**not** delete the file from your disk. You will be asked to confirm:

```
Delete ~/notes/old-note.md and 3 chunks? [y/N]
```

Use `--yes` to skip the prompt in scripts:

```bash
tbuk delete --yes ~/notes/old-note.md
```

### Keeping a whole folder current

Running `tbuk ingest` on a folder again picks up new and changed files, and
skips everything else:

```bash
tbuk ingest ~/notes/
```

You can run this any time after editing your documents. A reasonable habit is
to run it once a day, or after any batch of edits.

---

## 13. More Complex Use Cases

### Research archive

You have saved 50 PDF papers on a topic and want to query across all of them.

```bash
mkdir ~/papers
# copy your PDFs there
tbuk ingest ~/papers/
```

Now query:

```bash
tbuk ask --top 15 "What approaches have researchers used to solve X?"
tbuk ask "Which papers discuss the limitations of approach Y?"
tbuk search --mode keyword "Smith et al 2022"   # find a specific citation
```

Tips for research archives:
- Use `--top 15` or higher for synthesis questions that should draw on many
  papers
- Use keyword search when looking for a specific author name or technical term
- Re-run `tbuk ingest ~/papers/` whenever you add new papers

### Personal journal or diary

You keep daily notes in a folder, one file per day.

```bash
tbuk ingest ~/journal/
```

Useful queries:

```bash
tbuk ask "What was I thinking about in March?"
tbuk ask "What recurring themes appear in my notes from last year?"
tbuk ask "When did I last mention feeling overwhelmed?"
```

Tips for journals:
- Date-based filenames (e.g. `2024-03-15.md`) give the AI useful context
- Chunking works well on conversational prose — default settings are fine
- For emotional or reflective questions, use `--top 10` to draw on more entries

### Work documentation

You have a mix of meeting notes (Markdown), exported wiki pages (HTML), and
policy documents (PDF) saved locally.

```bash
tbuk ingest ~/work-docs/
```

Useful queries:

```bash
tbuk ask "What did we decide about the authentication system?"
tbuk ask "What tasks are assigned to me in meeting notes?"
tbuk ask --template actions "What are my open action items?"
tbuk search --mode keyword "GDPR"   # find exact policy references
```

For structured retrieval of decisions:

```bash
tbuk ask "List every architectural decision recorded in my notes, with rationale"
```

### Hobby knowledge base — cooking

You have saved recipes and technique notes.

```bash
tbuk ingest ~/recipes/
```

Queries:

```bash
tbuk ask "What can I make with chicken and lemon?"
tbuk ask "How do I make a roux?"
tbuk ask "Which of my recipes are suitable for vegetarians?"
tbuk ask "What did I write about knife skills?"
```

This illustrates that timbuktu is general-purpose — it works equally well for
technical notes, creative writing, recipes, or anything else you have written
down.

---

## 14. Tips and Limitations

### What works well

- Finding relevant passages you know you wrote, even when you cannot remember
  the exact words
- Synthesising across many documents to get a summary or extract patterns
- Natural-language queries that keyword search would miss entirely
- Asking for structured output (action items, summaries, lists) via custom
  templates

### What does not work well

- **Questions about things not in your knowledge base.** The model has no
  information beyond what you have indexed. If retrieval fails to find relevant
  chunks, the answer quality degrades; the model may fall back on its general
  knowledge or say it does not know.
- **Very long documents with dense information.** Chunking may split a key
  piece of context across two chunks. If answers about a specific document feel
  incomplete, try re-ingesting with a larger `chunking.size` (e.g. 1200).
- **Real-time or recent information.** The knowledge base knows only what you
  have ingested. Run `tbuk ingest` after updating your documents.

### Tuning chunk size

Edit `~/.tbuk/config.yaml`:

```yaml
chunking:
  size: 800    # default
  overlap: 100
```

| Chunk size | Effect |
|------------|--------|
| Smaller (400–600) | More precise retrieval; less context per chunk |
| Larger (800–1200) | More context per chunk; retrieval slightly less precise |

Start with the defaults. If answers feel too narrow (missing context), increase
`size`. If answers feel unfocused (too much irrelevant material), decrease it.
After changing chunk size, re-ingest your documents with `--force`:

```bash
tbuk ingest --force ~/notes/
```

### When answers are wrong or unhelpful

1. Run `tbuk search` on the same query to see what was actually retrieved:

   ```bash
   tbuk search "your query here"
   ```

   If the retrieved chunks are irrelevant, the problem is retrieval, not
   generation. Try a different search mode (`--mode vector` or `--mode
   keyword`) or rephrase your question.

2. Try `--top 10` to retrieve more chunks:

   ```bash
   tbuk ask --top 10 "your question"
   ```

3. Check that the relevant document is indexed:

   ```bash
   tbuk stats
   ```

4. If you recently edited the document, update it:

   ```bash
   tbuk update ~/notes/the-file.md
   ```

5. For persistent problems, re-index with force:

   ```bash
   tbuk ingest --force ~/notes/
   ```

### Common mistakes

- Forgetting to run `tbuk ingest` after editing a file — the knowledge base
  reflects the state of documents at ingest time, not their current state
- Using `tbuk ask` for questions that require information outside your
  documents — for general knowledge questions, use the AI model directly
- Setting `--top` very high (e.g. 50) — this slows the model down and can
  degrade answer quality by flooding the context with loosely related material
