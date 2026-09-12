package eval_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/eval"
)

// docsSet is the label set this repository's own decisions are argued from.
const docsSet = "../../docs/eval/timbuktu-docs.yaml"

// TestTimbuktuDocsSet_isScorableOnBothStages guards the committed label set
// against the failure that cost a run: it carried no reference answers, so
// `--stage both --judge` scored the retrieval half normally and reported no
// generation block at all — on the page, indistinguishable from a model that
// answered nothing.
//
// A set in this repository is an instrument. Nothing here checks whether the
// answers are *right* — that is what the judge and a reader are for — only that
// every case can be scored by the stages the set exists to run.
func TestTimbuktuDocsSet_isScorableOnBothStages(t *testing.T) {
	set, err := eval.LoadSet(filepath.Clean(docsSet))
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}
	if len(set.Cases) == 0 {
		t.Fatal("the set carries no cases")
	}

	for _, c := range set.Cases {
		if len(c.Relevant) == 0 {
			t.Errorf("%s: no relevant labels — the retrieval half cannot score it", c.ID)
		}
		if c.Answer == "" && len(c.MustInclude) == 0 {
			t.Errorf("%s: no answer and no must_include — the generation half cannot score it", c.ID)
		}
		for _, want := range c.MustInclude {
			if strings.TrimSpace(want) == "" {
				t.Errorf("%s: a blank must_include matches everything", c.ID)
			}
		}
	}
}

// The split is the design: a set with no threaded cases scores every rewrite
// mode identically, and the default it exists to decide never flips.
func TestTimbuktuDocsSet_keepsBothHalves(t *testing.T) {
	set, err := eval.LoadSet(filepath.Clean(docsSet))
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}
	var threaded, single int
	for _, c := range set.Cases {
		if len(c.Thread) > 0 {
			threaded++
			if c.GoldQuery == "" {
				t.Errorf("%s: a threaded case with no gold_query has no ceiling to be measured against", c.ID)
			}
			continue
		}
		single++
	}
	if threaded == 0 || single == 0 {
		t.Errorf("threaded %d, single-shot %d — the set needs both halves", threaded, single)
	}
}
