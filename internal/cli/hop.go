package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gotofritz/timbuktu/internal/retrieval"
)

// Hopper names what the passages retrieved so far do not cover. An empty query
// means nothing is missing.
//
// It is an interface here rather than a *rewrite.Hop so the loop is testable
// without a model, which is the only way a bounded loop's bounds get tested.
type Hopper interface {
	Next(ctx context.Context, question string, passages []string) (string, error)
}

// HopOptions bounds the loop.
type HopOptions struct {
	// MaxHops is how many follow-up rounds are allowed. 0 is the single shot
	// `tbuk ask` has always run.
	MaxHops int
	// Warn receives the reason the loop stopped early. Nil is silence.
	Warn io.Writer
	// OnGiveUp is called with the reason when a hop *failed* — not when the
	// loop finished because nothing was missing, or because the bound was
	// reached. Warn tells a person; this tells a caller that has to count. A
	// run whose hops all failed is a single-shot run wearing the loop's name,
	// and its numbers say nothing about the loop.
	OnGiveUp func(reason string)
	// Fits reports whether what has been retrieved still leaves room in the
	// model's context budget. Nil is no budget guard — the bound is MaxHops
	// alone, which is what the eval harness wants and what an ask with no
	// declared window gets.
	Fits func([]retrieval.RetrievedChunk) bool
}

// RetrieveWithHops runs retrieval, then lets the model name what is still
// missing and retrieves again, up to MaxHops times.
//
// Every round re-runs *every* query rather than fusing each round's list into
// the last: RRF over ranked lists is not associative, so folding round by round
// scores a chunk by its rank in a fusion rather than by its rank in a search.
// One search per query per round costs a search and buys a ranking that means
// what it says.
//
// Two things bound it, and both are checked before a hop is spent rather than
// after: the hop count, and the context budget. A loop that discovers it has
// overflowed on the fourth hop has spent four hops and a model call each.
//
// A hop that fails does not fail the ask. The floor is the passages already
// retrieved, and answering from those is exactly what MaxHops 0 does — so the
// loop stops, says why, and hands back what it has.
func RetrieveWithHops(
	ctx context.Context,
	retrieve retrieverFn,
	hop Hopper,
	question string,
	queries []string,
	topK int,
	opts HopOptions,
) ([]retrieval.RetrievedChunk, []string, error) {
	chunks, err := retrieve(ctx, queries, topK, nil)
	if err != nil {
		return nil, queries, fmt.Errorf("retrieve: %w", err)
	}
	if hop == nil || opts.MaxHops <= 0 {
		return chunks, queries, nil
	}

	warn := func(format string, args ...any) {
		if opts.Warn != nil {
			_, _ = fmt.Fprintf(opts.Warn, format+"\n", args...)
		}
	}

	for spent := 0; spent < opts.MaxHops; spent++ {
		if opts.Fits != nil && !opts.Fits(chunks) {
			warn("warning: stopped after %d of %d retrieval hops — what is already retrieved fills "+
				"the model's context budget, so another round has nowhere to go", spent, opts.MaxHops)
			break
		}
		next, err := hop.Next(ctx, question, passageTexts(chunks))
		if err != nil {
			if opts.OnGiveUp != nil {
				opts.OnGiveUp(err.Error())
			}
			warn("warning: stopped after %d of %d retrieval hops (%v) — answering with what "+
				"retrieval has already found", spent, opts.MaxHops, err)
			break
		}
		if next == "" {
			break
		}
		// Retrieving twice on one query fuses a ranked list with itself, which
		// moves nothing and costs a search. A model repeating itself is a loop
		// that has run out of ideas, not one that needs another round.
		if alreadyQueried(queries, next) {
			warn("warning: stopped after %d of %d retrieval hops — the model asked for a search "+
				"it had already run (%q)", spent, opts.MaxHops, next)
			break
		}
		queries = append(queries, next)
		if chunks, err = retrieve(ctx, queries, topK, nil); err != nil {
			return nil, queries, fmt.Errorf("retrieve: %w", err)
		}
	}
	return chunks, queries, nil
}

// passageTexts is what the hop reads: the retrieved text, in rank order.
func passageTexts(chunks []retrieval.RetrievedChunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.Text
	}
	return out
}

// alreadyQueried reports whether this search has already run, ignoring the
// case and spacing a model varies freely.
func alreadyQueried(queries []string, next string) bool {
	want := normalizeQuery(next)
	for _, q := range queries {
		if normalizeQuery(q) == want {
			return true
		}
	}
	return false
}

func normalizeQuery(q string) string {
	return strings.ToLower(strings.Join(strings.Fields(q), " "))
}
