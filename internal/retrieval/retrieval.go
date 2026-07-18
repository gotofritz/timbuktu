package retrieval

import (
	"context"
	"fmt"

	"github.com/gotofritz/timbuktu/internal/search"
)

// RetrievedChunk is a search result enriched with a citation string.
type RetrievedChunk struct {
	ChunkID    int64
	DocumentID int64
	Path       string
	Title      string
	ChunkIndex int
	Text       string
	Score      float64
	Citation   string // "path §chunkIndex"
}

// HybridSearcher is the subset of search.Searcher used by Retriever.
type HybridSearcher interface {
	Hybrid(ctx context.Context, query string, opts search.Options) ([]search.SearchResult, error)
}

// Retriever runs hybrid search and formats results with citations.
type Retriever struct {
	searcher HybridSearcher
}

// New returns a Retriever backed by the given searcher.
func New(s HybridSearcher) *Retriever {
	return &Retriever{searcher: s}
}

// Retrieve runs a hybrid search and returns chunks with citations.
func (r *Retriever) Retrieve(ctx context.Context, query string, topK int, meta map[string]string) ([]RetrievedChunk, error) {
	opts := search.Options{TopK: topK, Metadata: meta}
	results, err := r.searcher.Hybrid(ctx, query, opts)
	if err != nil {
		return nil, fmt.Errorf("retrieval: hybrid search: %w", err)
	}
	chunks := make([]RetrievedChunk, len(results))
	for i, sr := range results {
		chunks[i] = RetrievedChunk{
			ChunkID:    sr.ChunkID,
			DocumentID: sr.DocumentID,
			Path:       sr.Path,
			Title:      sr.Title,
			ChunkIndex: sr.ChunkIndex,
			Text:       sr.Text,
			Score:      sr.Score,
			Citation:   fmt.Sprintf("%s §%d", sr.Path, sr.ChunkIndex),
		}
	}
	return chunks, nil
}
