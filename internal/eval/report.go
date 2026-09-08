package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// Run records the instrument that produced a report: what was swept, and what
// answered. Two runs are only comparable to the extent this block matches, so
// it travels with the numbers rather than in the reader's memory.
type Run struct {
	Mode    string `json:"mode"`
	TopK    int    `json:"top_k"`
	Rewrite string `json:"rewrite,omitempty"`
	Expand  int    `json:"expand,omitempty"`
	// Embedding and LLM name the models. A frozen-vector run names the fixture
	// it replayed, so a number is always traceable to an instrument.
	Embedding string    `json:"embedding,omitempty"`
	LLM       string    `json:"llm,omitempty"`
	At        time.Time `json:"at"`
}

// CaseResult is one case's row: what was asked, what retrieval actually ran on,
// how it scored, and what it cost.
type CaseResult struct {
	ID    string `json:"id"`
	Query string `json:"query"`
	// Queries is what retrieval ran after planning — several, under an
	// expansion. It is where a bad rewrite becomes visible.
	Queries   []string `json:"queries,omitempty"`
	Metrics   Metrics  `json:"metrics"`
	LatencyMS float64  `json:"latency_ms"`
}

// Latency summarises what the run cost. Median and p95 rather than a mean: one
// cold start should not become the headline number.
type Latency struct {
	MedianMS float64 `json:"median_ms"`
	P95MS    float64 `json:"p95_ms"`
}

// SkippedCase is a case the run could not score, and why.
type SkippedCase struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// Report is a whole run: the instrument, the headline, and every row behind it.
type Report struct {
	Set     string       `json:"set"`
	Run     Run          `json:"run"`
	Overall Metrics      `json:"overall"`
	Latency Latency      `json:"latency"`
	Cases   []CaseResult `json:"cases"`
	// Skipped are the cases this run had nothing to score — a generation-only
	// case under the retrieval stage, or one with no gold query under the
	// ceiling. Every skip shrinks the denominator of every average above, so
	// the count is printed rather than left to be inferred from a case total
	// the reader would have to go and look up.
	Skipped []SkippedCase `json:"skipped,omitempty"`
}

// NewReport assembles a report, computing the headline from the rows.
//
// Overall is derived rather than passed in, so a report cannot carry a summary
// that disagrees with the cases printed underneath it.
func NewReport(set string, run Run, cases []CaseResult) Report {
	ms := make([]Metrics, len(cases))
	latencies := make([]float64, len(cases))
	for i, c := range cases {
		ms[i] = c.Metrics
		latencies[i] = c.LatencyMS
	}
	return Report{
		Set:     set,
		Run:     run,
		Overall: Aggregate(ms),
		Latency: Latency{MedianMS: percentile(latencies, 0.5), P95MS: percentile(latencies, 0.95)},
		Cases:   cases,
	}
}

// percentile returns the p-th percentile of xs by nearest rank, except at the
// median of an even-sized sample, where the middle pair is averaged — the
// reading people expect of the word "median".
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)

	if p == 0.5 && len(sorted)%2 == 0 {
		mid := len(sorted) / 2
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// WriteJSON writes the report as indented JSON — what --baseline reads back,
// and what a script diffs.
func (r Report) WriteJSON(w io.Writer) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("eval: render report as JSON: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("eval: write report: %w", err)
	}
	return nil
}

// WriteText writes the report for a person. verbose adds a row per case.
func (r Report) WriteText(w io.Writer, verbose bool) error {
	var b strings.Builder
	k := r.Run.TopK

	fmt.Fprintf(&b, "%s — %s, %s\n", r.Set,
		Plural(r.Overall.Cases, "case"), Plural(r.Overall.Labels, "label"))
	fmt.Fprintf(&b, "  mode %s   top %d", r.Run.Mode, k)
	if r.Run.Rewrite != "" {
		fmt.Fprintf(&b, "   rewrite %s", r.Run.Rewrite)
	}
	if r.Run.Expand > 0 {
		fmt.Fprintf(&b, "   expand %d", r.Run.Expand)
	}
	b.WriteString("\n")
	if r.Run.Embedding != "" {
		fmt.Fprintf(&b, "  embedding %s\n", r.Run.Embedding)
	}
	if r.Run.LLM != "" {
		fmt.Fprintf(&b, "  llm %s\n", r.Run.LLM)
	}
	if !r.Run.At.IsZero() {
		fmt.Fprintf(&b, "  run %s\n", r.Run.At.UTC().Format(time.RFC3339))
	}

	o := r.Overall
	fmt.Fprintf(&b, "\n  hit@%d %.2f   recall@%d %.2f   P@%d %.2f   MRR %.2f   nDCG@%d %.2f\n",
		k, o.Hit, k, o.Recall, k, o.Precision, o.MRR, k, o.NDCG)
	fmt.Fprintf(&b, "  latency  median %.0fms   p95 %.0fms\n", r.Latency.MedianMS, r.Latency.P95MS)

	if n := len(r.Skipped); n > 0 {
		fmt.Fprintf(&b, "  skipped %d of %s with nothing to score\n", n, Plural(r.Overall.Cases+n, "case"))
	}

	if note := precisionCeiling(r.Cases, k); note != "" {
		fmt.Fprintf(&b, "\n  %s\n", note)
	}

	if verbose && len(r.Cases) > 0 {
		fmt.Fprintf(&b, "\n  %-28s %6s %7s %7s %7s %7s %8s\n",
			"case", "hit", "recall", "P", "MRR", "nDCG", "ms")
		for _, c := range r.Cases {
			m := c.Metrics
			fmt.Fprintf(&b, "  %-28s %6.2f %7.2f %7.2f %7.2f %7.2f %8.0f\n",
				truncate(c.ID, 28), m.Hit, m.Recall, m.Precision, m.MRR, m.NDCG, c.LatencyMS)
		}
	}

	if verbose && len(r.Skipped) > 0 {
		b.WriteString("\n  skipped:\n")
		for _, s := range r.Skipped {
			fmt.Fprintf(&b, "    %-28s %s\n", truncate(s.ID, 28), s.Reason)
		}
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("eval: write report: %w", err)
	}
	return nil
}

// precisionCeiling explains a precision that cannot reach 1 however good the
// retriever is: with fewer labels a case than passages returned for it, some of
// the slots have nothing that could legitimately fill them. Saying so costs one
// line and saves the bug report it would otherwise produce.
//
// The bound is computed per case from what actually came back, not from k. A
// small corpus returns fewer passages than the cutoff, and precision is divided
// by what was retrieved — so a ceiling derived from k alone lands below the
// precision printed directly above it, and a footnote contradicting its own
// report is worse than no footnote.
func precisionCeiling(cases []CaseResult, k int) string {
	if k <= 0 || len(cases) == 0 {
		return ""
	}
	var ceiling, labels, retrieved float64
	for _, c := range cases {
		labels += float64(c.Metrics.Labels)
		retrieved += float64(c.Metrics.Retrieved)
		if c.Metrics.Retrieved == 0 {
			continue
		}
		ceiling += math.Min(1, float64(c.Metrics.Labels)/float64(c.Metrics.Retrieved))
	}
	n := float64(len(cases))
	ceiling /= n
	if ceiling >= 1 {
		return ""
	}
	return fmt.Sprintf("note: %.1f labels and %.1f retrieved passages a case, so P@%d cannot exceed %.2f here",
		labels/n, retrieved/n, k, ceiling)
}

// Plural renders a count with its noun, so a one-case run does not report
// "1 cases" in the first line a reader sees.
func Plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// MetricDelta is one number, then and now.
type MetricDelta struct {
	Name     string  `json:"name"`
	Current  float64 `json:"current"`
	Baseline float64 `json:"baseline"`
	Delta    float64 `json:"delta"`
}

// ReportDiff is a run measured against an earlier one.
type ReportDiff struct {
	Set           string        `json:"set"`
	Current       Run           `json:"current"`
	Baseline      Run           `json:"baseline"`
	Metrics       []MetricDelta `json:"metrics"`
	LatencyMedian MetricDelta   `json:"latency_median"`
	LatencyP95    MetricDelta   `json:"latency_p95"`
	// Warnings name the ways the two runs are not quite comparable. A swept
	// knob is never one of them — sweeping is the point — but a different
	// corpus, a different embedder or a different model is.
	Warnings []string `json:"warnings,omitempty"`
}

// Diff compares a run against a baseline, so "A/B'd with evidence" does not
// depend on someone subtracting nDCG in their head.
//
// A baseline from a different label set is an error: comparing two corpora is
// not a comparison.
func Diff(current, baseline Report) (ReportDiff, error) {
	if !strings.EqualFold(strings.TrimSpace(current.Set), strings.TrimSpace(baseline.Set)) {
		return ReportDiff{}, fmt.Errorf(
			"eval: baseline is for label set %q but this run is for %q; comparing two corpora is not a comparison",
			baseline.Set, current.Set)
	}

	c, b := current.Overall, baseline.Overall
	d := ReportDiff{
		Set:      current.Set,
		Current:  current.Run,
		Baseline: baseline.Run,
		Metrics: []MetricDelta{
			newDelta("hit", c.Hit, b.Hit),
			newDelta("recall", c.Recall, b.Recall),
			newDelta("precision", c.Precision, b.Precision),
			newDelta("mrr", c.MRR, b.MRR),
			newDelta("ndcg", c.NDCG, b.NDCG),
		},
		LatencyMedian: newDelta("latency median", current.Latency.MedianMS, baseline.Latency.MedianMS),
		LatencyP95:    newDelta("latency p95", current.Latency.P95MS, baseline.Latency.P95MS),
	}

	if c.Cases != b.Cases {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"the two runs cover different numbers of cases (%d now, %d in the baseline), "+
				"so the averages are over different questions", c.Cases, b.Cases))
	}
	// A swept knob is never a warning — sweeping is the point — and switching
	// --mode to or from keyword drops the embedder by definition, so that
	// difference is already explained by the sweep the reader asked for.
	if current.Run.Embedding != baseline.Run.Embedding && current.Run.Mode == baseline.Run.Mode {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"the embedding model changed (%s, was %s): this compares two instruments, "+
				"and the latency deltas are not comparable at all",
			orNone(current.Run.Embedding), orNone(baseline.Run.Embedding)))
	}
	if current.Run.LLM != baseline.Run.LLM {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"the model changed (%s, was %s): generation scores and latency move with it",
			orNone(current.Run.LLM), orNone(baseline.Run.LLM)))
	}
	return d, nil
}

func newDelta(name string, current, baseline float64) MetricDelta {
	return MetricDelta{Name: name, Current: current, Baseline: baseline, Delta: current - baseline}
}

func orNone(s string) string {
	if s == "" {
		return "unrecorded"
	}
	return s
}

// WriteText writes the diff for a person, deltas signed so the direction reads
// at a glance.
func (d ReportDiff) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — this run against the baseline\n", d.Set)
	fmt.Fprintf(&b, "  %-16s %9s %9s %9s\n", "metric", "current", "baseline", "delta")
	for _, m := range d.Metrics {
		fmt.Fprintf(&b, "  %-16s %9.2f %9.2f %+9.2f\n", m.Name, m.Current, m.Baseline, m.Delta)
	}
	for _, m := range []MetricDelta{d.LatencyMedian, d.LatencyP95} {
		fmt.Fprintf(&b, "  %-16s %7.0fms %7.0fms %+7.0fms\n", m.Name, m.Current, m.Baseline, m.Delta)
	}
	for _, warning := range d.Warnings {
		fmt.Fprintf(&b, "\n  warning: %s\n", warning)
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("eval: write diff: %w", err)
	}
	return nil
}
