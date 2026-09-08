package eval_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/eval"
	"github.com/gotofritz/timbuktu/internal/llm"
)

// passages builds the retrieved evidence an answer was given.
func passages(texts ...string) []eval.Result {
	out := make([]eval.Result, len(texts))
	for i, t := range texts {
		out[i] = eval.Result{Path: "/n/go/slices.md", Text: t}
	}
	return out
}

func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func TestScoreAnswer_mustInclude(t *testing.T) {
	tests := []struct {
		name     string
		answer   string
		required []string
		want     float64
		counted  bool
	}{
		{"all present", "append reallocates when len == cap", []string{"append", "cap"}, 1, true},
		{"none present", "maps rehash their buckets", []string{"append", "cap"}, 0, true},
		{"partly present", "append does the work", []string{"append", "cap"}, 0.5, true},
		{"case and whitespace folded", "Append\nreallocates", []string{"append reallocates"}, 1, true},
		{"nothing required is not a zero", "anything at all", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := eval.ScoreAnswer(eval.Answer{Text: tt.answer}, eval.Case{MustInclude: tt.required}, nil)
			if !closeTo(m.Includes, tt.want) {
				t.Errorf("Includes = %.2f, want %.2f", m.Includes, tt.want)
			}
			want := 0
			if tt.counted {
				want = 1
			}
			if m.WithIncludes != want {
				t.Errorf("WithIncludes = %d, want %d", m.WithIncludes, want)
			}
		})
	}
}

func TestScoreAnswer_citations(t *testing.T) {
	indexed := []string{"/n/go/slices.md", "/n/go/maps.md"}
	tests := []struct {
		name    string
		answer  string
		want    float64
		emitted int
	}{
		{
			name:    "every citation is indexed",
			answer:  "Slices grow by doubling (/n/go/slices.md §0), and maps rehash (go/maps.md §1).",
			want:    1,
			emitted: 2,
		},
		{
			name:    "a citation naming an unindexed document",
			answer:  "See go/slices.md and go/generics.md for the details.",
			want:    0.5,
			emitted: 2,
		},
		{
			name:    "no citation emitted is not a zero",
			answer:  "A slice grows when append finds len == cap.",
			want:    0,
			emitted: 0,
		},
		{
			name:    "the same document cited twice is one citation",
			answer:  "go/slices.md says so, and go/slices.md §2 says it again.",
			want:    1,
			emitted: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := eval.ScoreAnswer(eval.Answer{Text: tt.answer}, eval.Case{}, indexed)
			if !closeTo(m.Citations, tt.want) {
				t.Errorf("Citations = %.2f, want %.2f", m.Citations, tt.want)
			}
			counted := 0
			if tt.emitted > 0 {
				counted = 1
			}
			if m.WithCitations != counted {
				t.Errorf("WithCitations = %d, want %d", m.WithCitations, counted)
			}
		})
	}
}

func TestExtractCitations(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"a chunk marker", "see /n/go/slices.md §3 for this", []string{"/n/go/slices.md"}},
		{"a sentence-final path", "It is in slices.md.", []string{"slices.md"}},
		{"a bracketed path", "[1] (go/maps.md)", []string{"go/maps.md"}},
		{"prose abbreviations are not citations", "e.g. this, i.e. that", nil},
		{"version numbers are not citations", "go 1.26.2 and pi is 3.14", nil},
		{"no citation at all", "append reallocates when len == cap", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eval.ExtractCitations(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("ExtractCitations(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("citation %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestScoreAnswer_groundedness(t *testing.T) {
	tests := []struct {
		name     string
		answer   string
		passages []eval.Result
		want     float64
		counted  bool
	}{
		{
			name:     "every content word is in the passages",
			answer:   "The capacity is doubled.",
			passages: passages("the capacity is roughly doubled when append reallocates"),
			want:     1,
			counted:  true,
		},
		{
			name:     "nothing the passages say",
			answer:   "Goroutines multiplex onto threads.",
			passages: passages("the capacity is roughly doubled"),
			want:     0,
			counted:  true,
		},
		{
			name:     "half of it",
			answer:   "capacity goroutines",
			passages: passages("the capacity is roughly doubled"),
			want:     0.5,
			counted:  true,
		},
		{
			name:     "function words alone score nothing and are not counted",
			answer:   "It is the one that was.",
			passages: passages("the capacity is roughly doubled"),
			want:     0,
			counted:  false,
		},
		{
			name:     "no passages means nothing could ground it",
			answer:   "The capacity is doubled.",
			passages: nil,
			want:     0,
			counted:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := eval.ScoreAnswer(eval.Answer{Text: tt.answer, Passages: tt.passages}, eval.Case{}, nil)
			if !closeTo(m.Groundedness, tt.want) {
				t.Errorf("Groundedness = %.2f, want %.2f", m.Groundedness, tt.want)
			}
			want := 0
			if tt.counted {
				want = 1
			}
			if m.WithWords != want {
				t.Errorf("WithWords = %d, want %d", m.WithWords, want)
			}
		})
	}
}

func TestGenMetrics_WithJudgement_rescales(t *testing.T) {
	m := eval.GenMetrics{Cases: 1}.WithJudgement(eval.Judgement{
		Correctness:  eval.Verdict{Score: 2, Reason: "says what the reference says"},
		Faithfulness: eval.Verdict{Score: 1, Reason: "one claim is not in the passages"},
	})
	if !closeTo(m.Correctness, 1) {
		t.Errorf("Correctness = %.2f, want 1.00", m.Correctness)
	}
	if !closeTo(m.Faithfulness, 0.5) {
		t.Errorf("Faithfulness = %.2f, want 0.50", m.Faithfulness)
	}
	if m.Judged != 1 {
		t.Errorf("Judged = %d, want 1", m.Judged)
	}
}

func TestAggregateGen_averagesOnlyOverApplicableCases(t *testing.T) {
	// One case scored on must_include, one with nothing required. The average
	// is over the case that had something to score, not over both.
	ms := []eval.GenMetrics{
		{Cases: 1, Includes: 1, WithIncludes: 1, Groundedness: 0.5, WithWords: 1},
		{Cases: 1, Groundedness: 1, WithWords: 1},
	}
	got := eval.AggregateGen(ms)
	if !closeTo(got.Includes, 1) {
		t.Errorf("Includes = %.2f, want 1.00 (averaged over the one case that required anything)", got.Includes)
	}
	if !closeTo(got.Groundedness, 0.75) {
		t.Errorf("Groundedness = %.2f, want 0.75", got.Groundedness)
	}
	if got.Cases != 2 || got.WithIncludes != 1 || got.WithWords != 2 {
		t.Errorf("counts = %+v, want 2 cases, 1 with includes, 2 with words", got)
	}
	if n := eval.AggregateGen(nil).Cases; n != 0 {
		t.Errorf("AggregateGen(nil).Cases = %d, want 0", n)
	}
}

// fakeChat returns a ChatFn that streams reply, or fails with err.
func fakeChat(reply string, err error) eval.ChatFn {
	return func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		if err != nil {
			return nil, err
		}
		ch := make(chan llm.Token, 1)
		ch <- llm.Token{Text: reply, Done: true}
		close(ch)
		return ch, nil
	}
}

const goodVerdict = `{"correctness": 2, "correctness_reason": "matches the reference",
 "faithfulness": 1, "faithfulness_reason": "one claim is unsupported"}`

func TestJudge_wellFormedVerdict(t *testing.T) {
	var seen []llm.Message
	j := eval.Judge{Chat: func(_ context.Context, msgs []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		seen = msgs
		ch := make(chan llm.Token, 1)
		ch <- llm.Token{Text: "```json\n" + goodVerdict + "\n```", Done: true}
		close(ch)
		return ch, nil
	}}

	got, err := j.Judge(context.Background(),
		eval.Case{Query: "how do slices grow?", Answer: "append reallocates when len == cap"},
		eval.Answer{Text: "A slice doubles.", Passages: passages("the capacity is roughly doubled")})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if got.Correctness.Score != 2 || got.Faithfulness.Score != 1 {
		t.Errorf("scores = %d/%d, want 2/1", got.Correctness.Score, got.Faithfulness.Score)
	}
	if got.Correctness.Reason != "matches the reference" {
		t.Errorf("correctness reason = %q", got.Correctness.Reason)
	}
	// The prompt is versioned code, and the case travels in the user message.
	if len(seen) != 2 || seen[0].Content != eval.JudgeSystem {
		t.Fatalf("judge messages = %+v, want the versioned system prompt and one user message", seen)
	}
	for _, want := range []string{"how do slices grow?", "append reallocates when len == cap",
		"A slice doubles.", "the capacity is roughly doubled"} {
		if !strings.Contains(seen[1].Content, want) {
			t.Errorf("judge prompt is missing %q:\n%s", want, seen[1].Content)
		}
	}
}

func TestJudge_failuresAreUnjudgedNotZero(t *testing.T) {
	tests := []struct {
		name string
		chat eval.ChatFn
	}{
		{"a malformed verdict", fakeChat("the answer looks fine to me", nil)},
		{"a verdict off the scale", fakeChat(`{"correctness": 7, "faithfulness": 1}`, nil)},
		{"an empty completion", fakeChat("", nil)},
		{"the call failed", fakeChat("", errors.New("connection refused"))},
		{"no model configured", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := eval.Judge{Chat: tt.chat}.Judge(context.Background(),
				eval.Case{Answer: "a reference"}, eval.Answer{Text: "an answer"})
			if err == nil {
				t.Fatal("Judge returned no error; a judge that failed must not score the case at all")
			}
		})
	}
}

func TestJudge_timeoutIsUnjudged(t *testing.T) {
	j := eval.Judge{
		Timeout: 10 * time.Millisecond,
		Chat: func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
			return make(chan llm.Token), nil // never answers, never closes
		},
	}
	start := time.Now()
	if _, err := j.Judge(context.Background(), eval.Case{Answer: "a reference"}, eval.Answer{Text: "an answer"}); err == nil {
		t.Fatal("Judge returned no error on a model that never answered")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Judge waited %s; the timeout should have bounded it", elapsed)
	}
}

func TestJudge_needsAReference(t *testing.T) {
	// Correctness is measured against `answer:`. Without one there is no
	// reference to mark against, and inventing a mark is worse than saying so.
	_, err := eval.Judge{Chat: fakeChat(goodVerdict, nil)}.Judge(
		context.Background(), eval.Case{ID: "x"}, eval.Answer{Text: "an answer"})
	if err == nil {
		t.Fatal("Judge scored a case with no reference answer")
	}
}

func TestResolveCitations(t *testing.T) {
	indexed := []string{"/n/go/slices.md", "/n/go/maps.md"}
	resolved, unresolved := eval.ResolveCitations(
		"go/slices.md says so; go/generics.md does not exist.", indexed)

	if len(resolved) != 1 || resolved[0] != "go/slices.md" {
		t.Errorf("resolved = %v, want [go/slices.md]", resolved)
	}
	if len(unresolved) != 1 || unresolved[0] != "go/generics.md" {
		t.Errorf("unresolved = %v, want [go/generics.md]", unresolved)
	}
	// An empty index resolves nothing, which is true rather than convenient.
	if r, u := eval.ResolveCitations("go/slices.md", nil); len(r) != 0 || len(u) != 1 {
		t.Errorf("against an empty index: resolved %v, unresolved %v", r, u)
	}
}

func TestValidateStage(t *testing.T) {
	for _, ok := range []string{eval.StageRetrieval, eval.StageGeneration, eval.StageBoth} {
		if err := eval.ValidateStage(ok); err != nil {
			t.Errorf("ValidateStage(%q) = %v, want nil", ok, err)
		}
	}
	if err := eval.ValidateStage("answers"); err == nil {
		t.Error("ValidateStage(\"answers\") = nil, want an error naming the three stages")
	}
}
