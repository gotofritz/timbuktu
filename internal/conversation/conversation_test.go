package conversation_test

import (
	"testing"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/llm"
)

// turns builds a thread of n turns, numbered from 1 so the questions read.
func turns(n int) []conversation.Turn {
	out := make([]conversation.Turn, n)
	for i := range out {
		out[i] = conversation.Turn{
			Question: "q" + string(rune('1'+i)),
			Answer:   "a" + string(rune('1'+i)),
		}
	}
	return out
}

func questions(ts []conversation.Turn) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Question
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Replay keeps the most recent turns: a thread is read backwards from the
// question in front of you, and history_turns bounds how far back.
func TestReplay(t *testing.T) {
	tests := []struct {
		name  string
		turns []conversation.Turn
		limit int
		want  []string
	}{
		{"fewer turns than the limit", turns(2), 6, []string{"q1", "q2"}},
		{"exactly the limit", turns(3), 3, []string{"q1", "q2", "q3"}},
		{"more turns than the limit keeps the recent ones", turns(5), 2, []string{"q4", "q5"}},
		{"a limit of zero replays nothing", turns(3), 0, nil},
		{"a negative limit replays nothing", turns(3), -1, nil},
		{"an empty thread", nil, 6, nil},
		{"one turn", turns(1), 6, []string{"q1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := questions(conversation.Replay(tt.turns, tt.limit))
			if !equal(got, tt.want) {
				t.Errorf("Replay = %v, want %v", got, tt.want)
			}
		})
	}
}

// Replay must not hand back a window onto the caller's slice that later
// appends could scribble over.
func TestReplay_doesNotAliasInput(t *testing.T) {
	ts := turns(3)
	got := conversation.Replay(ts, 2)
	got[0].Question = "mutated"
	if ts[1].Question != "q2" {
		t.Errorf("Replay aliased its input: source turn is now %q", ts[1].Question)
	}
}

// History replays as role-tagged pairs, oldest first, ahead of the current
// turn's rendered user message — not as text inlined into the template (D3).
func TestMessages_order(t *testing.T) {
	got := conversation.Messages("SYSTEM", "USER", turns(2))
	want := []llm.Message{
		{Role: llm.RoleSystem, Content: "SYSTEM"},
		{Role: llm.RoleUser, Content: "q1"},
		{Role: llm.RoleAssistant, Content: "a1"},
		{Role: llm.RoleUser, Content: "q2"},
		{Role: llm.RoleAssistant, Content: "a2"},
		{Role: llm.RoleUser, Content: "USER"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d messages, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// With no thread the prompt is exactly what a single-shot ask has always sent:
// a system message and a user message, in that order. That is the regression
// bar the whole feature is held to.
func TestMessages_withoutHistory(t *testing.T) {
	for _, ts := range [][]conversation.Turn{nil, {}} {
		got := conversation.Messages("SYSTEM", "USER", ts)
		if len(got) != 2 {
			t.Fatalf("got %d messages, want 2: %v", len(got), got)
		}
		if got[0] != (llm.Message{Role: llm.RoleSystem, Content: "SYSTEM"}) {
			t.Errorf("first message = %+v", got[0])
		}
		if got[1] != (llm.Message{Role: llm.RoleUser, Content: "USER"}) {
			t.Errorf("second message = %+v", got[1])
		}
	}
}
