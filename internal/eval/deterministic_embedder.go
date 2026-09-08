package eval

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
)

// DeterministicEmbedder generates embeddings deterministically from text.
// For testing and as a stopgap before frozen real vectors are recorded.
// It uses sha256 of text to seed a deterministic RNG.
type DeterministicEmbedder struct {
	dimension int
}

// NewDeterministicEmbedder returns an embedder that generates vectors
// deterministically from text using hash-seeded RNG.
func NewDeterministicEmbedder(dimension int) *DeterministicEmbedder {
	return &DeterministicEmbedder{dimension: dimension}
}

// Embed generates deterministic vectors from text.
// Uses sha256 hash of text as seed for a simple RNG.
func (d *DeterministicEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		hash := sha256.Sum256([]byte(text))
		// Use first 8 bytes of hash as seed.
		seed := binary.LittleEndian.Uint64(hash[:8])
		result[i] = d.generateVector(seed)
	}
	return result, nil
}

func (d *DeterministicEmbedder) generateVector(seed uint64) []float32 {
	vec := make([]float32, d.dimension)
	// Simple linear congruential generator seeded from sha256.
	state := seed
	for i := range vec {
		state = (state*1664525 + 1013904223) // MINSTD params
		// Convert to float in [-1, 1].
		bits := math.Float32bits(float32(state) / float32(^uint64(0)))
		vec[i] = math.Float32frombits(bits)
	}
	// Normalize to unit sphere.
	norm := float32(0)
	for _, v := range vec {
		norm += v * v
	}
	norm = float32(math.Sqrt(float64(norm)))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

// Dimension returns the embedding vector dimension.
func (d *DeterministicEmbedder) Dimension() int {
	return d.dimension
}
