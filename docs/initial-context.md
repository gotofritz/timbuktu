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
| 05 | LLM Providers — `LLM` interface, provider adapters | ✅ done |
| 06 | Ingestion — SHA256 dedup, chunk + embed pipeline | ✅ done |
| 07 | Search — vector, FTS5 keyword, hybrid | ✅ done |
| 08 | RAG — retrieval pipeline, prompt templates, streaming | ✅ done |
| 09 | Management — `tbuk stats`, delete, update | stub |

---

## Architecture

```
cmd/tbuk/           cobra entry point

internal/
  config/           Config struct, Load(), Defaults(), DefaultYAML()
  cli/              cobra root + subcommands (init, version, doctor, preprocess, ingest, search, find)
  storage/          DB wrapper, RunMigrations, DocumentRepo, ChunkRepo, MetadataRepo
  preprocess/       Extractor interface + backends; DetectMIME; SHA256 helpers
  chunking/         Chunker.Split — greedy sentence boundary, Size/Overlap in tokens
  embeddings/       Embedder interface + factory; llama, ollama, openai adapters
  llm/              LLM interface + factory; claude, openai, ollama adapters (SSE + JSON-lines streaming)
  ingest/           Ingester, FileExtractor, DefaultFileExtractor; IngestFile(), IngestDir()
  prompts/          TemplateDir, Load(), List(), Render(); Manifest (YAML); TemplateData
  retrieval/        Retriever, RetrievedChunk (with Citation); HybridSearcher interface
  search/           Searcher; Vector, Keyword, Metadata, Hybrid methods; CheckFTS5
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

SHA256: `preprocess.HashFile(path)` and `HashReader(r)`.

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

## LLM

```go
type Role string
const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
)

type Message struct { Role Role; Content string }
type Token   struct { Text string; Done bool; Error error }
type CallOptions struct { Model string; Temperature float64; MaxTokens int }

type LLM interface {
    Chat(ctx context.Context, messages []Message, opts ...CallOptions) (<-chan Token, error)
}

type LLMError struct { Provider string; StatusCode int; Message string }
func AsLLMError(err error, target **LLMError) bool

func NewLLM(cfg *config.LLMConfig) (LLM, error)
```

| Provider | Key | Endpoint | Notes |
|----------|-----|----------|-------|
| `claude` | `ANTHROPIC_API_KEY` env | `POST {base_url}/v1/messages` | SSE, `content_block_delta` events, `x-api-key` header |
| `openai` | `OPENAI_API_KEY` env | `POST {base_url}/v1/chat/completions` | SSE, `[DONE]` sentinel, Bearer auth |
| `ollama` | — | `POST {base_url}/api/chat` | JSON-lines streaming, `"done":true` sentinel |

`base_url` defaults to `https://api.anthropic.com` (claude), `https://api.openai.com` (openai), `http://localhost:11434` (ollama).

Stream: channel closed after `Token{Done:true}` or `Token{Error:...}`. System messages extracted from the messages slice and sent as top-level `"system"` field (Claude API requirement).

---

## Preprocessing

```go
// Extract opens path, detects MIME, extracts plain text, returns (text, mime, sha256, err).
func Extract(ctx context.Context, path string) (text, mime, sha string, err error)

// ExtractToFile saves extracted text to outputDir/<sha256>.txt. Creates dir if needed.
func ExtractToFile(ctx context.Context, srcPath, outputDir string) (savedPath string, err error)
```

Extracted files named `<sha256-of-source>.txt` — staleness-safe (changed source = different name).
Default store: `~/.tbuk/extracted/` (configurable via `preprocess.output_dir` in config).

---

## Ingestion

Two-stage pipeline:

1. **`tbuk preprocess`** — extract + normalize → save to `~/.tbuk/extracted/<sha256>.txt`
2. **`tbuk ingest`** — read extracted text → chunk → embed → store in DB

```go
type FileExtractor interface {
    ExtractFile(ctx context.Context, path string) (string, error)
}

type Options struct { Force bool }

type Result struct {
    Path    string
    Skipped bool   // SHA256 unchanged and Force=false
    Chunks  int
    Err     error
}

func NewIngester(docs, chunks, meta repos, ext FileExtractor, chunker, emb, extractedDir, progress) *Ingester
func (ing *Ingester) IngestFile(ctx, path, opts) Result
func (ing *Ingester) IngestDir(ctx, dir, opts) []Result
```

Pipeline per file: SHA256 → dedup check → read `extractedDir/<sha256>.txt` (auto-preprocess if missing) → chunk → embed (batch 16) → upsert doc + chunks.
Changed SHA256 → old chunks deleted before re-index.

Supported extensions for `IngestDir`: `.md`, `.txt`, `.pdf`, `.html`, `.htm`.

Doctor shows document/chunk counts from the live DB.

---

## Search

```go
type SearchResult struct {
    ChunkID    int64
    DocumentID int64
    Path       string
    Title      string
    ChunkIndex int
    Text       string
    Score      float64 // higher is better (0-1 for vector/hybrid; negated BM25 for keyword)
    Source     string  // "vector" | "keyword" | "hybrid" | "metadata"
}

type Options struct {
    TopK     int               // default 5
    MinScore float64           // skip results below threshold
    Metadata map[string]string // AND-combined pre-filter (unused by Vector/Keyword)
}

type Searcher struct { /* db, embedder */ }

func New(db *sql.DB, emb embeddings.Embedder) *Searcher
func (s *Searcher) Vector(ctx, query, opts)   ([]SearchResult, error) // cosine, full table scan
func (s *Searcher) Keyword(ctx, query, opts)  ([]SearchResult, error) // FTS5 BM25
func (s *Searcher) Metadata(ctx, filters)     ([]SearchResult, error) // AND-joined metadata keys
func (s *Searcher) Hybrid(ctx, query, opts)   ([]SearchResult, error) // RRF k=60 over vector+keyword
func CheckFTS5(db *sql.DB) error                                       // probes chunks_fts index
```

Vector: full table scan acceptable for < 100k chunks; swap sqlite-vec later without interface change.
Hybrid RRF: `score(d) = Σ 1/(60 + rank_i(d))` — runs both searches at 2×TopK then fuses.

---

## RAG

### Retrieval

```go
type RetrievedChunk struct {
    ChunkID, DocumentID int64
    Path, Title         string
    ChunkIndex          int
    Text                string
    Score               float64
    Citation            string // "path §chunkIndex"
}

type HybridSearcher interface {
    Hybrid(ctx context.Context, query string, opts search.Options) ([]search.SearchResult, error)
}

type Retriever struct { /* searcher HybridSearcher */ }

func New(s HybridSearcher) *Retriever
func (r *Retriever) Retrieve(ctx context.Context, query string, topK int, meta map[string]string) ([]RetrievedChunk, error)
```

### Prompt Templates

Disk layout: `~/.tbuk/prompts/<name>/{manifest.yaml, system.tmpl, user.tmpl}`

```go
type TemplateData struct {
    Question  string
    Chunks    []retrieval.RetrievedChunk
    Variables map[string]string
}

type TemplateDir struct { Root string }

func NewTemplateDir(dir string) *TemplateDir
func (td *TemplateDir) Load(name string) (*Template, error)
func (td *TemplateDir) List() ([]Manifest, error)
func (t *Template) Render(data TemplateData) (system, user string, err error)
func (t *Template) Manifest() Manifest
```

Built-in `qa` template installed by `tbuk init`. `temperature`, `max_tokens`, `retrieval.top_k`, `variables` come from `manifest.yaml`.

`tbuk ask` core logic is in exported `RunAsk(out, retrieveFn, chatFn, tmpl, ...)` for dependency-injected unit testing.

---

## CLI Commands (implemented)

```
tbuk init                      create ~/.tbuk/, write default config.yaml and prompts/
tbuk version                   print version string
tbuk doctor                    probe config, DB (with doc/chunk counts), LLM/embedding/search
tbuk preprocess <path>         extract text → save to extracted store (--dry-run, --output-dir)
tbuk ingest <path>             read extracted text → chunk → embed → store (--force, --verbose)
tbuk search <query>            search chunks (--mode vector|keyword|hybrid, --top N, --min-score F, --format text|json)
tbuk find <key=value>...       find docs by metadata filters (--limit N, --format text|json)
tbuk ask <question>            RAG query: retrieve chunks → render template → stream LLM answer (--template qa, --var k=v, --top N, --no-stream)
tbuk template list             list prompt templates in ~/.tbuk/prompts/
tbuk template show <name>      print manifest + template files to stdout
tbuk template edit <name>      open manifest in $EDITOR
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
