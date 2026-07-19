package search

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/gotofritz/timbuktu/internal/storage"
)

// Vector embeds query and returns top-K chunks by cosine similarity.
func (s *Searcher) Vector(ctx context.Context, query string, opts Options) ([]SearchResult, error) {
	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	queryVec := vecs[0]

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.document_id, c.chunk_index, c.text, c.embedding,
		       d.path, d.title
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE c.embedding IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	type scored struct {
		r     SearchResult
		score float64
	}
	var candidates []scored

	for rows.Next() {
		var r SearchResult
		var blob []byte
		if err := rows.Scan(&r.ChunkID, &r.DocumentID, &r.ChunkIndex, &r.Text, &blob, &r.Path, &r.Title); err != nil {
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
		if score >= opts.MinScore {
			r.Score = score
			r.Source = "vector"
			candidates = append(candidates, scored{r: r, score: score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	k := opts.topK()
	if k > len(candidates) {
		k = len(candidates)
	}
	out := make([]SearchResult, k)
	for i := range out {
		out[i] = candidates[i].r
	}
	return out, nil
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
