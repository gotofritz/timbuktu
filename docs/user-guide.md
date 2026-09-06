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
│                  │  (embedding) using a local AI model (MLX, llama.cpp).
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
│                  │  chunks, hand them to a language model (MLX,
│                  │  llama.cpp, or Claude), and get a written answer.
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
- Works with MLX (default; Apple silicon), llama.cpp, Ollama, OpenAI, or
  Claude as the AI backend

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
   and move it onto your `PATH` (e.g. `/usr/local/bin` on macOS/Linux; on
   Windows unzip `tbuk.exe` into a folder on your `PATH`). No Go
   toolchain is needed — the binary is self-contained. Linux, macOS and Windows
   are all built *and* tested on every change. If you prefer to build
   from source, see the [README](../README.md#install). Then run `tbuk version`
   — it should print a version number. If you get "command not found", the
   binary is not on your `PATH`.

2. **An AI backend is available.** Timbuktu needs two things: an *embedding*
   model (to fingerprint your text) and a *chat* model (to write answers). The
   next section shows three ways to provide them — a fully local setup with
   MLX (the default, for Apple silicon Macs), llama.cpp as the alternative
   fully-local setup (any OS), or a hybrid setup that uses Claude for the
   answers.

3. **You have some documents** to index — notes, PDFs, saved articles, anything
   in the supported formats.

### Choosing an AI backend

Timbuktu always needs a **local embedding model** (embeddings are what make
meaning-based search work, and there is no hosted embedding provider for
Claude). For *generating answers* you can keep everything local — MLX is the
default on an Apple silicon Mac, with llama.cpp as the alternative on any
OS (or if you'd rather not run MLX) — or hand the writing off to Claude.
Pick one of the three paths below.

| | Fully local (MLX, Apple silicon) | Fully local (llama.cpp, any OS) | Hybrid (local embeddings + Claude) |
|---|---|---|---|
| Answer quality | Depends on local model size | Depends on local model size | Strongest |
| Privacy | Nothing leaves your machine | Nothing leaves your machine | Retrieved chunks are sent to Anthropic |
| Cost | Free (uses your hardware) | Free (uses your hardware) | Pay-per-use Anthropic API |
| Needs internet | No (after models downloaded) | No (after models downloaded) | Yes, for `tbuk ask` |
| Hardware | Apple silicon Mac | Any (GPU recommended) | Only the embedding model runs locally |

Embeddings run locally in **every** path, so set up a local embedding server
first (MLX or llama.cpp), then choose your generation backend.

#### Path A — Fully local with MLX (Apple silicon, the default)

[MLX](https://github.com/ml-explore/mlx) is Apple's machine-learning framework
for Apple silicon; models quantised for it live on Hugging Face under
[`mlx-community`](https://huggingface.co/mlx-community). Timbuktu's `mlx`
provider talks to any **OpenAI-compatible server fronting MLX models** —
`mlx_lm.server` (the official one, used below),
[mlx-openai-server](https://github.com/cubist38/mlx-openai-server),
LM Studio, or [nativ](https://github.com/Blaizzy/nativ). It is the default
provider: a fresh `tbuk init` config assumes an MLX server on
`http://localhost:8080` with no edits.

Both servers below are installed with [uv](https://docs.astral.sh/uv/)'s
`uv tool install` rather than `pip install` — `pip install` needs an active
virtualenv (or fights your system Python's PEP 668 "externally managed
environment" guard); `uv tool install` builds an isolated environment for the
tool automatically and puts its commands straight on your `PATH`, the same
way `pipx` does. Install uv itself with `brew install uv` if you don't have
it, or see the [uv install docs](https://docs.astral.sh/uv/getting-started/installation/).

Chat server (port 8080 — the default). Install the official
[mlx-lm](https://github.com/ml-explore/mlx-lm) package and start its server;
the `--model` flag pulls the model from Hugging Face and caches it on first
run:

```bash
uv tool install mlx-lm
mlx_lm.server --model mlx-community/Qwen3-8B-4bit --port 8080
```

Embedding server (port 8000). `mlx_lm.server` serves chat only, so run an
embedding-capable MLX server beside it —
[mlx-openai-server](https://github.com/cubist38/mlx-openai-server), which
uses [mlx-embeddings](https://github.com/Blaizzy/mlx-embeddings) as its
backend. Set `embedding.dimension` in your config to whatever the model you
pick actually outputs — it is not auto-detected. A verified example,
[`mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ`](https://huggingface.co/mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ)
(small, fast, official `mlx-community` conversion), natively outputs
**1024**-dimensional vectors:

```bash
uv tool install mlx-openai-server
mlx-openai-server launch --model-type embeddings \
  --model-path mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ --port 8000
```

The underlying Qwen3-Embedding model supports Matryoshka truncation down to
32 dimensions, but whether `mlx-openai-server`'s API exposes that is
unverified — stick with the native 1024 unless you've confirmed truncation
works for your setup. (There is no confirmed `mlx-community` conversion of a
classic 768-dimensional BERT-style embedder like `nomic-embed-text` or `bge`
at the time of writing — if you want to match `embedding.dimension: 768`
exactly, use llama.cpp for embeddings instead, per Path B below.)

> **If `uv tool install mlx-openai-server` fails building `outlines-core`
> with `error: can't find Rust compiler`:** that package has no prebuilt
> wheel for every Python/macOS combination, so the install falls back to
> compiling it, which needs Rust. Two fixes:
> - Point the tool install at an older Python that does have a wheel:
>   `uv tool install mlx-openai-server --python 3.12`.
> - Or install Rust and let it compile: `brew install rust`, then retry the
>   original command.
>
> `mlx-openai-server` pulls in a large dependency stack (torch, opencv,
> outlines) even when you only want its embeddings endpoint, since one
> package serves chat, vision, and embeddings alike. If neither fix above
> appeals, use LM Studio instead (below) or keep embeddings on llama.cpp
> from Path B (`embedding.provider: llama`).

(LM Studio — download from [lmstudio.ai](https://lmstudio.ai), no Python
install at all — serves both chat and embeddings from one process and is a
reasonable alternative to running two separate servers above.)

Then point the config at both servers (see section 5 for the full file):

```yaml
llm:
  provider: mlx                          # the default — line optional
  model: mlx-community/Qwen3-8B-4bit     # HF repo id, as passed to the server
  base_url: http://localhost:8080

embedding:
  provider: mlx
  model: mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ
  base_url: http://localhost:8000
  dimension: 1024                        # matches this model's native output
```

Set `llm.model` / `embedding.model` to the same Hugging Face repo id you
started each server with — MLX servers identify models by repo id, and some
reject requests that omit or misname it. If a server enforces an API key,
export it as `MLX_API_KEY` and Timbuktu sends it as a Bearer token (loopback
or HTTPS URLs only).

#### Path B — Alternative: fully local with llama.cpp (any OS)

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

#### Path C — Hybrid: local embeddings + Claude for answers

Keep a local **embedding** server from Path A or B (Claude has no embedding
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
     provider: mlx              # still local (or llama, per your Path A/B choice)
     model: mlx-community/Qwen3-Embedding-0.6B-4bit-DWQ
     base_url: http://localhost:8000
     dimension: 1024
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
Platform
  os:          darwin/arm64
  home:        ✓ /Users/you

Config
  path:        ~/.tbuk/config.yaml
  status:      ✓ valid

Database
  path:        ~/.tbuk/tbuk.sqlite
  status:      ✓ open
  documents:   0
  chunks:      0

LLM (mlx)
  url:         http://localhost:8080
  status:      ✓ healthy
  model:       mlx-community/Qwen3-8B-4bit

Embedding (mlx)
  url:         http://localhost:8000
  status:      ✓ healthy
  dimension:   1024

Preprocessing
  extractors:  markdown, text, html, pdf

Chunking
  size:        400 tokens (keep ≤ the embedding server's batch)
  overlap:     50 tokens
  estimator:   script-aware (~4 ASCII chars, ~1 CJK rune, ~2 Latin accents per token)
  stored:      no chunks stored yet

Search
  fts5:        ✓ available
  indexed:     ✓ search_text (reduced encoding)
  tokenizer:   ✓ unicode61 tokenchars '_' (exact terms, phrases, exclusions)
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

`~` is your home directory: `/Users/you` on macOS, `/home/you` on Linux, and
`C:\Users\you` on Windows — so every `~/.tbuk` in this guide means
`C:\Users\you\.tbuk` there. The **Platform** section of `tbuk doctor` prints
the directory it actually resolved, which is the quickest way to see where your
knowledge base went.

It is safe to run more than once. It never overwrites your own settings: on a
folder that already has a `config.yaml`, it only **adds** default keys the file
is missing (leaving your values and comments in place), and it leaves a config
that already has every key untouched.

### Using a different data directory

By default everything lives under `~/.tbuk/`. To keep a knowledge base
somewhere else — a project folder, an external drive, or a second collection
kept separate from your main notes — pass `--root` to any command:

```bash
tbuk init  --root /data/work-kb      # scaffold config + templates under /data/work-kb
tbuk ingest --root /data/work-kb ./docs
tbuk ask   --root /data/work-kb "what did we decide about X?"
```

`--root DIR` relocates the database, extracted-text cache, raw archive, prompt
templates, and the config file — all of them move together to `DIR`. The
`config.yaml` written by `init --root DIR` uses paths **relative** to `DIR`, so
the whole folder is portable: move it elsewhere and point `--root` at the new
location, and every component follows. Without the flag, the default `~/.tbuk` is
used, so existing setups are unaffected. Point `--root` at different directories
to keep several independent knowledge bases side by side.

The config file always lives directly under its root, so
`--config /some/dir/config.yaml` (with no `--root`) is just another way of saying
`--root /some/dir` — the config's own folder becomes the data root. Pass both
when you want `--root` to set the root and `--config` to name a specific file
inside it.

### Putting components in different places

Each data path in `config.yaml` can be **relative to the root** (the portable
default) or an **absolute path** that pins one component somewhere specific — for
example, keeping the raw archive on a larger disk while everything else stays
under the root:

```yaml
database:
  path: ./tbuk.sqlite      # relative → beside the config, under the root
ingest:
  raw_dir: /mnt/big/raw    # absolute → pinned to a larger disk
```

Relative paths resolve against the root; absolute paths are used as-is. An empty
`ingest.raw_dir` still turns the archive off entirely.

### Understanding the configuration

Open `~/.tbuk/config.yaml` in any text editor. It looks like this:

```yaml
database:
  path: ./tbuk.sqlite    # relative to the data root (the config file's folder)

llm:
  provider: mlx      # mlx | llama | ollama | claude | openai
  model: ""          # mlx: the HF repo id served; llama.cpp: empty = loaded model
  base_url: http://localhost:8080
  max_tokens: 4096      # how long an answer may get
  context_tokens: 8192  # how much the model can hold at once, question and answer together

embedding:
  provider: mlx      # mlx | llama | ollama | openai
  model: ""          # as above
  base_url: http://localhost:8080
  dimension: 768

chunking:
  size: 400          # how large each chunk is, in estimated tokens
  overlap: 50        # how much consecutive chunks overlap, same units

ingest:
  embed_concurrency: 4   # embed batches sent to the server at once per file
  raw_dir: ./raw         # keep a copy of every ingested source here (empty to disable)

preprocess:
  output_dir: ./extracted  # where extracted plain text is cached

prompts:
  dir: ./prompts       # where prompt templates live

session:
  history_turns: 6     # earlier question/answer pairs replayed in a conversation thread
  max_turns: 0         # turns kept per thread; 0 = keep everything
```

The paths above are **relative to the data root** — the folder holding this
`config.yaml` — so the whole knowledge base is portable. Give any of them an
**absolute** path instead to pin that component to a fixed location (see
[Putting components in different places](#putting-components-in-different-places)).

**Settings most users never need to change:** `database.path`, `chunking.size`,
`chunking.overlap`, `ingest.embed_concurrency`, `ingest.raw_dir`.

`ingest.raw_dir` is where Timbuktu keeps a copy of every document you ingest
(see [Your original files are kept](#your-original-files-are-kept) below). Leave
it empty to turn archiving off.

`ingest.embed_concurrency` controls how many embedding requests `tbuk ingest`
keeps in flight at once for a single file. The default of `4` overlaps network
round-trips so large ingests finish faster. Lower it to `1` (fully serial) if
your embedding server is rate-limited or easily overloaded; raising it past a
handful rarely helps and can trip provider rate limits. Must be at least `1`.

The `session:` block bounds conversation threads (`tbuk ask --session` and
`tbuk chat`, see [Having a conversation](#having-a-conversation)). `history_turns` is how many
earlier question/answer pairs are replayed into the prompt — six is enough for
a follow-up to make sense without crowding out the passages retrieved for it;
`0` still records the thread but replays none of it. `max_turns` caps how many
turns a thread keeps, oldest dropped first; `0` keeps everything.

`llm.context_tokens` is the size of the model's memory for one exchange — the
question, the passages retrieved for it, and the answer, all together. Timbuktu
keeps the prompt inside it (see [Fitting the model's context
window](#fitting-the-models-context-window) in section 9). The default of
`8192` suits most local models; set it to the window your model actually has —
larger models are often 32768 or more — and you get more of your notes in every
answer. Set it to `0` to switch the check off entirely.

**Settings you set once, per backend** (see [section 4](#4-before-you-start)
for the three backend paths):

- `llm.base_url` / `embedding.base_url` — the address of each local server.
  If you run separate embedding and chat servers, give them different ports
  (e.g. MLX chat on `8080`, embeddings on `8000`; or llama.cpp embeddings on
  `8080`, chat on `8081`).
- `llm.provider` — `mlx` (the default) for the MLX path, `llama` for the
  llama.cpp path, or `claude` for the hybrid path. With `claude`, also set
  `llm.model` (Claude has no loaded-model default) and export
  `ANTHROPIC_API_KEY`. `embedding.provider` stays local (`mlx` or `llama`)
  either way.
- `llm.model` / `embedding.model` — for `mlx`, set each to the Hugging Face
  repo id the server was started with (some MLX servers reject requests that
  omit it). For llama.cpp, leave empty when only one model is loaded per
  server.
- `MLX_API_KEY` — only if your MLX server enforces an API key; sent as a
  Bearer token (the server URL must then be loopback or HTTPS).

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

This reads the extracted text, sends each chunk to your embedding server to
compute its embedding, and stores everything in the database. You will see a one-line
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

### Your original files are kept

Every time you ingest a document, Timbuktu also saves an untouched copy of the
original into `~/.tbuk/raw/`. So even if you later move, edit, or delete the
source file, the exact bytes you indexed are still on hand. Copies are named by
content fingerprint, so re-ingesting the same file never makes a duplicate.

This archive is what lets `tbuk reindex` rebuild your whole index after an
embedding-model change (see [section 12](#12-keeping-up-to-date)) without
needing a single original file.

To skip the copy for a single run, pass `--no-raw`:

```bash
tbuk ingest --no-raw ~/notes/first-note.md
```

To turn archiving off entirely, set `ingest.raw_dir` to an empty value in
`~/.tbuk/config.yaml`.

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

### Listing documents

```bash
tbuk list
```

Output:

```
PATH                 TITLE          CHUNKS  UPDATED
/home/me/notes.md    notes          12      2026-07-20T09:14:00Z
/home/me/report.pdf  Q3 Report      48      2026-07-19T18:02:11Z
```

`tbuk list` shows every indexed document — path, title, chunk count, and when
it was last ingested — so you can confirm an ingest worked, spot stale or
renamed files, and decide what to delete. Use `--limit N` to cap the number of
rows and `--format json` for scripting:

```bash
tbuk list --limit 20
tbuk list --format json | jq '.[].path'
```

### Tagging documents, and finding them again

Timbuktu keeps a set of **key=value labels** on every document. Some it writes
itself at ingest time — `filename`, `extension`, `mime`, `dir` — and you can
attach your own:

```bash
tbuk meta set ~/notes/soup.md tag=cooking
tbuk meta set ~/notes/soup.md tag=cooking author=Mum year=1998
tbuk meta list ~/notes/soup.md
```

Then find documents by any of them:

```bash
tbuk find tag=cooking
tbuk find extension=md
tbuk find tag=cooking author=Mum      # several filters: all must match
```

Matching is exact — `tag=cooking` does not find `tag=Cooking` or
`tag=cooking-fast`. There is no wildcard; `tbuk search` is the tool for
"documents that mention cooking".

Two things worth knowing before you lean on this:

- **One value per key, per document.** `tag=cooking` and `tag=quick` on the
  same document is not possible — the second replaces the first. Timbuktu
  refuses a command that sets the same key twice rather than silently keeping
  the last one. If you need several labels, use distinct keys
  (`tag=cooking cuisine=italian`) or a compound value you search consistently.
- **Your labels survive re-ingesting.** Editing a document and running
  `tbuk ingest` again refreshes the automatic keys and leaves yours alone. They
  travel with `tbuk export`/`tbuk import` too.

For a plain inventory of everything indexed, use `tbuk list`; for aggregate
counts, use `tbuk stats`.

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

If the model itself returns nothing — an answer that is blank above the
`Sources:` list — `tbuk ask` says so on stderr. The usual cause is a template
whose `max_tokens` is too small: it is the model's output budget, and a model
that reasons before it writes can spend all of it and emit no answer. Raise
`max_tokens` in `~/.tbuk/prompts/<template>/manifest.yaml`.

### Fitting the model's context window

A model can only hold so much at once. Everything Timbuktu sends — the
instructions, your question, and the passages it retrieved — has to fit
alongside the answer it still has to write. That size is `llm.context_tokens`
in `~/.tbuk/config.yaml`, and the answer's share of it is `max_tokens`.

Before calling the model, `tbuk ask` checks the prompt against that budget. If
it is too big, it says so on stderr and makes room a step at a time:

```
warning: the prompt exceeds the model's context budget — dropped the 1 oldest of 6 replayed turns …
warning: the prompt exceeds the model's context budget — compacted the retrieved text …
warning: still over the context budget after compacting — dropped 2 of 10 retrieved chunks …
```

**Dropping earlier turns** only happens inside a conversation thread (see
[Having a conversation](#having-a-conversation)); a plain `tbuk ask` has no
earlier turns to drop. It goes first, because evidence for the question you are
asking now is worth more than a transcript of the ones before it — and you can
always repeat what the thread forgot, while you cannot repeat a passage that was
never fetched. **Compacting** squeezes the retrieved passages — repeated spaces
and blank lines go, and so do words like "the", "a", "really", "basically" that
a model can read straight past. Code, file paths, URLs and anything in backticks
are left exactly as they were, and your question is never touched. **Dropping**
removes the weakest matches, lowest-scoring first; the `Sources:` list then
shows only the passages the model actually saw. The most recent question and
answer of a thread are kept to the very last, and if even they have to go,
`ask` says so — an answer that cannot see the turn before it will read
differently.

If even a question with no passages at all would not fit, `ask` stops before
calling the model:

```
prompt needs ~2100 tokens but only 1000 are available for it, even with no
retrieved context: shorten the question, or raise llm.context_tokens …
```

Seeing these warnings often means one of three things: `llm.context_tokens` is
lower than your model's real window (raise it), `--top` is asking for more
passages than fit (lower it), or the template's `max_tokens` is reserving too
much for the answer. `tbuk doctor` prints the window and how much of it is left
for the prompt. With `--require-context`, a budget too small for even one
passage aborts instead of answering from the model's general knowledge.

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
incomplete. Ask for more than the context window holds and the extra passages
are compacted, then dropped again, with a warning saying so — see [Fitting the
model's context window](#fitting-the-models-context-window).

### Having a conversation

By default every `tbuk ask` is a fresh start: it knows nothing about the
question you asked a minute ago. That is usually what you want for a one-off
lookup, and it is unchanged.

For a back-and-forth, name a **thread** with `--session`:

```bash
tbuk ask --session alpha "What are the action items for Project Alpha?"
tbuk ask --session alpha "Who is doing the second one?"
tbuk ask -c "and when is it due?"          # -c = the thread you used last
```

The thread does two things. The model is shown the earlier questions and
answers, so "the second one" and "it" mean something. And the *search* is
widened with the last couple of questions, so "and when is it due?" still finds
passages about Project Alpha — without that, a two-word follow-up matches almost
nothing, the model answers from what it happens to remember, and you would not
be able to tell.

You do not create a thread first; naming one that does not exist starts it.
Names are case-insensitive (`--session Alpha` and `--session alpha` are the same
thread) and threads belong to the knowledge base, so they follow `--root` and
survive closing the terminal, re-running `tbuk reindex`, and rebooting. `tbuk
doctor` reports how many you have, under **Database**.

`-c` / `--continue` picks up the thread you used most recently, which is what
you actually want to type for a second question. If there are no threads at all
it is an error rather than a silent one-off answer — the two look identical on
screen and behave very differently.

Two settings in `~/.tbuk/config.yaml` bound a thread:

```yaml
session:
  history_turns: 6   # how many earlier question/answer pairs are replayed
  max_turns: 0       # how many turns are kept at all; 0 = keep everything
```

A turn is recorded only when the answer finishes. Press `Ctrl-C` half way, or
have the model fail, and nothing is written — a half-finished answer replayed
later is worse than a missing one, because the model reads it as something it
meant to say.

### Chatting instead of typing `tbuk ask` each time

`tbuk chat` is the same thing without the retyping: one question per line, until
you leave.

```bash
tbuk chat                     # a conversation that is not saved
tbuk chat --session alpha     # the "alpha" thread, saved as you go
```

```
tbuk chat — thread "alpha".
/help for commands, /exit to leave.

> What are the action items for Project Alpha?
Three are listed in the January review: …

Sources:
  [1] /notes/alpha-review.md §2

> Who is doing the second one?
…
```

**Without `--session`, a chat is not saved.** The conversation still works
normally — follow-ups resolve exactly as they do above — it just leaves nothing
behind when you leave. That is deliberate: most conversations are not worth
keeping, and a tool that quietly filed every idle question would make your list
of threads useless within a week. If a chat turns out to be worth keeping,
`/new NAME` starts recording from that point.

Five commands, and no more:

| Type | To |
|---|---|
| `/sources` | see the citations behind the last answer again |
| `/new` | start over in a fresh, unsaved conversation |
| `/new NAME` | switch to the saved thread `NAME`, starting it if it is new |
| `/forget` | make it forget the conversation so far, without deleting anything |
| `/help` | list these |
| `/exit` | leave (`Ctrl-D` does the same) |

`/forget` is the one to reach for when you change subject: the earlier questions
stop being replayed and stop widening the search, so a new topic is not dragged
back towards the old one. Nothing already recorded is lost.

If a question fails — the model server is down, say — the error is printed and
the conversation carries on. One timeout should not cost you the thread.

### Looking after your threads

```bash
tbuk session list                    # what you have, most recently used first
tbuk session show alpha              # the whole conversation, with its sources
tbuk session show alpha --verbose    # …and what was actually searched for, per turn
tbuk session rename alpha q1-review  # keeps every turn
tbuk session delete alpha            # asks first; --yes skips the question
```

`tbuk session show --verbose` answers "why did it fetch *that*?". Alongside each
question it prints the search that was actually run — your follow-up with the
previous questions folded in — which is usually where a surprising answer is
explained.

Unlike `tbuk ask --session`, these four never invent a thread: a name you have
not used is an error, and it lists the threads you do have, since the usual
cause is a typo.

One thing worth knowing before you share a knowledge base: `tbuk export` copies
the database whole, so your threads travel inside the archive. (`tbuk import`
never reads them, so they do not land on anyone else's machine — but the text is
in the file.) `tbuk session delete` first if that matters.

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

A document does not have to contain every word you typed. Common words (`the`,
`about`, `what`, `how`…) are ignored, the rest are matched independently, and
results are ranked by BM25 — so a document using a rare word you asked for beats
one that only shares the ordinary ones. Ask for more words to get a better
ranking, not a narrower filter.

### Asking for exactly what you mean

`tbuk search` reads your query as an expression, so you can be more precise than
"any of these words". (`tbuk ask` does not — it takes a question, and a question
has no operators in it.)

**An exact term.** An underscore inside a word is part of the word, so
`main_consumption` is a single term:

```bash
tbuk search 'main_consumption'   # only notes with the underscore form
tbuk search 'main consumption'   # notes with either form
```

Typing the underscore is how you say you meant it. Type the words apart and you
get both.

A hyphen is not like that. `long-term` searches for `long term` next to each
other, and finds the note whichever way it is written — which is the point:
hyphenated English is ordinary writing, and `long-term` used to be a word of
its own that neither `long` nor `long term` could reach. A kebab-case name goes
the same way: `check-ci` and `check ci` are one query.

**A phrase.** Wrap it in double quotes to require the words next to each other,
in that order:

```bash
tbuk search '"main consumption"'       # not "consumption of the main supply"
tbuk search '"the main consumption"'   # inside quotes, even "the" counts
```

**An exclusion.** A `-` in front of a word leaves out anything containing it:

```bash
tbuk search 'main consumption -main_consumption'   # the words apart, not the identifier
tbuk search 'meter -deprecated'
```

The dash only means "exclude" at the start of a word, so `check-ci` still
searches for those words together. An exclusion needs something to exclude
*from*: a query of nothing but exclusions returns no results. And since a
hyphen inside a word is not part of the word, `-check-ci` leaves out notes that
say `check ci` either way round.

Two things worth knowing:

- In the default hybrid mode an exclusion is honoured throughout — a chunk you
  excluded will not come back through the meaning-based half. In `--mode vector`
  there is nothing to exclude against, so the word is simply dropped from the
  query.
- Quoting a phrase does not, on its own, rule out a note whose code mentions the
  identifier: the index stores `main_consumption` alongside the words it is made
  of, so both are there and adjacent. Add `-main_consumption` when you want only
  the prose.

### Finding notes that contain code

Notes about software are half prose and half code, and the code is mostly
punctuation. Timbuktu does not search code as code — it is not a code search
tool. What it answers is *which of my notes talks about this*, and in a code
block that is carried by the comments, the names, and the strings.

So a fenced block is indexed as those, and its syntax is thrown away. Each name
is indexed twice over: as you wrote it, and as the words it is made of. A block
containing

```go
// read the meter
func readMeter(id int) error {
    return fmt.Errorf("read holding: %w", err)
}
```

is found by `readMeter`, by `read meter`, by `read the meter` (the comment), and
by `read holding` (the error string) — but not by `func` or `return`. The same
goes for an identifier in an inline span in ordinary prose: a note mentioning
`` `main_consumption` `` answers both `main_consumption` and `main consumption`.

That second reach comes from the split words being stored, not from the search
taking your query apart — so it works for an identifier in a fenced block or in
`` `backticks` ``, and not for one typed bare in a sentence. A bare
`main_consumption` in prose is found by typing it exactly. Backticks are the
usual way to write it anyway, and they are what makes it searchable both ways.

What you get *back* is the chunk as you wrote it, code fences and all. Only the
index and the meaning-fingerprints see the reduced form; nothing is lost from
your notes, and nothing changes about what `tbuk ask` shows the model.

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

The `retrieval:` block takes two more keys, both of which only matter inside a
conversation thread: `rewrite` (`window`, the default, or `off`) chooses whether
the last few questions are folded into the search, and `window_turns` (default
`2`) says how many. Set `rewrite: off` for a template whose questions always
stand on their own.

A manifest may also set `max_tokens` (how long the answer may get) and
`context_tokens` (the window of the model this template runs on, overriding
`llm.context_tokens` in the config — useful when the template pins a `model:`
of its own with a bigger or smaller window). See [Fitting the model's context
window](#fitting-the-models-context-window).

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

### When you change your embedding model

Switching `embedding.provider` or `embedding.model` changes the *shape* of the
numbers Timbuktu stores — a 768-number vector where the new model produces
1024, or the other way round. Everything already indexed stays in the old
shape, and searches stop working:

```
query embedding has 1024 dimensions but stored vectors have 768
```

`tbuk doctor` flags the same thing. One command fixes it:

```bash
tbuk reindex
```

That re-embeds every document in your knowledge base with the new model. It
does not need your original files. The text comes from whichever of three
places still has it:

1. the **extracted text** Timbuktu kept when it first read the document (if a
   later version of Timbuktu reads that kind of file better, the old text is
   not reused — the document is read again);
2. the **untouched copy** in its raw archive (each document records where its
   own copy is, so it is found whatever it is called);
3. the original file, if it is still where you ingested it from.

So it works for documents whose originals now live on another machine, or
nowhere at all — and even for ones whose archived copy has gone too, as long as
the extracted text survived.

Check what it will do before spending the time:

```bash
tbuk reindex --dry-run
```

That lists every document and where its content would come from, without
embedding anything. Output looks like:

```
[1/3] /Users/you/notes/first-note.md → would re-embed from the archive (/Users/you/.tbuk/raw/9f2a….md)
[2/3] /Users/you/notes/old.md → would re-embed from /Users/you/notes/old.md (no archived copy)
[3/3] /Users/you/gone/deleted.md → skipped: no archived copy and stored path unreadable
Done: 2 would be re-embedded, 1 skipped, 0 errors
```

A document is only skipped when all three come up empty. The line names every
path it looked at, so you can see what is missing.

By default `tbuk reindex` prints only those problem documents and a closing
count — one unreadable document among hundreds should be easy to spot, not
buried. Add `-v` to see a line for every document. The rest of the run carries on regardless.

One case worth knowing: a `raw/` folder you assembled yourself, holding your
documents under *their own* names rather than the content-addressed ones
Timbuktu writes, is not an archive it can match up — nothing in the database
points at those files. The fix is to ingest that folder as the ordinary source
folder it is (`tbuk ingest ~/that-folder`); from then on each document records
where its copy went.

If your raw archive lives somewhere else — restored from a backup, say — point
at it for one run:

```bash
tbuk reindex --source-dir /mnt/backup/raw
```

Remember to set `embedding.dimension` in your config to the new model's value
first; reindex writes what the model gives it, and the rest of Timbuktu reads
that setting.

### When you upgrade Timbuktu

Sometimes a new version reads your files *better* than the one that indexed
them. Documents already in your knowledge base keep the text they were indexed
with, so the improvement does not reach them on its own:

```bash
tbuk reindex
```

That re-reads every document and rebuilds its chunks. As above, it works from
the extracted text, the raw archive, or your original files, so the originals
need not still be where you first ingested them.

The Markdown reader is one of these. It used to mangle punctuation inside
software terms: a note mentioning `main_consumption` and `total_output` on one
line came back as `mainconsumption` and `totaloutput`, so the term you were
searching for was not in the index under the name you knew it by. Text inside
backticks — a fenced code block or an inline span — is now kept exactly as
written, and identifiers such as `snake_case` or `__init__` survive in ordinary
prose too. If you indexed notes before that fix, `tbuk reindex` is what applies
it to them.

How code is indexed is another. Notes with code blocks used to be indexed and
fingerprinted as their raw syntax; they are now indexed as their comments,
names and strings, with each name also broken into words (see [Finding notes
that contain code](#finding-notes-that-contain-code)). This changes the
fingerprints as well as the index, so `tbuk reindex` is again what applies it.

If your knowledge base was built before that change, one step comes first.
`tbuk doctor` will say so under **Search**:

```
Search
  fts5:      ✓ available
  indexed:   ✗ chunks.search_text missing — run scripts/add-search-text/, then tbuk reindex
```

From a checkout of the source:

```bash
go run ./scripts/add-search-text ~/.tbuk/tbuk.sqlite   # your database path
tbuk reindex
```

The first command adds the new column, fills it in from what is already stored,
and rebuilds the search index, so search keeps working straight away. The second
re-reads and re-fingerprints your documents, which is what actually applies the
new encoding. Until the first has run, `tbuk ingest` will fail on a missing
column.

How the search index splits words is the third, and it has changed twice. It
used to break every word at `_`, which made `main_consumption` and `main
consumption` the same thing: an exact term, a phrase and an exclusion all
answered as if you had never typed the punctuation (see [Asking for exactly
what you mean](#asking-for-exactly-what-you-mean)). The fix for that briefly
kept `-` inside a word too, which had the opposite problem — `long-term` became
a word of its own, and asking about "long term costs" no longer found it.
Neither failed; the answers were just wrong. `tbuk doctor` names whichever one
your index has, under **Search**:

```
Search
  fts5:      ✓ available
  indexed:   ✓ search_text (reduced encoding)
  tokenizer: ✗ default unicode61 — '_' splits terms; run scripts/retokenize-fts/
```

or

```
  tokenizer: ✗ unicode61 tokenchars '_-' — '-' locks hyphenated words into one term; run scripts/retokenize-fts/
```

From a checkout of the source:

```bash
go run ./scripts/retokenize-fts ~/.tbuk/tbuk.sqlite   # your database path
```

That one takes seconds and needs no `tbuk reindex`: it rebuilds the index from
what is already stored and leaves your documents and fingerprints alone. It is
the same command for either tokenizer, and running it twice does no harm.

How chunk size is counted is the fourth, and it only ever mattered outside
English. `chunking.size` is a token budget, and Timbuktu used to guess it by
dividing the byte length by four — which is about right for English and wrong
by roughly a factor of three for Chinese, Japanese, Korean, and anything else
written in multi-byte characters. Chunks of those came out about three times
larger than the setting allowed, which is what made a local embedding server
answer `HTTP 500` on a corpus that was fine in English. Counting is now per
character and weighted by writing system, so a chunk holds the budget it says
it does whatever the language.

Only chunks already stored are affected, and `tbuk doctor` re-measures the
largest of them for you, under **Chunking**:

```
Chunking
  size:      400 tokens (keep ≤ the embedding server's batch)
  overlap:   50 tokens
  estimator: script-aware (~4 ASCII chars, ~1 CJK rune, ~2 Latin accents per token)
  stored:    ✗ 14 of 20 sampled chunks are over chunking.size 400 (largest 533) — indexed under an older estimator or a larger size; re-chunk with `tbuk reindex`
```

```bash
tbuk reindex
```

That re-chunks and re-embeds, so the new counting reaches documents you already
have. An all-English knowledge base will not see this line: its chunks were
counted the same way all along.

### Backing up or moving your knowledge base

`tbuk export` bundles everything — your config plus every data folder
(database, extracted-text cache, raw archive, and prompt templates) — into a
single `.tar` archive. Use it to back up a knowledge base or move it to another
machine.

```bash
tbuk export ~/backups            # writes ~/backups/tbuk-export-<timestamp>.tar
tbuk export ~/backups/kb.tar     # writes exactly that file
```

If you point it at an existing directory, Timbuktu creates a timestamped
filename inside it. If you give it a filename, it uses that name — and asks
before overwriting an existing archive unless you pass `--force`:

```bash
tbuk export --force ~/backups/kb.tar
```

The `--root` flag chooses *which* knowledge base to export, the same as for
every other command:

```bash
tbuk export --root /data/work-kb ~/backups/work.tar
```

Inside the archive, the config's data-folder paths are **commented out**. That
keeps the export portable, so nothing in it is tied to the absolute paths of
the machine that made it.

### Importing an archive on another machine

`tbuk import` reads a snapshot back into a knowledge base:

```bash
tbuk import ~/backups/kb.tar                        # into ~/.tbuk
tbuk import --root /data/work-kb ~/backups/kb.tar   # into a specific root
```

It takes out of the archive the things that are *yours*: your **documents**
(the untouched copies in `raw/`), the **index** describing them — each one's
path, title, and any labels you attached with `tbuk meta set` — and your
**prompt templates**. The documents are then embedded here, with this
machine's settings.

You can do one half at a time:

```bash
tbuk import data ~/backups/kb.tar        # documents only
tbuk import templates ~/backups/kb.tar   # templates only
```

All three take the same flags. Templates come across first, because they are
instant and need no model server — so if the embedding step is slow or fails,
you still have your templates. `tbuk import templates` needs no model server
running at all.

Everything else in the archive is ignored on purpose:

- **The settings file is never read.** It describes the machine it came from —
  where its folders live, which model server it talks to — and none of that is
  likely right here. Your own `config.yaml` is left exactly as it is.
- **The stored numbers are never reused.** Vectors made by one embedding model
  are meaningless to another, and nothing in an archive says which model made
  them. Rather than guess, import works them out again from your documents.

That second point is why importing takes a while: it is embedding every
document, exactly as if you had ingested them here. What you get for the wait
is a knowledge base that works on arrival.

Look before you leap:

```bash
tbuk import --dry-run ~/backups/kb.tar
```

```
[1/2] template mine → would install
[2/2] template qa → skipped: already installed
[1/3] /old-machine/notes/a.md → would import from 9f2a….md
[2/3] /old-machine/notes/b.md → would import from 4c81….md
[3/3] /old-machine/notes/c.md → would skip: the archive holds no copy of this file
Dry run: 2 document(s) would be imported, 1 skipped, 0 errors; 1 template(s) would be imported, 1 skipped, 0 errors
```

A dry run writes nothing at all. The skipped document in that example was
ingested with `--no-raw` on the other machine, so the archive has its index
entry but not its content — there is nothing to import it from.

#### Importing into a knowledge base you already have

A document already indexed **at the same path**, or a template already
installed under the **same name**, is left alone. That makes importing the same
archive twice a no-op, and lets you re-run an interrupted import safely. Two
flags change that:

```bash
tbuk import --on-conflict overwrite ~/backups/kb.tar   # archive's copy wins
tbuk import --on-conflict ask ~/backups/kb.tar         # decide one at a time
```

With `ask`, you are prompted per document and per template:

```
template mine is already installed. Replace it with the archive's copy? [y/N]
/notes/a.md is already indexed. Replace it with the archive's copy? [y/N]
```

Every machine has the built-in templates (`qa`, `brief`, `anki`), so the
archive's copies of those always collide — which means that by default only
templates you actually made yourself come across. If you had edited a built-in
on the other machine and want that version here, use `--on-conflict overwrite`.
A template is replaced as a whole, never merged file-by-file, so what you end
up with is always a template that loads.

If a document fails to embed — your model server is down, say — it is undone
rather than left half-imported, so running the import again picks it up where
it stopped.

#### Importing onto a brand-new machine

Point it at an empty directory and it sets one up for you first, exactly as
`tbuk init` would, then tells you what it is about to use and waits:

```
Created config: /Users/you/.tbuk/config.yaml
Documents will be embedded with mlx (the provider's default model), at 768 dimensions, per /Users/you/.tbuk/config.yaml.
Continue? [y/N]
```

Answer `n` if those are not your settings: the config stays where it is, so you
can edit it and run the import again. `--yes` skips the question for scripts.
`tbuk import templates` never asks — it embeds nothing, so there is nothing to
spend.

### Rewinding your own knowledge base

There is no "restore" command. Reusing an archive's stored numbers is only safe
when both machines run the same embedding model, and Timbuktu has no way to
check that — the mismatch would show up as quietly bad answers rather than an
error, which is the worst way for anything to fail.

Rolling *your own* machine back to a snapshot is a different matter: your
settings have not changed, so the numbers in the archive are still yours. That
needs no command, because `tbuk export` writes an ordinary uncompressed tar:

```bash
tar -xf ~/backups/kb.tar -C ~/.tbuk
```

That puts everything back, `config.yaml` included — worth a glance first if
you have changed settings since. If you have changed your embedding model since
that snapshot, run `tbuk reindex` afterwards.

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
  incomplete, try re-ingesting with a larger `chunking.size` (e.g. 500). With
  llama.cpp, keep `chunking.size` below the server's physical batch size
  (default 512 tokens); increase `-b` and `-ub` at server startup to go higher.
- **Real-time or recent information.** The knowledge base knows only what you
  have ingested. Run `tbuk ingest` after updating your documents.
- **A follow-up question without a thread.** `tbuk ask "and maps?"` on its own
  searches for the words "and maps" and nothing else — it has no idea what came
  before. Use `--session` / `-c`, or `tbuk chat`, for anything conversational
  (see [Having a conversation](#having-a-conversation)).
- **A `tbuk chat` you forgot to name.** Without `--session` a chat is not saved,
  by design; when you leave it, it is gone. `/new NAME` part-way through starts
  recording from there, but does not go back for what came before.

### Tuning chunk size

Edit `~/.tbuk/config.yaml`:

```yaml
chunking:
  size: 400    # default; keep ≤ llama.cpp ubatch size (default 512)
  overlap: 50
```

| Chunk size | Effect |
|------------|--------|
| Smaller (200–300) | More precise retrieval; less context per chunk |
| Larger (400–500) | More context per chunk; retrieval slightly less precise |

The unit is a **token** — roughly a word-piece, which is what the model actually
counts. Timbuktu estimates it per character, weighted by writing system: about
four characters of English to a token, two of accented Latin, Greek or Cyrillic,
and one of Chinese, Japanese or Korean. So `size: 400` is roughly 1600 English
characters but only about 400 Chinese ones — the same budget for the model,
different amounts of text. It is an estimate, not the model's own tokenizer, so
leave the margin the defaults already have rather than tuning to the exact
limit.

> **llama.cpp note:** the server's `-ub` (ubatch) flag sets the maximum input
> size per embedding request (default 512 tokens). Keep `chunking.size` below
> that value or raise it at server startup — `-b` and `-ub` must match:
> `llama-server -b 1024 -ub 1024 …`.

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
   keyword`) or rephrase your question. Note that `tbuk search` reads
   operators and `tbuk ask` does not: if a `-word` or a `"phrase"` in your
   search changed what came back, that difference is yours, not the retrieval
   step's.

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
