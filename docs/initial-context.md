# Timbuktu — Initial Context

Local-first CLI RAG knowledge base. Go 1.26+, SQLite, no web UI, no frameworks.

Module: `github.com/gotofritz/timbuktu`

---

## Architecture

```
cmd/tbuk/           cobra entry point

internal/
  config/           Config struct, Load(), Validate(), Defaults(), DefaultYAML()
  cli/              cobra root + subcommands (init, version, doctor, preprocess, ingest, search, find, meta)
  storage/          DB wrapper, RunMigrations, DocumentRepo, ChunkRepo, MetadataRepo
  preprocess/       Extractor interface + backends; DetectMIME; SHA256 helpers
  chunking/         Chunker.Split — sentence-boundary search (rune-safe), Size/Overlap in tokens
  embeddings/       Embedder interface + factory; llama, ollama, openai adapters
  llm/              LLM interface + factory; claude, openai, llama, ollama adapters (SSE + JSON-lines streaming)
  ingest/           Ingester, FileExtractor, DefaultFileExtractor; IngestFile(), IngestDir()
  prompts/          TemplateDir, Load(), List(), Render(); Manifest (YAML); TemplateData
  retrieval/        Retriever, RetrievedChunk (with Citation); HybridSearcher interface
  search/           Searcher; Vector, Keyword, Metadata, Hybrid methods; CheckFTS5
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

`Config.Validate()` runs in the root `PersistentPreRunE` right after `Load`, so every command fails fast on a bad config (non-positive chunk size, overlap ≥ size, non-positive max_tokens/dimension, empty db path, an unknown llm/embedding provider, or ingest embed_concurrency < 1) instead of crashing deep inside a provider factory.

---

## Storage

SQLite, WAL mode, foreign keys ON. Pragmas are set in the DSN (`dsnFor`) so every pooled connection inherits them. The DB file is `chmod 0o600` after open — knowledge-base content is personal data.

```sql
documents   — id, path (UNIQUE), sha256, title, mime_type, created_at, updated_at
chunks      — id, document_id (FK→documents CASCADE), chunk_index, text, token_count, embedding BLOB
metadata    — document_id (FK→documents CASCADE), key, value  (PK: document_id+key)
chunks_fts  — FTS5 virtual table over chunks.text, auto-synced via INSERT/DELETE triggers
```

Migrations versioned in `schema_migrations` table. Add new migrations by appending to the `migrations` slice in `storage/migrate.go`. Each migration's SQL and its version record are applied in one transaction (crash-safe: never changed-but-unrecorded). A DB whose recorded version exceeds the binary's latest migration is rejected with `ErrSchemaTooNew` rather than read with a misunderstood schema.

Embeddings: `storage.Float32SliceToBlob` / `BlobToFloat32Slice` — little-endian `[]float32`.

Repos: `DocumentRepo`, `ChunkRepo`, `MetadataRepo` — all take `*sql.DB`, return typed errors wrapping `fmt.Errorf("Repo.Method: %w", err)`.

Lookups that can miss (`DocumentRepo.GetByPath` / `GetBySHA256`) return the sentinel `storage.ErrNotFound` (wrapping `sql.ErrNoRows`) when no row matches. Callers branch with `errors.Is(err, storage.ErrNotFound)` so a genuine "does not exist" is never conflated with a transient DB error.

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
Boundary and overlap byte offsets snap back to a UTF-8 rune start
(`snapRuneStart`) so non-ASCII text is never sliced mid-rune.

CLI paths (`ingest`/`update`/`delete`) are resolved to absolute+cleaned form
via `cli.NormalizePath` (`filepath.Abs`) so a document is keyed by one
canonical path regardless of the spelling used.

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

The `openai` and `ollama` adapters wrap each POST in `doWithRetry` (`retry.go`):
2 retries / 3 attempts total, exponential backoff (500 ms → 1 s), retrying
connection errors and transient statuses (429 + 5xx) and honouring a
`Retry-After` header of seconds. This keeps a large bulk ingest against a
rate-limiting hosted provider from degrading into repeated manual re-runs. The
final response is handed back on exhaustion so the existing `EmbedError` path
still surfaces the provider's message. LLM streaming is deliberately **not**
retried — `ask` is interactive and should fail fast.

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
type CallOptions struct { Model string; Temperature *float64; MaxTokens int } // nil Temperature = provider default

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
| `llama`  | — | `POST {base_url}/v1/chat/completions` | OpenAI-compatible SSE, no auth header; shares the `openai` adapter |
| `ollama` | — | `POST {base_url}/api/chat` | JSON-lines streaming, `"done":true` sentinel |

`base_url` defaults to `https://api.anthropic.com` (claude), `https://api.openai.com` (openai), `http://localhost:8080` (llama), `http://localhost:11434` (ollama).

Stream: channel closed after `Token{Done:true}` or `Token{Error:...}`. Every send goes through `sendToken`, which selects on `ctx.Done()`, so a consumer that abandons the channel (e.g. `RunAsk` returning on a mid-stream error) releases the goroutine instead of leaking it. `RunAsk` runs retrieval and the chat call under a cancellable context derived from `cmd.Context()`, cancelled on return (Ctrl-C interrupts). System messages extracted from the messages slice and sent as top-level `"system"` field (Claude API requirement).

On a non-200 response the adapters read up to ~2 KB of the body into `LLMError`/`EmbedError.Message` (falling back to the HTTP status text when empty), preserving the provider's own error text ("model not found", "context length exceeded").

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

Pipeline per file: SHA256 → dedup check → read `extractedDir/<sha256>.txt` (auto-preprocess if missing) → chunk → embed (batch 16) → upsert doc → `ChunkRepo.ReplaceForDocument` → write automatic metadata.
Embed batches within a file run through a bounded worker pool (`ingest.embed_concurrency`, default 4; `WithEmbedConcurrency` option) so embedder round-trips — the latency bottleneck — overlap. Results are reassembled in chunk order and the per-file DB write stays serial, so `ReplaceForDocument` atomicity is untouched; the first batch error cancels the rest.
Re-index is atomic: extraction and embedding run *first*, then `ReplaceForDocument` deletes old chunks and inserts new ones in a single transaction. A failed re-ingest (embedding error) leaves the previous chunks intact rather than destroying the index.

Automatic metadata written per document: `filename`, `extension` (lowercased, no leading dot), `mime`, `dir`. Refreshed on every ingest via `MetadataRepo.Set` upsert; user-set keys are left intact. Makes `tbuk find filename=README.md` work after plain ingest.

Supported extensions for `IngestDir`: `.md`, `.txt`, `.pdf`, `.html`, `.htm`.

Doctor shows document/chunk counts from the live DB. The FTS5 health check is
gated on database health (its own flag), not on any embedding server's
reachability, so a down embedder can't mask FTS corruption. Hosted providers
(`claude`/`openai`) are not HTTP-probed — they lack the llama.cpp/ollama
`/health` & `/v1/models` endpoints — so doctor prints `hosted API — not probed`
instead of a misleading status. `runDoctor` writes to an `io.Writer`
(`RunDoctorTo`) for testability.

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
func (s *Searcher) Vector(ctx, query, opts)   ([]SearchResult, error) // cosine, two-phase: rank (id,embedding) then hydrate top-K
func (s *Searcher) Keyword(ctx, query, opts)  ([]SearchResult, error) // FTS5 BM25 (query sanitized to quoted phrases)
func (s *Searcher) Metadata(ctx, filters)     ([]SearchResult, error) // AND-joined metadata keys
func (s *Searcher) Hybrid(ctx, query, opts)   ([]SearchResult, error) // RRF k=60 over vector+keyword
func CheckFTS5(db *sql.DB) error                                       // probes chunks_fts index
```

Vector: O(n) embedding scan acceptable for < 100k chunks; swap sqlite-vec later without interface change. Runs two-phase — phase 1 scans only `(id, embedding)` and keeps a bounded min-heap of the top-K ids (O(n log K) time, O(K) memory, never touches chunk text); phase 2 hydrates text/path/title for just those K ids. Peak memory is O(K), not O(corpus).
Hybrid RRF: `score(d) = Σ 1/(60 + rank_i(d))` — runs both searches at 2×TopK then fuses.
`Options.MinScore` filters the fused RRF sums (a different scale from vector cosine),
applied before truncating to TopK.
Keyword: user input is sanitized for FTS5 — each whitespace-separated term becomes a
double-quoted phrase, neutralizing operators/special chars; real query errors propagate.

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

Disk layout: `~/.tbuk/prompts/<name>/{manifest.yaml, system.tmpl, user.tmpl}` (root configurable via `prompts.dir`)

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

Built-in `qa` template installed by `tbuk init`. `temperature`, `max_tokens`, `retrieval.top_k`, `retrieval.max_tokens`, `variables` come from `manifest.yaml`. `RunAsk` forwards `model`/`temperature`/`max_tokens` into the LLM via `CallOptions`; `Manifest.Temperature` is `*float64` so an explicit `0` is distinct from unset. `retrieval.max_tokens`, when set, trims retrieved chunks to that approximate token budget before rendering (at least one chunk is always kept).

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
tbuk meta set <path> k=v...    attach metadata key=value pairs to a document
tbuk meta list <path>          list all metadata for a document
tbuk ask <question>            RAG query: retrieve chunks → render template → stream LLM answer (--template qa, --var k=v, --top N, --no-stream)
tbuk template list             list prompt templates in ~/.tbuk/prompts/
tbuk template show <name>      print manifest + template files to stdout
tbuk template edit <name>      open manifest in $EDITOR
tbuk delete <path>             remove document + cascade-delete chunks/metadata + extracted-text cache (--yes skips prompt)
tbuk update <path>             re-ingest if SHA256 changed, skip otherwise (--force)
tbuk stats                     knowledge base summary: documents, chunks, embedded count, sizes (--format text|json)
tbuk list                      list indexed documents: path, title, chunk count, updated_at (--limit, --format text|json)
```

---

## Patterns

- Table-driven tests with `testing` stdlib only
- HTTP providers mocked with `net/http/httptest`
- In-memory SQLite (`:memory:`) for storage tests
- Unit tests inject fakes at package seams; one CLI end-to-end test (`internal/cli/integration_test.go`) drives the real root command (`init → ingest → search → meta → stats → delete`) with only the embedding server faked, so the production wiring — `DefaultFileExtractor`, composition root, exit codes — is exercised assembled. `Execute`'s non-zero exit is checked via a re-exec-self subprocess (it calls `os.Exit`)
- `fmt.Errorf("context: %w", err)` for error wrapping
- Sentinel errors (e.g. `storage.ErrNotFound`) matched with `errors.Is`, not string comparison
- No `init()`, no global mutable state, no `interface{}` — CLI config is loaded in the root `PersistentPreRunE` and threaded through the cobra command context (`configFrom`/`configPathFrom`), not package-level vars
- Composition root is a single `openApp(cfg) (*App, error)` builder (`internal/cli/app.go`), not per-command wiring. `App` owns the open DB and lazily/memoized builds the embedder, repos (`Docs()`), `Ingester()` and `LLM()`; commands call `openApp`, `defer app.Close()`, then pull only what they need. Adding a dependency touches the builder, not every command
- `Execute()` builds a `signal.NotifyContext` (SIGINT/SIGTERM) and runs `root.ExecuteContext(ctx)`, so Ctrl-C cancels the ctx-plumbed pipeline cleanly (deferred cleanup runs, transactions roll back, `IngestDir` stops the walk via `filepath.SkipAll` and still prints its partial summary); a second signal force-quits
- Data files are owner-only: `~/.tbuk` dirs `0o700`; config, extracted text and DB files `0o600`
- Ingested documents are untrusted: text can carry ANSI/OSC terminal escapes (OSC 52 clipboard write, window-title rewrite, cursor/erase). Document-derived output to the terminal is filtered of C0/C1 control chars (keeping `\n`/`\t`) via `internal/cli/sanitize.go` — streamed `ask` output goes through the rune-aware `sanitizeWriter` (buffers split UTF-8 across writes), and discrete fields (`search` paths, `list` path/title, `meta list` values) through `stripControl`. JSON output paths need no filtering (the encoder escapes control runes)
- `defer func() { _ = resp.Body.Close() }()` for HTTP responses
- HTTP error responses surface the provider's body (bounded read), not just the status text
- Stream goroutines select on `ctx.Done()` on every channel send to avoid leaks
- Imports grouped: stdlib / external / internal
