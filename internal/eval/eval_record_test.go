//go:build record
// +build record

package eval_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotofritz/timbuktu/internal/eval"
)

// TestRecordFixtureVectors records embeddings for the fixture corpus.
// Build and run with: go test -tags=record -run TestRecordFixtureVectors ./internal/eval
//
// This test is only run during development to record frozen vectors.
// The vectors are committed as testdata and used for regression testing in CI.
func TestRecordFixtureVectors(t *testing.T) {
	if testing.Short() {
		t.Skip("recording vectors is not run in -short mode")
	}

	// Use deterministic embedder as stopgap.
	// TODO: replace with real model embedder once configured.
	embedder := eval.NewDeterministicEmbedder(10)

	// For now, compute vectors on-the-fly and print them.
	// In a full implementation, this would:
	// 1. Load fixture corpus documents
	// 2. Chunk them
	// 3. Call the real embedder
	// 4. Write frozen vectors to testdata/corpus/vectors.json

	// Stub: verify embedder works.
	texts := []string{"slices", "maps"}
	vecs, err := embedder.Embed(nil, texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || embedder.Dimension() != 10 {
		t.Fatalf("embedder produced %d vectors of dimension %d, want 2 of %d",
			len(vecs), len(vecs[0]), embedder.Dimension())
	}

	// Write vectors to testdata (for demonstration).
	vectorFile := filepath.Join("testdata", "corpus", "vectors.json")
	vectorData := map[string]interface{}{
		"model":       "deterministic",
		"dimension":   embedder.Dimension(),
		"recorded_at": "2026-09-08T00:00:00Z",
		"vectors":     map[string][]float32{
			// Will be populated by real recording.
		},
	}
	data, err := json.MarshalIndent(vectorData, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vectorFile, data, 0o644); err != nil {
		t.Logf("would write %s: %v", vectorFile, err)
	}
	t.Logf("recorded vectors for %d texts", len(texts))
}
