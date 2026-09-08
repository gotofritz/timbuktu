package eval_test

import (
	"math"
	"testing"

	"github.com/gotofritz/timbuktu/internal/eval"
)

// chunk is a retrieved passage written as one word, since matching is tested
// on its own and these tables are about arithmetic.
func chunk(path string) eval.Result { return eval.Result{Path: path, Text: "text"} }

func label(path string, grade int) eval.Label { return eval.Label{Path: path, Grade: grade} }

func almost(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestScore(t *testing.T) {
	tests := []struct {
		name      string
		results   []eval.Result
		labels    []eval.Label
		k         int
		hit       float64
		recall    float64
		precision float64
		mrr       float64
		ndcg      float64
		found     int
		retrieved int
	}{
		{
			name:    "the only labelled passage comes back first",
			results: []eval.Result{chunk("a.md")}, labels: []eval.Label{label("a.md", 1)}, k: 5,
			hit: 1, recall: 1, precision: 1, mrr: 1, ndcg: 1, found: 1, retrieved: 1,
		},
		{
			name:    "nothing labelled comes back",
			results: []eval.Result{chunk("x.md")}, labels: []eval.Label{label("a.md", 1)}, k: 5,
			hit: 0, recall: 0, precision: 0, mrr: 0, ndcg: 0, found: 0, retrieved: 1,
		},
		{
			name:    "found at rank two",
			results: []eval.Result{chunk("x.md"), chunk("a.md")}, labels: []eval.Label{label("a.md", 1)}, k: 5,
			hit: 1, recall: 1, precision: 0.5, mrr: 0.5, ndcg: 0.6309297535714575, found: 1, retrieved: 2,
		},
		{
			name:    "one of two labels found",
			results: []eval.Result{chunk("a.md"), chunk("x.md")},
			labels:  []eval.Label{label("a.md", 1), label("b.md", 1)}, k: 5,
			hit: 1, recall: 0.5, precision: 0.5, mrr: 1, ndcg: 0.6131471927654584, found: 1, retrieved: 2,
		},
		{
			// Graded relevance: the grade-2 passage arriving last is what nDCG
			// is for — hit-rate and recall are both perfect here.
			name:    "graded, best passage ranked last",
			results: []eval.Result{chunk("b.md"), chunk("x.md"), chunk("a.md")},
			labels:  []eval.Label{label("a.md", 2), label("b.md", 1)}, k: 5,
			hit: 1, recall: 1, precision: 2.0 / 3.0, mrr: 1, ndcg: 0.6885288809404666, found: 2, retrieved: 3,
		},
		{
			// k is the depth the metrics are cut at: a passage below it did not
			// reach the prompt, so it did not reach the score either.
			name:    "k truncates the ranking",
			results: []eval.Result{chunk("x.md"), chunk("y.md"), chunk("a.md")},
			labels:  []eval.Label{label("a.md", 1)}, k: 2,
			hit: 0, recall: 0, precision: 0, mrr: 0, ndcg: 0, found: 0, retrieved: 2,
		},
		{
			name:    "a k of zero scores the whole list",
			results: []eval.Result{chunk("x.md"), chunk("y.md"), chunk("a.md")},
			labels:  []eval.Label{label("a.md", 1)}, k: 0,
			hit: 1, recall: 1, precision: 1.0 / 3.0, mrr: 0.3333333333333333, ndcg: 0.5, found: 1, retrieved: 3,
		},
		{
			// Two chunks of one labelled document are both genuinely relevant,
			// but the label is credited once — otherwise nDCG could exceed 1
			// and a retriever could score well by returning one document twice.
			name:    "two chunks satisfy one label",
			results: []eval.Result{chunk("a.md"), chunk("a.md")},
			labels:  []eval.Label{label("a.md", 1)}, k: 5,
			hit: 1, recall: 1, precision: 1, mrr: 1, ndcg: 1, found: 1, retrieved: 2,
		},
		{
			name:    "retrieval came back empty",
			results: nil, labels: []eval.Label{label("a.md", 1)}, k: 5,
			hit: 0, recall: 0, precision: 0, mrr: 0, ndcg: 0, found: 0, retrieved: 0,
		},
		{
			// A generation-only case has no labels; scoring it for retrieval is
			// meaningless rather than perfect, so every metric is zero.
			name:    "no labels to score against",
			results: []eval.Result{chunk("a.md")}, labels: nil, k: 5,
			hit: 0, recall: 0, precision: 0, mrr: 0, ndcg: 0, found: 0, retrieved: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eval.Score(tt.results, tt.labels, tt.k)
			almost(t, "Hit", got.Hit, tt.hit)
			almost(t, "Recall", got.Recall, tt.recall)
			almost(t, "Precision", got.Precision, tt.precision)
			almost(t, "MRR", got.MRR, tt.mrr)
			almost(t, "NDCG", got.NDCG, tt.ndcg)
			if got.Found != tt.found {
				t.Errorf("Found = %d, want %d", got.Found, tt.found)
			}
			if got.Retrieved != tt.retrieved {
				t.Errorf("Retrieved = %d, want %d", got.Retrieved, tt.retrieved)
			}
			if got.Labels != len(tt.labels) {
				t.Errorf("Labels = %d, want %d", got.Labels, len(tt.labels))
			}
			if got.NDCG > 1+1e-9 {
				t.Errorf("NDCG = %v, which is above 1 and so not a normalised gain", got.NDCG)
			}
		})
	}
}

func TestScore_anchorsSelectThePassage(t *testing.T) {
	// Same document, different passages: only the anchored one counts, which is
	// what makes a label chunk-granular without naming a chunk id.
	labels := []eval.Label{{Path: "a.md", Contains: "capacity is doubled", Grade: 1}}
	results := []eval.Result{
		{Path: "/n/a.md", Text: "a slice header is three words"},
		{Path: "/n/a.md", Text: "the capacity is doubled on append"},
	}
	got := eval.Score(results, labels, 5)
	almost(t, "MRR", got.MRR, 0.5)
	almost(t, "Recall", got.Recall, 1)
	almost(t, "Precision", got.Precision, 0.5)
}

func TestAggregate(t *testing.T) {
	ms := []eval.Metrics{
		{Hit: 1, Recall: 1, Precision: 0.5, MRR: 1, NDCG: 1, Labels: 2, Retrieved: 4, Found: 2},
		{Hit: 0, Recall: 0, Precision: 0, MRR: 0, NDCG: 0, Labels: 1, Retrieved: 4, Found: 0},
	}
	got := eval.Aggregate(ms)

	// Macro-average: the case is the unit, so a case with many labels does not
	// outvote one with few.
	almost(t, "Hit", got.Hit, 0.5)
	almost(t, "Recall", got.Recall, 0.5)
	almost(t, "Precision", got.Precision, 0.25)
	almost(t, "MRR", got.MRR, 0.5)
	almost(t, "NDCG", got.NDCG, 0.5)
	// Counts are totals, not averages: they are how many, not how well.
	if got.Labels != 3 || got.Retrieved != 8 || got.Found != 2 {
		t.Errorf("counts = %+v, want labels 3, retrieved 8, found 2", got)
	}
	if got.Cases != 2 {
		t.Errorf("Cases = %d, want 2", got.Cases)
	}
}

func TestAggregate_empty(t *testing.T) {
	got := eval.Aggregate(nil)
	if got != (eval.Metrics{}) {
		t.Errorf("Aggregate(nil) = %+v, want the zero value", got)
	}
}
