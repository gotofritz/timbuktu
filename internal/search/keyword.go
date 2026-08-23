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

	joinSQL, metaArgs := metadataFilterJoins(opts.Metadata, "d")
	args := append(metaArgs, match, opts.topK())
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.document_id, c.chunk_index, c.text,
		       bm25(chunks_fts) AS bm25score,
		       d.path, d.title
		FROM chunks_fts
		JOIN chunks   c ON chunks_fts.rowid = c.id
		JOIN documents d ON d.id = c.document_id `+joinSQL+`
		WHERE chunks_fts MATCH ?
		ORDER BY bm25score
		LIMIT ?`, args...)
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

// stopWords are the English function words a question is mostly made of. They
// are dropped from a MATCH expression because they carry no signal about which
// chunk is wanted: BM25 discounts them by IDF, but only once the corpus is big
// enough for them to be common in it, and a personal knowledge base of a few
// dozen documents is not. Removing them makes ranking depend on the terms the
// user actually meant, whatever the corpus size.
var stopWords = map[string]bool{
	"a": true, "about": true, "all": true, "am": true, "an": true, "and": true,
	"any": true, "are": true, "as": true, "at": true, "be": true, "been": true,
	"but": true, "by": true, "can": true, "did": true, "do": true, "does": true,
	"for": true, "from": true, "had": true, "has": true, "have": true, "how": true,
	"i": true, "if": true, "in": true, "is": true, "it": true, "its": true,
	"know": true, "me": true, "my": true, "no": true, "not": true, "of": true,
	"on": true, "or": true, "so": true, "some": true, "tell": true, "that": true,
	"the": true, "their": true, "them": true, "then": true, "there": true,
	"these": true, "they": true, "this": true, "to": true, "was": true, "we": true,
	"were": true, "what": true, "when": true, "where": true, "which": true,
	"who": true, "why": true, "will": true, "with": true, "would": true,
	"you": true, "your": true,
}

// sanitizeFTS5Query neutralises FTS5 operators so arbitrary user input is a
// valid MATCH expression: each whitespace-separated term becomes a
// double-quoted phrase (embedded quotes doubled). Returns "" for input with no
// terms.
//
// Terms are OR-combined rather than left to FTS5's implicit AND. A question
// ("what do you know about PPAs") carries words no single chunk has to contain,
// so an AND match returned nothing at all for exactly the queries `tbuk ask`
// sends — and a Hybrid whose keyword leg is always empty is vector search
// wearing a different name. Recall is what this leg is for; BM25 ranking and
// TopK are what keep it precise.
//
// Stop words are dropped first, so what is OR-combined is the content of the
// query. A query of nothing but stop words keeps them all rather than matching
// everything or nothing.
func sanitizeFTS5Query(query string) string {
	fields := strings.Fields(query)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if !stopWords[strings.ToLower(f)] {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		kept = fields
	}

	quoted := make([]string, 0, len(kept))
	for _, f := range kept {
		quoted = append(quoted, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}
