package search

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gotofritz/timbuktu/internal/embeddings"
)

// SearchResult is one chunk returned by a search.
type SearchResult struct {
	ChunkID    int64
	DocumentID int64
	Path       string
	Title      string
	ChunkIndex int
	Text       string
	Score      float64 // higher is better (0-1 normalised for vector/hybrid)
	Source     string  // "vector" | "keyword" | "hybrid"
}

// Options controls search behaviour.
type Options struct {
	TopK     int               // default 5
	MinScore float64           // skip results below this threshold
	Metadata map[string]string // AND-combined metadata pre-filter, applied by Vector/Keyword/Hybrid

	// Operators reads the query as an expression rather than as a bag of
	// words: a quoted run is a phrase, and a leading '-' excludes. `tbuk
	// search` sets it, because a person typing punctuation means it. `tbuk
	// ask` does not: it sends a natural-language question, where the lenient
	// reading is what keeps the keyword leg from coming back empty.
	Operators bool
}

// DefaultTopK is how many results a search returns when the caller names no
// number of its own. Exported because multi-query retrieval truncates the fused
// list to the same default, and two spellings of "how many is normal" is one
// too many.
const DefaultTopK = 5

func (o *Options) topK() int {
	if o == nil || o.TopK <= 0 {
		return DefaultTopK
	}
	return o.TopK
}

// Searcher runs vector, keyword, metadata, and hybrid searches.
type Searcher struct {
	db       *sql.DB
	embedder embeddings.Embedder
}

// New returns a Searcher. embedder may be nil if only keyword/metadata search is used.
func New(db *sql.DB, emb embeddings.Embedder) *Searcher {
	return &Searcher{db: db, embedder: emb}
}

// CheckFTS5 runs a trivial FTS5 query to verify the index is intact.
// QueryRowContext scans (and so closes) the single row, releasing the pooled
// connection; an empty index (no rows) is healthy, so sql.ErrNoRows is tolerated.
func CheckFTS5(db *sql.DB) error {
	var rowid int64
	err := db.QueryRowContext(context.Background(), `SELECT rowid FROM chunks_fts LIMIT 1`).Scan(&rowid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}
