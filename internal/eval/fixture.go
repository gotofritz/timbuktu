package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Fixture is a corpus embedded once and committed, so the eval suite can be
// scored with no embedding server running. Vectors are keyed by the sha256 of
// the exact text handed to the embedder, which makes an edited corpus a cache
// miss — a loud failure — rather than a quiet zero.
//
// The header names the instrument. Vectors from two models are never mixed,
// and a report over a fixture says which one produced it, so a number is
// always traceable to something that could have produced it.
type Fixture struct {
	Model      string    `json:"model"`
	Dimension  int       `json:"dimension"`
	RecordedAt time.Time `json:"recorded_at"`
	// Chunking is what split the corpus into the texts below. Both the
	// recorder and the suite that replays the fixture take it from here rather
	// than from a constant each holds separately: chunk boundaries decide what
	// the keys even are, so two sides disagreeing about them is a fixture that
	// misses on every lookup.
	Chunking ChunkSpec            `json:"chunking"`
	Vectors  map[string][]float32 `json:"vectors"`
}

// ChunkSpec is the chunker configuration a fixture was recorded under.
type ChunkSpec struct {
	Size    int `json:"size"`
	Overlap int `json:"overlap"`
}

// NewFixture returns an empty fixture stamped with the instrument recording it.
func NewFixture(model string, dimension int, recordedAt time.Time) *Fixture {
	return &Fixture{
		Model:      model,
		Dimension:  dimension,
		RecordedAt: recordedAt,
		Vectors:    make(map[string][]float32),
	}
}

// VectorKey is the fixture key for a text: the sha256 of the exact bytes the
// embedder was shown.
func VectorKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Put records one text's vector, refusing one that does not match the header.
func (f *Fixture) Put(text string, vec []float32) error {
	if len(vec) != f.Dimension {
		return fmt.Errorf(
			"eval: fixture: %q embedded to %d values but the fixture is %d-dimensional",
			text, len(vec), f.Dimension)
	}
	f.Vectors[VectorKey(text)] = vec
	return nil
}

// LoadFixture reads a recorded fixture, rejecting one whose vectors disagree
// with its own header.
func LoadFixture(path string) (*Fixture, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a test fixture path, not user input
	if err != nil {
		return nil, fmt.Errorf("eval: fixture: %w", err)
	}
	var f Fixture
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("eval: fixture %s: %w", path, err)
	}
	if f.Vectors == nil {
		f.Vectors = make(map[string][]float32)
	}
	if f.Chunking.Size < 1 {
		return nil, fmt.Errorf(
			"eval: fixture %s: records no chunk size, so the texts behind its keys cannot be reproduced",
			path)
	}
	for key, vec := range f.Vectors {
		if len(vec) != f.Dimension {
			return nil, fmt.Errorf(
				"eval: fixture %s: vector %s has %d values but the header says %d",
				path, key, len(vec), f.Dimension)
		}
	}
	return &f, nil
}

// Save writes the fixture as indented JSON.
func (f *Fixture) Save(path string) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("eval: fixture: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("eval: fixture: %w", err)
	}
	return nil
}

// Embedder replays the fixture as an embedder for a run using model at
// dimension. A header naming another instrument is an error and not a warning:
// scoring one model's corpus with another model's vectors produces a number
// that looks like every other number in the report.
func (f *Fixture) Embedder(model string, dimension int) (*CacheEmbedder, error) {
	if f.Model != model {
		return nil, fmt.Errorf(
			"eval: fixture recorded by %q but this run embeds with %q — re-record with `make eval-record`",
			f.Model, model)
	}
	if f.Dimension != dimension {
		return nil, fmt.Errorf(
			"eval: fixture is %d-dimensional but this run embeds at %d — re-record with `make eval-record`",
			f.Dimension, dimension)
	}
	return NewCacheEmbedder(f.Vectors, f.Dimension), nil
}
