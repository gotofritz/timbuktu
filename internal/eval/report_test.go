package eval_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/eval"
)

func caseResult(id string, hit, ndcg, ms float64, labels int) eval.CaseResult {
	return eval.CaseResult{
		ID:    id,
		Query: "q " + id,
		Metrics: eval.Metrics{
			Hit: hit, Recall: hit, Precision: hit / 2, MRR: hit, NDCG: ndcg,
			Cases: 1, Labels: labels, Retrieved: 5, Found: int(hit),
		},
		LatencyMS: ms,
	}
}

func sampleRun() eval.Run {
	return eval.Run{
		Mode: "hybrid", TopK: 5, Rewrite: "window",
		Embedding: "llama/nomic-embed-text",
		At:        time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewReport_aggregatesAndTimes(t *testing.T) {
	cases := []eval.CaseResult{
		caseResult("a", 1, 1, 10, 2),
		caseResult("b", 0, 0, 30, 2),
	}
	r := eval.NewReport("go-docs", sampleRun(), cases)

	if r.Set != "go-docs" {
		t.Errorf("Set = %q", r.Set)
	}
	// Overall is computed from the cases rather than passed in, so a report
	// cannot carry a headline that disagrees with its own rows.
	almost(t, "Overall.Hit", r.Overall.Hit, 0.5)
	almost(t, "Overall.NDCG", r.Overall.NDCG, 0.5)
	if r.Overall.Cases != 2 || r.Overall.Labels != 4 {
		t.Errorf("Overall counts = %+v, want 2 cases and 4 labels", r.Overall)
	}
	almost(t, "median", r.Latency.MedianMS, 20)
	almost(t, "p95", r.Latency.P95MS, 30)
}

func TestNewReport_latencyPercentiles(t *testing.T) {
	tests := []struct {
		name        string
		ms          []float64
		median, p95 float64
	}{
		{"one case", []float64{42}, 42, 42},
		{"even count averages the middle pair", []float64{10, 20, 30, 40}, 25, 40},
		{"odd count takes the middle", []float64{10, 20, 30}, 20, 30},
		{"ten cases", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 100}, 5.5, 100},
		{"unsorted input", []float64{30, 10, 20}, 20, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cases := make([]eval.CaseResult, len(tt.ms))
			for i, ms := range tt.ms {
				cases[i] = caseResult("c", 1, 1, ms, 1)
			}
			r := eval.NewReport("s", sampleRun(), cases)
			almost(t, "median", r.Latency.MedianMS, tt.median)
			almost(t, "p95", r.Latency.P95MS, tt.p95)
		})
	}
}

func TestNewReport_noCases(t *testing.T) {
	r := eval.NewReport("empty", sampleRun(), nil)
	if r.Overall != (eval.Metrics{}) {
		t.Errorf("Overall = %+v, want the zero value", r.Overall)
	}
	if r.Latency.MedianMS != 0 || r.Latency.P95MS != 0 {
		t.Errorf("Latency = %+v, want zeroes", r.Latency)
	}
}

func TestReportWriteJSON(t *testing.T) {
	r := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	var sb strings.Builder
	if err := r.WriteJSON(&sb); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	// The JSON is what --baseline reads back, so it has to round-trip.
	var back eval.Report
	if err := json.Unmarshal([]byte(sb.String()), &back); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, sb.String())
	}
	if back.Set != "go-docs" || back.Run.Mode != "hybrid" || back.Run.TopK != 5 {
		t.Errorf("round-tripped run = %+v", back.Run)
	}
	if len(back.Cases) != 1 || back.Cases[0].ID != "a" {
		t.Errorf("round-tripped cases = %+v", back.Cases)
	}
	almost(t, "round-tripped overall hit", back.Overall.Hit, 1)
	if !strings.HasSuffix(sb.String(), "\n") {
		t.Error("JSON output should end with a newline")
	}
}

func TestReportWriteText(t *testing.T) {
	cases := []eval.CaseResult{caseResult("slices-growth", 1, 1, 10, 2), caseResult("maps", 0, 0, 30, 2)}
	r := eval.NewReport("go-docs", sampleRun(), cases)

	var plain strings.Builder
	if err := r.WriteText(&plain, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := plain.String()
	for _, want := range []string{"go-docs", "2 cases", "hybrid", "hit@5", "nDCG@5", "median", "llama/nomic-embed-text"} {
		if !strings.Contains(out, want) {
			t.Errorf("text report is missing %q:\n%s", want, out)
		}
	}
	// Per-case rows are the verbose view; the summary stays a summary.
	if strings.Contains(out, "slices-growth") {
		t.Errorf("non-verbose report should not list cases:\n%s", out)
	}

	var verbose strings.Builder
	if err := r.WriteText(&verbose, true); err != nil {
		t.Fatalf("WriteText verbose: %v", err)
	}
	for _, want := range []string{"slices-growth", "maps"} {
		if !strings.Contains(verbose.String(), want) {
			t.Errorf("verbose report is missing %q:\n%s", want, verbose.String())
		}
	}
}

func TestReportWriteText_precisionCeilingFootnote(t *testing.T) {
	// Two labels a case, cut at 5: precision cannot exceed 0.4 however good the
	// retriever is. Saying so is cheaper than the bug report it prevents.
	r := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	var sb strings.Builder
	if err := r.WriteText(&sb, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.Contains(sb.String(), "0.40") || !strings.Contains(sb.String(), "cannot exceed") {
		t.Errorf("want a note that precision cannot exceed 0.40:\n%s", sb.String())
	}
}

func TestDiff(t *testing.T) {
	baseline := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{
		caseResult("a", 0, 0, 20, 2),
		caseResult("b", 0, 0, 20, 2),
	})
	current := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{
		caseResult("a", 1, 1, 30, 2),
		caseResult("b", 0, 0, 30, 2),
	})

	d, err := eval.Diff(current, baseline)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	byName := map[string]eval.MetricDelta{}
	for _, m := range d.Metrics {
		byName[m.Name] = m
	}
	hit, ok := byName["hit"]
	if !ok {
		t.Fatalf("no hit delta in %+v", d.Metrics)
	}
	almost(t, "hit delta", hit.Delta, 0.5)
	almost(t, "hit baseline", hit.Baseline, 0)
	almost(t, "hit current", hit.Current, 0.5)
	almost(t, "latency delta", d.LatencyMedian.Delta, 10)

	var sb strings.Builder
	if err := d.WriteText(&sb); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.Contains(sb.String(), "hit") || !strings.Contains(sb.String(), "+0.50") {
		t.Errorf("diff text is missing the delta:\n%s", sb.String())
	}
}

func TestDiff_identicalIsAllZeroes(t *testing.T) {
	r := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	d, err := eval.Diff(r, r)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, m := range d.Metrics {
		almost(t, m.Name+" delta", m.Delta, 0)
	}
	if len(d.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", d.Warnings)
	}
}

func TestDiff_mismatchedSetIsAnError(t *testing.T) {
	a := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	b := eval.NewReport("ops", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	// Comparing two corpora is not a comparison.
	if _, err := eval.Diff(a, b); err == nil {
		t.Fatal("Diff across two label sets = nil error, want one")
	}
}

func TestDiff_warnings(t *testing.T) {
	base := sampleRun()
	changed := sampleRun()
	changed.Embedding = "openai/text-embedding-3-small"

	baseline := eval.NewReport("go-docs", base, []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	current := eval.NewReport("go-docs", changed, []eval.CaseResult{
		caseResult("a", 1, 1, 10, 2),
		caseResult("b", 1, 1, 10, 2),
	})

	d, err := eval.Diff(current, baseline)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	joined := strings.Join(d.Warnings, "\n")
	if !strings.Contains(joined, "case") {
		t.Errorf("want a warning that the sets differ in size: %v", d.Warnings)
	}
	if !strings.Contains(joined, "embedding") {
		t.Errorf("want a warning that the embedder changed: %v", d.Warnings)
	}
}

func TestReportWriteText_skippedCasesAreNamed(t *testing.T) {
	r := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	// A case the run could not score shrinks the denominator of every average,
	// so it is reported rather than quietly dropped.
	r.Skipped = []eval.SkippedCase{
		{ID: "maps-followup", Reason: "no gold_query"},
		{ID: "chit-chat", Reason: "no labels"},
	}

	var plain strings.Builder
	if err := r.WriteText(&plain, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.Contains(plain.String(), "skipped 2") {
		t.Errorf("summary should say how many cases were skipped:\n%s", plain.String())
	}
	// The names are detail, so they wait for --verbose.
	if strings.Contains(plain.String(), "maps-followup") {
		t.Errorf("summary should not list the skipped ids:\n%s", plain.String())
	}

	var verbose strings.Builder
	if err := r.WriteText(&verbose, true); err != nil {
		t.Fatalf("WriteText verbose: %v", err)
	}
	for _, want := range []string{"maps-followup", "no gold_query", "chit-chat", "no labels"} {
		if !strings.Contains(verbose.String(), want) {
			t.Errorf("verbose report is missing %q:\n%s", want, verbose.String())
		}
	}
}

func TestReportWriteText_noSkippedLineWhenNoneSkipped(t *testing.T) {
	r := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	var sb strings.Builder
	if err := r.WriteText(&sb, true); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(sb.String(), "skipped") {
		t.Errorf("a run that scored every case should not mention skipping:\n%s", sb.String())
	}
}

func TestReportWriteJSON_roundTripsSkipped(t *testing.T) {
	r := eval.NewReport("go-docs", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	r.Skipped = []eval.SkippedCase{{ID: "chit-chat", Reason: "no labels"}}

	var sb strings.Builder
	if err := r.WriteJSON(&sb); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var back eval.Report
	if err := json.Unmarshal([]byte(sb.String()), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Skipped) != 1 || back.Skipped[0].ID != "chit-chat" || back.Skipped[0].Reason != "no labels" {
		t.Errorf("round-tripped skipped = %+v", back.Skipped)
	}
}

func TestReportWriteText_precisionCeilingUsesWhatCameBack(t *testing.T) {
	// A small corpus returns fewer passages than the cutoff, and precision's
	// denominator is what came back. A ceiling computed from k alone lands
	// below the precision actually scored — a footnote contradicting the number
	// directly above it is worse than no footnote.
	cases := []eval.CaseResult{
		{ID: "one", Metrics: eval.Metrics{Precision: 1, Cases: 1, Labels: 1, Retrieved: 1}},
		{ID: "two", Metrics: eval.Metrics{Precision: 0.5, Cases: 1, Labels: 1, Retrieved: 2}},
	}
	r := eval.NewReport("small", sampleRun(), cases)

	var sb strings.Builder
	if err := r.WriteText(&sb, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := sb.String()
	// min(1, 1/1) and min(1, 1/2) average to 0.75 — exactly the precision
	// scored, so the note has to say 0.75 and not 0.20.
	if strings.Contains(out, "0.20") {
		t.Errorf("ceiling was computed from k rather than from what was retrieved:\n%s", out)
	}
	if !strings.Contains(out, "0.75") {
		t.Errorf("want a ceiling of 0.75:\n%s", out)
	}
}

func TestReportWriteText_noCeilingNoteWhenPrecisionCanReachOne(t *testing.T) {
	// Enough labels to fill every retrieved slot: nothing to explain.
	cases := []eval.CaseResult{
		{ID: "one", Metrics: eval.Metrics{Precision: 1, Cases: 1, Labels: 5, Retrieved: 5}},
	}
	r := eval.NewReport("dense", sampleRun(), cases)
	var sb strings.Builder
	if err := r.WriteText(&sb, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(sb.String(), "cannot exceed") {
		t.Errorf("no note is due when precision can reach 1:\n%s", sb.String())
	}
}

func TestReportWriteText_pluralisesCounts(t *testing.T) {
	one := eval.NewReport("s", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 1)})
	var sb strings.Builder
	if err := one.WriteText(&sb, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.Contains(sb.String(), "1 case,") || !strings.Contains(sb.String(), "1 label\n") {
		t.Errorf("a single case and label should read in the singular:\n%s", sb.String())
	}

	two := eval.NewReport("s", sampleRun(), []eval.CaseResult{
		caseResult("a", 1, 1, 10, 2), caseResult("b", 1, 1, 10, 2),
	})
	sb.Reset()
	if err := two.WriteText(&sb, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.Contains(sb.String(), "2 cases, 4 labels") {
		t.Errorf("plurals:\n%s", sb.String())
	}
}

func TestDiff_sweptModeExplainsTheMissingEmbedder(t *testing.T) {
	// hybrid vs keyword is the most ordinary A/B there is, and keyword uses no
	// embedder by definition. Warning that "the embedding model changed" there
	// is a false positive, and false positives teach people to skip warnings.
	hybrid := sampleRun()
	keyword := sampleRun()
	keyword.Mode = "keyword"
	keyword.Embedding = ""

	baseline := eval.NewReport("go-docs", hybrid, []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	current := eval.NewReport("go-docs", keyword, []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})

	d, err := eval.Diff(current, baseline)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, w := range d.Warnings {
		if strings.Contains(w, "embedding") {
			t.Errorf("swept mode should explain the embedder difference, got: %q", w)
		}
	}
}

func TestDiff_embeddingChangeUnderTheSameModeStillWarns(t *testing.T) {
	base := sampleRun()
	changed := sampleRun()
	changed.Embedding = "openai/text-embedding-3-small"

	baseline := eval.NewReport("go-docs", base, []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	current := eval.NewReport("go-docs", changed, []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})

	d, err := eval.Diff(current, baseline)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(strings.Join(d.Warnings, "\n"), "embedding") {
		t.Errorf("same mode, different embedder is a real instrument change: %v", d.Warnings)
	}
}

func TestDiffWriteText_columnsLineUp(t *testing.T) {
	r := eval.NewReport("s", sampleRun(), []eval.CaseResult{caseResult("a", 1, 1, 10, 2)})
	d, err := eval.Diff(r, r)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var sb strings.Builder
	if err := d.WriteText(&sb); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	width := len(lines[1]) // the header row
	for _, l := range lines[2:] {
		if l == "" || strings.Contains(l, "warning") {
			continue
		}
		if len(l) != width {
			t.Errorf("row %q is %d wide, header is %d — the columns do not line up", l, len(l), width)
		}
	}
}
