# Timbuktu — Initial Context

Local-first CLI RAG knowledge base. Go 1.26+, SQLite, no web UI, no frameworks.

Module: `github.com/gotofritz/timbuktu`

---

## Architecture

```
cmd/tbuk/           cobra entry point

internal/
  config/           Config struct, Load(), Validate(), Defaults(), DefaultYAML(), ExportYAML()
  cli/              cobra root + subcommands (init, version, doctor, preprocess, ingest, reindex, search, find, meta, export, import)
  storage/          DB wrapper, RunMigrations, DocumentRepo, ChunkRepo, MetadataRepo
  preprocess/       Extractor interface + backends; DetectMIME; SHA256 helpers
  chunking/         Chunker.Split — sentence-boundary search (rune-safe), Size/Overlap in tokens
  embeddings/       Embedder interface + factory; mlx, llama, ollama, openai adapters
  llm/              LLM interface + factory; mlx, claude, openai, llama, ollama adapters (SSE + JSON-lines streaming)
  ingest/           Ingester, FileExtractor, DefaultFileExtractor; IngestFile(), IngestDir()
  prompts/          TemplateDir, Load(), List(), Render(); Manifest (YAML); TemplateData
  retrieval/        Retriever, RetrievedChunk (with Citation); HybridSearcher interface
  search/           Searcher; Vector, Keyword, Metadata, Hybrid methods; CheckFTS5; parseQuery (phrases, exclusions)
  searchtext/       Reduce() — the reduced encoding stored in chunks.search_text and embedded
  squeeze/          Text(), Chunks() — lossy prose compaction of retrieved text, code left byte-exact
  export/           Create() — tar snapshot of config + data folders (portable, path-commented config)
  importer/         Extract() — a tar snapshot's raw sources + its database as a manifest; ignores config, cache, prompts
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

**Data root.** Every data path (database, extracted cache, raw archive, prompt
templates, config file) hangs off a single *root*, `config.DefaultRoot()`
= `~/.tbuk`. The global `--root DIR` flag overrides it per invocation:
`config.DefaultsForRoot(root)` / `LoadForRoot(path, root)` / `DefaultYAMLForRoot(root)`
re-base all paths under `DIR`, and the no-arg `Defaults()`/`Load()`/`DefaultYAML()`
are thin wrappers rooted at `DefaultRoot()`.

**Per-component paths (relative or absolute).** Each data path in the config may
be **relative to the root** or **absolute**. `Config.ResolvePaths(root)` rebases
every relative data path onto the root and leaves absolute paths and empty values
(an empty `ingest.raw_dir` disables the archive) untouched; `LoadForRoot` applies
it after decoding, so every command reads already-resolved absolute paths through
the one config chokepoint. This lets the pipeline's parts live in different places
— most relative to a portable root, one or two pinned absolutely (e.g. the raw
archive on a larger disk). The single source of truth is `relativeDefaults()`
(paths like `./tbuk.sqlite`, `./raw`); `DefaultsForRoot(root)` is
`relativeDefaults().ResolvePaths(root)`, and `defaultConfigNode()` builds the
commented YAML node tree shared by `DefaultYAMLForRoot` (header-topped) and
`FillMissingDefaults`.

**Flag resolution.** The root command's `PersistentPreRunE` resolves root and
config path together: `--root DIR` → root=`DIR`, config=`DIR/config.yaml` (or the
`--config` file if given); `--config FILE` alone → root=`dir(FILE)`, config=`FILE`
(so `--config DIR/config.yaml` ≡ `--root DIR`, the config always sitting directly
under its root); neither → `~/.tbuk`. The resolved root is stored in the command
context (`rootFrom`). `init` scaffolds under that root; for a fresh config it
writes a portable `config.yaml` with relative paths under a header naming the
root, and for an existing config it fills in any missing default keys in place
(`config.FillMissingDefaults`, a YAML-node merge that preserves the user's values
and comments) or leaves a complete file untouched. This is the concrete form of
the "DB switching" roadmap item — a whole-collection switch rather than a single
`--db` path.

```go
type Config struct {
    Database  DatabaseConfig   // path
    LLM       LLMConfig        // provider, model, max_tokens, context_tokens, base_url
    Embedding EmbeddingConfig  // provider, model, dimension, base_url
    Chunking  ChunkingConfig   // size (tokens), overlap (tokens)
}
```

Defaults: llm.provider=`mlx`, embedding.provider=`mlx`, dimension=768, chunk size=400, overlap=50. The `mlx` provider targets any OpenAI-compatible server fronting MLX models on Apple silicon (mlx_lm.server, mlx-openai-server, LM Studio, nativ); `llama` (llama.cpp) remains the cross-platform local alternative. Chunk size kept below llama.cpp default ubatch size (512 tokens) to avoid HTTP 500 errors from the embedding server. To use larger chunks, raise both `-b` and `-ub` at server startup (they must match; e.g. `llama-server -b 1024 -ub 1024 …`).

`Config.Validate()` runs in the root `PersistentPreRunE` right after `Load`, so every command fails fast on a bad config (non-positive chunk size, overlap ≥ size, non-positive max_tokens/dimension, a context_tokens that does not exceed max_tokens, empty db path, an unknown llm/embedding provider, or ingest embed_concurrency < 1) instead of crashing deep inside a provider factory.

---

## Storage

SQLite, WAL mode, foreign keys ON. Pragmas are set in the DSN (`dsnFor`) so every pooled connection inherits them. The DB file is `chmod 0o600` after open — knowledge-base content is personal data.

```sql
documents   — id, path (UNIQUE), sha256, title, mime_type, raw_path, created_at, updated_at
chunks      — id, document_id (FK→documents CASCADE), chunk_index, text, search_text, token_count, embedding BLOB
metadata    — document_id (FK→documents CASCADE), key, value  (PK: document_id+key)
chunks_fts  — FTS5 virtual table over chunks.search_text (tokenize="unicode61 tokenchars '_'"), auto-synced via INSERT/DELETE triggers
```

`chunks` holds one segment in two encodings. `text` is the segment as written —
what `tbuk search` prints and what `tbuk ask` feeds the model. `search_text` is
the same segment reduced for retrieval (see **Search encoding** below): it is
what `chunks_fts` indexes and what the stored `embedding` was taken of. Prose is
carried through the reduction unchanged, so for most of a corpus the two columns
agree and the duplication costs little; a code chunk is where they diverge, and
that divergence is the point.

Schema versioned in the `schema_migrations` table. `storage/migrate.go` holds a
single migration — `schemaSQL` at `schemaVersion` — that creates the whole
schema: a knowledge base either does not exist yet or already has this shape,
so there is no upgrade path to carry. `schemaSQL` is therefore edited in place
rather than followed by a versioned migration, and an existing knowledge base is
brought forward by a throwaway script under `scripts/` (AGENTS.md, "proof of
concept"); `search_text` and the `chunks_fts` tokenizer were both changed that
way, and `storage.HasSearchTextColumn` / `storage.HasPunctuationTokenizer` are
how `tbuk doctor` spots a knowledge base that has not had the matching script
run. (`schemaVersion` is 2 because an earlier
build created the same schema in two steps and recorded 2; keeping the number
lets those knowledge bases open untouched.) Future changes append to the
`migrations` slice. Each migration's SQL and its version record are applied in
one transaction (crash-safe: never changed-but-unrecorded). A DB whose recorded
version exceeds the binary's latest migration is rejected with `ErrSchemaTooNew`
rather than read with a misunderstood schema.

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

Backends: markdownExtractor, htmlExtractor (golang.org/x/net), plainTextExtractor, pdfExtractor (ledongthuc/pdf).

The markdown backend cuts a document into code and prose before touching anything: whatever sits inside backticks — a fenced block or an inline span — is copied out verbatim, **markers included**, and only the prose between gets its headings, bold and emphasis markers stripped. Punctuation is part of a software term, so `main_consumption` and `__init__` survive extraction intact. In prose, underscore emphasis is only stripped at word boundaries, which leaves an unbackticked snake_case identifier alone too.

The fences and backticks are kept because they are the only record of which bytes are code, and the search encoding downstream cannot reduce a code region it cannot find. They also read as code on the page, which is what the reader and the model want anyway.

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
| `mlx`    | default | `POST {base_url}/v1/embeddings` | OpenAI shape, no auth; optional `MLX_API_KEY` env sent as Bearer (keyed base URL must be HTTPS/loopback) |
| `llama`  | — | `POST {base_url}/embedding` | `{"content":"..."}` → `{"embedding":[...]}`, one request per text |
| `ollama` | — | `POST {base_url}/api/embed` | `{"model":"...","input":[...]}`, batch size 8 |
| `openai` | `OPENAI_API_KEY` env | `POST {base_url}/v1/embeddings` | Bearer auth, OpenAI response format |

`base_url` defaults to `http://localhost:8080` (mlx/llama), `http://localhost:11434` (ollama), or `https://api.openai.com` (openai).
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
| `mlx`    | default | `POST {base_url}/v1/chat/completions` | OpenAI-compatible SSE, shares the `openai` adapter; optional `MLX_API_KEY` env sent as Bearer (keyed base URL must be HTTPS/loopback) |
| `claude` | `ANTHROPIC_API_KEY` env | `POST {base_url}/v1/messages` | SSE, `content_block_delta` events, `x-api-key` header |
| `openai` | `OPENAI_API_KEY` env | `POST {base_url}/v1/chat/completions` | SSE, `[DONE]` sentinel, Bearer auth |
| `llama`  | — | `POST {base_url}/v1/chat/completions` | OpenAI-compatible SSE, no auth header; shares the `openai` adapter |
| `ollama` | — | `POST {base_url}/api/chat` | JSON-lines streaming, `"done":true` sentinel |

`base_url` defaults to `https://api.anthropic.com` (claude), `https://api.openai.com` (openai), `http://localhost:8080` (mlx/llama), `http://localhost:11434` (ollama).

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

1. **`tbuk preprocess`** — extract + normalize → save to `~/.tbuk/extracted/<sha256>.v<N>.txt`, where N is `preprocess.ExtractorVersion` (bumped when extraction output can differ for the same bytes, which invalidates every cached extraction; `tbuk delete` clears all versions)
2. **`tbuk ingest`** — read extracted text → chunk → embed → store in DB

```go
type FileExtractor interface {
    ExtractFile(ctx context.Context, path string) (string, error)
}

type Options struct { Force bool; NoRaw bool } // NoRaw skips the raw archive copy

type Result struct {
    Path    string
    Skipped bool   // SHA256 unchanged and Force=false
    Chunks  int
    Err     error
}

func NewIngester(docs, chunks, meta repos, ext FileExtractor, chunker, emb, extractedDir, opts...) *Ingester
func (ing *Ingester) IngestFile(ctx, path, opts) Result
func (ing *Ingester) IngestDir(ctx, dir, opts) []Result
```

Pipeline per file: SHA256 → dedup check → raw archive (see below) → read `extractedDir/<sha256>.txt` (auto-preprocess if missing) → chunk → reduce (`searchtext.Reduce`) → embed the reduced form (batch 16) → upsert doc → `ChunkRepo.ReplaceForDocument` (storing both encodings) → write automatic metadata.

Raw archive: when a raw dir is configured (`ingest.raw_dir`, default `~/.tbuk/raw`; `WithRawDir` option) and `Options.NoRaw` is false, the untouched source is copied to `rawDir/<sha256><ext>` — content-addressed like the extracted store, written via temp-file + rename (crash-safe), `0o600`, and idempotent (an existing copy is left as-is). The copy runs *before* extraction/embedding so a copy failure aborts the whole ingest and is retried, never stranding an indexed document without its raw backup. `tbuk ingest --no-raw` suppresses it for one run.
Embed batches within a file run through a bounded worker pool (`ingest.embed_concurrency`, default 4; `WithEmbedConcurrency` option) so embedder round-trips — the latency bottleneck — overlap. Results are reassembled in chunk order and the per-file DB write stays serial, so `ReplaceForDocument` atomicity is untouched; the first batch error cancels the rest.
Re-index is atomic: extraction and embedding run *first*, then `ReplaceForDocument` deletes old chunks and inserts new ones in a single transaction. A failed re-ingest (embedding error) leaves the previous chunks intact rather than destroying the index.

Automatic metadata written per document: `filename`, `extension` (lowercased, no leading dot), `mime`, `dir`. Refreshed on every ingest via `MetadataRepo.Set` upsert; user-set keys are left intact. Makes `tbuk find filename=README.md` work after plain ingest.

Supported extensions for `IngestDir`: `.md`, `.txt`, `.pdf`, `.html`, `.htm`.

### Reindex (re-embed from the raw archive)

```go
type ReindexOptions struct {
    SourceDir string // resolve <sha256><ext> here instead of the configured raw dir
    DryRun    bool
    OnResult  func(index, total int, res ReindexResult) // streams progress
}

type ReindexResult struct {
    Path    string // the document's stored path — its identity in the KB
    Source  string // file the content was read from; "" when unresolvable
    FromRaw bool   // Source is the archived copy, not the stored path
    Chunks  int    // 0 on a dry run
    Skipped bool   // no usable source; document left untouched
    Err     error
}

var ErrSourceUnavailable = errors.New(...) // typed, and Skipped is set with it

func (ing *Ingester) ReindexDocument(ctx, doc *storage.Document, opts) ReindexResult
func (ing *Ingester) ReindexDocuments(ctx, docs []*storage.Document, opts) []ReindexResult
func (ing *Ingester) ReindexAll(ctx, opts) ([]ReindexResult, error)
```

Text resolution, in order: the extracted-text cache
(`extracted/<sha256>.v<N>.txt`, keyed on content *and* `ExtractorVersion`, so it
answers when every file has gone and is credited as the source when it does); then a source file to extract from — `documents.raw_path`
under `SourceDir` (else `ingest.raw_dir`); the derived `<sha256><ext>` for rows
indexed before that column existed — recorded via `SetRawPath` when it resolves, so the fallback is
paid once, and never for a `--source-dir` hit, which lives in another archive;
then `documents.path`; and last, text cached by an *older* extractor version
(`preprocess.OlderCacheNames`, flagged `StaleText`), which is read only when
re-extraction is impossible — a build that produced different output is worse
than re-extracting, and better than an unreadable document. The archived copy
is still resolved and recorded on a cache hit — the cache is disposable, the copy is not. Nothing readable anywhere
⇒ `Skipped` + `ErrSourceUnavailable`, with `ReindexResult.Reason` naming every
path tried, since a skipped document is something the user has to act on.
Reading from the content-addressed archive rather than the live path is what
makes reindex work for a knowledge base whose originals are gone — an imported
document keeps the *exporting* machine's absolute path, which resolves to
nothing here. `filepath.Ext(doc.Path)` supplies the extension the extractor
keys off; the extracted cache is looked up under `doc.SHA256`, so text already
extracted on this machine is reused rather than re-derived.

Three deliberate differences from `IngestFile`:

- **No SHA256 comparison or skip.** The content is presumed unchanged; the
  trigger is an embedding config change, which stored chunks carry no record of.
- **No dimension guard.** `IngestFile` refuses when new vectors disagree with
  the rest of the index; reindex must not, or the first document would abort the
  very repair it exists for.
- **The document row is left alone.** Only chunks (via `ReplaceForDocument`,
  same atomicity) and the automatic metadata are rewritten — the latter derived
  from `doc.Path`, never from the sha-named archived copy that supplied the
  bytes.

Per-document failures are reported, never fatal (`IngestDir`'s
partial-failure pattern); only listing the documents is fatal. Cancellation
stops the loop so the caller can print what it has.

CLI seam (`internal/cli/reindex.go`):
`RunReindex(ctx, outW, errW, ing, opts, verbose)` sets `OnResult` to stream
`[i/N] <path> → …` lines, then prints a re-embedded/skipped/errors summary and
returns non-zero only on real errors. Successes are printed only under
`verbose` (or `--dry-run`); skips and failures always are — the same rule
`ingest` and `import` follow, so a single bad document is not buried under
hundreds of good ones.

The root command sets `SilenceUsage` in `PersistentPreRunE`, which cobra runs
*after* argument validation: misuse still gets its usage text, a command that
ran and failed does not. `Execute` no longer prints the error itself, since
cobra already has.
`--topic` is deliberately absent: topics (`docs/plans/32-topics.md`) are not
implemented, and reindex is not gated on them.

Doctor shows document/chunk counts from the live DB. The FTS5 health check is
gated on database health (its own flag), not on any embedding server's
reachability, so a down embedder can't mask FTS corruption. Its Search section
also reports the two things about an index that fail silently rather than
loudly: a missing `chunks.search_text`, and an index still on the default
tokenizer (`'_'`/`'-'` split terms, so exact terms, phrases and exclusions all
answer as if the punctuation had not been typed). Each names the script under
`scripts/` that fixes it. Hosted providers
(`claude`/`openai`) are not HTTP-probed — they lack the local-server
`/health` & `/v1/models` endpoints — so doctor prints `hosted API — not probed`
instead of a misleading status. Local status probes pick their path per
provider (`statusProbeURL`): `llama`/`ollama` hit `/health`, `mlx` hits
`/v1/models` (MLX servers expose no `/health`). `runDoctor` writes to an
`io.Writer` (`RunDoctorTo`) for testability.

---

## Search encoding

```go
func searchtext.Reduce(text string) string
```

The retrieval encoding of a chunk, stored in `chunks.search_text` and handed to
the embedder. The requirement is *this topic is referenced in that bit of code*,
not code search: what carries that signal is comments, identifiers and strings,
and syntax is noise. One string cannot serve display, the model and the index at
once, so the faithful text and the reduced form are stored side by side.

- **Prose** — passed through unchanged. An inline `` `span` `` gets the
  identifier treatment below, with the backticks dropped.
- **Code region** (a fenced block; an unclosed fence, which is what a chunk
  boundary through a block leaves, runs to the end of the chunk) — comments and
  the contents of string literals keep their words; identifiers are emitted as
  written and then split; keywords, operators, punctuation and numeric literals
  are dropped. The fence's language tag is emitted as a term of its own and
  selects extra stop words; nothing depends on it being there.
- **Identifier split** — `main_consumption` → `main_consumption main
  consumption`, `readMeter` → `readMeter read meter`. Terms split on `_ - . /`
  and at camelCase boundaries (an acronym run splits once: `HTTPServer` → `http
  server`). Emitting the term *before* its split forms is what lets an exact
  query and a loose word query reach the same chunk from one index.
- **Dropped whole** — a term that reduces to nothing: a keyword, a numeric
  literal, a single character (loop variables, format verbs).

No AST, no parser, no per-language analysis. `stopwords.go` holds a short
cross-language keyword list plus small per-language extras (go, python,
javascript, sql, shell, c) reached through the fence tag and its aliases.

A chunk that reduces to nothing — all syntax, no names — keeps its text as
written (`ingest.reduceForSearch`), since an unembedded chunk drops out of the
knowledge base entirely.

Known limitations:

- A chunk boundary that cuts a fenced block leaves the tail chunks with no
  opening fence, and those are treated as prose — the chunker defects tracked
  separately (a block cut mid-line, overlap starting mid-word) are what make
  this reachable.
- Prose passes through unchanged, so an identifier written bare in prose (no
  backticks, no fence) gets no split form. Since the index no longer breaks it
  at `_`/`-` either, a loose `main consumption` does not reach it — only the
  exact term does. Backticking it, which is what markdown notes normally do, is
  enough.

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
    TopK      int               // default 5
    MinScore  float64           // skip results below threshold
    Metadata  map[string]string // AND-combined pre-filter (unused by Vector/Keyword)
    Operators bool              // read the query as an expression (phrases, exclusions)
}

type Searcher struct { /* db, embedder */ }

func New(db *sql.DB, emb embeddings.Embedder) *Searcher
func (s *Searcher) Vector(ctx, query, opts)   ([]SearchResult, error) // cosine, two-phase: rank (id,embedding) then hydrate top-K
func (s *Searcher) Keyword(ctx, query, opts)  ([]SearchResult, error) // FTS5 BM25 (query parsed to a MATCH expression)
func (s *Searcher) Metadata(ctx, filters)     ([]SearchResult, error) // AND-joined metadata keys
func (s *Searcher) Hybrid(ctx, query, opts)   ([]SearchResult, error) // RRF k=60 over vector+keyword
func CheckFTS5(db *sql.DB) error                                       // probes chunks_fts index
```

Vector: O(n) embedding scan acceptable for < 100k chunks; swap sqlite-vec later without interface change. Runs two-phase — phase 1 scans only `(id, embedding)` and keeps a bounded min-heap of the top-K ids (O(n log K) time, O(K) memory, never touches chunk text); phase 2 hydrates text/path/title for just those K ids. Peak memory is O(K), not O(corpus).
Hybrid RRF: `score(d) = Σ 1/(60 + rank_i(d))` — runs both searches at 2×TopK then fuses.
`Options.MinScore` filters the fused RRF sums (a different scale from vector cosine),
applied before truncating to TopK.
Keyword: matches `chunks.search_text` (the reduced encoding) and returns `chunks.text`
(the chunk as written), so a code chunk is *found* by its names and comments but
*shown* as it was written.

### Query semantics

`internal/search/query.go` turns a user query into an FTS5 MATCH expression.
Every operand is a double-quoted string with embedded quotes doubled, so
arbitrary input is always valid syntax rather than syntax errors; real query
errors propagate.

`Options.Operators` picks between two readings of the same input:

- **Lenient** (`tbuk ask`, via `retrieval.Retriever` → `Hybrid`) — every
  whitespace-separated field is a positive term, punctuation and all. Terms are
  OR-combined, not left to FTS5's implicit AND: AND required every word of a
  question to appear in one chunk, so `tbuk ask`'s queries matched nothing and
  Hybrid silently ran vector-only. BM25 ranking and TopK, not the match
  operator, are what keep the leg precise.
- **Operator-aware** (`tbuk search`) — a double-quoted run is one phrase term,
  and a `-` at the *start* of a field excludes. The dash counts only there, so
  `check-ci` is a term and `-draft` is an exclusion; a leading dash is searched
  literally by quoting the word. `check-ci` reaches the index as the phrase
  `check ci`, since `-` is a separator there. Exclusions become the right-hand side of
  FTS5's binary `NOT`; an exclusion with no positive term beside it yields no
  expression at all, since FTS5 has no "everything except".

English stop words are dropped from bare positive terms (an all-stop-word query
keeps them). They survive inside a quoted phrase, where dropping one breaks the
phrase, and inside an exclusion, which is explicit enough to take at face value.

Both readings depend on the index keeping `_` inside a token
(`tokenize="unicode61 tokenchars '_'"` on `chunks_fts`): under the FTS5 default
tokenizer `main_consumption` and `main consumption` are the same two tokens, and
nothing downstream can tell an exact term from the words apart — `NOT
main_consumption` excluded both. What keeps a loose query reaching an identifier
is the split form `searchtext.Reduce` emits beside it, not the tokenizer, so the
reach follows the encoding: an identifier in a fence or an inline span carries
its split form, one written bare in prose does not.

`-` is not in that list, and the asymmetry is deliberate. It was, briefly
(#136), and a token character applies everywhere rather than only inside
identifiers: `long-term`, `day-to-day`, `state-of-the-art` were each one token,
reachable only by typing the hyphen. That fell on `tbuk ask` — a question is
prose, nobody writes the hyphen back into it — and hyphenated English is far
more of this corpus than kebab-case names are (#143). The price is that a
kebab-case name can no longer be told from its words: `check-ci` and `check ci`
are the same query, both reading as the phrase. `_` has no such cost, since
underscores do not occur in English prose. The principled alternative — having
`searchtext.Reduce` emit split forms for hyphenated prose the way it does for
identifiers — keeps both, at the cost of a full `tbuk reindex`; it is the way
back if kebab-case exactness ever earns it.

Hybrid applies exclusions again after fusion (`excludedChunkIDs`): the keyword
leg has already dropped them, but the vector leg knows nothing about `NOT` and
would hand back exactly what was excluded. Filtering happens before `MinScore`
and TopK so an excluded chunk does not spend a slot. The vector leg embeds
`parsedQuery.vectorText()` — the positive half as prose, stop words kept — since
embedding `-main_consumption` verbatim pulls the excluded chunks *towards* the
query. A `--mode vector` search therefore drops exclusions rather than honouring
them; cosine similarity has no `NOT`.

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

Built-in `qa`, `brief`, and `anki` templates installed by `tbuk init`. `temperature`, `max_tokens`, `context_tokens`, `retrieval.top_k`, `retrieval.max_tokens`, `variables` come from `manifest.yaml`. `RunAsk` forwards `model`/`temperature`/`max_tokens` into the LLM via `CallOptions`; `Manifest.Temperature` is `*float64` so an explicit `0` is distinct from unset. `retrieval.max_tokens`, when set, trims retrieved chunks to that approximate token budget before rendering (at least one chunk is always kept).

`tbuk ask` core logic is in exported `RunAsk(out, retrieveFn, chatFn, tmpl, ...)` for dependency-injected unit testing.

#### Context budget

`retrieval.max_tokens` bounds the retrieved chunks; the context budget bounds
the *whole* prompt — system + template + chunks + question — so an oversized
prompt is caught locally instead of by the provider (HTTP 4xx "context length
exceeded"). The window is `llm.context_tokens` (0 = guard off), overridable per
template by a top-level `context_tokens` in `manifest.yaml`; what it leaves for
the prompt is the window minus the reply's budget (`manifest.max_tokens`, else
`llm.max_tokens`). `Config.Validate` rejects a config window that does not
exceed `llm.max_tokens`, and `tbuk doctor` reports both numbers plus any
template whose own `max_tokens` swallows its window.

```go
func WithContextBudget(window, outputReserve int) AskOption
func fitToContext(render renderFn, chunks []retrieval.RetrievedChunk, budget int) (fittedPrompt, error)
```

`fitToContext` renders, measures with `chunking.CountTokens` (chars/4, the same
estimator as the chunker — approximate by design, see #120), and climbs a
ladder while over budget: **compact** the retrieved text via `internal/squeeze`
(whitespace and filler words out, fenced/indented code byte-exact), then
**drop** trailing — lowest-ranked — chunks one at a time. Compaction comes
first because text the model can still read beats a passage it can no longer
cite. Each rung warns on the diagnostics stream; a prompt that still overflows
with no chunks left is an error naming the knobs, and `--require-context`
aborts when the guard leaves no context at all. Citations reflect what
survived, so the `Sources:` footer never lists a passage the model did not see.

#### Output normalization

Long generations drift away from whatever shape `system.tmpl` asked for —
markdown lists come back, separators stop being emitted — and no prompt wording
fixes it. A template therefore *declares* the shape it needs and
`internal/normalize` enforces it on the completion:

```yaml
normalize:
  filters: [strip_preamble, strip_fences, strip_list_markers, collapse_blank_lines]
  records:
    separator: "----"
    fields: [lead, note, body]
```

```go
type Config struct {
    Filters []string       `yaml:"filters"`
    Records *RecordsConfig `yaml:"records"`
}

func (c Config) Declared() bool   // template asked for normalization
func (c Config) Validate() error  // unknown filter / empty separator
func Apply(raw string, cfg Config) (string, error)
func FilterNames() []string
```

`filters` are line-level cleanups run in the order listed (`strip_fences`,
`strip_headings`, `strip_list_markers`, `strip_preamble`, `collapse_blank_lines`,
`trim_trailing_space`). The optional `records` block is the one structured
primitive, and it carries no template-specific vocabulary: `fields` lists
positional line roles — `lead` (first line), optional `note` (a parenthesised
second line), `body` (the rest, one item per line). It starts with `lead`, ends
with `body`, and defaults to `[lead, body]`. The `note` field is emitted even
when empty, since dropping it shifts the first body line into the note.
Record boundaries come from the declared `separator` where the model still
emitted it — whatever follows one opens a record, lead-shaped or not — and
otherwise from a lead line (trailing `?`) after a blank line;
output with no question marks falls back to blank-line-delimited blocks.
Anything ahead of the first separator or question line is chatter ("Here are the
cards.") and is dropped — but only in that question-mark mode, since with no
questions anywhere there is nothing to separate an opening pleasantry from a
legitimate first lead. A
separator that is itself a markdown rule (`----`) also matches neighbouring
rules, since a model asked for four dashes often writes three or five; any other
separator splits on itself alone, so a rule sitting in the body stays content.

A declared `records` block also changes what else reaches stdout: the `Sources:`
footer goes to the diagnostics stream instead. `RunAsk` also owns the final
newline on every path now: it adds one only when the output does not already end
with one, so prose no longer gains a blank line and record output stays
byte-exact — an empty stream when there are no records, not a blank line. Templates
with only `filters` still produce prose and keep their citations inline.

`Manifest.Normalize` carries the config and `loadManifest` validates it, so a
misspelt filter or field name fails at template load rather than after a model
call. `RunAsk`
buffers the completion whenever a pipeline is declared — normalization rewrites
whole records, so those templates cannot stream. Templates that declare nothing
(`qa`, `brief`) are untouched, streaming included. The builtin `anki` template
declares the pipeline above; `anki` is the only template that calls its records
"cards", and that name lives in its prompt, not in the mechanism.

---

## Export

`tbuk export <path>` snapshots a knowledge base as a tar archive for backup or
transfer.

```go
// Create writes a tar of cfg's data to w: config.yaml (paths commented out) plus
// the database (+ SQLite -wal/-shm sidecars), extracted store, raw archive and
// prompt templates that exist. Entries are named relative to root, or by basename
// when the source lives outside root, so ".." never escapes the archive. Missing
// or disabled components (e.g. raw_dir="") are skipped without error.
func Create(w io.Writer, cfg config.Config, root string) error
```

Portable config: `config.ExportYAML(cfg)` marshals the config and comments out
the four data-folder path keys (`database.path`, `preprocess.output_dir`,
`ingest.raw_dir`, `prompts.dir`), also commenting the section header when doing
so would leave it empty — so the key is *absent* on reload rather than decoded as
a zero value. An import's `LoadForRoot` then re-homes every component under the
target root; portable settings (providers, models, chunk sizes) stay active.

CLI seams (`internal/cli/export.go`), exported for unit testing like
`RunDelete`/`ConfirmYes`:
- `ResolveExportTarget(arg, now)` — existing dir → `tbuk-export-<ts>.tar` inside
  it; existing file or non-existent path → used as-is; non-existent path with a
  trailing separator → error.
- `RunExport(in, out, cfg, root, target, force)` — prompts before overwriting an
  existing target unless `force`; writes to a temp file in the destination dir,
  re-reads it through `export.Verify` and only then renames into place
  (crash-safe, `0o600`).

Only regular files are archived: directories, sockets, devices and named pipes
are skipped, so a FIFO left in a data folder cannot block the export.

```go
// CheckComplete walks every header, checking each entry's declared extent — and
// the two zero blocks that terminate a tar — against the file's length. Returns
// how many entries are intact.
func CheckComplete(rs io.ReadSeeker) (int, error)

// Verify is CheckComplete plus a config.yaml entry; one walk, not two.
func Verify(rs io.ReadSeeker) error

var ErrIncomplete = errors.New("archive is truncated")
```

Both sit on one internal walk that seeks from header to header: it reads a
header block per entry and never touches a body, so cost is O(entries) rather
than O(archive size) — 256 MiB verifies in ~300µs. The seek is explicit rather
than left to `archive/tar` skipping unread bodies, and a byte-counting test pins
the bound.

The length comparison is the part a header walk cannot do on its own: a tar cut
at a block boundary — its terminating zero blocks gone — reads back as a clean
`io.EOF`, indistinguishable from a complete archive. Checking extents rather
than waiting for a read to fail also keeps the entry count honest: an entry
whose body is cut short is not counted among the intact ones, so the error can
name it. The streaming import path tracks the same thing by remembering whether
an entry's body was consumed, since `tar.Next` swallows an unread body on its
way to the next header.

## Import

`tbuk import` rebuilds a knowledge base from an archive on *this* machine's
terms. It takes from the archive what belongs to the user rather than to the
exporting machine:

- the source files under `raw/`, copied into the local `ingest.raw_dir`;
- the database, copied to a temp path and read as a **manifest** — each
  document's path, title, mime type, SHA256 and user metadata;
- the prompt templates under `prompts/`.

Three commands select among them: `tbuk import` (both), `tbuk import data`,
`tbuk import templates`, sharing one persistent flag set. `ImportOptions.Scope`
carries the choice, its zero value meaning everything.

Everything else is deliberately ignored. The config describes the machine it
came from (paths, providers, models), so adopting it is never right. The
archive's **embeddings are never read**: a vector produced by another model
cannot be searched here, and nothing in an archive proves which model made it —
matching `embedding.dimension` is not proof, and the failure mode is silent
nonsense rather than the loud dimension error. The extracted-text cache is
skipped because extraction is deterministic and re-derivable from the raw
bytes.

There is deliberately **no verbatim-restore command**. Its correctness would
depend on a condition tbuk cannot verify (source and target embedding models
being identical), failing silently when wrong. Rewinding one's own knowledge
base is `tar -xf backup.tar -C ~/.tbuk` — a plain file operation on a plain
uncompressed tar, no command needed.

```go
// Extract reads the tar in r, writing raw/ entries under opts.RawDir and the
// database (with its WAL/SHM sidecars) to opts.DBDest. Everything else is read
// past and discarded. Entries escaping via an absolute path or ".." are
// rejected; a raw copy already present is left alone, its name being
// content-addressed.
func Extract(r io.Reader, opts Options) (Result, error)

type Options struct {
    RawDir     string // destination for raw/ entries; empty writes none (dry run)
    DBDest     string // caller-owned temp path for the database; empty skips it
    PromptsDir string // destination for prompt templates; empty writes none
}

type Result struct {
    Written     []string // destinations actually written
    RawNames    []string // every raw entry the archive holds, written or not
    PromptNames []string // every template the archive holds, once each
    DBPath      string   // where the database landed; "" if the archive had none
}
```

The WAL/SHM sidecars matter: without them a manifest read can miss the most
recent commits.

Templates are unpacked into a scratch directory the CLI owns, never straight
into `cfg.Prompts.Dir` — which of them replace an installed template is policy,
and policy is the CLI's. A template is a directory below `prompts/`, so a loose
file directly under `prompts/` belongs to none and is left out.

CLI seam (`internal/cli/import.go`), exported for testing:
`RunImport(ctx, in, outW, errW, archivePath, cfg, root, configPath, opts, newIngester)`.

Templates run first: they are quick, cannot fail for want of a provider, and a
templates-only import therefore needs no reachable embedder, no usable
`ingest.raw_dir`, and no fresh-config confirmation (it spends nothing). Each is
decided per template *name* and installed by replacing the directory whole —
`replaceDir` removes the old one and copies, rather than renaming, since the
scratch directory is usually on another filesystem. Merging a manifest from one
machine with files from another would leave a template that may not load.

The built-ins (`builtinTemplateNames`) exist on every machine, so the archive's
copies always collide; under the default `skip` only custom templates travel. A
dry run against a config-less root counts them as present too — the real run
scaffolds before importing, so promising to install them would be a lie in
exactly the case a dry run matters most.

Per document, keyed on `documents.path` (its UNIQUE identity in the DB):

1. No raw copy in the archive (the source was ingested `--no-raw`) → reported
   and skipped; the run continues.
2. Already indexed at that path → `ImportOptions.OnConflict` decides:
   `skip` (default, so a repeat import is a no-op), `overwrite`, or `ask`.
3. Otherwise the row is created (or updated to the archive's identity), the
   document is embedded via `Ingester.ReindexDocument` — which resolves
   `raw/<sha256><ext>`, just written — and the manifest's user metadata is
   written last.

A failed embed **rolls back**: a row this run created is deleted, and a row it
overwrote gets its identity restored (its chunks were never replaced, since
`ReplaceForDocument` runs only after a successful embed). Without that, a
chunk-less row would be treated as "already indexed" by every later import and
the document would be stranded. Metadata is written after the embed for the
same reason; it cannot collide with the automatic keys, which `readManifest`
filters out (`filename`, `extension`, `mime`, `dir` are this machine's to
derive).

Importing into a root with no `config.yaml` calls `Scaffold` — init's setup,
factored out — then shows the embedding settings and confirms before spending
on them (`--yes` skips it, for scripts). An existing config is never touched or
confirmed against. `ingest.raw_dir` disabled is a hard error: imported sources
would have nowhere to live.

All prompts share one `bufio.Reader` for the whole run. A reader per prompt
buffers the answers meant for later documents and drops them, which makes
`--on-conflict ask` able to ask only once.

`Extract` runs `export.CheckComplete` first when its reader is an
`io.ReadSeeker` (as the CLI's `*os.File` is), so an incomplete archive is
refused before a single file is written. A non-seekable stream cannot establish
completeness up front and is read as it arrives.

Archive faults are named rather than surfaced raw: `Extract` keeps the first 512
bytes for format sniffing and reports an empty file, an entry-less archive, a
mid-entry truncation, a corrupt entry (with its index), or a non-tar file —
including the format it looks like (`gzip`, `zip`, `zstd`, `bzip2`, `xz`, a
SQLite database), or an archive shorter than the entries it declares. Failures
on the first entry are classified before the generic cases, since a file too
short to hold a tar header fails identically to a truncated archive.
`RunImport` prefixes the archive path.

Because the target may be a fresh machine, import works with no pre-existing
config — the root `PersistentPreRunE` loads `DefaultsForRoot(root)`, which
passes `Validate()`.

---

## CLI Commands (implemented)

```
tbuk init                      create ~/.tbuk/, write default config.yaml and prompts/
tbuk version                   print version string
tbuk doctor                    probe config, DB (with doc/chunk counts), LLM/embedding/search
tbuk preprocess <path>         extract text → save to extracted store (--dry-run, --output-dir)
tbuk ingest <path>             read extracted text → chunk → embed → store (--force, --verbose)
tbuk search <query>            search chunks; query read as an expression — "phrase", -exclude (--mode vector|keyword|hybrid, --top N, --min-score F, --format text|json)
tbuk find <key=value>...       find docs by metadata filters (--limit N, --format text|json)
tbuk meta set <path> k=v...    attach metadata key=value pairs to a document
tbuk meta list <path>          list all metadata for a document
tbuk ask <question>            RAG query: retrieve chunks → render template → stream LLM answer (--template qa, --var k=v, --top N, --no-stream)
tbuk template list             list prompt templates in ~/.tbuk/prompts/
tbuk template show <name>      print manifest + template files to stdout
tbuk template edit <name>      open manifest in $EDITOR
tbuk delete <path>             remove document + cascade-delete chunks/metadata + extracted-text cache (--yes skips prompt)
tbuk update <path>             re-ingest if SHA256 changed, skip otherwise (--force)
tbuk reindex                   re-embed every document from raw/<sha256><ext> under the local embedding config (--source-dir, --dry-run)
tbuk stats                     knowledge base summary: documents, chunks, embedded count, sizes (--format text|json)
tbuk list                      list indexed documents: path, title, chunk count, updated_at (--limit, --format text|json)
tbuk export <path>             tar snapshot of config + data folders (dir→timestamped file, file→as-is; --force, --root)
tbuk import <archive>          import an archive's documents and templates, indexed with the local config (--on-conflict skip|overwrite|ask, --dry-run, --yes)
tbuk import data <archive>     documents only (same flags)
tbuk import templates <archive>  prompt templates only; no embedding provider needed (same flags)
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
