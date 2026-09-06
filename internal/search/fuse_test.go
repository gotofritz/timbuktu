package search_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/gotofritz/timbuktu/internal/search"
)

// result is the shorthand these tests rank: fusion reads the chunk id and the
// position, and carries everything else along untouched.
func result(id int64, text string) search.SearchResult {
	return search.SearchResult{ChunkID: id, DocumentID: 1, Path: "/a.md", ChunkIndex: int(id), Text: text, Score: 0.5}
}

func ids(results []search.SearchResult) []int64 {
	out := make([]int64, len(results))
	for i, r := range results {
		out[i] = r.ChunkID
	}
	return out
}

// The whole point of RRF: agreement across lists beats a high rank in one of
// them. A chunk every query found ranks above a chunk only one query found,
// however well that one query ranked it.
func TestFuseRRF_agreementWins(t *testing.T) {
	lists := [][]search.SearchResult{
		{result(9, "only here"), result(1, "everywhere")},
		{result(2, "twice"), result(1, "everywhere")},
		{result(3, "once"), result(1, "everywhere")},
	}

	fused := search.FuseRRF(lists, 60)

	if got := ids(fused)[0]; got != 1 {
		t.Errorf("want the chunk in all three lists first, got chunk %d (order %v)", got, ids(fused))
	}
}

func TestFuseRRF(t *testing.T) {
	tests := []struct {
		name  string
		lists [][]search.SearchResult
		want  []int64
	}{
		{
			name:  "no lists fuse to nothing",
			lists: nil,
			want:  []int64{},
		},
		{
			name:  "empty lists fuse to nothing",
			lists: [][]search.SearchResult{{}, {}},
			want:  []int64{},
		},
		{
			name:  "one list keeps its order",
			lists: [][]search.SearchResult{{result(7, "a"), result(8, "b"), result(9, "c")}},
			want:  []int64{7, 8, 9},
		},
		{
			name: "a chunk in two lists outranks two chunks in one",
			lists: [][]search.SearchResult{
				{result(1, "a"), result(2, "b")},
				{result(3, "c"), result(2, "b")},
			},
			want: []int64{2, 1, 3},
		},
		{
			name: "a duplicate is returned once",
			lists: [][]search.SearchResult{
				{result(5, "a")},
				{result(5, "a")},
				{result(5, "a")},
			},
			want: []int64{5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ids(search.FuseRRF(tt.lists, 60))
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fused order = %v, want %v", got, tt.want)
			}
		})
	}
}

// Ties are broken by chunk id rather than by map iteration order, so the same
// inputs fuse to the same list every time — a ranked result that reshuffles
// between identical runs is unreviewable, and TopK would truncate it
// differently each run.
func TestFuseRRF_isDeterministic(t *testing.T) {
	lists := [][]search.SearchResult{
		{result(4, "a"), result(3, "b"), result(2, "c"), result(1, "d")},
		{result(8, "e"), result(7, "f"), result(6, "g"), result(5, "h")},
	}

	first := ids(search.FuseRRF(lists, 60))
	for i := 0; i < 20; i++ {
		if got := ids(search.FuseRRF(lists, 60)); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d disagrees: %v vs %v", i, got, first)
		}
	}
}

// The fused score is the RRF sum, not whatever score the legs carried in: it is
// what Hybrid's MinScore is compared against, so it has to be the number the
// ranking was actually done on.
func TestFuseRRF_scoresAreTheRRFSum(t *testing.T) {
	lists := [][]search.SearchResult{
		{result(1, "a")},
		{result(1, "a")},
	}

	fused := search.FuseRRF(lists, 60)

	want := 2.0 / 61.0
	if len(fused) != 1 {
		t.Fatalf("want 1 fused result, got %d", len(fused))
	}
	if diff := fused[0].Score - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("score = %v, want %v (1/(60+1) from each of two lists)", fused[0].Score, want)
	}
}

// Fusion ranks; it does not rewrite. Everything but the score comes back as the
// first list that held the chunk had it, since that is what the caller renders.
func TestFuseRRF_carriesTheResultThrough(t *testing.T) {
	full := search.SearchResult{
		ChunkID: 3, DocumentID: 42, Path: "/docs/x.md", Title: "X", ChunkIndex: 7,
		Text: "chunk text", Score: 0.9, Source: "vector",
	}

	fused := search.FuseRRF([][]search.SearchResult{{full}}, 60)

	got := fused[0]
	got.Score = full.Score // the score is fusion's own; everything else is not
	if !reflect.DeepEqual(got, full) {
		t.Errorf("fusion changed the result:\n got %+v\nwant %+v", got, full)
	}
}

// Parity for the extraction: Hybrid's ranking is FuseRRF over its two legs, so
// the fused order of the same legs is the order Hybrid returns. A refactor of
// ranked output needs an equality bar, not a plausibility argument.
func TestHybridSearch_ranksItsLegsWithFuseRRF(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/p.txt", "Doc P")
	seedChunk(t, db, docID, 0, "authentication JWT token RS256 security", []float32{1, 0, 0})
	seedChunk(t, db, docID, 1, "JWT configuration setting", nil)
	seedChunk(t, db, docID, 2, "unrelated text about cars", []float32{1, 0, 0})
	seedChunk(t, db, docID, 3, "more JWT authentication notes", []float32{0, 1, 0})

	s := search.New(db, &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3})
	ctx := context.Background()
	const query = "JWT authentication"
	opts := search.Options{TopK: 4}

	// The legs exactly as Hybrid runs them: twice the requested depth, no
	// MinScore, the same metadata filter.
	legOpts := search.Options{TopK: 8}
	vec, err := s.Vector(ctx, query, legOpts)
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	kw, err := s.Keyword(ctx, query, legOpts)
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	fused := search.FuseRRF([][]search.SearchResult{vec, kw}, search.RRFK)

	got, err := s.Hybrid(ctx, query, opts)
	if err != nil {
		t.Fatalf("Hybrid: %v", err)
	}
	if len(got) > len(fused) {
		t.Fatalf("Hybrid returned %d results, fusion of its legs has %d", len(got), len(fused))
	}
	for i, r := range got {
		if r.ChunkID != fused[i].ChunkID {
			t.Errorf("rank %d: Hybrid has chunk %d, fusion of its legs has %d (%v vs %v)",
				i, r.ChunkID, fused[i].ChunkID, ids(got), ids(fused))
		}
		if diff := r.Score - fused[i].Score; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("rank %d: score %v, want the fused %v", i, r.Score, fused[i].Score)
		}
	}
}
