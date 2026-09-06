package rewrite_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/rewrite"
)

// expander builds the planner these tests drive: a window over the thread, N
// paraphrases from a scripted model.
func expander(n int, chat rewrite.ChatFn, warn *strings.Builder) rewrite.Expand {
	e := rewrite.Expand{
		Base: rewrite.Window{Turns: rewrite.DefaultWindowTurns},
		Chat: chat,
		N:    n,
	}
	// Assigned only when there is one: a typed nil in an io.Writer is not a nil
	// writer, and "nil is silence" is the contract the planner documents.
	if warn != nil {
		e.Warn = warn
	}
	return e
}

// The planned query comes first and the paraphrases follow it: expansion adds
// wordings, it does not replace the one the rest of the plan arrived at.
func TestExpand_addsParaphrasesAfterTheBaseQuery(t *testing.T) {
	chat := chatReturning("how does a slice reallocate?\nwhen does append copy the backing array?\n")
	got, err := expander(2, chat, nil).Queries(context.Background(), nil, "how do slices grow?")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	want := []string{
		"how do slices grow?",
		"how does a slice reallocate?",
		"when does append copy the backing array?",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("queries =\n %q\nwant\n %q", got, want)
	}
}

// It expands what the thread planned, not what was typed: inside a session the
// paraphrases are of the folded query, so they carry the topic too.
func TestExpand_expandsThePlannedQuery(t *testing.T) {
	var asked string
	chat := func(_ context.Context, messages []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		asked = messages[len(messages)-1].Content
		return chatReturning("go map growth")(context.Background(), messages)
	}
	th := []conversation.Turn{{Question: "how do slices grow?", Answer: "they double."}}

	if _, err := expander(1, chat, nil).Queries(context.Background(), th, "and maps?"); err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if !strings.Contains(asked, "how do slices grow? and maps?") {
		t.Errorf("the model was asked to paraphrase %q, want the window's folded query", asked)
	}
}

func TestExpand_cleansTheCompletion(t *testing.T) {
	tests := []struct {
		name       string
		completion string
		want       []string
	}{
		{"plain lines", "slice growth\nappend reallocation", []string{"slice growth", "append reallocation"}},
		{"a numbered list", "1. slice growth\n2. append reallocation",
			[]string{"slice growth", "append reallocation"}},
		{"a bulleted list", "- slice growth\n* append reallocation\n• capacity doubling",
			[]string{"slice growth", "append reallocation", "capacity doubling"}},
		{"quoted lines", "\"slice growth\"\n'append reallocation'",
			[]string{"slice growth", "append reallocation"}},
		{"blank lines between", "slice growth\n\n\nappend reallocation",
			[]string{"slice growth", "append reallocation"}},
		{"trailing whitespace", "  slice growth  \n\tappend reallocation\t",
			[]string{"slice growth", "append reallocation"}},
		// A list marker is a number followed by a separator and a space. The
		// digits a query opens with are part of the query.
		{"a query that starts with a number", "2024 budget report\n3.5 inch floppy capacity",
			[]string{"2024 budget report", "3.5 inch floppy capacity"}},
		{"a numbered list of numbered things", "1. 2024 budget report\n2) 1099 filing deadline",
			[]string{"2024 budget report", "1099 filing deadline"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expander(3, chatReturning(tt.completion), nil).
				Queries(context.Background(), nil, "how do slices grow?")
			if err != nil {
				t.Fatalf("Queries: %v", err)
			}
			want := append([]string{"how do slices grow?"}, tt.want...)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("queries =\n %q\nwant\n %q", got, want)
			}
		})
	}
}

// N is a ceiling. A model that returns eight paraphrases for a request of two
// has not earned six extra searches per question.
func TestExpand_capsTheParaphrasesAtN(t *testing.T) {
	chat := chatReturning("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight")

	got, err := expander(2, chat, nil).Queries(context.Background(), nil, "q")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d queries, want the base plus 2: %q", len(got), got)
	}
}

// A paraphrase identical to another query is a search run twice for one
// opinion — and in RRF, a duplicate list is a vote counted twice.
func TestExpand_deduplicates(t *testing.T) {
	tests := []struct {
		name       string
		completion string
		want       []string
	}{
		{"a repeat of the base query", "how do slices grow?\nslice growth", []string{"slice growth"}},
		{"a repeat in a different case", "HOW DO SLICES GROW?\nslice growth", []string{"slice growth"}},
		{"the same paraphrase twice", "slice growth\nslice growth", []string{"slice growth"}},
		{"differing only in whitespace", "slice   growth\nslice growth", []string{"slice growth"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expander(4, chatReturning(tt.completion), nil).
				Queries(context.Background(), nil, "how do slices grow?")
			if err != nil {
				t.Fatalf("Queries: %v", err)
			}
			want := append([]string{"how do slices grow?"}, tt.want...)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("queries =\n %q\nwant\n %q", got, want)
			}
		})
	}
}

// Same rule as condense (D6): an LLM in the retrieval path is a new way for
// `ask` to be slow, wrong or down, and none of those may cost the answer. A
// failed expansion is one query instead of three, never an error.
func TestExpand_fallsBackToTheBaseQuery(t *testing.T) {
	tests := []struct {
		name string
		chat rewrite.ChatFn
		warn string
	}{
		{"the call fails", chatFailing(errors.New("connection refused")), "connection refused"},
		{"the stream breaks", chatBreaking(errors.New("stream reset")), "stream reset"},
		{"nothing comes back", chatReturning(""), "nothing"},
		{"only whitespace comes back", chatReturning("   \n\n  "), "nothing"},
		{"no model is configured", nil, "no model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log strings.Builder
			got, err := expander(3, tt.chat, &log).Queries(context.Background(), nil, "how do slices grow?")
			if err != nil {
				t.Fatalf("Queries: %v", err)
			}
			if want := []string{"how do slices grow?"}; !reflect.DeepEqual(got, want) {
				t.Errorf("queries = %q, want the base query alone %q", got, want)
			}
			if !strings.Contains(log.String(), "warning:") || !strings.Contains(log.String(), tt.warn) {
				t.Errorf("warning %q does not say what went wrong (%q)", log.String(), tt.warn)
			}
		})
	}
}

// An expander that hangs would hang the ask; the base query is standing right
// there.
func TestExpand_timesOut(t *testing.T) {
	var log strings.Builder
	e := expander(3, chatHanging(), &log)
	e.Timeout = 20 * time.Millisecond

	start := time.Now()
	got, err := e.Queries(context.Background(), nil, "how do slices grow?")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %s, want the timeout to cut it short", elapsed)
	}
	if len(got) != 1 {
		t.Errorf("queries = %q, want the base query alone", got)
	}
	if !strings.Contains(log.String(), "20ms") {
		t.Errorf("warning %q does not name the timeout", log.String())
	}
}

// The caller's own Ctrl-C is not an expansion failure to be warned about and
// retried around — but it must not cost the answer either, since the ask that
// follows will notice the cancelled context itself.
func TestExpand_honoursCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := expander(3, chatHanging(), nil).Queries(ctx, nil, "how do slices grow?")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("queries = %q, want the base query alone", got)
	}
}

// N of zero (and below) is off: no call, no cost, the base plan untouched.
func TestExpand_zeroIsOff(t *testing.T) {
	for _, n := range []int{0, -1} {
		called := false
		chat := func(ctx context.Context, m []llm.Message, o ...llm.CallOptions) (<-chan llm.Token, error) {
			called = true
			return chatReturning("paraphrase")(ctx, m, o...)
		}
		got, err := expander(n, chat, nil).Queries(context.Background(), nil, "how do slices grow?")
		if err != nil {
			t.Fatalf("Queries: %v", err)
		}
		if called {
			t.Errorf("N=%d called the model", n)
		}
		if want := []string{"how do slices grow?"}; !reflect.DeepEqual(got, want) {
			t.Errorf("N=%d: queries = %q, want %q", n, got, want)
		}
	}
}

// Expansion spends the template's model at the template's temperature, the same
// rule condense follows, and caps its own output: the paraphrases are one line
// each, not the answer's budget.
func TestExpand_callOptions(t *testing.T) {
	var got llm.CallOptions
	chat := func(ctx context.Context, m []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error) {
		if len(opts) > 0 {
			got = opts[0]
		}
		return chatReturning("paraphrase")(ctx, m, opts...)
	}
	temp := 0.4
	e := expander(1, chat, nil)
	e.Opts = llm.CallOptions{Model: "llama3", Temperature: &temp}

	if _, err := e.Queries(context.Background(), nil, "q"); err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if got.Model != "llama3" {
		t.Errorf("model = %q, want the template's llama3", got.Model)
	}
	if got.Temperature == nil || *got.Temperature != temp {
		t.Errorf("temperature = %v, want the template's %v", got.Temperature, temp)
	}
	if got.MaxTokens != rewrite.ExpandMaxTokens {
		t.Errorf("max tokens = %d, want the expansion cap %d", got.MaxTokens, rewrite.ExpandMaxTokens)
	}
}

// A base planner that returns several queries keeps all of them; the
// paraphrases are of the first, which is the one the plan is about.
func TestExpand_keepsEveryBaseQuery(t *testing.T) {
	e := expander(1, chatReturning("paraphrase"), nil)
	e.Base = fixedPlanner{"first", "second"}

	got, err := e.Queries(context.Background(), nil, "q")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	want := []string{"first", "second", "paraphrase"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("queries = %q, want %q", got, want)
	}
}

// Without a base planner the query is the question, which is what a single-shot
// ask expands: `--expand 3` on its own is three extra wordings of what was
// typed.
func TestExpand_defaultBaseIsTheQuestion(t *testing.T) {
	e := rewrite.Expand{Chat: chatReturning("paraphrase"), N: 1}

	got, err := e.Queries(context.Background(),
		[]conversation.Turn{{Question: "earlier", Answer: "a"}}, "how do slices grow?")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	want := []string{"how do slices grow?", "paraphrase"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("queries = %q, want %q", got, want)
	}
}

// A base planner that fails is a failure of the plan, not of the expansion:
// there is no query to paraphrase and nothing to fall back to.
func TestExpand_propagatesABaseFailure(t *testing.T) {
	sentinel := errors.New("base failed")
	e := expander(1, chatReturning("paraphrase"), nil)
	e.Base = failingPlanner{sentinel}

	if _, err := e.Queries(context.Background(), nil, "q"); !errors.Is(err, sentinel) {
		t.Errorf("want the base planner's error, got %v", err)
	}
}

func TestExpand_implementsPlanner(t *testing.T) {
	var _ rewrite.Planner = rewrite.Expand{}
}

type fixedPlanner []string

func (f fixedPlanner) Queries(_ context.Context, _ []conversation.Turn, _ string) ([]string, error) {
	return f, nil
}

type failingPlanner struct{ err error }

func (f failingPlanner) Queries(_ context.Context, _ []conversation.Turn, _ string) ([]string, error) {
	return nil, f.err
}
