package search

import (
	"context"
)

// Keyword runs an FTS5 BM25 search and returns top-K results.
func (s *Searcher) Keyword(ctx context.Context, query string, opts Options) ([]SearchResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.document_id, c.chunk_index, c.text,
		       bm25(chunks_fts) AS bm25score,
		       d.path, d.title
		FROM chunks_fts
		JOIN chunks   c ON chunks_fts.rowid = c.id
		JOIN documents d ON d.id = c.document_id
		WHERE chunks_fts MATCH ?
		ORDER BY bm25score
		LIMIT ?`, query, opts.topK())
	if err != nil {
		// FTS5 returns an error for no-match queries with special tokens;
		// treat as empty result set rather than propagating.
		return nil, nil //nolint:nilerr
	}
	defer func() { _ = rows.Close() }()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var bm25 float64
		if err := rows.Scan(&r.ChunkID, &r.DocumentID, &r.ChunkIndex, &r.Text, &bm25, &r.Path, &r.Title); err != nil {
			return nil, err
		}
		r.Score = -bm25 // BM25 in SQLite FTS5 is negative; negate for consistent "higher=better"
		r.Source = "keyword"
		results = append(results, r)
	}
	return results, rows.Err()
}
