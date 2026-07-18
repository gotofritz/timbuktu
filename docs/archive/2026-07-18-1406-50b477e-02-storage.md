# Subplan 02: Storage Layer

## Goal

Implement the SQLite persistence layer: schema, migrations, and typed CRUD
repositories for documents, chunks, and metadata. No ingestion logic here —
just the data access layer that all other subplans depend on.

## Deliverables

- SQLite connection helper (open, WAL mode, foreign keys, timeouts)
- Schema migration runner (versioned, forward-only)
- `DocumentRepo` with typed CRUD + existence check
- `ChunkRepo` with bulk insert and delete-by-document
- `MetadataRepo` with key/value get/set/delete per document
- FTS5 virtual table wired to `chunks.text`
- Embedding BLOB column on `chunks` (raw `[]float32` → little-endian bytes)
- Unit tests with in-memory SQLite (`:memory:`)

## Schema

```sql
-- Migration 001
CREATE TABLE IF NOT EXISTS schema_migrations (
    version   INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS documents (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    path        TEXT    NOT NULL UNIQUE,
    sha256      TEXT    NOT NULL,
    title       TEXT    NOT NULL DEFAULT '',
    mime_type   TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS chunks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id   INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index   INTEGER NOT NULL,
    text          TEXT    NOT NULL,
    token_count   INTEGER NOT NULL DEFAULT 0,
    embedding     BLOB,
    UNIQUE(document_id, chunk_index)
);

CREATE TABLE IF NOT EXISTS metadata (
    document_id  INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    key          TEXT    NOT NULL,
    value        TEXT    NOT NULL,
    PRIMARY KEY (document_id, key)
);

-- FTS5 index over chunk text
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    text,
    content='chunks',
    content_rowid='id'
);

-- Triggers to keep FTS5 in sync
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
    INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;
```

## Package Layout

```
internal/storage/
  db.go           ← Open(), pragmas, Close()
  migrate.go      ← RunMigrations(), migration list
  document.go     ← DocumentRepo struct + methods
  chunk.go        ← ChunkRepo struct + methods
  metadata.go     ← MetadataRepo struct + methods
  embed.go        ← Float32SliceToBlob() / BlobToFloat32Slice() helpers
  storage_test.go ← all tests (in-memory DB)
```

## Key Types

```go
type Document struct {
    ID        int64
    Path      string
    SHA256    string
    Title     string
    MimeType  string
    CreatedAt time.Time
    UpdatedAt time.Time
}

type Chunk struct {
    ID          int64
    DocumentID  int64
    ChunkIndex  int
    Text        string
    TokenCount  int
    Embedding   []float32 // nil if not yet embedded
}

type Metadata struct {
    DocumentID int64
    Key        string
    Value      string
}
```

## Repository Interface Pattern

Each repo takes `*sql.DB` in its constructor. No global state.

```go
type DocumentRepo struct { db *sql.DB }
func NewDocumentRepo(db *sql.DB) *DocumentRepo

func (r *DocumentRepo) Create(ctx, doc *Document) error
func (r *DocumentRepo) GetByPath(ctx, path string) (*Document, error)
func (r *DocumentRepo) GetBySHA256(ctx, sha string) (*Document, error)
func (r *DocumentRepo) Update(ctx, doc *Document) error
func (r *DocumentRepo) Delete(ctx, id int64) error
func (r *DocumentRepo) List(ctx) ([]*Document, error)
```

## Dependencies

| Package | Version | Reason |
|---------|---------|--------|
| `modernc.org/sqlite` | latest | Pure-Go SQLite, no CGo |

## Tests

- `TestMigrations_idempotent` — run twice, no error
- `TestDocumentRepo_CRUD` — create / get / update / delete round-trip
- `TestDocumentRepo_GetBySHA256` — dedup check
- `TestChunkRepo_BulkInsert` — insert N chunks, read back
- `TestChunkRepo_DeleteByDocument` — cascades to FTS
- `TestMetadataRepo_SetGet` — key/value round-trip
- `TestEmbedRoundtrip` — float32 slice → BLOB → float32 slice

## PR Scope

One PR. Depends on Subplan 01 (go.mod + config). No ingestion, no HTTP.

## Doctor

No additions in this subplan. `tbuk doctor` is introduced in the foundation
layer (subplan 01 follow-up) and already reports config, database, and LLM
connectivity.
