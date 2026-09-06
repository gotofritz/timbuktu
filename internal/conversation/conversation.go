// Package conversation holds the shape of a multi-turn ask: the thread, its
// turns, and the rules for replaying them into a prompt.
//
// It is deliberately pure — no database, no LLM call, no cobra — so the replay
// limits and the message assembly the context budget leans on can be tested as
// arithmetic, without a fake of anything.
package conversation

import "github.com/gotofritz/timbuktu/internal/llm"

// Turn is one completed exchange: what was asked, what retrieval was run on,
// what came back, and where it came from.
//
// The rendered prompt is deliberately absent. Replaying it would carry that
// turn's retrieved chunks into every later prompt, so the cost would grow with
// the square of the thread and stale evidence would outrank the passages
// retrieved for the question actually being asked.
type Turn struct {
	Question string
	// Query is what retrieval actually ran, which stops being the question
	// once a planner folds the thread into it.
	Query     string
	Answer    string
	Citations []string
}

// Thread is a named conversation over one knowledge base, holding the turns
// that are to be replayed — already bounded by Replay, not the whole history.
type Thread struct {
	ID       int64
	Name     string
	Template string
	Turns    []Turn
}

// Replay returns the last limit turns, oldest first: a follow-up is read
// against the questions nearest it, and limit (session.history_turns) bounds
// how far back that reaches. A limit of zero or less replays nothing, which is
// how history_turns turns threading off while still recording the thread.
//
// The result never aliases turns, so a caller appending to the thread cannot
// scribble over a slice that has already been handed out.
func Replay(turns []Turn, limit int) []Turn {
	if limit <= 0 || len(turns) == 0 {
		return nil
	}
	if limit > len(turns) {
		limit = len(turns)
	}
	out := make([]Turn, limit)
	copy(out, turns[len(turns)-limit:])
	return out
}

// Messages assembles the prompt: the system message, then each replayed turn as
// a user/assistant pair oldest first, then the current turn's rendered user
// message.
//
// Prior turns go back as messages rather than as text inlined into the user
// template: every provider already takes a role-tagged slice, and a question
// pasted into the template is typographically indistinguishable from the one
// being asked — which is how a model comes to answer the turn before last.
//
// With no turns the result is exactly what a single-shot ask has always sent,
// which is what keeps `tbuk ask` without --session byte-identical.
func Messages(system, user string, turns []Turn) []llm.Message {
	messages := make([]llm.Message, 0, 2+2*len(turns))
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: system})
	for _, t := range turns {
		messages = append(messages,
			llm.Message{Role: llm.RoleUser, Content: t.Question},
			llm.Message{Role: llm.RoleAssistant, Content: t.Answer},
		)
	}
	return append(messages, llm.Message{Role: llm.RoleUser, Content: user})
}
