// Package rewrite plans the queries retrieval runs for a turn.
//
// A follow-up question is not a query: `and maps?` retrieved on its own reaches
// nothing, so a multi-turn ask without planning is a chat whose evidence is one
// turn stale — the model sees the thread, the retriever does not, and the
// answer quietly comes from the previous turn's chunks plus the model's priors.
//
// One seam — (thread, question) → the queries retrieval runs — serves the whole
// cluster: Window ships here, Condense (one LLM call) and Expand (N queries,
// fused) land behind the same interface.
package rewrite

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotofritz/timbuktu/internal/conversation"
)

// Modes accepted by New, and by the manifest's retrieval.rewrite key.
const (
	// ModeOff retrieves on the question exactly as typed.
	ModeOff = "off"
	// ModeWindow folds the last few questions of the thread into the query.
	ModeWindow = "window"
)

// Planner turns a thread and the question in front of it into the queries
// retrieval actually runs. Returning more than one query is how expansion and
// multi-hop will fit the same seam; Window returns exactly one.
type Planner interface {
	Queries(ctx context.Context, thread []conversation.Turn, question string) ([]string, error)
}

// Window prepends the last Turns questions of the thread to the current one and
// retrieves on that string.
//
// It is the default because it costs nothing and cannot fail: no model, no
// network, no latency, no new way for `ask` to be slow or down. It already
// fixes the case that matters — `and maps?` reaches Go's maps, because the
// words `slices grow` are still in the query.
//
// Turns of zero or less is the "off" mode: the question, as typed.
type Window struct{ Turns int }

// Queries returns the single folded query.
func (w Window) Queries(_ context.Context, thread []conversation.Turn, question string) ([]string, error) {
	parts := make([]string, 0, w.Turns+1)
	for _, t := range conversation.Replay(thread, w.Turns) {
		if q := strings.TrimSpace(t.Question); q != "" {
			parts = append(parts, q)
		}
	}
	if q := strings.TrimSpace(question); q != "" {
		parts = append(parts, q)
	}
	return []string{strings.Join(parts, " ")}, nil
}

// DefaultWindowTurns is how many prior questions the window folds in when a
// manifest names no number of its own. Two is enough for the pronoun and the
// topic of the question before last without dragging a whole thread into every
// search.
const DefaultWindowTurns = 2

// ValidateMode reports whether mode names a planner this build ships. It is
// what template load checks, so a typo fails there rather than after a model
// call — the rule normalize's filters already follow.
//
// `condense` is refused by name rather than silently treated as `window`: a
// template asking to be rewritten by a model should not quietly get the
// deterministic planner instead.
func ValidateMode(mode string) error {
	switch mode {
	case "", ModeWindow, ModeOff:
		return nil
	case "condense":
		return fmt.Errorf("rewrite: mode %q is not available yet (want off or window)", mode)
	default:
		return fmt.Errorf("rewrite: unknown mode %q (want off or window)", mode)
	}
}

// New returns the planner named by mode, which comes from the template
// manifest's retrieval.rewrite key. An empty mode is the default (window), and
// a windowTurns of zero or less takes DefaultWindowTurns — "no window at all"
// is spelled `off`.
func New(mode string, windowTurns int) (Planner, error) {
	if err := ValidateMode(mode); err != nil {
		return nil, err
	}
	if mode == ModeOff {
		return Window{Turns: 0}, nil
	}
	if windowTurns <= 0 {
		windowTurns = DefaultWindowTurns
	}
	return Window{Turns: windowTurns}, nil
}
