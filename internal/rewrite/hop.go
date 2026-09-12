package rewrite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gotofritz/timbuktu/internal/llm"
)

const (
	// HopMaxTokens caps a hop when the caller sets no budget of its own. The
	// output is a search query, and the template's max_tokens is sized for a
	// whole answer.
	HopMaxTokens = 128
	// HopTimeout bounds one hop. A loop that hangs hangs the ask, and the
	// passages already retrieved are standing right there.
	HopTimeout = 20 * time.Second
	// HopQueryLimit is how long a follow-up query may be before it stops being
	// one. A model that writes more than this has answered the question rather
	// than named what is missing from it.
	HopQueryLimit = 512
	// MaxHopsLimit caps --hops. Each hop is a model call and a search on top of
	// every answer, and a loop longer than this is an agent with a tool — which
	// is deliberately out of scope (#161).
	MaxHopsLimit = 5
	// hopPassageRunes is how much of each passage the prompt carries. Enough to
	// see what a passage covers; not the whole passage, which is the prompt the
	// answer itself is built from.
	hopPassageRunes = 300
	// hopPassages is how many passages the model is shown. The top of the
	// ranking is what decides whether retrieval got there; the tail is what the
	// context ladder drops first anyway.
	hopPassages = 8
	// hopNothing is what the model says when the passages are enough. It is the
	// reply that ends the loop, so it is one word and it is checked for
	// exactly.
	hopNothing = "nothing"
)

const hopSystem = `You decide whether a search has found enough to answer a question.

You are given a question and the passages a document search returned for it. Judge only whether the passages contain what is needed — do not answer the question.

If something needed is missing, reply with a single search query: the words you would search the document collection for to find it. Keep the user's technical terms exactly as written, and do not search for what the passages already cover.

If the passages are enough, reply with exactly: NOTHING

Reply with the search query, or NOTHING, and nothing else.`

// Hop asks the model what the passages retrieved so far do not cover, and
// returns the query to retrieve on next. An empty query means nothing is
// missing and the loop is done.
//
// It is not a Planner: a planner turns (thread, question) into queries before
// anything has been retrieved, and a hop reads what came back. The shape is the
// same model call with the same failure modes, which is why it lives here.
//
// Unlike Condense it reports its failures rather than falling back. There is
// nothing to fall back to — the loop's floor is the passages it already has —
// so the caller stops hopping and answers with those, which is exactly what
// --hops 0 would have done.
type Hop struct {
	// Chat is the model call. Nil is an error: a hop with no model is a hop
	// that was configured by mistake.
	Chat ChatFn
	// Opts carries the template's model and temperature. A zero MaxTokens
	// takes HopMaxTokens.
	Opts llm.CallOptions
	// Warn receives the diagnostic when a hop is given up on. Nil is silence.
	Warn io.Writer
	// Timeout bounds one hop. Zero takes HopTimeout.
	Timeout time.Duration
}

// Next returns the follow-up query, or "" when the passages already cover the
// question.
func (h Hop) Next(ctx context.Context, question string, passages []string) (string, error) {
	if h.Chat == nil {
		return "", errors.New("no model is configured for multi-hop retrieval")
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = HopTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := h.Opts
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = HopMaxTokens
	}
	tokens, err := h.Chat(ctx, hopMessages(question, passages), opts)
	if err != nil {
		return "", fmt.Errorf("the model call failed: %w", err)
	}
	raw, reasoning, err := collect(ctx, tokens, timeout)
	if err != nil {
		return "", err
	}

	out := cleanCondensed(stripThinking(raw))
	if out == "" {
		if reasoning != "" || strings.Contains(raw, thinkOpen) {
			return "", fmt.Errorf(
				"the model spent its %d-token budget reasoning without naming a query — "+
					"raise the template's max_tokens or hop with a non-reasoning model",
				opts.MaxTokens)
		}
		return "", errors.New("the model returned nothing to retrieve on")
	}
	if isNothing(out) {
		return "", nil
	}
	// A query longer than the limit is the model answering, reciting or
	// looping. Retrieving on it would search for an answer nobody has.
	if len(out) > HopQueryLimit {
		return "", fmt.Errorf(
			"the model returned %d characters, longer than the %d a search query may be — "+
				"it answered the question instead of searching for it", len(out), HopQueryLimit)
	}
	return out, nil
}

// isNothing recognises the reply that ends the loop through whatever the model
// wrapped it in. A sentinel nobody can hit is a loop that always runs to its
// bound.
func isNothing(s string) bool {
	s = strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), ".!:;,")
	return s == hopNothing
}

// ValidateHops rejects a hop count that cannot mean anything, so a typo fails
// at the flag or at template load rather than after a model call.
func ValidateHops(n int) error {
	if n < 0 {
		return fmt.Errorf("rewrite: hops must not be negative, got %d", n)
	}
	if n > MaxHopsLimit {
		return fmt.Errorf(
			"rewrite: hops must be at most %d, got %d — each hop costs a model call and a search "+
				"on top of every answer", MaxHopsLimit, n)
	}
	return nil
}

// hopMessages renders the decision prompt: the question, then the passages as
// a numbered list of excerpts.
func hopMessages(question string, passages []string) []llm.Message {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Question: %s\n\n", strings.TrimSpace(question))
	if len(passages) == 0 {
		sb.WriteString("The search returned no passages at all.\n")
	} else {
		sb.WriteString("Passages retrieved so far:\n")
		for i, p := range passages {
			if i >= hopPassages {
				break
			}
			fmt.Fprintf(&sb, "%d. %s\n", i+1, truncateRunes(p, hopPassageRunes))
		}
	}
	return []llm.Message{
		{Role: llm.RoleSystem, Content: hopSystem},
		{Role: llm.RoleUser, Content: sb.String()},
	}
}
