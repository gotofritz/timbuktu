# Subplan 06: Ingestion Pipeline

## Goal

Wire preprocessing, chunking, embedding, and storage into a single `Ingester`
that processes one document at a time. Implement `tbuk ingest <path|dir>` CLI
command. Handle deduplication via SHA256.

## Deliverables

- `Ingester` struct that orchestrates the full pipeline
- SHA256-based duplicate detection (skip unchanged, re-index changed)
- Lineage: document → extracted text → chunks → embeddings all stored
- Progress output (file processed, chunks embedded, skipped)
- `tbuk ingest <path>` and `tbuk ingest <dir>` commands
- Unit tests with mocked embedder and in-memory storage

## Package Layout

```
internal/ingest/
  ingester.go       ← Ingester struct, IngestFile(), IngestDir()
  progress.go       ← progress writer (io.Writer-based, testable)
  ingest_test.go
```

## Ingester

```go
type Options struct {
    Force bool   // re-ingest even if SHA256 unchanged
}

type Result struct {
    Path    string
    Skipped bool   // SHA256 unchanged
    Chunks  int
    Err     error
}

type Ingester struct {
    docs      *storage.DocumentRepo
    chunks    *storage.ChunkRepo
    meta      *storage.MetadataRepo
    extractor preprocess.Extractor  // dispatcher
    chunker   *chunking.Chunker
    embedder  embeddings.Embedder
    progress  io.Writer
}

func NewIngester(...) *Ingester

func (ing *Ingester) IngestFile(ctx context.Context, path string, opts Options) Result
func (ing *Ingester) IngestDir(ctx context.Context, dir string, opts Options) []Result
```

## Pipeline per File

1. `HashFile(path)` → SHA256
2. `docs.GetByPath(path)`:
   - Found + same SHA256 + `!opts.Force` → return `Result{Skipped: true}`
   - Found + different SHA256 → delete old chunks/metadata, continue
   - Not found → create new document record
3. `extractor.Extract(file)` → plain text
4. `chunker.Split(text)` → `[]Chunk`
5. For each chunk batch (size 16):
   - `embedder.Embed(texts)` → vectors
   - Store chunks with embeddings
6. Store/update document record (`sha256`, `updated_at`)
7. Return `Result{Chunks: len(chunks)}`

## Dir Walk

`IngestDir` walks the directory recursively, filters by supported extensions
(`.md`, `.txt`, `.pdf`, `.html`, `.htm`), calls `IngestFile` on each.
Unsupported files are skipped silently (logged to progress writer at debug
level if `--verbose` flag set).

## CLI

```
tbuk ingest <path> [--force] [--verbose]
tbuk ingest <dir>  [--force] [--verbose] [--ext md,txt,pdf]
```

Progress output:
```
[1/3] /docs/README.md     → 4 chunks embedded
[2/3] /docs/ARCHITECTURE.md → skipped (unchanged)
[3/3] /docs/api.pdf       → 12 chunks embedded
Done: 2 ingested, 1 skipped, 0 errors
```

## Error Handling

- Extractor failure → log error, continue to next file (non-fatal)
- Embed error → abort file, roll back any partial chunk inserts (transaction)
- Storage error → return error up to CLI for display

Each file is wrapped in a single DB transaction: all chunks committed together
or none.

## Tests

- `TestIngester_newFile` — ingests text file, chunks stored with embeddings
- `TestIngester_skipUnchanged` — same SHA256 → Result.Skipped=true
- `TestIngester_reindexChanged` — new SHA256 → old chunks deleted, new chunks stored
- `TestIngester_forceFlag` — Force=true re-ingests even if unchanged
- `TestIngester_extractorError` — extractor fails → Result.Err set, no partial data
- `TestIngester_dirWalk` — walks dir, processes supported files, skips others

## Dependencies

Depends on: storage (02), preprocessing (03), embeddings (04). No new packages.

## PR Scope

One PR. First PR that wires multiple subplans together.

## Doctor

Add to `tbuk doctor` output:

```
Database
  path:        ~/.tbuk/tbuk.sqlite
  status:      ✓ open
  documents:   12
  chunks:      347
```

Query `DocumentRepo.List` and `ChunkRepo` counts and display them.
