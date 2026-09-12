package cli_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/retrieval"
)

// scriptedHop answers each hop from a list, recording the passages it was shown.
type scriptedHop struct {
	replies []string
	err     error
	calls   int
	seen    [][]string
}

func (h *scriptedHop) Next(_ context.Context, _ string, passages []string) (string, error) {
	h.calls++
	h.seen = append(h.seen, passages)
	if h.err != nil {
		return "", h.err
	}
	if h.calls > len(h.replies) {
		return "", nil
	}
	return h.replies[h.calls-1], nil
}

// countingRetriever answers every search with one chunk and records the plans.
func countingRetriever(plans *[][]string) func(context.Context, []string, int, map[string]string) ([]retrieval.RetrievedChunk, error) {
	return func(_ context.Context, queries []string, _ int, _ map[string]string) ([]retrieval.RetrievedChunk, error) {
		*plans = append(*plans, append([]string(nil), queries...))
		return []retrieval.RetrievedChunk{{ChunkID: int64(len(*plans)), Text: "passage " + strings.Join(queries, "+")}}, nil
	}
}

// The default, and the one that must not change: no hops is the single search
// `tbuk ask` has always run.
func TestRetrieveWithHops_zeroHopsIsOneSearch(t *testing.T) {
	var plans [][]string
	hop := &scriptedHop{replies: []string{"never asked"}}

	chunks, queries, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
		"how do maps grow?", []string{"how do maps grow?"}, 5, cli.HopOptions{MaxHops: 0})
	if err != nil {
		t.Fatalf("RetrieveWithHops: %v", err)
	}
	if len(plans) != 1 {
		t.Errorf("searches = %d, want 1", len(plans))
	}
	if hop.calls != 0 {
		t.Errorf("hops = %d, want none — the loop is off", hop.calls)
	}
	if len(queries) != 1 || len(chunks) != 1 {
		t.Errorf("queries = %v, chunks = %d", queries, len(chunks))
	}
}

// The loop stops the moment the model says the passages are enough, rather
// than spending its whole bound.
func TestRetrieveWithHops_stopsWhenNothingIsMissing(t *testing.T) {
	var plans [][]string
	hop := &scriptedHop{replies: []string{"map growth factor", ""}}

	_, queries, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
		"how do maps grow?", []string{"how do maps grow?"}, 5, cli.HopOptions{MaxHops: 4})
	if err != nil {
		t.Fatalf("RetrieveWithHops: %v", err)
	}
	if hop.calls != 2 {
		t.Errorf("hops = %d, want 2 — one that found a gap, one that did not", hop.calls)
	}
	if len(plans) != 2 {
		t.Errorf("searches = %d, want 2", len(plans))
	}
	// The second search carries both queries, so the fusion ranks them
	// together rather than folding one ranked list into another.
	if len(queries) != 2 || queries[1] != "map growth factor" {
		t.Errorf("queries = %v", queries)
	}
	if len(plans[1]) != 2 {
		t.Errorf("second search ran on %v, want both queries", plans[1])
	}
}

func TestRetrieveWithHops_stopsAtTheBound(t *testing.T) {
	var plans [][]string
	hop := &scriptedHop{replies: []string{"one", "two", "three", "four"}}

	_, queries, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
		"q", []string{"q"}, 5, cli.HopOptions{MaxHops: 2})
	if err != nil {
		t.Fatalf("RetrieveWithHops: %v", err)
	}
	if hop.calls != 2 {
		t.Errorf("hops = %d, want the bound of 2", hop.calls)
	}
	if len(queries) != 3 {
		t.Errorf("queries = %v, want the question and two follow-ups", queries)
	}
}

// The budget is re-checked every hop rather than once at the end: a loop that
// discovers it has overflowed after four hops has spent four hops.
func TestRetrieveWithHops_stopsWhenThePromptIsAlreadyFull(t *testing.T) {
	var plans [][]string
	hop := &scriptedHop{replies: []string{"more", "more still"}}
	var warn strings.Builder

	_, queries, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
		"q", []string{"q"}, 5, cli.HopOptions{
			MaxHops: 3,
			Warn:    &warn,
			Fits:    func([]retrieval.RetrievedChunk) bool { return false },
		})
	if err != nil {
		t.Fatalf("RetrieveWithHops: %v", err)
	}
	if hop.calls != 0 {
		t.Errorf("hops = %d, want none — there is no room for what they would find", hop.calls)
	}
	if len(queries) != 1 {
		t.Errorf("queries = %v", queries)
	}
	if !strings.Contains(warn.String(), "context") {
		t.Errorf("warning = %q, want it to say the budget stopped the loop", warn.String())
	}
}

// A hop that fails is not an ask that fails: the passages already retrieved
// are the floor, and answering from them is exactly what --hops 0 does.
func TestRetrieveWithHops_aFailedHopLeavesTheAnswerStanding(t *testing.T) {
	var plans [][]string
	hop := &scriptedHop{err: errors.New("the model did not answer within 20s")}
	var warn strings.Builder

	chunks, queries, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
		"q", []string{"q"}, 5, cli.HopOptions{MaxHops: 3, Warn: &warn})
	if err != nil {
		t.Fatalf("a failed hop must not fail the ask: %v", err)
	}
	if len(chunks) != 1 || len(queries) != 1 {
		t.Errorf("chunks = %d, queries = %v, want what the first search found", len(chunks), queries)
	}
	if !strings.Contains(warn.String(), "did not answer") {
		t.Errorf("warning = %q, want the reason the loop stopped", warn.String())
	}
}

// Retrieving twice on one query fuses a list with itself and moves nothing.
func TestRetrieveWithHops_ignoresAQueryItAlreadyRan(t *testing.T) {
	var plans [][]string
	hop := &scriptedHop{replies: []string{"  How Do Maps GROW? ", "still the same"}}
	var warn strings.Builder

	_, queries, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
		"how do maps grow?", []string{"how do maps grow?"}, 5, cli.HopOptions{MaxHops: 3, Warn: &warn})
	if err != nil {
		t.Fatalf("RetrieveWithHops: %v", err)
	}
	if len(queries) != 1 || len(plans) != 1 {
		t.Errorf("queries = %v, searches = %d, want the repeat dropped", queries, len(plans))
	}
	if hop.calls != 1 {
		t.Errorf("hops = %d, want the loop to stop rather than ask again", hop.calls)
	}
}

// The model can only say what is missing if it is shown what was found.
func TestRetrieveWithHops_showsTheHopWhatWasRetrieved(t *testing.T) {
	var plans [][]string
	hop := &scriptedHop{replies: []string{""}}

	if _, _, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
		"q", []string{"q"}, 5, cli.HopOptions{MaxHops: 1}); err != nil {
		t.Fatalf("RetrieveWithHops: %v", err)
	}
	if len(hop.seen) != 1 || len(hop.seen[0]) != 1 || !strings.Contains(hop.seen[0][0], "passage q") {
		t.Errorf("the hop was shown %v, want the retrieved text", hop.seen)
	}
}

func TestRetrieveWithHops_retrieveErrorFailsTheAsk(t *testing.T) {
	boom := errors.New("no such table: chunks")
	retrieve := func(context.Context, []string, int, map[string]string) ([]retrieval.RetrievedChunk, error) {
		return nil, boom
	}
	if _, _, err := cli.RetrieveWithHops(context.Background(), retrieve, &scriptedHop{},
		"q", []string{"q"}, 5, cli.HopOptions{MaxHops: 2}); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the retrieval failure", err)
	}
}

func TestRetrieveWithHops_noHopperIsASingleShot(t *testing.T) {
	var plans [][]string
	_, _, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), nil,
		"q", []string{"q"}, 5, cli.HopOptions{MaxHops: 3})
	if err != nil {
		t.Fatalf("RetrieveWithHops: %v", err)
	}
	if len(plans) != 1 {
		t.Errorf("searches = %d, want 1", len(plans))
	}
}

// A hop that failed and a hop that decided nothing was missing look identical
// in the report unless the loop says which it was: the first measured nothing,
// the second measured what it set out to.
func TestRetrieveWithHops_reportsOnlyRealFailures(t *testing.T) {
	t.Run("a failed hop is reported", func(t *testing.T) {
		var plans [][]string
		var gaveUp []string
		hop := &scriptedHop{err: errors.New("the model stream failed")}

		if _, _, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
			"q", []string{"q"}, 5, cli.HopOptions{
				MaxHops:  2,
				OnGiveUp: func(reason string) { gaveUp = append(gaveUp, reason) },
			}); err != nil {
			t.Fatalf("RetrieveWithHops: %v", err)
		}
		if len(gaveUp) != 1 || !strings.Contains(gaveUp[0], "stream failed") {
			t.Errorf("gave up = %v, want the model failure", gaveUp)
		}
	})

	t.Run("nothing missing is not a failure", func(t *testing.T) {
		var plans [][]string
		var gaveUp []string
		hop := &scriptedHop{replies: []string{""}}

		if _, _, err := cli.RetrieveWithHops(context.Background(), countingRetriever(&plans), hop,
			"q", []string{"q"}, 5, cli.HopOptions{
				MaxHops:  2,
				OnGiveUp: func(reason string) { gaveUp = append(gaveUp, reason) },
			}); err != nil {
			t.Fatalf("RetrieveWithHops: %v", err)
		}
		if len(gaveUp) != 0 {
			t.Errorf("gave up = %v, want none — the loop finished on purpose", gaveUp)
		}
	})
}
