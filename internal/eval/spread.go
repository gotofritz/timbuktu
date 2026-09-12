package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

// MetricSpread is one number across repeated runs of the same sweep.
//
// The runs are kept alongside the summary because three numbers are few enough
// to read, and a mean with a standard deviation over three samples hides
// whether the middle run sat between the other two or on top of one of them.
type MetricSpread struct {
	Name string    `json:"name"`
	Runs []float64 `json:"runs"`
	Min  float64   `json:"min"`
	Max  float64   `json:"max"`
	Mean float64   `json:"mean"`
	// StdDev is the sample standard deviation, over n-1: these runs are a
	// sample of what the sweep does, not the population of everything it could
	// have done.
	StdDev float64 `json:"stddev"`
}

// Span is how far the metric moved between its best and worst run — the number
// a claimed gain has to clear before it is a gain rather than a run.
func (m MetricSpread) Span() float64 { return m.Max - m.Min }

// CaseSpread is one case's hit across the runs, and the distinct queries it
// retrieved on.
//
// An average over a small set moves in whole cases, so "the number went up" is
// only ever "these cases flipped". The queries are what flipped them: under a
// model-written rewrite the same question is not the same search twice.
type CaseSpread struct {
	ID string `json:"id"`
	// Query is the question as the set asks it, kept beside the plans so the
	// rewrite can be read against what it was given without opening the set.
	Query string    `json:"query,omitempty"`
	Hit   []float64 `json:"hit"`
	// Queries are the distinct plans this case ran on, in the order first seen.
	// One entry means the planner produced the same search every time, whatever
	// else moved.
	Queries []string `json:"queries,omitempty"`
	// Moved is whether this case scored differently between runs.
	Moved bool `json:"moved,omitempty"`
}

// Spread is what repeating one sweep produced.
//
// Every other knob in this harness is deterministic, so a re-run is a proof of
// that determinism and nothing more. The knobs that spend a model are the ones
// worth repeating, and a single run of one of those is an anecdote: it cannot
// say whether a gain is the setting or the sampling.
type Spread struct {
	Set string `json:"set"`
	// Run is the first run's instrument block. The warnings say when the
	// others did not match it.
	Run           Run            `json:"run"`
	Runs          int            `json:"runs"`
	Metrics       []MetricSpread `json:"metrics"`
	LatencyMedian MetricSpread   `json:"latency_median"`
	PlanMedian    MetricSpread   `json:"plan_median"`
	// DegradedPerRun counts, per run, the cases whose query planning fell back.
	// Zeroes are reported rather than omitted: that every run held up is the
	// evidence that the spread is the planner's and not the fallback's.
	DegradedPerRun []int `json:"degraded_per_run"`
	// Cases is every case scored by every run, in the first run's order — not
	// only the ones that moved. A set whose halves are read apart (follow-ups
	// against single-shot, say) can only be re-split from the rows, so the
	// spread carries them rather than the averages alone.
	Cases    []CaseSpread `json:"cases"`
	Warnings []string     `json:"warnings,omitempty"`
}

// Stable reports whether the quality metrics came out identical every run.
//
// Latency is deliberately not part of it: it moves with the machine on every
// run of everything, and folding it in would make every spread unstable and
// the word useless.
func (s Spread) Stable() bool {
	for _, m := range s.Metrics {
		if m.StdDev != 0 || m.Span() != 0 {
			return false
		}
	}
	return len(s.Unstable()) == 0
}

// Unstable are the cases that did not score the same every run. An average
// over a small set moves in whole cases, so these are what a change in the
// headline is actually made of.
func (s Spread) Unstable() []CaseSpread {
	var out []CaseSpread
	for _, c := range s.Cases {
		if c.Moved {
			out = append(out, c)
		}
	}
	return out
}

// NewSpread summarises repeated runs of one sweep.
//
// Fewer than two runs is an error rather than a degenerate spread: a report of
// one run with a standard deviation of zero states something false in the
// language of a measurement.
func NewSpread(reports []Report) (Spread, error) {
	if len(reports) < 2 {
		return Spread{}, fmt.Errorf(
			"eval: a spread needs at least two runs of the same sweep, got %s", Plural(len(reports), "run"))
	}
	first := reports[0]
	for _, r := range reports[1:] {
		if !strings.EqualFold(strings.TrimSpace(r.Set), strings.TrimSpace(first.Set)) {
			return Spread{}, fmt.Errorf(
				"eval: a run scored label set %q but the first scored %q; "+
					"repeating a sweep means repeating it over one set", r.Set, first.Set)
		}
	}

	pick := func(f func(Report) float64) []float64 {
		xs := make([]float64, len(reports))
		for i, r := range reports {
			xs[i] = f(r)
		}
		return xs
	}
	s := Spread{
		Set:  first.Set,
		Run:  first.Run,
		Runs: len(reports),
		Metrics: []MetricSpread{
			newMetricSpread("hit", pick(func(r Report) float64 { return r.Overall.Hit })),
			newMetricSpread("recall", pick(func(r Report) float64 { return r.Overall.Recall })),
			newMetricSpread("precision", pick(func(r Report) float64 { return r.Overall.Precision })),
			newMetricSpread("mrr", pick(func(r Report) float64 { return r.Overall.MRR })),
			newMetricSpread("ndcg", pick(func(r Report) float64 { return r.Overall.NDCG })),
		},
		LatencyMedian: newMetricSpread("latency median",
			pick(func(r Report) float64 { return r.Latency.MedianMS })),
		PlanMedian: newMetricSpread("planning median",
			pick(func(r Report) float64 { return r.PlanLatency.MedianMS })),
		DegradedPerRun: make([]int, len(reports)),
	}
	for i, r := range reports {
		s.DegradedPerRun[i] = r.Degraded
	}
	s.Cases = caseSpreads(reports)
	s.Warnings = spreadWarnings(reports)
	return s, nil
}

func newMetricSpread(name string, xs []float64) MetricSpread {
	m := MetricSpread{Name: name, Runs: xs}
	if len(xs) == 0 {
		return m
	}
	m.Min, m.Max = xs[0], xs[0]
	var sum float64
	for _, x := range xs {
		sum += x
		m.Min = math.Min(m.Min, x)
		m.Max = math.Max(m.Max, x)
	}
	m.Mean = sum / float64(len(xs))
	if len(xs) < 2 {
		return m
	}
	var sq float64
	for _, x := range xs {
		sq += (x - m.Mean) * (x - m.Mean)
	}
	m.StdDev = math.Sqrt(sq / float64(len(xs)-1))
	return m
}

// caseSpreads collects each case's score across the runs, in the first run's
// order.
//
// A case some run did not score is left out rather than compared against a zero
// it never earned; the differing case counts are a warning of their own.
func caseSpreads(reports []Report) []CaseSpread {
	byID := make([]map[string]CaseResult, len(reports))
	for i, r := range reports {
		byID[i] = make(map[string]CaseResult, len(r.Cases))
		for _, c := range r.Cases {
			byID[i][c.ID] = c
		}
	}

	var out []CaseSpread
	for _, c := range reports[0].Cases {
		if c.Metrics.Cases == 0 {
			continue
		}
		spread := CaseSpread{ID: c.ID, Query: c.Query, Hit: make([]float64, 0, len(reports))}
		complete := true
		for i := range reports {
			got, ok := byID[i][c.ID]
			if !ok {
				complete = false
				break
			}
			spread.Hit = append(spread.Hit, got.Metrics.Hit)
			spread.Queries = addDistinct(spread.Queries, strings.Join(got.Queries, " | "))
		}
		if !complete {
			continue
		}
		spread.Moved = !sameFloats(spread.Hit)
		out = append(out, spread)
	}
	return out
}

func addDistinct(xs []string, s string) []string {
	if strings.TrimSpace(s) == "" {
		return xs
	}
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

func sameFloats(xs []float64) bool {
	for _, x := range xs {
		if x != xs[0] {
			return false
		}
	}
	return true
}

// spreadWarnings names the ways the runs were not repetitions of one another.
// A spread over two different instruments measures the instruments.
func spreadWarnings(reports []Report) []string {
	var out []string
	first := reports[0]
	for i, r := range reports[1:] {
		n := i + 2 // the run's position, counting from one
		if r.Run.Embedding != first.Run.Embedding {
			out = append(out, fmt.Sprintf(
				"run %d used a different embedding model (%s, was %s): this is a spread over two instruments",
				n, orNone(r.Run.Embedding), orNone(first.Run.Embedding)))
		}
		if r.Run.LLM != first.Run.LLM {
			out = append(out, fmt.Sprintf(
				"run %d used a different model (%s, was %s): this is a spread over two instruments",
				n, orNone(r.Run.LLM), orNone(first.Run.LLM)))
		}
		if r.Run.Host != first.Run.Host {
			out = append(out, fmt.Sprintf(
				"run %d ran on a different machine (%s, was %s): the quality numbers still compare, the latencies do not",
				n, orNone(r.Run.Host), orNone(first.Run.Host)))
		}
		if r.Run.Rewrite != first.Run.Rewrite || r.Run.Expand != first.Run.Expand ||
			r.Run.Mode != first.Run.Mode || r.Run.TopK != first.Run.TopK {
			out = append(out, fmt.Sprintf(
				"run %d swept a different setting (mode %s, top %d, rewrite %s, expand %d): "+
					"repeating a sweep means repeating it unchanged",
				n, r.Run.Mode, r.Run.TopK, orNone(r.Run.Rewrite), r.Run.Expand))
		}
		if r.Overall.Cases != first.Overall.Cases {
			out = append(out, fmt.Sprintf(
				"run %d scored %d cases and the first scored %d, so the averages are over different questions",
				n, r.Overall.Cases, first.Overall.Cases))
		}
	}
	var degraded int
	for _, r := range reports {
		degraded += r.Degraded
	}
	if degraded > 0 {
		out = append(out, fmt.Sprintf(
			"query planning fell back on %d cases across the runs: a run that fell back is "+
				"not measuring the planner it names, and its numbers are the fallback's",
			degraded))
	}
	return out
}

// WriteJSON writes the spread as indented JSON.
func (s Spread) WriteJSON(w io.Writer) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("eval: render spread as JSON: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("eval: write spread: %w", err)
	}
	return nil
}

// WriteText writes the spread for a person.
func (s Spread) WriteText(w io.Writer) error {
	var b strings.Builder
	k := s.Run.TopK

	fmt.Fprintf(&b, "%s — %s, %s each\n", s.Set, Plural(s.Runs, "run"), Plural(len(s.Cases), "case"))
	fmt.Fprintf(&b, "  mode %s   top %d", s.Run.Mode, k)
	if s.Run.Rewrite != "" {
		fmt.Fprintf(&b, "   rewrite %s", s.Run.Rewrite)
	}
	if s.Run.Expand > 0 {
		fmt.Fprintf(&b, "   expand %d", s.Run.Expand)
	}
	b.WriteString("\n")
	if s.Run.Embedding != "" {
		fmt.Fprintf(&b, "  embedding %s\n", s.Run.Embedding)
	}
	if s.Run.LLM != "" {
		fmt.Fprintf(&b, "  llm %s\n", s.Run.LLM)
	}
	if s.Run.Host != "" {
		fmt.Fprintf(&b, "  host %s\n", s.Run.Host)
	}
	for _, warning := range s.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", warning)
	}

	fmt.Fprintf(&b, "\n  %-16s %7s %7s %7s %8s %8s\n", "metric", "mean", "min", "max", "stddev", "span")
	for _, m := range s.Metrics {
		fmt.Fprintf(&b, "  %-16s %7.3f %7.3f %7.3f %8.3f %8.3f\n",
			metricLabel(m.Name, k), m.Mean, m.Min, m.Max, m.StdDev, m.Span())
	}
	fmt.Fprintf(&b, "  %-16s %7.0f %7.0f %7.0f %8.0f %8.0f   ms\n", "latency median",
		s.LatencyMedian.Mean, s.LatencyMedian.Min, s.LatencyMedian.Max, s.LatencyMedian.StdDev, s.LatencyMedian.Span())
	if s.PlanMedian.Max > 0 {
		fmt.Fprintf(&b, "  %-16s %7.0f %7.0f %7.0f %8.0f %8.0f   ms\n", "planning median",
			s.PlanMedian.Mean, s.PlanMedian.Min, s.PlanMedian.Max, s.PlanMedian.StdDev, s.PlanMedian.Span())
	}

	if n := len(s.Cases); n > 0 {
		fmt.Fprintf(&b, "\n  note: %s, so one case moving is ±%.3f on hit\n",
			Plural(n, "case"), 1/float64(n))
	}

	s.writeCases(&b)

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("eval: write spread: %w", err)
	}
	return nil
}

// writeCases names the cases that moved, or states that none did.
func (s Spread) writeCases(b *strings.Builder) {
	if s.Stable() {
		fmt.Fprintf(b, "  every metric identical across %s — nothing here varies\n", Plural(s.Runs, "run"))
		return
	}
	moved := s.Unstable()
	if len(moved) == 0 {
		return
	}
	fmt.Fprintf(b, "\n  %d of %s changed between runs:\n", len(moved), Plural(len(s.Cases), "case"))
	for _, c := range moved {
		fmt.Fprintf(b, "    %-28s", truncate(c.ID, 28))
		for _, h := range c.Hit {
			fmt.Fprintf(b, " %4.2f", h)
		}
		b.WriteString("\n")
		// One plan every run explains nothing about the movement, so printing
		// it would suggest that it did.
		if len(c.Queries) > 1 {
			for _, q := range c.Queries {
				fmt.Fprintf(b, "      ran on: %s\n", q)
			}
		}
	}
}

// metricLabel prints the cutoff alongside the metrics that have one, so a
// column of numbers cannot be read at the wrong depth.
func metricLabel(name string, k int) string {
	switch name {
	case "hit", "recall", "precision", "ndcg":
		return fmt.Sprintf("%s@%d", name, k)
	default:
		return name
	}
}
