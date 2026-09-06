package rewrite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/llm"
)

const (
	// ExpandMaxTokens caps the paraphrases when the caller sets no budget of
	// its own. The output is a handful of one-line queries, and the template's
	// max_tokens is sized for a whole answer.
	ExpandMaxTokens = 256
	// ExpandTimeout bounds the call, for the reason condense has one: an
	// expander that hangs would hang the ask.
	ExpandTimeout = 20 * time.Second
)

const expandSystem = `You write alternative search queries for a document search engine.

Given one query, write different ways of asking for the same thing: synonyms, the technical term for a plain-language phrase and the plain-language phrase for a technical term, the singular and the plural, the spelled-out form and the abbreviation. Keep every alternative about the same subject as the original — you are rephrasing it, not broadening it or answering it.

Reply with one query per line and nothing else: no numbering, no bullets, no quotes, no explanation.`

// Expand plans several wordings of one query, so retrieval reaches the passages
// whose vocabulary the original missed — the corpus that says "reallocate"
// where the question says "grow".
//
// It wraps a base planner rather than replacing one: `window` (or `condense`)
// plans the query for the turn, and expansion paraphrases what that arrived at.
// The base's queries come back first and unchanged; the paraphrases follow, and
// retrieval fuses the ranked lists with RRF, where a chunk several wordings
// agree on outranks one that a single wording found.
//
// Like Condense (D6) it cannot fail: an error, a timeout, an empty completion
// or a cancelled context falls back to the base queries with a line on Warn.
type Expand struct {
	// Base plans the query being paraphrased. Nil retrieves on the question as
	// typed, which is what a single-shot `--expand N` expands.
	Base Planner
	// Chat is the model call. Nil falls back, like any other failure.
	Chat ChatFn
	// Opts carries the template's model and temperature — expansion spends the
	// template's model, which is why it is configured in the manifest. A zero
	// MaxTokens takes ExpandMaxTokens.
	Opts llm.CallOptions
	// N is how many paraphrases to ask for, and the ceiling on how many are
	// used. Zero or less is off: no call, no cost.
	N int
	// Warn receives the diagnostic when the expansion is given up on. Nil is
	// silence, which only a test should want.
	Warn io.Writer
	// Timeout bounds one call. Zero takes ExpandTimeout.
	Timeout time.Duration
}

// Queries returns the base query (or queries) followed by up to N paraphrases
// of the first of them.
func (e Expand) Queries(ctx context.Context, thread []conversation.Turn, question string) ([]string, error) {
	base := e.Base
	if base == nil {
		base = Window{Turns: 0}
	}
	queries, err := base.Queries(ctx, thread, question)
	if err != nil {
		// The plan itself failed, so there is no query to paraphrase and
		// nothing to fall back to. Expansion's own failures are handled below;
		// this one is the caller's.
		return nil, err //nolint:wrapcheck // the base planner's error, unadorned
	}
	if e.N <= 0 || len(queries) == 0 {
		return queries, nil
	}

	paraphrases, err := e.paraphrase(ctx, queries[0])
	if err != nil {
		if e.Warn != nil {
			_, _ = fmt.Fprintf(e.Warn,
				"warning: could not expand the query (%v) — retrieving on the planned query alone\n", err)
		}
		return queries, nil
	}
	// Copied rather than appended in place: the slice belongs to the base
	// planner, and a caller's backing array is not ours to write into.
	plan := make([]string, 0, len(queries)+len(paraphrases))
	plan = append(append(plan, queries...), paraphrases...)
	return dedupeQueries(plan, len(queries)+e.N), nil
}

// paraphrase makes the call and vets what comes back. Every error it returns is
// a fallback, not a failure of the ask.
func (e Expand) paraphrase(ctx context.Context, query string) ([]string, error) {
	if e.Chat == nil {
		return nil, errors.New("no model is configured for query expansion")
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = ExpandTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := e.Opts
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = ExpandMaxTokens
	}
	tokens, err := e.Chat(ctx, expandMessages(query, e.N), opts)
	if err != nil {
		return nil, fmt.Errorf("the model call failed: %w", err)
	}

	var sb strings.Builder
	for done := false; !done; {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("the model did not answer within %s", timeout)
			}
			return nil, ctx.Err() //nolint:wrapcheck // the caller's own cancellation, unadorned
		case tok, ok := <-tokens:
			if !ok {
				done = true
				break
			}
			if tok.Error != nil {
				return nil, fmt.Errorf("the model stream failed: %w", tok.Error)
			}
			sb.WriteString(tok.Text)
			done = tok.Done
		}
	}

	out := cleanParaphrases(sb.String(), e.N)
	if len(out) == 0 {
		return nil, errors.New("the model returned nothing to retrieve on")
	}
	return out, nil
}

// listMarker is what a model puts in front of a query when it lists them
// despite being told not to. The trailing space is required: it is what tells a
// marker from a query that opens with a number, so "2024 budget report" keeps
// its year and "3.5 inch floppy" its size.
var listMarker = regexp.MustCompile(`^(?:\d+[.)]|[-*•])\s+`)

// cleanParaphrases reduces the completion to the queries retrieval runs. A
// model told to reply with one per line and nothing else still numbers them,
// bullets them or quotes them; none of that is the query.
func cleanParaphrases(s string, n int) []string {
	out := make([]string, 0, n)
	for _, line := range strings.Split(s, "\n") {
		line = listMarker.ReplaceAllString(strings.TrimSpace(line), "")
		if line = cleanCondensed(line); line == "" {
			continue
		}
		out = append(out, line)
		if len(out) == n {
			break
		}
	}
	return out
}

// dedupeQueries drops the repeats, keeping the first spelling of each and
// capping the result at limit. A paraphrase identical to another query is a
// search run twice for one opinion — and under RRF a duplicated list is a vote
// counted twice, which is how a bad paraphrase gets promoted rather than
// outvoted.
func dedupeQueries(queries []string, limit int) []string {
	seen := make(map[string]bool, len(queries))
	out := make([]string, 0, len(queries))
	for _, q := range queries {
		key := strings.ToLower(strings.Join(strings.Fields(q), " "))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, q)
		if len(out) == limit {
			break
		}
	}
	return out
}

// expandMessages renders the expansion prompt. It carries the query, not the
// thread: whatever the thread had to say is already in the query the base
// planner arrived at, and repeating it invites paraphrases of the conversation
// instead of the question.
func expandMessages(query string, n int) []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: expandSystem},
		{Role: llm.RoleUser, Content: fmt.Sprintf(
			"Write %d alternative queries for:\n%s", n, strings.TrimSpace(query))},
	}
}

// ValidateExpand reports whether n is a usable number of paraphrases. It is
// what the manifest and the flag check, so a typo fails at the edge rather than
// after a model call — the rule ValidateMode already follows.
func ValidateExpand(n int) error {
	if n < 0 {
		return fmt.Errorf("rewrite: expand must not be negative, got %d", n)
	}
	return nil
}
