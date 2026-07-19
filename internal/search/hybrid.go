package search

import (
	"context"
	"sort"
)

const rrfK = 60

// Hybrid runs vector + keyword search and fuses results with Reciprocal Rank Fusion.
func (s *Searcher) Hybrid(ctx context.Context, query string, opts Options) ([]SearchResult, error) {
	expanded := Options{TopK: opts.topK() * 2, MinScore: 0}

	vecResults, err := s.Vector(ctx, query, expanded)
	if err != nil {
		return nil, err
	}
	kwResults, err := s.Keyword(ctx, query, expanded)
	if err != nil {
		return nil, err
	}

	type entry struct {
		result SearchResult
		rrf    float64
	}
	scores := map[int64]*entry{}

	mergeRank := func(results []SearchResult) {
		for rank, r := range results {
			if e, ok := scores[r.ChunkID]; ok {
				e.rrf += 1.0 / float64(rrfK+rank+1)
			} else {
				cp := r
				scores[r.ChunkID] = &entry{result: cp, rrf: 1.0 / float64(rrfK+rank+1)}
			}
		}
	}
	mergeRank(vecResults)
	mergeRank(kwResults)

	fused := make([]*entry, 0, len(scores))
	for _, e := range scores {
		fused = append(fused, e)
	}
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].rrf > fused[j].rrf
	})

	// Apply MinScore to the fused RRF scores. Note these are RRF sums
	// (1/(k+rank) across legs), not cosine values, so a hybrid MinScore is on a
	// different scale from vector search.
	if opts.MinScore > 0 {
		kept := make([]*entry, 0, len(fused))
		for _, e := range fused {
			if e.rrf >= opts.MinScore {
				kept = append(kept, e)
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
		r := fused[i].result
		r.Score = fused[i].rrf
		r.Source = "hybrid"
		out[i] = r
	}
	return out, nil
}
