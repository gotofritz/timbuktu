// Package rewrite plans the queries retrieval runs for a turn.
//
// A follow-up question is not a query: `and maps?` retrieved on its own reaches
// nothing, so a multi-turn ask without planning is a chat whose evidence is one
// turn stale — the model sees the thread, the retriever does not, and the
// answer quietly comes from the previous turn's chunks plus the model's priors.
//
// One seam — (thread, question) → the queries retrieval runs — serves the whole
// cluster: Window (deterministic) and Condense (one LLM call) ship here, and
// Expand (N queries, fused) lands behind the same interface.
package rewrite

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/llm"
)

// Modes accepted by New, and by the manifest's retrieval.rewrite key.
const (
	// ModeOff retrieves on the question exactly as typed.
	ModeOff = "off"
	// ModeWindow folds the last few questions of the thread into the query.
	ModeWindow = "window"
)

// Options names the planner to build and gives it what it needs. Query
// planning is configured on the template — it spends the template's model at
// the template's temperature — so Mode and WindowTurns come from the manifest's
// retrieval block, and Chat, CallOptions and Warn from the command.
type Options struct {
	Mode        string
	WindowTurns int
	// Chat is the model a rewriting planner spends. Required by ModeCondense,
	// unused by the deterministic modes.
	Chat ChatFn
	// CallOptions carries the template's model and temperature into that call.
	CallOptions llm.CallOptions
	// Warn receives the diagnostic when a rewrite is given up on and the window
	// plans the query instead.
	Warn io.Writer
	// OnFallback is called with the reason each time that happens. Warn tells a
	// person; this tells a caller that has to count, which the eval harness
	// does: a rewrite that degrades rather than failing produces numbers
	// indistinguishable from ones where it worked.
	OnFallback func(reason string)
	// Expand is how many extra wordings of the planned query to retrieve on,
	// fused by RRF. 0 is off. Like ModeCondense it needs a Chat, and it
	// composes with the mode rather than replacing it.
	Expand int
}

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
func ValidateMode(mode string) error {
	switch mode {
	case "", ModeWindow, ModeOff, ModeCondense:
		return nil
	default:
		return fmt.Errorf("rewrite: unknown mode %q (want off, window or condense)", mode)
	}
}

// New returns the planner named by opts.Mode, which comes from the template
// manifest's retrieval.rewrite key (or from --rewrite for one run), wrapped in
// the expansion opts.Expand asks for. An empty mode is the default (window),
// and a WindowTurns of zero or less takes DefaultWindowTurns — "no window at
// all" is spelled `off`.
//
// `condense` (or an expansion) without a Chat is an error rather than a silent
// window: a template asking to be rewritten by a model should not quietly get
// the deterministic planner instead. Once built, neither can fail — they fall
// back (D6).
func New(opts Options) (Planner, error) {
	if err := ValidateMode(opts.Mode); err != nil {
		return nil, err
	}
	if err := ValidateExpand(opts.Expand); err != nil {
		return nil, err
	}
	base, err := newBase(opts)
	if err != nil {
		return nil, err
	}
	if opts.Expand <= 0 {
		return base, nil
	}
	if opts.Chat == nil {
		return nil, fmt.Errorf("rewrite: expand %d needs a model to write the alternative queries", opts.Expand)
	}
	return Expand{Base: base, Chat: opts.Chat, Opts: opts.CallOptions, N: opts.Expand, Warn: opts.Warn}, nil
}

// newBase builds the planner the mode names, which is what expansion (when
// asked for) paraphrases.
func newBase(opts Options) (Planner, error) {
	if opts.Mode == ModeOff {
		return Window{Turns: 0}, nil
	}
	turns := opts.WindowTurns
	if turns <= 0 {
		turns = DefaultWindowTurns
	}
	window := Window{Turns: turns}
	if opts.Mode != ModeCondense {
		return window, nil
	}
	if opts.Chat == nil {
		return nil, fmt.Errorf("rewrite: mode %q needs a model to condense with", opts.Mode)
	}
	return Condense{
		Chat:       opts.Chat,
		Opts:       opts.CallOptions,
		Fallback:   window,
		Warn:       opts.Warn,
		OnFallback: opts.OnFallback,
	}, nil
}
