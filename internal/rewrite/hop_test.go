package rewrite_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/rewrite"
)

func passages() []string {
	return []string{"A slice grows by doubling until 256 elements.", "append reallocates when len == cap."}
}

// The point of the milestone: the model reads what came back and names what is
// still missing, and that becomes the next search.
func TestHop_namesWhatIsMissing(t *testing.T) {
	h := rewrite.Hop{Chat: chatReturning("map growth ", "factor")}
	got, err := h.Next(context.Background(), "how do maps and slices grow?", passages())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "map growth factor" {
		t.Errorf("query = %q, want %q", got, "map growth factor")
	}
}

// Nothing missing ends the loop. It is the answer that stops a token furnace,
// so it has to survive the padding a model puts around it.
func TestHop_nothingMissingStopsTheLoop(t *testing.T) {
	for _, raw := range []string{"NOTHING", "nothing", "  Nothing.  ", "\"NOTHING\"", "<think>hmm</think>NOTHING"} {
		t.Run(raw, func(t *testing.T) {
			h := rewrite.Hop{Chat: chatReturning(raw)}
			got, err := h.Next(context.Background(), "q", passages())
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if got != "" {
				t.Errorf("query = %q, want empty — the passages were enough", got)
			}
		})
	}
}

func TestHop_errors(t *testing.T) {
	tests := []struct {
		name    string
		hop     rewrite.Hop
		wantSub string
	}{
		{"no model", rewrite.Hop{}, "no model"},
		{"call refused", rewrite.Hop{Chat: chatFailing(errors.New("connection refused"))}, "connection refused"},
		{"stream breaks", rewrite.Hop{Chat: chatBreaking(errors.New("boom"))}, "boom"},
		{"empty completion", rewrite.Hop{Chat: chatReturning("   ")}, "returned nothing"},
		{
			"timeout",
			rewrite.Hop{Chat: chatHanging(), Timeout: 10 * time.Millisecond},
			"did not answer",
		},
		{
			"an answer, not a query",
			rewrite.Hop{Chat: chatReturning(strings.Repeat("x", rewrite.HopQueryLimit+1))},
			"longer than",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.hop.Next(context.Background(), "how do maps grow?", passages())
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

// A reasoning model given a budget sized for one query spends all of it
// thinking. The cause is worth naming rather than reading as an empty corpus.
func TestHop_reasoningWithoutAQueryNamesTheBudget(t *testing.T) {
	chat := func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		ch := make(chan llm.Token, 2)
		ch <- llm.Token{Reasoning: "let me think about what is missing"}
		ch <- llm.Token{Done: true}
		close(ch)
		return ch, nil
	}
	_, err := rewrite.Hop{Chat: chat}.Next(context.Background(), "q", passages())
	if err == nil || !strings.Contains(err.Error(), "reasoning") {
		t.Fatalf("error = %v, want one naming the reasoning budget", err)
	}
}

// The prompt has to carry both halves of the decision: the question, and what
// retrieval has already found. Without the passages the model cannot say what
// is missing, only what it would search for.
func TestHop_promptCarriesTheQuestionAndThePassages(t *testing.T) {
	var seen []llm.Message
	chat := func(_ context.Context, messages []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		seen = messages
		ch := make(chan llm.Token, 2)
		ch <- llm.Token{Text: "NOTHING", Done: true}
		close(ch)
		return ch, nil
	}
	if _, err := (rewrite.Hop{Chat: chat}).Next(context.Background(), "how do maps grow?", passages()); err != nil {
		t.Fatalf("Next: %v", err)
	}

	var all strings.Builder
	for _, m := range seen {
		all.WriteString(m.Content)
		all.WriteString("\n")
	}
	for _, want := range []string{"how do maps grow?", "A slice grows by doubling", "append reallocates"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("prompt is missing %q:\n%s", want, all.String())
		}
	}
}

// Retrieval can legitimately come back with nothing, and the hop is exactly
// what should happen next — so it asks, rather than refusing for want of
// passages to read.
func TestHop_worksWithNoPassagesAtAll(t *testing.T) {
	h := rewrite.Hop{Chat: chatReturning("go map internals")}
	got, err := h.Next(context.Background(), "how do maps grow?", nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != "go map internals" {
		t.Errorf("query = %q", got)
	}
}

func TestValidateHops(t *testing.T) {
	for _, tc := range []struct {
		n       int
		wantErr bool
	}{{0, false}, {1, false}, {rewrite.MaxHopsLimit, false}, {-1, true}, {rewrite.MaxHopsLimit + 1, true}} {
		err := rewrite.ValidateHops(tc.n)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateHops(%d) = %v, wantErr %v", tc.n, err, tc.wantErr)
		}
	}
}
