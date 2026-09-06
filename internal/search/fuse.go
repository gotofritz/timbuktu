package search

import "sort"

// RRFK is the rank constant in Reciprocal Rank Fusion's 1/(k+rank). At 60 —
// the value the original paper settles on — the gap between rank 1 and rank 2
// is small enough that two lists agreeing on a chunk outweigh one list ranking
// it highest, which is the property the whole scheme is for.
const RRFK = 60

// FuseRRF merges ranked lists with Reciprocal Rank Fusion: every list scores
// each of its results 1/(k+rank), and a chunk's score is the sum across the
// lists it appears in. Results are de-duplicated by chunk id and returned best
// first.
//
// It is exported because three callers fuse: the hybrid search's vector and
// keyword legs, multi-query retrieval, and (later) multi-hop. One implementation
// means one ranking, and one place to change it.
//
// Everything but Score comes from the first list that held the chunk; the score
// is fusion's own, since it is what the caller filters and truncates on. Ties
// are broken by chunk id so the same lists always fuse to the same order — a
// ranked result that reshuffles between identical runs would be truncated
// differently by TopK each time.
func FuseRRF(lists [][]SearchResult, k int) []SearchResult {
	type entry struct {
		result SearchResult
		rrf    float64
	}
	scores := map[int64]*entry{}
	for _, list := range lists {
		for rank, r := range list {
			weight := 1.0 / float64(k+rank+1)
			if e, ok := scores[r.ChunkID]; ok {
				e.rrf += weight
				continue
			}
			scores[r.ChunkID] = &entry{result: r, rrf: weight}
		}
	}

	fused := make([]*entry, 0, len(scores))
	for _, e := range scores {
		fused = append(fused, e)
	}
	sort.Slice(fused, func(i, j int) bool {
		if fused[i].rrf != fused[j].rrf {
			return fused[i].rrf > fused[j].rrf
		}
		return fused[i].result.ChunkID < fused[j].result.ChunkID
	})

	out := make([]SearchResult, len(fused))
	for i, e := range fused {
		out[i] = e.result
		out[i].Score = e.rrf
	}
	return out
}
