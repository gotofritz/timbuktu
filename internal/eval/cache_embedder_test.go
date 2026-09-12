package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestCacheEmbedder(t *testing.T) {
	// Vectors are keyed by sha256(text).
	text1 := "abc"
	hash1 := sha256.Sum256([]byte(text1))
	key1 := hex.EncodeToString(hash1[:])

	vectors := map[string][]float32{
		key1: {0.1, 0.2, 0.3},
	}
	embedder := NewCacheEmbedder(vectors, 3)

	if dim := embedder.Dimension(); dim != 3 {
		t.Fatalf("Dimension() = %d, want 3", dim)
	}

	// Test successful embedding lookup.
	vecs, err := embedder.Embed(context.Background(), []string{text1})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 {
		t.Fatalf("Embed returned %d vectors of dim %d, want 1 of 3", len(vecs), len(vecs[0]))
	}

	// Test cache miss.
	_, err = embedder.Embed(context.Background(), []string{"missing"})
	if err == nil {
		t.Fatal("Embed: want error on cache miss, got nil")
	}
}
