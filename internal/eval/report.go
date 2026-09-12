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
	// Hops is how many follow-up retrieval rounds the run allowed. Zero is the
	// single shot, and two reports that differ only in this are otherwise
	// indistinguishable — which is the comparison #28's kill criterion is.
	Hops int `json:"hops,omitempty"`
	// Stage is what was measured: retrieval, generation, or both. A report that
	// does not say which half it scored is a report whose empty block cannot be
	// told from a failing one.
	Stage string `json:"stage,omitempty"`
	// Embedding and LLM name the models. A frozen-vector run names the fixture
	// it replayed, so a number is always traceable to an instrument.
	Embedding string `json:"embedding,omitempty"`
	LLM       string `json:"llm,omitempty"`
	// Judge names the model that graded the answers, recorded separately from
	// the one that wrote them: a judge upgrade has to be visible as a judge
	// upgrade rather than as a quality change.
	Judge string `json:"judge,omitempty"`
	// Host is the machine that produced the latency figures. Latency is the
	// noisiest number in the report — it moves with the hardware, the server
	// and whatever else was running — so a baseline from somewhere else has to
	// say so rather than let a faster machine read as a faster retriever.
	Host string    `json:"host,omitempty"`
	At   time.Time `json:"at"`
}

// CaseResult is one case's row: what was asked, what retrieval actually ran on,
// how it scored, and what it cost.
type CaseResult struct {
	ID    string `json:"id"`
	Query string `json:"query"`
	// Queries is what retrieval ran after planning — several, under an
	// expansion. It is where a bad rewrite becomes visible.
	Queries []string `json:"queries,omitempty"`
	// Metrics is the retrieval score. A row with Cases 0 was not scored on
	// retrieval at all — a generation-only case, under --stage generation —
	// and it stays out of the retrieval average rather than dragging it down
	// with zeroes it never earned.
	Metrics   Metrics `json:"metrics"`
	LatencyMS float64 `json:"latency_ms"`
	// PlanMS is what query planning cost before retrieval began — a model call
	// under condense or an expansion, nothing under the deterministic modes.
	// Kept apart from LatencyMS so "the rewrite is slow" stays distinguishable
	// from "the search is slow"; #28's kill criterion needs the sum, and a
	// reader deciding what to fix needs the split.
	PlanMS float64 `json:"plan_ms,omitempty"`
	// Degraded is why this case did not get the query planning the run asked
	// for — a condense that timed out and fell back to the window, say. Empty
	// is the normal case. It is recorded per row because a rewrite that cannot
	// fail can still stop working, and a run where it did produces numbers
	// that look exactly like a run where it did not.
	Degraded string `json:"degraded,omitempty"`

	// Generation is the answer's score, absent when the stage did not run.
	Generation   *GenMetrics `json:"generation,omitempty"`
	GenLatencyMS float64     `json:"gen_latency_ms,omitempty"`
	// Judge is what the judge said, when it said anything.
	Judge *Judgement `json:"judge,omitempty"`
	// Unjudged is why the judge did not score this case, when one was asked
	// for and did not arrive. It is recorded instead of a zero: a judge that
	// failed is not evidence that the answer was wrong.
	Unjudged string `json:"unjudged,omitempty"`
	// UnresolvedCitations are the documents the answer named that the index
	// does not hold — the rows behind a citations score, so a surprising one
	// can be traced rather than argued with.
	UnresolvedCitations []string `json:"unresolved_citations,omitempty"`
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

// GenerationReport is the generation stage's half of a run: what the answers
// scored, and what they cost. Its latency is its own, because a model call an
// answer is a different order of expense from an embedding call a query, and
// #28's kill criterion is stated in correctness and latency together.
type GenerationReport struct {
	Overall GenMetrics `json:"overall"`
	Latency Latency    `json:"latency"`
	// PlanLatency is the query planning cost across the run. Zero under the
	// deterministic planners, which is the point of comparison.
	PlanLatency Latency `json:"plan_latency"`
}

// Report is a whole run: the instrument, the headline, and every row behind it.
type Report struct {
	Set     string  `json:"set"`
	Run     Run     `json:"run"`
	Overall Metrics `json:"overall"`
	Latency Latency `json:"latency"`
	// PlanLatency is what query planning cost across the run, before retrieval
	// began. Zero under the deterministic planners, which is the comparison
	// #28's kill criterion turns on: a rewrite's model call has to appear
	// somewhere, and folding it into Latency would hide which half is slow.
	PlanLatency Latency `json:"plan_latency"`
	// Generation is present only when the generation stage ran. Absent is not
	// zero: nobody asked, so nothing was measured.
	Generation *GenerationReport `json:"generation,omitempty"`
	Cases      []CaseResult      `json:"cases"`
	// Skipped are the cases this run had nothing to score — a generation-only
	// case under the retrieval stage, or one with no gold query under the
	// ceiling. Every skip shrinks the denominator of every average above, so
	// the count is printed rather than left to be inferred from a case total
	// the reader would have to go and look up.
	Skipped []SkippedCase `json:"skipped,omitempty"`
	// UnindexedLabels counts the labels naming a document the knowledge base
	// does not hold. Each scores zero on every run and reads exactly like a
	// retrieval failure, so the count belongs beside the metrics it is dragging
	// down. Zero labels resolving is refused outright rather than reported.
	UnindexedLabels int `json:"unindexed_labels,omitempty"`
	// Degraded counts the cases whose query planning fell back. A run with any
	// is not measuring the planner it names, so the number belongs beside the
	// metrics rather than in a warning on stderr that has already scrolled past.
	Degraded int `json:"degraded,omitempty"`
}

// NewReport assembles a report, computing the headline from the rows.
//
// Overall is derived rather than passed in, so a report cannot carry a summary
// that disagrees with the cases printed underneath it.
func NewReport(set string, run Run, cases []CaseResult) Report {
	var (
		ms            []Metrics
		latencies     = make([]float64, len(cases))
		planLatencies = make([]float64, len(cases))
		gens          []GenMetrics
		genLat        []float64
		degraded      int
	)
	for i, c := range cases {
		// A row scored on generation alone carries no retrieval metrics, and
		// counting its zeroes would print a retrieval failure that never
		// happened. Retrieval latency is kept either way: retrieval ran, it is
		// what produced the evidence, and it cost what it cost.
		if c.Metrics.Cases > 0 {
			ms = append(ms, c.Metrics)
		}
		latencies[i] = c.LatencyMS
		planLatencies[i] = c.PlanMS
		if c.Degraded != "" {
			degraded++
		}
		if c.Generation != nil {
			gens = append(gens, *c.Generation)
			genLat = append(genLat, c.GenLatencyMS)
		}
	}

	report := Report{
		Set:         set,
		Run:         run,
		Overall:     Aggregate(ms),
		Latency:     latencyOf(latencies),
		PlanLatency: latencyOf(planLatencies),
		Cases:       cases,
		Degraded:    degraded,
	}
	if len(gens) > 0 {
		report.Generation = &GenerationReport{
			Overall: AggregateGen(gens),
			Latency: latencyOf(genLat),
		}
	}
	return report
}

func latencyOf(ms []float64) Latency {
	return Latency{MedianMS: percentile(ms, 0.5), P95MS: percentile(ms, 0.95)}
}

// headlineCounts names what the run actually scored. A generation-only run has
// no retrieval cases, and heading it "0 cases" would be a claim about the run
// rather than a fact about the set.
func (r Report) headlineCounts() string {
	if r.Overall.Cases == 0 && r.Generation != nil {
		return Plural(r.Generation.Overall.Cases, "answer")
	}
	counts := fmt.Sprintf("%s, %s", Plural(r.Overall.Cases, "case"), Plural(r.Overall.Labels, "label"))
	if r.Generation != nil && r.Generation.Overall.Cases != r.Overall.Cases {
		counts += ", " + Plural(r.Generation.Overall.Cases, "answer")
	}
	return counts
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

	fmt.Fprintf(&b, "%s — %s\n", r.Set, r.headlineCounts())
	fmt.Fprintf(&b, "  mode %s   top %d", r.Run.Mode, k)
	if r.Run.Rewrite != "" {
		fmt.Fprintf(&b, "   rewrite %s", r.Run.Rewrite)
	}
	if r.Run.Expand > 0 {
		fmt.Fprintf(&b, "   expand %d", r.Run.Expand)
	}
	if r.Run.Hops > 0 {
		fmt.Fprintf(&b, "   hops %d", r.Run.Hops)
	}
	b.WriteString("\n")
	if r.Run.Embedding != "" {
		fmt.Fprintf(&b, "  embedding %s\n", r.Run.Embedding)
	}
	if r.Run.Host != "" {
		fmt.Fprintf(&b, "  host %s\n", r.Run.Host)
	}

	if r.UnindexedLabels > 0 {
		fmt.Fprintf(&b,
			"  ! %s not in the index — each scores zero and reads as a retrieval failure\n",
			Plural(r.UnindexedLabels, "label"))
	}
	if r.Degraded > 0 {
		fmt.Fprintf(&b,
			"  ! query planning fell back on %d of %d cases — this is not a %s measurement\n",
			r.Degraded, len(r.Cases), orNone(r.Run.Rewrite))
	}
	if r.Run.LLM != "" {
		fmt.Fprintf(&b, "  llm %s\n", r.Run.LLM)
	}
	if r.Run.Judge != "" {
		fmt.Fprintf(&b, "  judge %s\n", r.Run.Judge)
	}
	if !r.Run.At.IsZero() {
		fmt.Fprintf(&b, "  run %s\n", r.Run.At.UTC().Format(time.RFC3339))
	}

	// The retrieval headline is printed only when something was scored on
	// retrieval. Under --stage generation the numbers would all be zero, and a
	// row of zeroes reads exactly like the failure this harness exists to find.
	if r.Overall.Cases > 0 {
		o := r.Overall
		fmt.Fprintf(&b, "\n  hit@%d %.2f   recall@%d %.2f   P@%d %.2f   MRR %.2f   nDCG@%d %.2f\n",
			k, o.Hit, k, o.Recall, k, o.Precision, o.MRR, k, o.NDCG)
	}
	fmt.Fprintf(&b, "  latency  median %.0fms   p95 %.0fms\n", r.Latency.MedianMS, r.Latency.P95MS)
	// Planning is reported separately rather than folded in: #28's kill
	// criterion needs the sum, and a reader deciding what to fix needs to know
	// which half it came from.
	if r.PlanLatency.MedianMS > 0 {
		fmt.Fprintf(&b, "  planning median %.0fms   p95 %.0fms   (a model call per question, before retrieval)\n",
			r.PlanLatency.MedianMS, r.PlanLatency.P95MS)
	}

	if n := len(r.Skipped); n > 0 {
		fmt.Fprintf(&b, "  skipped %d of %s with nothing to score\n", n, Plural(len(r.Cases)+n, "case"))
	}

	if r.Overall.Cases > 0 {
		if note := precisionCeiling(r.Cases, k); note != "" {
			fmt.Fprintf(&b, "\n  %s\n", note)
		}
	}

	r.writeGeneration(&b)

	if verbose && r.Overall.Cases > 0 {
		fmt.Fprintf(&b, "\n  %-28s %6s %7s %7s %7s %7s %8s\n",
			"case", "hit", "recall", "P", "MRR", "nDCG", "ms")
		for _, c := range r.Cases {
			if c.Metrics.Cases == 0 {
				continue
			}
			m := c.Metrics
			fmt.Fprintf(&b, "  %-28s %6.2f %7.2f %7.2f %7.2f %7.2f %8.0f\n",
				truncate(c.ID, 28), m.Hit, m.Recall, m.Precision, m.MRR, m.NDCG, c.LatencyMS)
		}
	}

	if verbose {
		r.writePlanning(&b)
	}

	if verbose && r.Generation != nil {
		r.writeGenerationCases(&b)
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

// writePlanning prints what a rewritten case actually searched on, next to what
// it was asked.
//
// A query planner is argued about from the queries it wrote, and until now
// those only existed in the JSON — which means the argument was had from the
// scores instead, one level removed from the thing that produced them. Cases
// the planner left alone are not listed: they are the majority under every
// mode, and printing them would bury the handful that were changed.
func (r Report) writePlanning(b *strings.Builder) {
	var rewritten []CaseResult
	for _, c := range r.Cases {
		if plannedDiffers(c) {
			rewritten = append(rewritten, c)
		}
	}
	if len(rewritten) == 0 {
		return
	}

	fmt.Fprintf(b, "\n  planning — %d of %s searched on something other than the question\n",
		len(rewritten), Plural(len(r.Cases), "case"))
	for _, c := range rewritten {
		fmt.Fprintf(b, "    %s\n", c.ID)
		fmt.Fprintf(b, "      asked  %s\n", c.Query)
		for _, q := range c.Queries {
			fmt.Fprintf(b, "      ran    %s\n", q)
		}
	}
}

// plannedDiffers reports whether planning changed what was searched for. One
// query identical to the question is what every deterministic mode produces on
// a single-shot case, and it is not a rewrite.
func plannedDiffers(c CaseResult) bool {
	switch len(c.Queries) {
	case 0:
		return false
	case 1:
		return strings.TrimSpace(c.Queries[0]) != strings.TrimSpace(c.Query)
	default:
		return true
	}
}

// writeGeneration prints the generation half, when the stage ran.
//
// Every rate is printed with the number of answers behind it whenever that
// differs from the number scored, because these denominators genuinely differ
// case by case: a set where six of twelve cases carry must_include reports what
// those six scored, and a reader who assumed twelve would read it as half.
func (r Report) writeGeneration(b *strings.Builder) {
	if r.Generation == nil {
		return
	}
	g := r.Generation.Overall
	fmt.Fprintf(b, "\n  generation — %s\n", Plural(g.Cases, "answer"))
	fmt.Fprintf(b, "    includes %s   citations %s   groundedness %s\n",
		rateOver(g.Includes, g.WithIncludes, g.Cases),
		rateOver(g.Citations, g.WithCitations, g.Cases),
		rateOver(g.Groundedness, g.WithWords, g.Cases))
	if g.Judged > 0 {
		fmt.Fprintf(b, "    correctness %.2f   faithfulness %.2f   (%d of %d judged)\n",
			g.Correctness, g.Faithfulness, g.Judged, g.Cases)
	}
	fmt.Fprintf(b, "    latency  median %.0fms   p95 %.0fms\n",
		r.Generation.Latency.MedianMS, r.Generation.Latency.P95MS)
}

// rateOver renders a rate, naming its denominator when it is not the whole set.
// A rate over nothing prints as a dash rather than as 0.00: nothing asked for
// it, so nothing failed it.
func rateOver(rate float64, over, cases int) string {
	switch over {
	case 0:
		return "   —"
	case cases:
		return fmt.Sprintf("%.2f", rate)
	default:
		return fmt.Sprintf("%.2f (of %d)", rate, over)
	}
}

// writeGenerationCases prints the per-case generation rows, then whatever the
// judge said about them. The reasons are the point of a judged number: without
// them a 1 is an oracle, and an oracle cannot be argued with or debugged.
func (r Report) writeGenerationCases(b *strings.Builder) {
	fmt.Fprintf(b, "\n  %-28s %8s %8s %8s %6s %7s %8s\n",
		"answer", "includes", "cites", "grounded", "corr", "faith", "ms")
	for _, c := range r.Cases {
		if c.Generation == nil {
			continue
		}
		g := c.Generation
		fmt.Fprintf(b, "  %-28s %8.2f %8.2f %8.2f %6s %7s %8.0f\n",
			truncate(c.ID, 28), g.Includes, g.Citations, g.Groundedness,
			judgedCell(g.Correctness, g.Judged), judgedCell(g.Faithfulness, g.Judged), c.GenLatencyMS)
	}

	var judged, unjudged []CaseResult
	for _, c := range r.Cases {
		switch {
		case c.Judge != nil:
			judged = append(judged, c)
		case c.Unjudged != "":
			unjudged = append(unjudged, c)
		}
	}
	if len(judged) > 0 {
		b.WriteString("\n  judge:\n")
		for _, c := range judged {
			fmt.Fprintf(b, "    %s\n", c.ID)
			fmt.Fprintf(b, "      correctness %d — %s\n", c.Judge.Correctness.Score, orNoReason(c.Judge.Correctness.Reason))
			fmt.Fprintf(b, "      faithfulness %d — %s\n", c.Judge.Faithfulness.Score, orNoReason(c.Judge.Faithfulness.Reason))
		}
	}
	if len(unjudged) > 0 {
		b.WriteString("\n  unjudged:\n")
		for _, c := range unjudged {
			fmt.Fprintf(b, "    %-28s %s\n", truncate(c.ID, 28), c.Unjudged)
		}
	}

	var unresolved []CaseResult
	for _, c := range r.Cases {
		if len(c.UnresolvedCitations) > 0 {
			unresolved = append(unresolved, c)
		}
	}
	if len(unresolved) > 0 {
		b.WriteString("\n  citations naming no indexed document:\n")
		for _, c := range unresolved {
			fmt.Fprintf(b, "    %-28s %s\n", truncate(c.ID, 28), strings.Join(c.UnresolvedCitations, ", "))
		}
	}
}

// judgedCell renders a judged score, or a dash when the judge did not score it.
func judgedCell(v float64, judged int) string {
	if judged == 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f", v)
}

func orNoReason(s string) string {
	if s == "" {
		return "(no reason given)"
	}
	return s
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
	// PlanMedian is the query planning cost either side spent. Comparing
	// `window` to `condense` without it compares two prices as though they
	// were one.
	PlanMedian MetricDelta `json:"plan_median"`
	// Generation is empty unless both runs scored the generation stage —
	// there is no delta between a number and the absence of one.
	Generation       []MetricDelta `json:"generation,omitempty"`
	GenLatencyMedian *MetricDelta  `json:"generation_latency_median,omitempty"`
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
		PlanMedian:    newDelta("planning median", current.PlanLatency.MedianMS, baseline.PlanLatency.MedianMS),
	}

	if c.Cases != b.Cases {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"the two runs cover different numbers of cases (%d now, %d in the baseline), "+
				"so the averages are over different questions", c.Cases, b.Cases))
	}
	d.Generation, d.GenLatencyMedian = diffGeneration(current, baseline)
	if (current.Generation == nil) != (baseline.Generation == nil) {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"only one of the two runs scored the generation stage (%s now, %s in the baseline), "+
				"so there is nothing to compare on the answers",
			ranOrNot(current.Generation != nil), ranOrNot(baseline.Generation != nil)))
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
	if current.Degraded > 0 || baseline.Degraded > 0 {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"query planning fell back on %d of the current run's cases and %d of the baseline's: "+
				"a run that fell back is not measuring the planner it names",
			current.Degraded, baseline.Degraded))
	}
	if current.Run.Host != baseline.Run.Host {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"the run moved machine (%s, was %s): the quality numbers still compare, "+
				"the latency deltas do not",
			orNone(current.Run.Host), orNone(baseline.Run.Host)))
	}
	if current.Run.LLM != baseline.Run.LLM {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"the model changed (%s, was %s): generation scores and latency move with it",
			orNone(current.Run.LLM), orNone(baseline.Run.LLM)))
	}
	// A judge upgrade has to read as a judge upgrade. Without this the judged
	// numbers move, the deterministic ones do not, and the obvious conclusion
	// is about the answers rather than about the instrument.
	if current.Run.Judge != baseline.Run.Judge {
		d.Warnings = append(d.Warnings, fmt.Sprintf(
			"the judge changed (%s, was %s): correctness and faithfulness are two different "+
				"instruments here, and the deterministic scores are the ones still comparable",
			orNone(current.Run.Judge), orNone(baseline.Run.Judge)))
	}
	return d, nil
}

// diffGeneration compares the generation halves, or returns nothing when only
// one run has one.
func diffGeneration(current, baseline Report) ([]MetricDelta, *MetricDelta) {
	if current.Generation == nil || baseline.Generation == nil {
		return nil, nil
	}
	c, b := current.Generation.Overall, baseline.Generation.Overall
	deltas := []MetricDelta{
		newDelta("includes", c.Includes, b.Includes),
		newDelta("citations", c.Citations, b.Citations),
		newDelta("groundedness", c.Groundedness, b.Groundedness),
	}
	// The judged axes are compared only when both runs actually judged. A run
	// without --judge scores them zero, and a zero minus a zero is a delta that
	// says nothing while looking exactly like one that says something.
	if c.Judged > 0 && b.Judged > 0 {
		deltas = append(deltas,
			newDelta("correctness", c.Correctness, b.Correctness),
			newDelta("faithfulness", c.Faithfulness, b.Faithfulness))
	}
	latency := newDelta("gen latency median",
		current.Generation.Latency.MedianMS, baseline.Generation.Latency.MedianMS)
	return deltas, &latency
}

func ranOrNot(ran bool) string {
	if ran {
		return "it did"
	}
	return "it did not"
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
	for _, m := range d.Generation {
		fmt.Fprintf(&b, "  %-16s %9.2f %9.2f %+9.2f\n", m.Name, m.Current, m.Baseline, m.Delta)
	}
	latencies := []MetricDelta{d.LatencyMedian, d.LatencyP95}
	if d.GenLatencyMedian != nil {
		latencies = append(latencies, *d.GenLatencyMedian)
	}
	for _, m := range latencies {
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
