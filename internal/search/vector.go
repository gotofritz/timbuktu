package search

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/gotofritz/timbuktu/internal/storage"
)

// Vector embeds query and returns the top-K chunks by cosine similarity.
//
// It runs in two phases so peak memory is O(K), not O(corpus). The first pass
// scans only (id, embedding) — never the chunk text — and keeps a bounded
// min-heap of the K best-scoring chunk ids (O(n log K) time, O(K) memory). The
// second pass hydrates text/path/title for just those K ids. The old single
// pass selected every chunk's full text and appended every above-threshold row
// before sorting all n, so peak memory grew with the whole corpus.
func (s *Searcher) Vector(ctx context.Context, query string, opts Options) ([]SearchResult, error) {
	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	queryVec := vecs[0]

	ranked, err := s.rankVector(ctx, queryVec, opts)
	if err != nil {
		return nil, err
	}
	return s.hydrateVector(ctx, ranked)
}

// scoredID is a chunk id with its cosine score, ranked in phase one.
type scoredID struct {
	id    int64
	score float64
}

// rankVector scans (id, embedding) for all embedded chunks matching the
// metadata filter and returns the top-K by cosine score, highest first.
func (s *Searcher) rankVector(ctx context.Context, queryVec []float32, opts Options) ([]scoredID, error) {
	joinSQL, args := metadataFilterJoins(opts.Metadata, "d")
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.embedding
		FROM chunks c
		JOIN documents d ON d.id = c.document_id `+joinSQL+`
		WHERE c.embedding IS NOT NULL`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	k := opts.topK()
	h := &minScoreHeap{}
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		emb, err := storage.BlobToFloat32Slice(blob)
		if err != nil {
			continue
		}
		if len(emb) != len(queryVec) {
			// A stored vector whose dimension differs from the query embedding
			// means the embedding model or config changed since ingest. Cosine
			// similarity would silently score every such chunk 0, so fail loud.
			return nil, fmt.Errorf(
				"vector search: query embedding has %d dimensions but stored vectors have %d — "+
					"the embedding model/config changed since ingest; re-ingest the corpus or "+
					"restore the previous embedding configuration",
				len(queryVec), len(emb))
		}
		score := cosineSimilarity(queryVec, emb)
		if score < opts.MinScore {
			continue
		}
		// Keep only the K highest scores: push until full, then replace the
		// current minimum whenever a better score arrives.
		if h.Len() < k {
			heap.Push(h, scoredID{id: id, score: score})
		} else if score > (*h)[0].score {
			(*h)[0] = scoredID{id: id, score: score}
			heap.Fix(h, 0)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ranked := make([]scoredID, h.Len())
	copy(ranked, *h)
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	return ranked, nil
}

// hydrateVector fetches text/path/title for the ranked chunk ids and returns
// them as SearchResults in the ranked order.
func (s *Searcher) hydrateVector(ctx context.Context, ranked []scoredID) ([]SearchResult, error) {
	if len(ranked) == 0 {
		return []SearchResult{}, nil
	}

	placeholders := make([]string, len(ranked))
	args := make([]any, len(ranked))
	for i, sc := range ranked {
		placeholders[i] = "?"
		args[i] = sc.id
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.document_id, c.chunk_index, c.text, d.path, d.title
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE c.id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	byID := make(map[int64]SearchResult, len(ranked))
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ChunkID, &r.DocumentID, &r.ChunkIndex, &r.Text, &r.Path, &r.Title); err != nil {
			return nil, err
		}
		r.Source = "vector"
		byID[r.ChunkID] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]SearchResult, 0, len(ranked))
	for _, sc := range ranked {
		r, ok := byID[sc.id]
		if !ok {
			continue // chunk vanished between the two queries; skip it
		}
		r.Score = sc.score
		out = append(out, r)
	}
	return out, nil
}

// minScoreHeap is a min-heap of scoredID ordered by score, so the root is the
// current lowest-scoring candidate and can be evicted when a better one arrives.
type minScoreHeap []scoredID

func (h minScoreHeap) Len() int            { return len(h) }
func (h minScoreHeap) Less(i, j int) bool  { return h[i].score < h[j].score }
func (h minScoreHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *minScoreHeap) Push(x any)         { *h = append(*h, x.(scoredID)) }
func (h *minScoreHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		normA += fa * fa
		normB += fb * fb
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
