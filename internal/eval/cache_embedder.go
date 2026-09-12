package eval

import (
	"context"
	"fmt"
)

// CacheEmbedder is a test embedder that returns precomputed vectors from a cache.
// It panics on a cache miss rather than falling back, so frozen vector files stay honest.
type CacheEmbedder struct {
	vectors   map[string][]float32
	dimension int
}

// NewCacheEmbedder returns an embedder backed by precomputed vectors.
// vectors maps sha256(text) -> embedding vector.
func NewCacheEmbedder(vectors map[string][]float32, dimension int) *CacheEmbedder {
	return &CacheEmbedder{
		vectors:   vectors,
		dimension: dimension,
	}
}

// Embed looks up precomputed vectors by sha256 of each text.
// A cache miss is a fatal error to keep the corpus in sync.
func (c *CacheEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		key := VectorKey(text)
		vec, ok := c.vectors[key]
		if !ok {
			return nil, fmt.Errorf("cache embedder: no vector for %s (text: %q)", key, text)
		}
		result[i] = vec
	}
	return result, nil
}

// Dimension returns the embedding vector dimension.
func (c *CacheEmbedder) Dimension() int {
	return c.dimension
}
