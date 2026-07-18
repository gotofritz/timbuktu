# Timbuktu — Initial Context

Local-first CLI RAG knowledge base. Go 1.25+, SQLite, no web UI, no frameworks.

Module: `github.com/gotofritz/timbuktu`

---

## Subplan Status

| # | Area | State |
|---|------|-------|
| 01 | Foundation — CLI skeleton, config, `tbuk init` | ✅ done |
| 02 | Storage — SQLite schema, migrations, typed repos, FTS5 | ✅ done |
| 03 | Preprocessing — text extraction, chunking, SHA256 | ✅ done |
| 04 | Embeddings — `Embedder` interface, llama/ollama/openai adapters | ✅ done |
| 05 | LLM Providers — `LLM` interface, provider adapters | stub |
| 06 | Ingestion — SHA256 dedup, chunk + embed pipeline | stub |
| 07 | Search — vector, FTS5 keyword, hybrid | stub |
| 08 | RAG — retrieval pipeline, prompt templates, streaming | stub |
| 09 | Management — `tbuk stats`, delete, update | stub |

---

## Architecture

```
cmd/tbuk/           cobra entry point

internal/
  config/           Config struct, Load(), Defaults(), DefaultYAML()
  cli/              cobra root + subcommands (init, version, doctor, preprocess)
  storage/          DB wrapper, RunMigrations, DocumentRepo, ChunkRepo, MetadataRepo
  preprocess/       Extractor interface + backends; DetectMIME; SHA256 helpers
  chunking/         Chunker.Split — greedy sentence boundary, Size/Overlap in tokens
  embeddings/       Embedder interface + factory; llama, ollama, openai adapters
  llm/              STUB — LLM interface (not yet implemented)
  ingest/           STUB
  prompts/          STUB
  retrieval/        STUB
  search/           STUB
  metadata/         STUB
```

Dependencies point inward. Providers depend only on shared interfaces.

---

## Dependencies

```
github.com/spf13/cobra        CLI framework
gopkg.in/yaml.v3              config parsing
modernc.org/sqlite            pure-Go SQLite (no CGO)
github.com/ledongthuc/pdf     PDF text extraction
golang.org/x/net              HTML parsing (html.Parse)
```

No test-only external deps — `net/http/httptest` from stdlib.

---

## Config

File: `~/.tbuk/config.yaml` (created by `tbuk init`)

```go
type Config struct {
    Database  DatabaseConfig   // path
    LLM       LLMConfig        // provider, model, max_tokens, base_url
    Embedding EmbeddingConfig  // provider, model, dimension, base_url
    Chunking  ChunkingConfig   // size (tokens), overlap (tokens)
}
```

Defaults: llm.provider=`llama`, embedding.provider=`llama`, dimension=768, chunk size=800, overlap=100.

---

## Storage

SQLite, WAL mode, foreign keys ON.

```sql
documents   — id, path (UNIQUE), sha256, title, mime_type, created_at, updated_at
chunks      — id, document_id (FK→documents CASCADE), chunk_index, text, token_count, embedding BLOB
metadata    — document_id (FK→documents CASCADE), key, value  (PK: document_id+key)
chunks_fts  — FTS5 virtual table over chunks.text, auto-synced via INSERT/DELETE triggers
```

Migrations versioned in `schema_migrations` table. Add new migrations by appending to the `migrations` slice in `storage/migrate.go`.

Embeddings: `storage.Float32SliceToBlob` / `BlobToFloat32Slice` — little-endian `[]float32`.

Repos: `DocumentRepo`, `ChunkRepo`, `MetadataRepo` — all take `*sql.DB`, return typed errors wrapping `fmt.Errorf("Repo.Method: %w", err)`.

---

## Preprocessing

```go
type Extractor interface {
    Extract(ctx context.Context, r io.Reader) (string, error)
}
```

`preprocess.NewExtractor(mime)` returns the right backend. `DetectMIME(path)` maps extension → MIME.

Backends: markdownExtractor (strips fences/headings), htmlExtractor (golang.org/x/net), plainTextExtractor, pdfExtractor (ledongthuc/pdf).

SHA256: `preprocess.SHA256File(path)` and `SHA256Reader(r)`.

---

## Chunking

```go
type Chunker struct { Size, Overlap int }  // tokens (approx chars/4)
type Chunk    struct { Index, TokenCount, StartByte, EndByte int; Text string }
func (c *Chunker) Split(text string) []Chunk
```

Token approximation: `CountTokens(s) = len(s) / 4`.
Boundary search: walks backwards from target end looking for `. `, `\n\n`, `! `, `? `.

---

## Embeddings

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimension() int
}

type EmbedError struct { Provider string; StatusCode int; Message string }
func AsEmbedError(err error, target **EmbedError) bool  // errors.As wrapper

func NewEmbedder(cfg config.EmbeddingConfig) (Embedder, error)
```

| Provider | Key | Endpoint | Notes |
|----------|-----|----------|-------|
| `llama`  | default | `POST {base_url}/embedding` | `{"content":"..."}` → `{"embedding":[...]}`, one request per text |
| `ollama` | — | `POST {base_url}/api/embed` | `{"model":"...","input":[...]}`, batch size 8 |
| `openai` | `OPENAI_API_KEY` env | `POST {base_url}/v1/embeddings` | Bearer auth, OpenAI response format |

`base_url` defaults to `http://localhost:8080` (llama/ollama) or `https://api.openai.com` (openai).
`dimension` is read from config — no auto-detection round-trip.

---

## CLI Commands (implemented)

```
tbuk init         create ~/.tbuk/, write default config.yaml and prompts/
tbuk version      print version string
tbuk doctor       probe config, DB, LLM/embedding connectivity
tbuk preprocess   extract + chunk text from file/dir (--format text|json)
```

---

## Patterns

- Table-driven tests with `testing` stdlib only
- HTTP providers mocked with `net/http/httptest`
- In-memory SQLite (`:memory:`) for storage tests
- `fmt.Errorf("context: %w", err)` for error wrapping
- No `init()`, no global mutable state, no `interface{}`
- `defer func() { _ = resp.Body.Close() }()` for HTTP responses
- Imports grouped: stdlib / external / internal
