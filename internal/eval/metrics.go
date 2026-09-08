package eval

import (
	"math"
	"sort"
)

// Metrics is how one case, or a whole set, scored on retrieval.
//
// The rates are what a change is argued from; the counts are what stops the
// rates being misread. Precision at a k larger than the number of labels is
// bounded above by labels/k, which is a property of the label set and not of
// the retriever, so a report that prints the rate without the counts invites
// someone to go looking for a bug that is not there.
type Metrics struct {
	Hit       float64 `json:"hit"`
	Recall    float64 `json:"recall"`
	Precision float64 `json:"precision"`
	MRR       float64 `json:"mrr"`
	NDCG      float64 `json:"ndcg"`

	Cases     int `json:"cases"`
	Labels    int `json:"labels"`
	Retrieved int `json:"retrieved"`
	Found     int `json:"found"` // distinct labels satisfied within the cutoff
}

// Score marks one ranked list against the labels for its case, cut at depth k.
// A k of zero or less scores the whole list.
//
// A passage below the cutoff never reached the prompt, so it does not reach the
// score either — which is what makes the numbers comparable to what the model
// actually saw.
//
// Each label is credited at most once. Two chunks of one labelled document are
// both genuinely relevant and both count towards precision, but only the first
// earns gain: otherwise nDCG could exceed 1, and a retriever could score well
// by returning the same document twice.
//
// A case with no labels scores zero rather than perfect. It is a
// generation-only case, and asking what its retrieval was worth is a question
// with no answer; the caller decides which cases belong in a retrieval average.
func Score(results []Result, labels []Label, k int) Metrics {
	cut := k
	if cut <= 0 {
		cut = len(results)
	}
	if cut > len(results) {
		results = results[:len(results):len(results)]
	} else {
		results = results[:cut:cut]
	}

	m := Metrics{Cases: 1, Labels: len(labels), Retrieved: len(results)}
	if len(labels) == 0 || len(results) == 0 {
		return m
	}

	credited := make([]bool, len(labels))
	firstRelevant := -1
	relevantRanks := 0
	var dcg float64

	for rank, r := range results {
		matched := false
		best, bestGrade := -1, 0
		for i, l := range labels {
			if !l.Matches(r) {
				continue
			}
			matched = true
			if g := gradeOf(l); !credited[i] && g > bestGrade {
				best, bestGrade = i, g
			}
		}
		if !matched {
			continue
		}
		relevantRanks++
		if firstRelevant < 0 {
			firstRelevant = rank
		}
		if best >= 0 {
			credited[best] = true
			dcg += gain(bestGrade) / math.Log2(float64(rank+2))
		}
	}

	for _, ok := range credited {
		if ok {
			m.Found++
		}
	}
	m.Recall = float64(m.Found) / float64(len(labels))
	m.Precision = float64(relevantRanks) / float64(len(results))
	if firstRelevant >= 0 {
		m.Hit = 1
		m.MRR = 1 / float64(firstRelevant+1)
	}
	m.NDCG = dcg / idealDCG(labels, cut)
	return m
}

// idealDCG is the gain of the best ranking the label set allows within the
// cutoff. Bounded by the cutoff rather than by how many results came back, so a
// retriever that returns one passage and gets it right does not score a perfect
// nDCG on a set of five labels.
func idealDCG(labels []Label, cut int) float64 {
	grades := make([]int, len(labels))
	for i, l := range labels {
		grades[i] = gradeOf(l)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	if cut < len(grades) {
		grades = grades[:cut]
	}
	var idcg float64
	for i, g := range grades {
		idcg += gain(g) / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 1 // nothing to gain, so nothing to normalise against
	}
	return idcg
}

// gain is the standard exponential gain, so a grade-2 passage is worth more
// than two grade-1 ones rather than exactly two.
func gain(grade int) float64 { return math.Pow(2, float64(grade)) - 1 }

// gradeOf reads an unset grade as 1. Parsing already fills the default, but
// Score is also called with labels built in code, and a silently ungraded
// label would contribute no gain at all.
func gradeOf(l Label) int {
	if l.Grade <= 0 {
		return 1
	}
	return l.Grade
}

// Aggregate macro-averages per-case metrics: the case is the unit, so a case
// with eight labels does not outvote one with two. Counts are summed, because
// they answer how many rather than how well.
//
// It is meant for the output of Score, not for its own output.
func Aggregate(ms []Metrics) Metrics {
	if len(ms) == 0 {
		return Metrics{}
	}
	var out Metrics
	for _, m := range ms {
		out.Hit += m.Hit
		out.Recall += m.Recall
		out.Precision += m.Precision
		out.MRR += m.MRR
		out.NDCG += m.NDCG
		out.Labels += m.Labels
		out.Retrieved += m.Retrieved
		out.Found += m.Found
	}
	n := float64(len(ms))
	out.Hit /= n
	out.Recall /= n
	out.Precision /= n
	out.MRR /= n
	out.NDCG /= n
	out.Cases = len(ms)
	return out
}
