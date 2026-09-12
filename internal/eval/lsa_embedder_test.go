package eval_test

import (
	"context"
	"math"
	"testing"

	"github.com/gotofritz/timbuktu/internal/eval"
)

// lsaCorpus is three passages with little vocabulary in common, so a query
// that belongs to one of them has somewhere to be wrong.
var lsaCorpus = []string{
	"A slice grows when append finds len equals cap. " +
		"The capacity is roughly doubled by allocating a new underlying array.",
	"A map grows by allocating new buckets and re-hashing every entry " +
		"once the load factor is exceeded.",
	"Goroutines are scheduled by the runtime onto operating system threads.",
}

func fitLSA(t *testing.T, corpus []string, dim int) *eval.LSAEmbedder {
	t.Helper()
	e, err := eval.FitLSA(corpus, dim)
	if err != nil {
		t.Fatalf("FitLSA: %v", err)
	}
	return e
}

func embedOne(t *testing.T, e *eval.LSAEmbedder, text string) []float32 {
	t.Helper()
	vecs, err := e.Embed(context.Background(), []string{text})
	if err != nil {
		t.Fatalf("Embed(%q): %v", text, err)
	}
	if len(vecs) != 1 {
		t.Fatalf("Embed(%q) returned %d vectors, want 1", text, len(vecs))
	}
	return vecs[0]
}

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

func TestFitLSA_isDeterministic(t *testing.T) {
	a := fitLSA(t, lsaCorpus, 3)
	b := fitLSA(t, lsaCorpus, 3)

	first := embedOne(t, a, "how does append grow a slice")
	second := embedOne(t, b, "how does append grow a slice")

	if len(first) != len(second) {
		t.Fatalf("two fits produced %d and %d dimensions", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("two fits disagree at %d: %v vs %v", i, first[i], second[i])
		}
	}
}

func TestFitLSA_ranksTheRelatedPassageFirst(t *testing.T) {
	e := fitLSA(t, lsaCorpus, 3)

	query := embedOne(t, e, "what makes append allocate a bigger capacity")
	slices := embedOne(t, e, lsaCorpus[0])
	maps := embedOne(t, e, lsaCorpus[1])
	sched := embedOne(t, e, lsaCorpus[2])

	toSlices := cosine(query, slices)
	if s := cosine(query, maps); toSlices <= s {
		t.Errorf("query scored %.4f against the slices passage but %.4f against maps", toSlices, s)
	}
	if s := cosine(query, sched); toSlices <= s {
		t.Errorf("query scored %.4f against the slices passage but %.4f against goroutines", toSlices, s)
	}
}

func TestFitLSA_embedsUnitVectors(t *testing.T) {
	e := fitLSA(t, lsaCorpus, 3)

	vec := embedOne(t, e, lsaCorpus[0])
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if math.Abs(math.Sqrt(norm)-1) > 1e-6 {
		t.Fatalf("vector norm = %.6f, want 1", math.Sqrt(norm))
	}
}

func TestFitLSA_clampsDimensionToRank(t *testing.T) {
	e := fitLSA(t, lsaCorpus, 64)

	if got := e.Dimension(); got > len(lsaCorpus) {
		t.Fatalf("Dimension() = %d over a %d-passage corpus, want no more than the rank",
			got, len(lsaCorpus))
	}
	if got := len(embedOne(t, e, lsaCorpus[0])); got != e.Dimension() {
		t.Fatalf("Embed returned %d values but Dimension() says %d", got, e.Dimension())
	}
}

func TestFitLSA_textOutsideTheVocabularyIsZero(t *testing.T) {
	e := fitLSA(t, lsaCorpus, 3)

	vec := embedOne(t, e, "zygote xylophone quixotic")
	for i, v := range vec {
		if v != 0 {
			t.Fatalf("unknown text embedded to a non-zero vector at %d: %v", i, v)
		}
		if math.IsNaN(float64(v)) {
			t.Fatalf("unknown text embedded to NaN at %d", i)
		}
	}
}

func TestFitLSA_rejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		corpus []string
		dim    int
	}{
		{"empty corpus", nil, 3},
		{"blank corpus", []string{"", "   "}, 3},
		{"zero dimension", lsaCorpus, 0},
		{"negative dimension", lsaCorpus, -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := eval.FitLSA(tc.corpus, tc.dim); err == nil {
				t.Fatal("FitLSA returned no error")
			}
		})
	}
}
