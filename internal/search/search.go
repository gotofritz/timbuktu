package search

import (
	"context"
	"database/sql"

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
	TopK     int            // default 5
	MinScore float64        // skip results below this threshold
	Metadata map[string]string // AND-combined metadata pre-filter, applied by Vector/Keyword/Hybrid
}

func (o *Options) topK() int {
	if o == nil || o.TopK <= 0 {
		return 5
	}
	return o.TopK
}

// Searcher runs vector, keyword, metadata, and hybrid searches.
type Searcher struct {
	db      *sql.DB
	embedder embeddings.Embedder
}

// New returns a Searcher. embedder may be nil if only keyword/metadata search is used.
func New(db *sql.DB, emb embeddings.Embedder) *Searcher {
	return &Searcher{db: db, embedder: emb}
}

// CheckFTS5 runs a trivial FTS5 query to verify the index is intact.
func CheckFTS5(db *sql.DB) error {
	_, err := db.QueryContext(context.Background(), `SELECT rowid FROM chunks_fts LIMIT 1`)
	if err != nil {
		return err
	}
	return nil
}
