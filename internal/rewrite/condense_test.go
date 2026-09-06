package rewrite_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/rewrite"
)

// chatReturning is a fake model that streams text back one token at a time and
// then closes, the way every adapter does.
func chatReturning(tokens ...string) rewrite.ChatFn {
	return func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		ch := make(chan llm.Token, len(tokens)+1)
		for _, t := range tokens {
			ch <- llm.Token{Text: t}
		}
		ch <- llm.Token{Done: true}
		close(ch)
		return ch, nil
	}
}

// chatFailing refuses the call outright — an unreachable provider, a bad key.
func chatFailing(err error) rewrite.ChatFn {
	return func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		return nil, err
	}
}

// chatBreaking accepts the call and then breaks mid-stream.
func chatBreaking(err error) rewrite.ChatFn {
	return func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		ch := make(chan llm.Token, 2)
		ch <- llm.Token{Text: "how do "}
		ch <- llm.Token{Error: err}
		close(ch)
		return ch, nil
	}
}

// chatHanging never answers, which is what a timeout is.
func chatHanging() rewrite.ChatFn {
	return func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		return make(chan llm.Token), nil
	}
}

func goThread() []conversation.Turn {
	return []conversation.Turn{{Question: "how do slices grow?", Answer: "they double, then grow by a quarter."}}
}

// The point of the whole milestone: `and maps?` becomes a question that stands
// on its own, so retrieval reaches Go's maps rather than the previous answer's
// chunks.
func TestCondense_rewritesTheFollowUp(t *testing.T) {
	c := rewrite.Condense{Chat: chatReturning("how do maps ", "grow in Go?")}
	got, err := c.Queries(context.Background(), goThread(), "and maps?")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if len(got) != 1 || got[0] != "how do maps grow in Go?" {
		t.Errorf("queries = %q, want [\"how do maps grow in Go?\"]", got)
	}
}

// A model told to reply with the question and nothing else still wraps it in
// quotes, pads it, or wraps it over lines. None of that is the question.
func TestCondense_cleansTheCompletion(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"trims space", "  how do maps grow?  ", "how do maps grow?"},
		{"strips straight quotes", `"how do maps grow?"`, "how do maps grow?"},
		{"strips curly quotes", "\u201chow do maps grow?\u201d", "how do maps grow?"},
		{"collapses a wrapped question", "how do maps\n  grow in Go?", "how do maps grow in Go?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := rewrite.Condense{Chat: chatReturning(tt.raw)}
			got, err := c.Queries(context.Background(), goThread(), "and maps?")
			if err != nil {
				t.Fatalf("Queries: %v", err)
			}
			if got[0] != tt.want {
				t.Errorf("query = %q, want %q", got[0], tt.want)
			}
		})
	}
}

// An LLM in the retrieval path is a new way for `ask` to be slow, wrong or
// down. Every one of those failures costs the rewrite, never the answer: the
// window plans the query instead and the diagnostics stream says so (D6).
func TestCondense_fallsBackToTheWindow(t *testing.T) {
	tests := []struct {
		name    string
		chat    rewrite.ChatFn
		wantLog string
	}{
		{"the call is refused", chatFailing(errors.New("connection refused")), "connection refused"},
		{"the stream breaks", chatBreaking(errors.New("stream reset")), "stream reset"},
		{"nothing comes back", chatReturning(""), "returned nothing"},
		{"only whitespace comes back", chatReturning("  \n "), "returned nothing"},
		{"the answer is absurd", chatReturning(strings.Repeat("word ", 400)), "longer than"},
		{"there is no model", nil, "no model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log strings.Builder
			c := rewrite.Condense{
				Chat:     tt.chat,
				Fallback: rewrite.Window{Turns: 2},
				Warn:     &log,
			}
			got, err := c.Queries(context.Background(), goThread(), "and maps?")
			if err != nil {
				t.Fatalf("Queries: %v", err)
			}
			if want := "how do slices grow? and maps?"; got[0] != want {
				t.Errorf("query = %q, want the window's %q", got[0], want)
			}
			if !strings.Contains(log.String(), tt.wantLog) {
				t.Errorf("warning %q does not mention %q", log.String(), tt.wantLog)
			}
			if !strings.Contains(log.String(), "window") {
				t.Errorf("warning %q does not say what planned the query instead", log.String())
			}
		})
	}
}

// A rewriter that hangs would hang the ask, so the call is bounded and the
// window answers instead.
func TestCondense_timesOut(t *testing.T) {
	var log strings.Builder
	c := rewrite.Condense{
		Chat:     chatHanging(),
		Fallback: rewrite.Window{Turns: 2},
		Warn:     &log,
		Timeout:  20 * time.Millisecond,
	}
	got, err := c.Queries(context.Background(), goThread(), "and maps?")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if want := "how do slices grow? and maps?"; got[0] != want {
		t.Errorf("query = %q, want the window's %q", got[0], want)
	}
	if !strings.Contains(log.String(), "20ms") {
		t.Errorf("warning %q does not name the timeout it hit", log.String())
	}
}

// The caller's own cancellation is not a fallback case in spirit, but it must
// not hang either: Ctrl-C during a condense returns promptly.
func TestCondense_honoursCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := rewrite.Condense{Chat: chatHanging(), Fallback: rewrite.Window{Turns: 2}, Warn: &strings.Builder{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Queries(ctx, goThread(), "and maps?")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Queries did not return after the context was cancelled")
	}
}

// Without a fallback of its own a condenser still cannot fail: the default
// window is the floor.
func TestCondense_defaultFallbackIsTheWindow(t *testing.T) {
	c := rewrite.Condense{Chat: chatFailing(errors.New("down"))}
	got, err := c.Queries(context.Background(), goThread(), "and maps?")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if want := "how do slices grow? and maps?"; got[0] != want {
		t.Errorf("query = %q, want the default window's %q", got[0], want)
	}
}

// The condenser has to see the thread it is resolving against, and it has to be
// told to answer with the question rather than about it.
func TestCondense_promptCarriesTheThread(t *testing.T) {
	var sent []llm.Message
	c := rewrite.Condense{Chat: func(_ context.Context, m []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		sent = m
		return chatReturning("how do maps grow?")(context.Background(), m)
	}}
	if _, err := c.Queries(context.Background(), goThread(), "and maps?"); err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d messages, want a system and a user message: %v", len(sent), sent)
	}
	if sent[0].Role != llm.RoleSystem || sent[1].Role != llm.RoleUser {
		t.Errorf("roles = %s, %s; want system, user", sent[0].Role, sent[1].Role)
	}
	for _, want := range []string{"how do slices grow?", "they double", "and maps?"} {
		if !strings.Contains(sent[1].Content, want) {
			t.Errorf("user message does not carry %q:\n%s", want, sent[1].Content)
		}
	}
}

// A whole answer replayed into the rewrite prompt is what makes "one cheap LLM
// call" stop being cheap. The condenser needs enough of it to resolve "the
// second one", not all of it.
func TestCondense_truncatesReplayedAnswers(t *testing.T) {
	var sent []llm.Message
	long := strings.Repeat("é", 5000)
	thread := []conversation.Turn{{Question: "list the options", Answer: long}}
	c := rewrite.Condense{Chat: func(_ context.Context, m []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		sent = m
		return chatReturning("what is the second option?")(context.Background(), m)
	}}
	if _, err := c.Queries(context.Background(), thread, "the second one?"); err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if strings.Contains(sent[1].Content, long) {
		t.Error("the whole answer was replayed into the condense prompt")
	}
	if !strings.Contains(sent[1].Content, "…") {
		t.Error("the truncated answer does not say that it was truncated")
	}
}

// Condensing is worth something on a question with nothing behind it too —
// typos and chit-chat — so an empty thread is a call, not a skip.
func TestCondense_withoutAThread(t *testing.T) {
	var sent []llm.Message
	c := rewrite.Condense{Chat: func(_ context.Context, m []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		sent = m
		return chatReturning("how do slices grow?")(context.Background(), m)
	}}
	got, err := c.Queries(context.Background(), nil, "hey so umm how do slices grow")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if got[0] != "how do slices grow?" {
		t.Errorf("query = %q, want the condensed question", got[0])
	}
	if !strings.Contains(sent[1].Content, "hey so umm how do slices grow") {
		t.Errorf("user message does not carry the question:\n%s", sent[1].Content)
	}
}

// Query planning spends the template's model at the template's temperature —
// that is why it is configured in the manifest — but never the template's whole
// answer budget on one rewritten line.
func TestCondense_callOptions(t *testing.T) {
	temp := 0.1
	var got llm.CallOptions
	c := rewrite.Condense{
		Opts: llm.CallOptions{Model: "a-model", Temperature: &temp},
		Chat: func(_ context.Context, m []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error) {
			got = opts[0]
			return chatReturning("how do maps grow?")(context.Background(), m)
		},
	}
	if _, err := c.Queries(context.Background(), goThread(), "and maps?"); err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if got.Model != "a-model" || got.Temperature == nil || *got.Temperature != temp {
		t.Errorf("call options = %+v, want the template's model and temperature", got)
	}
	if got.MaxTokens != rewrite.CondenseMaxTokens {
		t.Errorf("max_tokens = %d, want the rewrite cap %d", got.MaxTokens, rewrite.CondenseMaxTokens)
	}
}

// A caller that wants a different cap gets it.
func TestCondense_keepsAnExplicitMaxTokens(t *testing.T) {
	var got llm.CallOptions
	c := rewrite.Condense{
		Opts: llm.CallOptions{MaxTokens: 32},
		Chat: func(_ context.Context, m []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error) {
			got = opts[0]
			return chatReturning("how do maps grow?")(context.Background(), m)
		},
	}
	if _, err := c.Queries(context.Background(), goThread(), "and maps?"); err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if got.MaxTokens != 32 {
		t.Errorf("max_tokens = %d, want the caller's 32", got.MaxTokens)
	}
}

func TestCondense_implementsPlanner(t *testing.T) {
	var _ rewrite.Planner = rewrite.Condense{}
}
