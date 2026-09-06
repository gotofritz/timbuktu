package search

import (
	"context"
	"fmt"
	"strings"
)

// Hybrid runs vector + keyword search and fuses results with Reciprocal Rank Fusion.
func (s *Searcher) Hybrid(ctx context.Context, query string, opts Options) ([]SearchResult, error) {
	expanded := Options{TopK: opts.topK() * 2, MinScore: 0, Metadata: opts.Metadata, Operators: opts.Operators}

	vecResults, err := s.Vector(ctx, query, expanded)
	if err != nil {
		return nil, err
	}
	kwResults, err := s.Keyword(ctx, query, expanded)
	if err != nil {
		return nil, err
	}

	fused := FuseRRF([][]SearchResult{vecResults, kwResults}, RRFK)

	// The keyword leg already dropped the excluded chunks, but the vector leg
	// knows nothing about NOT, so fusion hands them back. Apply the exclusions
	// to the fused set — before MinScore and TopK, so an excluded chunk does
	// not spend one of the K slots.
	if opts.Operators {
		if neg := parseQuery(query, true).negativeMatch(); neg != "" {
			ids := make([]int64, len(fused))
			for i, r := range fused {
				ids[i] = r.ChunkID
			}
			excluded, err := s.excludedChunkIDs(ctx, neg, ids)
			if err != nil {
				return nil, err
			}
			kept := fused[:0]
			for _, r := range fused {
				if !excluded[r.ChunkID] {
					kept = append(kept, r)
				}
			}
			fused = kept
		}
	}

	// Apply MinScore to the fused RRF scores. Note these are RRF sums
	// (1/(k+rank) across legs), not cosine values, so a hybrid MinScore is on a
	// different scale from vector search.
	if opts.MinScore > 0 {
		kept := make([]SearchResult, 0, len(fused))
		for _, r := range fused {
			if r.Score >= opts.MinScore {
				kept = append(kept, r)
			}
		}
		fused = kept
	}

	k := opts.topK()
	if k > len(fused) {
		k = len(fused)
	}
	out := make([]SearchResult, k)
	for i := range out {
		r := fused[i]
		r.Source = "hybrid"
		out[i] = r
	}
	return out, nil
}

// excludedChunkIDs returns which of ids match the exclusion expression, asking
// the same index the keyword leg used so both legs agree on what a term means.
func (s *Searcher) excludedChunkIDs(ctx context.Context, negMatch string, ids []int64) (map[int64]bool, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, negMatch)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT rowid FROM chunks_fts WHERE chunks_fts MATCH ? AND rowid IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: apply exclusions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	excluded := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		excluded[id] = true
	}
	return excluded, rows.Err()
}
