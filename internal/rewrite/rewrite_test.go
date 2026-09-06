package rewrite_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/rewrite"
)

func thread(questions ...string) []conversation.Turn {
	out := make([]conversation.Turn, len(questions))
	for i, q := range questions {
		out[i] = conversation.Turn{Question: q, Answer: "an answer"}
	}
	return out
}

// `and maps?` retrieved on its own reaches nothing; folded together with the
// turn before it, the words `slices grow` are still in the query, so it reaches
// Go's maps rather than the previous answer's chunks (D5).
func TestWindow_Queries(t *testing.T) {
	tests := []struct {
		name     string
		turns    int
		thread   []conversation.Turn
		question string
		want     string
	}{
		{"no thread is the question itself", 2, nil, "how do slices grow?", "how do slices grow?"},
		{"one prior turn", 2, thread("how do slices grow?"), "and maps?",
			"how do slices grow? and maps?"},
		{"only the last n turns", 2,
			thread("what is a goroutine?", "how do slices grow?", "what about append?"), "and maps?",
			"how do slices grow? what about append? and maps?"},
		{"a window of zero is the question alone", 0,
			thread("how do slices grow?"), "and maps?", "and maps?"},
		{"a negative window is the question alone", -1,
			thread("how do slices grow?"), "and maps?", "and maps?"},
		{"blank prior questions are skipped", 2,
			thread("  ", "how do slices grow?"), "and maps?", "how do slices grow? and maps?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rewrite.Window{Turns: tt.turns}.Queries(context.Background(), tt.thread, tt.question)
			if err != nil {
				t.Fatalf("Queries: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d queries, want 1: %v", len(got), got)
			}
			if got[0] != tt.want {
				t.Errorf("query = %q, want %q", got[0], tt.want)
			}
		})
	}
}

// The window costs nothing and cannot fail, which is why it is the default: no
// model, no network, no latency (D5).
func TestWindow_isDeterministic(t *testing.T) {
	w := rewrite.Window{Turns: 3}
	th := thread("one", "two")
	first, _ := w.Queries(context.Background(), th, "three")
	second, _ := w.Queries(context.Background(), th, "three")
	if first[0] != second[0] {
		t.Errorf("two runs disagree: %q vs %q", first[0], second[0])
	}
}

func TestNew(t *testing.T) {
	tests := []struct {
		mode    string
		turns   int
		want    string // the query planned for a one-turn thread
		wantErr bool
	}{
		{mode: "window", turns: 2, want: "prior question"},
		{mode: "off", turns: 2, want: ""},
		{mode: "", turns: 2, want: "prior question"}, // unset means the default
		{mode: "nonsense", turns: 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			p, err := rewrite.New(rewrite.Options{Mode: tt.mode, WindowTurns: tt.turns})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("New(%q): want an error, got nil", tt.mode)
				}
				if !strings.Contains(err.Error(), tt.mode) {
					t.Errorf("error %q does not name the mode", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q): %v", tt.mode, err)
			}
			got, err := p.Queries(context.Background(), thread("prior question"), "follow-up")
			if err != nil {
				t.Fatalf("Queries: %v", err)
			}
			want := strings.TrimSpace(tt.want + " follow-up")
			if got[0] != want {
				t.Errorf("query = %q, want %q", got[0], want)
			}
		})
	}
}

// A manifest that names no window size gets the default rather than a window of
// nothing: "no window at all" is spelled `off`.
func TestNew_defaultsWindowTurns(t *testing.T) {
	p, err := rewrite.New(rewrite.Options{Mode: "window"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(rewrite.Window).Turns; got != rewrite.DefaultWindowTurns {
		t.Errorf("window turns = %d, want the default %d", got, rewrite.DefaultWindowTurns)
	}
}

// Every planner has to satisfy the interface the ask path holds them by.
func TestWindow_implementsPlanner(t *testing.T) {
	var _ rewrite.Planner = rewrite.Window{}
}

// `condense` names a model call, so a build that has no model to spend has to
// say so at construction rather than quietly plan with the window: a template
// asking to be rewritten by a model should not silently get the deterministic
// planner instead.
func TestNew_condenseNeedsAModel(t *testing.T) {
	if _, err := rewrite.New(rewrite.Options{Mode: "condense"}); err == nil {
		t.Fatal("New(condense) without a chat function: want an error, got nil")
	}
}

// With a model behind it, `condense` is the planner — and it carries the window
// it falls back to, so a failed rewrite still folds the thread in (D6).
func TestNew_condense(t *testing.T) {
	var log strings.Builder
	p, err := rewrite.New(rewrite.Options{
		Mode:        "condense",
		WindowTurns: 2,
		Chat:        chatFailing(errors.New("down")),
		Warn:        &log,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := p.Queries(context.Background(), thread("prior question"), "follow-up")
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if want := "prior question follow-up"; got[0] != want {
		t.Errorf("query = %q, want the window's %q", got[0], want)
	}
}

// The manifest's window size is the one the fallback uses: a template that
// widens the window widens what a failed condense falls back to.
func TestNew_condenseFallbackUsesTheManifestWindow(t *testing.T) {
	p, err := rewrite.New(rewrite.Options{
		Mode: "condense",
		Chat: chatFailing(errors.New("down")),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c, ok := p.(rewrite.Condense)
	if !ok {
		t.Fatalf("New(condense) returned %T, want rewrite.Condense", p)
	}
	if w, ok := c.Fallback.(rewrite.Window); !ok || w.Turns != rewrite.DefaultWindowTurns {
		t.Errorf("fallback = %+v, want a window of %d turns", c.Fallback, rewrite.DefaultWindowTurns)
	}
}
