package retrieval_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/search"
)

// scriptedSearcher answers each query from a script and records what it was
// asked, so a test can assert both the fusion and the searches behind it.
type scriptedSearcher struct {
	byQuery map[string][]search.SearchResult
	err     error
	asked   []string
	topK    []int
}

func (s *scriptedSearcher) Hybrid(_ context.Context, query string, opts search.Options) ([]search.SearchResult, error) {
	s.asked = append(s.asked, query)
	s.topK = append(s.topK, opts.TopK)
	if s.err != nil {
		return nil, s.err
	}
	return s.byQuery[query], nil
}

func hit(id int64) search.SearchResult {
	return search.SearchResult{ChunkID: id, DocumentID: 1, Path: "/a.md", ChunkIndex: int(id), Text: "text", Score: 0.5}
}

func chunkIDs(chunks []retrieval.RetrievedChunk) []int64 {
	out := make([]int64, len(chunks))
	for i, c := range chunks {
		out[i] = c.ChunkID
	}
	return out
}

// The point of expansion: a chunk that several paraphrases agree on outranks a
// chunk only one of them found.
func TestRetrieveMany_fusesTheQueries(t *testing.T) {
	s := &scriptedSearcher{byQuery: map[string][]search.SearchResult{
		"how do slices grow": {hit(9), hit(1)},
		"slice growth":       {hit(2), hit(1)},
		"append reallocate":  {hit(3), hit(1)},
	}}
	r := retrieval.New(s)

	chunks, err := r.RetrieveMany(context.Background(),
		[]string{"how do slices grow", "slice growth", "append reallocate"}, 5, nil)
	if err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	if got := chunkIDs(chunks); got[0] != 1 {
		t.Errorf("want the chunk all three queries found first, got %v", got)
	}
	if len(s.asked) != 3 {
		t.Errorf("want one search per query, got %d: %v", len(s.asked), s.asked)
	}
}

// A chunk several queries return is one chunk in the prompt, not one per query:
// the same passage pasted three times spends the context window on nothing.
func TestRetrieveMany_deduplicatesByChunkID(t *testing.T) {
	s := &scriptedSearcher{byQuery: map[string][]search.SearchResult{
		"a": {hit(1), hit(2)},
		"b": {hit(1), hit(2)},
	}}
	r := retrieval.New(s)

	chunks, err := r.RetrieveMany(context.Background(), []string{"a", "b"}, 5, nil)
	if err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	if want := []int64{1, 2}; !reflect.DeepEqual(chunkIDs(chunks), want) {
		t.Errorf("chunks = %v, want %v", chunkIDs(chunks), want)
	}
}

// Parity: one query through RetrieveMany is the single-shot Retrieve, scores
// and all. Everything that already calls Retrieve has to keep getting exactly
// what it got, or "expansion is off by default" would not be true.
func TestRetrieveMany_oneQueryEqualsRetrieve(t *testing.T) {
	results := []search.SearchResult{
		{ChunkID: 1, DocumentID: 10, Path: "/docs/a.md", Title: "A", ChunkIndex: 0, Text: "hello", Score: 0.91},
		{ChunkID: 2, DocumentID: 11, Path: "/docs/b.md", Title: "B", ChunkIndex: 3, Text: "world", Score: 0.42},
	}
	one := &scriptedSearcher{byQuery: map[string][]search.SearchResult{"q": results}}
	many := &scriptedSearcher{byQuery: map[string][]search.SearchResult{"q": results}}

	single, err := retrieval.New(one).Retrieve(context.Background(), "q", 5, nil)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	fused, err := retrieval.New(many).RetrieveMany(context.Background(), []string{"q"}, 5, nil)
	if err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	if !reflect.DeepEqual(single, fused) {
		t.Errorf("one query differs from Retrieve:\n got %+v\nwant %+v", fused, single)
	}
}

func TestRetrieveMany_topK(t *testing.T) {
	tests := []struct {
		name    string
		queries []string
		topK    int
		want    int
	}{
		{"fewer results than asked for", []string{"a", "b"}, 5, 4},
		{"truncated to topK", []string{"a", "b"}, 2, 2},
		{"a zero topK takes the default", []string{"a", "b"}, 0, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &scriptedSearcher{byQuery: map[string][]search.SearchResult{
				"a": {hit(1), hit(2)},
				"b": {hit(3), hit(4)},
			}}
			chunks, err := retrieval.New(s).RetrieveMany(context.Background(), tt.queries, tt.topK, nil)
			if err != nil {
				t.Fatalf("RetrieveMany: %v", err)
			}
			if len(chunks) != tt.want {
				t.Errorf("got %d chunks, want %d: %v", len(chunks), tt.want, chunkIDs(chunks))
			}
		})
	}
}

// A topK of zero is the same default a single-shot search takes, applied to the
// fused list — not "no limit", which would put every paraphrase's whole result
// set into one prompt.
func TestRetrieveMany_zeroTopKIsTheDefault(t *testing.T) {
	s := &scriptedSearcher{byQuery: map[string][]search.SearchResult{
		"a": {hit(1), hit(2), hit(3)},
		"b": {hit(4), hit(5), hit(6)},
		"c": {hit(7), hit(8), hit(9)},
	}}

	chunks, err := retrieval.New(s).RetrieveMany(context.Background(), []string{"a", "b", "c"}, 0, nil)
	if err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	if len(chunks) != search.DefaultTopK {
		t.Errorf("got %d chunks, want the default %d", len(chunks), search.DefaultTopK)
	}
}

// Each query is searched at the full depth, not at topK/N: fusion needs ranked
// lists to fuse, and a paraphrase asked for one result contributes one opinion.
func TestRetrieveMany_searchesEachQueryAtFullDepth(t *testing.T) {
	s := &scriptedSearcher{byQuery: map[string][]search.SearchResult{"a": {hit(1)}, "b": {hit(2)}}}

	if _, err := retrieval.New(s).RetrieveMany(context.Background(), []string{"a", "b"}, 7, nil); err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	for i, k := range s.topK {
		if k != 7 {
			t.Errorf("query %d searched with TopK %d, want 7", i, k)
		}
	}
}

// A blank paraphrase is a search that returns the whole corpus in rank order,
// which would fuse noise into every answer. Dropping it costs nothing: the
// queries that mean something are still there.
func TestRetrieveMany_skipsBlankQueries(t *testing.T) {
	s := &scriptedSearcher{byQuery: map[string][]search.SearchResult{"a": {hit(1)}}}

	chunks, err := retrieval.New(s).RetrieveMany(context.Background(), []string{"", "  ", "a"}, 5, nil)
	if err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	if !reflect.DeepEqual(s.asked, []string{"a"}) {
		t.Errorf("searched %v, want only the non-blank query", s.asked)
	}
	if len(chunks) != 1 {
		t.Errorf("got %d chunks, want 1", len(chunks))
	}
}

func TestRetrieveMany_noQueriesRetrievesNothing(t *testing.T) {
	s := &scriptedSearcher{byQuery: map[string][]search.SearchResult{"a": {hit(1)}}}

	chunks, err := retrieval.New(s).RetrieveMany(context.Background(), []string{"", "   "}, 5, nil)
	if err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("got %d chunks, want none", len(chunks))
	}
	if len(s.asked) != 0 {
		t.Errorf("searched %v, want nothing searched", s.asked)
	}
}

func TestRetrieveMany_propagatesSearchError(t *testing.T) {
	sentinel := errors.New("search failed")
	r := retrieval.New(&scriptedSearcher{err: sentinel})

	if _, err := r.RetrieveMany(context.Background(), []string{"a", "b"}, 5, nil); !errors.Is(err, sentinel) {
		t.Errorf("want the sentinel error, got %v", err)
	}
}

func TestRetrieveMany_citationsAndFields(t *testing.T) {
	s := &scriptedSearcher{byQuery: map[string][]search.SearchResult{
		"a": {{ChunkID: 4, DocumentID: 8, Path: "/docs/readme.md", Title: "R", ChunkIndex: 2, Text: "content"}},
		"b": {{ChunkID: 5, DocumentID: 9, Path: "/docs/other.md", Title: "O", ChunkIndex: 0, Text: "more"}},
	}}

	chunks, err := retrieval.New(s).RetrieveMany(context.Background(), []string{"a", "b"}, 5, nil)
	if err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	byID := map[int64]retrieval.RetrievedChunk{}
	for _, c := range chunks {
		byID[c.ChunkID] = c
	}
	if got := byID[4]; got.Citation != "/docs/readme.md §2" || got.Title != "R" || got.Text != "content" {
		t.Errorf("chunk 4 = %+v, want the mapped fields and citation", got)
	}
	if got := byID[5]; got.Citation != "/docs/other.md §0" || got.DocumentID != 9 {
		t.Errorf("chunk 5 = %+v, want the mapped fields and citation", got)
	}
}

// The metadata pre-filter is the caller's, and every paraphrase searches under
// it: an expansion that quietly widened the corpus would be a filter that only
// held for the question as typed.
func TestRetrieveMany_passesMetadataToEveryQuery(t *testing.T) {
	var seen []map[string]string
	s := &metaSearcher{seen: &seen}
	meta := map[string]string{"lang": "go"}

	if _, err := retrieval.New(s).RetrieveMany(context.Background(), []string{"a", "b"}, 5, meta); err != nil {
		t.Fatalf("RetrieveMany: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("want 2 searches, got %d", len(seen))
	}
	for i, m := range seen {
		if !reflect.DeepEqual(m, meta) {
			t.Errorf("search %d filtered on %v, want %v", i, m, meta)
		}
	}
}

type metaSearcher struct{ seen *[]map[string]string }

func (m *metaSearcher) Hybrid(_ context.Context, _ string, opts search.Options) ([]search.SearchResult, error) {
	*m.seen = append(*m.seen, opts.Metadata)
	return nil, nil
}
