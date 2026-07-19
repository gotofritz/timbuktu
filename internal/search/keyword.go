package search

import (
	"context"
	"fmt"
	"strings"
)

// Keyword runs an FTS5 BM25 search and returns top-K results.
func (s *Searcher) Keyword(ctx context.Context, query string, opts Options) ([]SearchResult, error) {
	match := sanitizeFTS5Query(query)
	if match == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.document_id, c.chunk_index, c.text,
		       bm25(chunks_fts) AS bm25score,
		       d.path, d.title
		FROM chunks_fts
		JOIN chunks   c ON chunks_fts.rowid = c.id
		JOIN documents d ON d.id = c.document_id
		WHERE chunks_fts MATCH ?
		ORDER BY bm25score
		LIMIT ?`, match, opts.topK())
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
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

// sanitizeFTS5Query neutralises FTS5 operators so arbitrary user input is a
// valid MATCH expression: each whitespace-separated term becomes a
// double-quoted phrase (embedded quotes doubled). Space-separated phrases are
// implicitly AND-combined by FTS5. Returns "" for input with no terms.
func sanitizeFTS5Query(query string) string {
	fields := strings.Fields(query)
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		quoted = append(quoted, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " ")
}
