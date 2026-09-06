package rewrite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/llm"
)

// ModeCondense asks a model to rewrite the follow-up into a question that
// stands on its own. It is opt-in: it becomes the default only if the retrieval
// eval says it beats the window on follow-up turns.
const ModeCondense = "condense"

// ChatFn is the model call a planner spends. It is the shape every adapter
// already has, taken as a function rather than as llm.LLM so a fake chat drives
// the tests with no provider behind it.
type ChatFn func(ctx context.Context, messages []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error)

const (
	// CondenseMaxTokens caps the rewrite when the caller sets no budget of its
	// own. The output is one question, and the template's max_tokens is sized
	// for a whole answer.
	CondenseMaxTokens = 256
	// CondenseTimeout bounds the call. A rewriter that hangs would hang the
	// ask, and the window is standing right there.
	CondenseTimeout = 20 * time.Second
	// condenseAnswerRunes is how much of a replayed answer the prompt carries.
	// Enough to resolve "the second one"; not the whole answer, which is what
	// makes "one cheap LLM call" stop being cheap.
	condenseAnswerRunes = 400
	// condenseFloor is the shortest completion that can still be called absurd,
	// so a one-line thread cannot make a legitimate rewrite look too long.
	condenseFloor = 512
)

const condenseSystem = `You rewrite the last question of a conversation into a single standalone question.

Resolve pronouns and references against the conversation so the question can be understood on its own. Keep the user's wording and technical terms exactly as written. Fix obvious typos and drop pleasantries. Do not answer the question, do not explain your rewrite, and do not add anything that was not asked.

Reply with the rewritten question and nothing else.`

// Condense turns (thread, question) into one standalone question with a single
// model call — the rewriting that resolves `and maps?` against the thread
// instead of merely dragging the thread's words along, as Window does.
//
// It cannot fail: an error, a timeout, an empty completion or an absurd one
// falls back to Fallback with a line on Warn. An LLM in the retrieval path is
// a new way for `ask` to be slow, wrong or down, and none of those may cost the
// answer (D6).
type Condense struct {
	// Chat is the model call. Nil falls back, like any other failure.
	Chat ChatFn
	// Opts carries the template's model and temperature — query planning spends
	// the template's model, which is why it is configured in the manifest. A
	// zero MaxTokens takes CondenseMaxTokens.
	Opts llm.CallOptions
	// Fallback plans the query when the call does not produce one. Nil means a
	// window of DefaultWindowTurns: the floor is never "the question alone",
	// because that is the failure the whole planner exists to prevent.
	Fallback Planner
	// Warn receives the diagnostic when the rewrite is given up on. Nil is
	// silence, which only a test should want.
	Warn io.Writer
	// Timeout bounds one call. Zero takes CondenseTimeout.
	Timeout time.Duration
}

// Queries returns the standalone rewrite, or the fallback's query when the
// model does not produce a usable one.
func (c Condense) Queries(ctx context.Context, thread []conversation.Turn, question string) ([]string, error) {
	fallback := c.Fallback
	if fallback == nil {
		fallback = Window{Turns: DefaultWindowTurns}
	}
	condensed, err := c.condense(ctx, thread, question)
	if err != nil {
		if c.Warn != nil {
			_, _ = fmt.Fprintf(c.Warn,
				"warning: could not condense the question (%v) — planning the query with the window instead\n", err)
		}
		return fallback.Queries(ctx, thread, question) //nolint:wrapcheck // Window never errors
	}
	return []string{condensed}, nil
}

// condense makes the call and vets what comes back. Every error it returns is a
// fallback, not a failure of the ask.
func (c Condense) condense(ctx context.Context, thread []conversation.Turn, question string) (string, error) {
	if c.Chat == nil {
		return "", errors.New("no model is configured for query rewriting")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = CondenseTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := c.Opts
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = CondenseMaxTokens
	}
	tokens, err := c.Chat(ctx, condenseMessages(thread, question), opts)
	if err != nil {
		return "", fmt.Errorf("the model call failed: %w", err)
	}

	var sb strings.Builder
	for done := false; !done; {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("the model did not answer within %s", timeout)
			}
			return "", ctx.Err() //nolint:wrapcheck // the caller's own cancellation, unadorned
		case tok, ok := <-tokens:
			if !ok {
				done = true
				break
			}
			if tok.Error != nil {
				return "", fmt.Errorf("the model stream failed: %w", tok.Error)
			}
			sb.WriteString(tok.Text)
			done = tok.Done
		}
	}

	out := cleanCondensed(sb.String())
	if out == "" {
		return "", errors.New("the model returned nothing to retrieve on")
	}
	// A rewrite that is longer than what it was asked to condense is not a
	// question: it is the model answering, reciting, or looping.
	if limit := condenseLimit(thread, question); len(out) > limit {
		return "", fmt.Errorf("the model returned %d characters, longer than the %d it was given to condense",
			len(out), limit)
	}
	return out, nil
}

// cleanCondensed reduces the completion to the one line retrieval runs on. A
// model told to reply with the question and nothing else still pads it, quotes
// it, or wraps it over lines; none of that is the question.
func cleanCondensed(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for {
		trimmed := strings.TrimFunc(s, func(r rune) bool {
			return r == '"' || r == '\'' || r == '“' || r == '”' || r == '‘' || r == '’'
		})
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == s {
			return s
		}
		s = trimmed
	}
}

// condenseLimit is how long a rewrite may be before it stops being one: the
// text it was given, with a floor so a one-line thread cannot make a
// legitimate standalone question look absurd.
func condenseLimit(thread []conversation.Turn, question string) int {
	n := len(question)
	for _, t := range thread {
		n += len(t.Question) + len(t.Answer)
	}
	if n < condenseFloor {
		return condenseFloor
	}
	return n
}

// condenseMessages renders the rewrite prompt: the thread as plain Q/A text
// rather than as replayed message pairs, because the model is being asked to
// read a conversation, not to continue one.
func condenseMessages(thread []conversation.Turn, question string) []llm.Message {
	var sb strings.Builder
	if len(thread) == 0 {
		sb.WriteString("There is no earlier conversation.\n")
	} else {
		sb.WriteString("Conversation so far:\n")
		for _, t := range thread {
			fmt.Fprintf(&sb, "Q: %s\nA: %s\n", strings.TrimSpace(t.Question), truncateRunes(t.Answer, condenseAnswerRunes))
		}
	}
	fmt.Fprintf(&sb, "\nQuestion to rewrite: %s", strings.TrimSpace(question))
	return []llm.Message{
		{Role: llm.RoleSystem, Content: condenseSystem},
		{Role: llm.RoleUser, Content: sb.String()},
	}
}

// truncateRunes cuts s to at most n runes, saying so when it does. Runes, not
// bytes, so a non-Latin answer is not cut mid-character.
func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …"
}
