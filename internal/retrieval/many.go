package retrieval

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotofritz/timbuktu/internal/search"
)

// RetrieveMany runs every query and fuses the ranked lists into one, so a chunk
// several queries agree on outranks a chunk only one of them found. Results are
// de-duplicated by chunk id and cut to topK.
//
// It is what query expansion retrieves with: N paraphrases of one question,
// each reaching passages the others' wording missed. Blank queries are dropped
// — a search on nothing matches everything, in rank order, which would fuse
// noise into every answer — and no usable query retrieves nothing rather than
// searching for the empty string.
//
// A single query is Retrieve, exactly: the same chunks with the same scores,
// not the same chunks rescored by a fusion of one list. Expansion is off by
// default, and that is only true if one query stays byte-for-byte what it was.
func (r *Retriever) RetrieveMany(
	ctx context.Context,
	queries []string,
	topK int,
	meta map[string]string,
) ([]RetrievedChunk, error) {
	usable := make([]string, 0, len(queries))
	for _, q := range queries {
		if strings.TrimSpace(q) != "" {
			usable = append(usable, q)
		}
	}
	switch len(usable) {
	case 0:
		return nil, nil
	case 1:
		return r.Retrieve(ctx, usable[0], topK, meta)
	}

	// Each query is searched at the full depth rather than at topK/N: fusion
	// ranks by agreement between whole lists, and a paraphrase asked for one
	// result contributes one opinion instead of a ranking.
	opts := search.Options{TopK: topK, Metadata: meta}
	lists := make([][]search.SearchResult, 0, len(usable))
	for _, q := range usable {
		results, err := r.searcher.Hybrid(ctx, q, opts)
		if err != nil {
			return nil, fmt.Errorf("retrieval: hybrid search: %w", err)
		}
		lists = append(lists, results)
	}

	fused := search.FuseRRF(lists, search.RRFK)
	limit := topK
	if limit <= 0 {
		limit = search.DefaultTopK
	}
	if limit > len(fused) {
		limit = len(fused)
	}
	return chunksFrom(fused[:limit]), nil
}
