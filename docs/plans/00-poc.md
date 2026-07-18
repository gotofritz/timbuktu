# Timbuktu: CLI RAG Knowledge Base

## Problem Statement

Build a lightweight, local-first knowledge base system in Go for indexing, searching, and querying personal documents using Retrieval-Augmented Generation (RAG). The application should be CLI-only, use SQLite as its primary datastore (including vector search), and support multiple LLM providers (local and hosted). The architecture should be modular so that storage, retrieval, prompting, and model providers are independent components.

---

# Goals

* CLI-first (no web UI)
* Local-first
* Single SQLite database
* Modular architecture
* Provider-agnostic LLM interface
* Provider-agnostic embedding interface
* Extensible prompt template system
* Reproducible document ingestion with lineage
* Easy to extend without introducing frameworks

---

# Core Features

## Document preprocessing

Supported input formats:

* Markdown
* Plain text
* PDF
* HTML
* (Extensible)

Extract plain text before ingestion.

Optionally cache extracted text for debugging.

Example:

```text
tbuk preprocess docs/
```

---

## Document ingestion

For each document:

1. Calculate SHA256
2. Detect duplicates
3. Extract text
4. Chunk text
5. Generate embeddings
6. Store metadata
7. Store chunks
8. Store embeddings

Maintain lineage:

```text
Original document
    ↓
Extracted text
    ↓
Chunks
    ↓
Embeddings
```

---

## Storage

Use SQLite.

Suggested tables:

```text
documents
---------
id
path
sha256
title
mime_type
created_at
updated_at

chunks
------
id
document_id
chunk_index
text
token_count
embedding

metadata
--------
document_id
key
value
```

Use SQLite foreign keys with cascade delete.

Use SQLite FTS5 for keyword search.

Use sqlite-vec (preferred) or equivalent for vector search.

---

## Chunking

Simple initial implementation:

* ~800 tokens
* ~100 token overlap

No semantic chunking initially.

---

## Search

Support:

### Vector search

```text
tbuk search authentication
```

### Metadata search

```text
tbuk find tag=design
tbuk find filename=README.md
```

### Hybrid search

Combine:

* vector similarity
* FTS5
* metadata filters

---

## Retrieval

Pipeline:

```text
Question
    ↓
Embed query
    ↓
Retrieve top K chunks
    ↓
(Optional rerank)
    ↓
Construct prompt
    ↓
Send to LLM
```

All returned chunks should include citations:

* document
* section
* chunk id

---

## Chat

Example:

```text
tbuk ask "Explain Raft."
```

Flow:

1. Retrieve context
2. Render prompt template
3. Call LLM
4. Stream response

---

## Delete

```text
tbuk delete README.md
```

Delete:

* document
* chunks
* embeddings
* metadata

Use ON DELETE CASCADE.

---

## Update

```text
tbuk update README.md
```

Algorithm:

* Compute SHA256
* If unchanged → skip
* If changed:

  * delete previous index
  * re-index document

---

# Prompt Templates

Prompt templates are first-class resources.

Store on disk.

Example:

```text
~/.tbuk/prompts/

    summary/
        manifest.yaml
        system.tmpl
        user.tmpl

    explain/
        ...

    anki/
        ...
```

Use Go's standard `text/template`.

Do not use Jinja.

---

## Template Manifest

Example:

```yaml
name: anki

description: Generate Anki cards.

model: claude

temperature: 0.2

retrieval:
  top_k: 20
  max_tokens: 12000

variables:
  difficulty:
    default: intermediate

output: markdown-table
```

---

## System Prompt

Example:

```text
You are an expert educator.

Produce concise, accurate flashcards.
```

---

## User Prompt

Example:

```gotemplate
Question:

{{ .Question }}

Relevant context:

{{ range .Chunks }}
Source: {{ .Source }}

{{ .Text }}

{{ end }}
```

---

## Template Variables

Support CLI overrides.

Example:

```text
tbuk ask \
    --template explain \
    --var difficulty=beginner \
    "Explain TCP."
```

---

## Template Capabilities

Templates may configure:

* retrieval parameters
* preferred model
* temperature
* output format
* custom variables

Templates should not contain business logic.

---

# LLM Providers

Define a common interface.

```go
type LLM interface {
    Chat(ctx context.Context, messages []Message) (<-chan Token, error)
}
```

Implement providers for:

* llama.cpp
* Claude
* OpenAI / Codex

Switch providers through configuration.

---

# Embedding Providers

Define a separate interface.

```go
type Embedder interface {
    Embed(ctx context.Context, text []string) ([][]float32, error)
}
```

Possible implementations:

* llama.cpp
* Ollama
* OpenAI
* Voyage
* BGE
* Nomic

---

# Configuration

Example:

```yaml
database:
  path: ~/.tbuk/tbuk.sqlite

llm:
  provider: llama

embedding:
  provider: llama
```

Templates may override model selection.

---

# CLI

Example commands:

```text
tbuk init

tbuk preprocess docs/

tbuk ingest docs/

tbuk update README.md

tbuk delete README.md

tbuk search authentication

tbuk find tag=design

tbuk ask "Explain OAuth."

tbuk ask \
    --template anki \
    "Teach me Raft."

tbuk template list

tbuk template edit anki

tbuk stats
```

---

# Architecture

```text
cmd/
    tbuk/

internal/

    cli/

    config/

    storage/

    ingest/

    preprocess/

    chunking/

    embeddings/

    retrieval/

    prompts/

    llm/

    providers/

        llama/

        claude/

        openai/

    models/

    search/

    metadata/
```

Dependencies should point inward; providers should depend only on shared interfaces.

---

# Future Enhancements

Not required for MVP:

* reranking
* git repository indexing
* filesystem watch mode
* multiple knowledge bases
* tags/collections
* import/export
* context compression
* MCP server
* plugin system

---

# Non-Goals

Do not implement:

* web UI
* agents
* autonomous workflows
* LangChain-style abstractions
* distributed databases
* background job system
* knowledge graphs
* long-term conversational memory

Keep the implementation small, modular, idiomatic Go, and focused on a reliable local RAG workflow.
