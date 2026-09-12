package eval_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/eval"
)

// spreadRun builds a report whose per-case hit values are exactly hits, so a
// test can state the numbers it expects a spread to find rather than derive
// them from a scoring run.
func spreadRun(set string, run eval.Run, hits ...float64) eval.Report {
	cases := make([]eval.CaseResult, len(hits))
	for i, h := range hits {
		cases[i] = eval.CaseResult{
			ID:        caseID(i),
			Query:     "q" + caseID(i),
			Metrics:   eval.Metrics{Hit: h, Recall: h, Precision: h, MRR: h, NDCG: h, Cases: 1, Labels: 1, Retrieved: 5},
			LatencyMS: 100,
		}
	}
	return eval.NewReport(set, run, cases)
}

func caseID(i int) string {
	return string(rune('a' + i))
}

func condenseRun() eval.Run {
	return eval.Run{Mode: "hybrid", TopK: 5, Rewrite: "condense", Embedding: "mlx/e", LLM: "mlx/m", Host: "box"}
}

func TestNewSpreadNeedsTwoRuns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		reports []eval.Report
	}{
		{"none", nil},
		{"one", []eval.Report{spreadRun("s", condenseRun(), 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := eval.NewSpread(tc.reports); err == nil {
				t.Fatal("want an error: a spread of fewer than two runs is not a spread")
			}
		})
	}
}

func TestNewSpreadRejectsDifferentSets(t *testing.T) {
	t.Parallel()
	_, err := eval.NewSpread([]eval.Report{
		spreadRun("docs", condenseRun(), 1),
		spreadRun("other", condenseRun(), 1),
	})
	if err == nil {
		t.Fatal("want an error: runs over two label sets are not repetitions of one run")
	}
	if !strings.Contains(err.Error(), "other") {
		t.Errorf("error should name the mismatched set, got %q", err)
	}
}

func TestNewSpreadSummarisesEachMetric(t *testing.T) {
	t.Parallel()
	// Three runs over two cases: hit 0.0, 0.5, 1.0.
	s, err := eval.NewSpread([]eval.Report{
		spreadRun("docs", condenseRun(), 0, 0),
		spreadRun("docs", condenseRun(), 1, 0),
		spreadRun("docs", condenseRun(), 1, 1),
	})
	if err != nil {
		t.Fatalf("NewSpread: %v", err)
	}
	if s.Runs != 3 {
		t.Errorf("Runs = %d, want 3", s.Runs)
	}
	if s.Set != "docs" {
		t.Errorf("Set = %q, want docs", s.Set)
	}

	hit := metricNamed(t, s, "hit")
	if got, want := hit.Runs, []float64{0, 0.5, 1}; !equalFloats(got, want) {
		t.Errorf("hit runs = %v, want %v", got, want)
	}
	if hit.Min != 0 || hit.Max != 1 {
		t.Errorf("hit min/max = %v/%v, want 0/1", hit.Min, hit.Max)
	}
	if math.Abs(hit.Mean-0.5) > 1e-9 {
		t.Errorf("hit mean = %v, want 0.5", hit.Mean)
	}
	// Sample standard deviation of {0, 0.5, 1} is 0.5.
	if math.Abs(hit.StdDev-0.5) > 1e-9 {
		t.Errorf("hit stddev = %v, want 0.5", hit.StdDev)
	}
	if s.Stable() {
		t.Error("Stable() = true on runs that disagree")
	}
}

func TestNewSpreadReportsIdenticalRunsAsStable(t *testing.T) {
	t.Parallel()
	s, err := eval.NewSpread([]eval.Report{
		spreadRun("docs", condenseRun(), 1, 0),
		spreadRun("docs", condenseRun(), 1, 0),
		spreadRun("docs", condenseRun(), 1, 0),
	})
	if err != nil {
		t.Fatalf("NewSpread: %v", err)
	}
	if !s.Stable() {
		t.Fatal("Stable() = false on three identical runs")
	}
	if got := metricNamed(t, s, "hit").StdDev; got != 0 {
		t.Errorf("hit stddev = %v, want 0", got)
	}
	if len(s.Unstable()) != 0 {
		t.Errorf("Unstable = %v, want none", s.Unstable())
	}
}

func TestNewSpreadNamesTheCasesThatMoved(t *testing.T) {
	t.Parallel()
	// Case "a" is steady; case "b" flips. One case moving is what an average
	// over a small set moves in, so the spread has to say which one it was.
	s, err := eval.NewSpread([]eval.Report{
		spreadRun("docs", condenseRun(), 1, 0),
		spreadRun("docs", condenseRun(), 1, 1),
	})
	if err != nil {
		t.Fatalf("NewSpread: %v", err)
	}
	if len(s.Unstable()) != 1 {
		t.Fatalf("Unstable = %v, want exactly the one case that moved", s.Unstable())
	}
	if s.Unstable()[0].ID != "b" {
		t.Errorf("unstable case = %q, want b", s.Unstable()[0].ID)
	}
	if got, want := s.Unstable()[0].Hit, []float64{0, 1}; !equalFloats(got, want) {
		t.Errorf("unstable hit = %v, want %v", got, want)
	}
}

func TestNewSpreadWarnsWhenTheRunsAreNotComparable(t *testing.T) {
	t.Parallel()
	otherEmbedder := condenseRun()
	otherEmbedder.Embedding = "mlx/other"
	otherHost := condenseRun()
	otherHost.Host = "laptop"
	otherRewrite := condenseRun()
	otherRewrite.Rewrite = "window"

	for _, tc := range []struct {
		name    string
		second  eval.Run
		wantSub string
	}{
		{"embedding", otherEmbedder, "embedding"},
		{"host", otherHost, "machine"},
		{"rewrite", otherRewrite, "rewrite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := eval.NewSpread([]eval.Report{
				spreadRun("docs", condenseRun(), 1),
				spreadRun("docs", tc.second, 1),
			})
			if err != nil {
				t.Fatalf("NewSpread: %v", err)
			}
			if !containsSub(s.Warnings, tc.wantSub) {
				t.Errorf("warnings %v, want one mentioning %q", s.Warnings, tc.wantSub)
			}
		})
	}
}

func TestNewSpreadWarnsOnDegradedRuns(t *testing.T) {
	t.Parallel()
	degraded := spreadRun("docs", condenseRun(), 1)
	degraded.Degraded = 1

	s, err := eval.NewSpread([]eval.Report{spreadRun("docs", condenseRun(), 1), degraded})
	if err != nil {
		t.Fatalf("NewSpread: %v", err)
	}
	if !containsSub(s.Warnings, "fell back") {
		t.Errorf("warnings %v, want one about the fallback", s.Warnings)
	}
	if got, want := s.DegradedPerRun, []int{0, 1}; len(got) != len(want) || got[0] != 0 || got[1] != 1 {
		t.Errorf("DegradedPerRun = %v, want %v", got, want)
	}
}

func TestSpreadWriteText(t *testing.T) {
	t.Parallel()
	s, err := eval.NewSpread([]eval.Report{
		spreadRun("docs", condenseRun(), 1, 0),
		spreadRun("docs", condenseRun(), 1, 1),
	})
	if err != nil {
		t.Fatalf("NewSpread: %v", err)
	}
	var b strings.Builder
	if err := s.WriteText(&b); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"docs", "2 runs", "rewrite condense", "hit@5",
		"stddev", "b", // the case that moved is named
		"one case moving", // the arithmetic a small set turns on
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestSpreadWriteTextSaysSoWhenNothingMoved(t *testing.T) {
	t.Parallel()
	s, err := eval.NewSpread([]eval.Report{
		spreadRun("docs", condenseRun(), 1),
		spreadRun("docs", condenseRun(), 1),
	})
	if err != nil {
		t.Fatalf("NewSpread: %v", err)
	}
	var b strings.Builder
	if err := s.WriteText(&b); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.Contains(b.String(), "identical") {
		t.Errorf("a spread of zero should say so plainly:\n%s", b.String())
	}
}

func TestSpreadWriteJSON(t *testing.T) {
	t.Parallel()
	s, err := eval.NewSpread([]eval.Report{
		spreadRun("docs", condenseRun(), 1, 0),
		spreadRun("docs", condenseRun(), 0, 0),
	})
	if err != nil {
		t.Fatalf("NewSpread: %v", err)
	}
	var b strings.Builder
	if err := s.WriteJSON(&b); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var back eval.Spread
	if err := json.Unmarshal([]byte(b.String()), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Runs != 2 || back.Set != "docs" {
		t.Errorf("round trip lost the header: %+v", back.Run)
	}
	if len(back.Metrics) != len(s.Metrics) {
		t.Errorf("round trip has %d metrics, want %d", len(back.Metrics), len(s.Metrics))
	}
}

func metricNamed(t *testing.T, s eval.Spread, name string) eval.MetricSpread {
	t.Helper()
	for _, m := range s.Metrics {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no metric %q in %v", name, s.Metrics)
	return eval.MetricSpread{}
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func containsSub(hay []string, needle string) bool {
	for _, s := range hay {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// TestNewSpreadKeepsEveryCase is what lets a set's halves be read apart after
// the fact: #173 turns on the thirteen single-shot cases, which cannot be
// re-split out of an average.
func TestNewSpreadKeepsEveryCase(t *testing.T) {
	t.Parallel()
	s, err := eval.NewSpread([]eval.Report{
		spreadRun("docs", condenseRun(), 1, 0),
		spreadRun("docs", condenseRun(), 1, 1),
	})
	if err != nil {
		t.Fatalf("NewSpread: %v", err)
	}
	if len(s.Cases) != 2 {
		t.Fatalf("Cases = %d, want both the steady one and the mover", len(s.Cases))
	}
	if s.Cases[0].Moved || !s.Cases[1].Moved {
		t.Errorf("moved flags = %v / %v, want false / true", s.Cases[0].Moved, s.Cases[1].Moved)
	}
	// The question travels with the row, so a rewrite can be read against what
	// it was given without opening the label set.
	if s.Cases[0].Query != "qa" {
		t.Errorf("Query = %q, want the question as asked", s.Cases[0].Query)
	}
}
