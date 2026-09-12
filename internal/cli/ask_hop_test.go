package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/retrieval"
)

// The milestone, end to end through the ask path: the model names what the
// first search missed, and the second search runs on both queries.
func TestRunAsk_hopsRetrieveAgain(t *testing.T) {
	var out bytes.Buffer
	var plans [][]string
	hop := &scriptedHop{replies: []string{"map growth factor", ""}}

	err := cli.RunAsk(context.Background(), &out, countingRetriever(&plans),
		mockChat([]string{"ok"}, nil), buildQATemplate(t), "how do maps and slices grow?", nil, 0, false,
		cli.WithHops(2, hop))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("searches = %d, want 2", len(plans))
	}
	if len(plans[1]) != 2 || plans[1][1] != "map growth factor" {
		t.Errorf("second search ran on %v, want the question and the follow-up", plans[1])
	}
}

// Off by default, and off means byte-identical: one search on the question, no
// model call spent deciding anything about it.
func TestRunAsk_withoutHopsIsOneSearch(t *testing.T) {
	var out bytes.Buffer
	var plans [][]string
	hop := &scriptedHop{replies: []string{"never asked"}}

	err := cli.RunAsk(context.Background(), &out, countingRetriever(&plans),
		mockChat([]string{"ok"}, nil), buildQATemplate(t), "how do maps grow?", nil, 0, false,
		cli.WithHops(0, hop))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(plans) != 1 || hop.calls != 0 {
		t.Errorf("searches = %d, hops = %d, want 1 and 0", len(plans), hop.calls)
	}
}

// The budget is re-checked per hop, and the ask is the only place that knows
// what the budget is: a window this small has no room for a second round.
func TestRunAsk_hopsStopAtTheContextBudget(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	// The template reserves 2048 tokens for the reply, so this leaves ~250 for
	// the prompt — and the passage already retrieved is bigger than that.
	big := retrieval.RetrievedChunk{
		Path: "a.md", Citation: "a.md §0", Text: strings.Repeat("token ", 400),
	}
	retrieve := func(context.Context, []string, int, map[string]string) ([]retrieval.RetrievedChunk, error) {
		return []retrieval.RetrievedChunk{big}, nil
	}
	hop := &scriptedHop{replies: []string{"more please"}}

	err := cli.RunAsk(context.Background(), &out, retrieve,
		mockChat([]string{"ok"}, nil), buildQATemplate(t), "how do maps grow?", nil, 0, false,
		cli.WithHops(3, hop), cli.WithContextBudget(2300, 100), cli.WithErrOut(&errOut))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if hop.calls != 0 {
		t.Errorf("hops = %d, want none — the passages already fill the window", hop.calls)
	}
	if !strings.Contains(errOut.String(), "context budget") {
		t.Errorf("stderr = %q, want the loop to say the budget stopped it", errOut.String())
	}
}

// A turn records what retrieval ran, and under a loop that is every hop's
// query — otherwise `session show --verbose` explains the first search and not
// the answer.
func TestRunAsk_recordsTheHopQueriesInTheTurn(t *testing.T) {
	var out bytes.Buffer
	var plans [][]string
	rec := &recorder{}
	hop := &scriptedHop{replies: []string{"map growth factor", ""}}

	err := cli.RunAsk(context.Background(), &out, countingRetriever(&plans),
		mockChat([]string{"ok"}, nil), buildQATemplate(t), "how do maps grow?", nil, 0, false,
		cli.WithSession(threadOf(), rec.append), cli.WithHops(2, hop))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(rec.turns) != 1 {
		t.Fatalf("recorded %d turns", len(rec.turns))
	}
	if !strings.Contains(rec.turns[0].Query, "map growth factor") {
		t.Errorf("recorded query = %q, want the hop's search in it", rec.turns[0].Query)
	}
}
