# Subplan 07: Search

## Goal

Implement all three search modes — vector (cosine similarity), keyword (FTS5),
and metadata filter — plus a hybrid combiner. Expose via `tbuk search` and
`tbuk find` CLI commands.

## Deliverables

- `Searcher` struct with vector, keyword, metadata, and hybrid methods
- Cosine similarity computed in Go (no C extension required for MVP)
- FTS5 keyword search using the existing virtual table
- Metadata filter search (`key=value` pairs)
- Hybrid scorer: Reciprocal Rank Fusion (RRF) over vector + FTS results
- CLI: `tbuk search <query>` and `tbuk find <key=value>...`
- Unit tests with in-memory SQLite

## Package Layout

```
internal/search/
  search.go         ← Searcher, SearchResult, SearchOptions
  vector.go         ← cosine similarity, vector search query
  keyword.go        ← FTS5 query builder
  metadata.go       ← metadata filter query
  hybrid.go         ← RRF combiner
  search_test.go
```

## Types

```go
type SearchResult struct {
    ChunkID    int64
    DocumentID int64
    Path       string
    Title      string
    ChunkIndex int
    Text       string
    Score      float64 // higher is better (normalised 0-1 for display)
    Source     string  // "vector" | "keyword" | "hybrid"
}

type SearchOptions struct {
    TopK        int     // default 5
    MinScore    float64 // filter results below threshold (default 0)
    Metadata    map[string]string // AND-combined metadata filters
}

type Searcher struct {
    db       *sql.DB
    chunks   *storage.ChunkRepo
    meta     *storage.MetadataRepo
    embedder embeddings.Embedder
}
```

## Vector Search

```go
func (s *Searcher) Vector(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
```

1. `embedder.Embed([]string{query})` → query vector
2. `SELECT id, document_id, chunk_index, text, embedding FROM chunks WHERE embedding IS NOT NULL`
3. Deserialise each embedding BLOB → `[]float32`
4. Compute cosine similarity in Go
5. Sort descending, take top K
6. Join with `documents` for path/title

Note: full table scan is acceptable for POC (< 100k chunks). sqlite-vec can be
swapped in later by replacing this method without changing the interface.

## Keyword Search

```go
func (s *Searcher) Keyword(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
```

```sql
SELECT c.id, c.document_id, c.chunk_index, c.text,
       bm25(chunks_fts) AS score
FROM chunks_fts
JOIN chunks c ON chunks_fts.rowid = c.id
JOIN documents d ON c.document_id = d.id
WHERE chunks_fts MATCH ?
ORDER BY score
LIMIT ?
```

FTS5 BM25 score is negative (lower = better); negate for consistent ordering.

## Metadata Search

```go
func (s *Searcher) Metadata(ctx context.Context, filters map[string]string) ([]SearchResult, error)
```

AND-combines filters:
```sql
SELECT DISTINCT d.id, d.path, d.title
FROM documents d
JOIN metadata m1 ON m1.document_id = d.id AND m1.key = ? AND m1.value = ?
JOIN metadata m2 ON m2.document_id = d.id AND m2.key = ? AND m2.value = ?
...
```

Returns all chunks from matching documents (no score, just presence).

## Hybrid Search (RRF)

```go
func (s *Searcher) Hybrid(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
```

Reciprocal Rank Fusion:
```
RRF_score(d) = Σ 1 / (k + rank_i(d))   where k=60
```

1. Run `Vector(...)` with `TopK=opts.TopK*2`
2. Run `Keyword(...)` with `TopK=opts.TopK*2`
3. Union results by ChunkID
4. Assign RRF score, re-rank, take top K

## CLI

```
tbuk search <query> [--mode vector|keyword|hybrid] [--top 5] [--min-score 0.3]
tbuk find <key=value>... [--limit 20]
```

Output (default text):
```
[1] score=0.87  /docs/README.md §2
    "Authentication uses JWT tokens signed with RS256..."

[2] score=0.81  /docs/ARCHITECTURE.md §5
    "The auth middleware validates the token on each request..."
```

`--format json` outputs a JSON array for piping.

## Tests

- `TestVectorSearch_returnTopK` — 10 chunks stored, query returns correct top-5
- `TestVectorSearch_noEmbeddings` — empty DB → empty results, no error
- `TestKeywordSearch_match` — FTS match returns chunk with positive score
- `TestKeywordSearch_noMatch` — no results for nonsense query
- `TestMetadataSearch_singleFilter` — one k=v returns matching docs
- `TestMetadataSearch_multiFilter` — AND logic: only docs with both keys
- `TestHybridSearch_combinesResults` — chunk ranked high by both gets boosted

## Dependencies

No new packages (stdlib `math` for cosine, `database/sql` already present).

## PR Scope

One PR. Depends on Subplan 02 (storage) and Subplan 04 (embeddings).

## Doctor

Add to `tbuk doctor` output:

```
Search
  fts5:        ✓ available
  vector:      ✓ available (cosine, in-process)
  hybrid:      ✓ available (RRF)
```

Run a trivial FTS5 query against `chunks_fts` to confirm the index is intact.
Report which search modes are compiled in.
