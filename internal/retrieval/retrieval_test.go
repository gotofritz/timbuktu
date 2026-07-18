package retrieval_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/search"
)

// mockSearcher implements the SearcherInterface for testing.
type mockSearcher struct {
	results []search.SearchResult
	err     error
}

func (m *mockSearcher) Hybrid(_ context.Context, _ string, _ search.Options) ([]search.SearchResult, error) {
	return m.results, m.err
}

func TestRetriever_returnsTopK(t *testing.T) {
	results := []search.SearchResult{
		{ChunkID: 1, DocumentID: 10, Path: "/docs/a.md", Title: "A", ChunkIndex: 0, Text: "hello", Score: 0.9},
		{ChunkID: 2, DocumentID: 11, Path: "/docs/b.md", Title: "B", ChunkIndex: 3, Text: "world", Score: 0.8},
	}
	r := retrieval.New(&mockSearcher{results: results})

	chunks, err := r.Retrieve(context.Background(), "what is hello", 5, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(chunks))
	}
}

func TestRetriever_citationFormat(t *testing.T) {
	results := []search.SearchResult{
		{ChunkID: 1, DocumentID: 10, Path: "/docs/readme.md", ChunkIndex: 2, Text: "content", Score: 0.9},
	}
	r := retrieval.New(&mockSearcher{results: results})

	chunks, err := r.Retrieve(context.Background(), "query", 5, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/docs/readme.md §2"
	if chunks[0].Citation != want {
		t.Errorf("citation: want %q, got %q", want, chunks[0].Citation)
	}
}

func TestRetriever_propagatesSearchError(t *testing.T) {
	sentinel := errors.New("search failed")
	r := retrieval.New(&mockSearcher{err: sentinel})

	_, err := r.Retrieve(context.Background(), "query", 5, nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel error, got %v", err)
	}
}

func TestRetriever_mapsAllFields(t *testing.T) {
	sr := search.SearchResult{
		ChunkID:    42,
		DocumentID: 7,
		Path:       "/x.md",
		Title:      "Title",
		ChunkIndex: 5,
		Text:       "chunk text",
		Score:      0.75,
	}
	r := retrieval.New(&mockSearcher{results: []search.SearchResult{sr}})

	chunks, err := r.Retrieve(context.Background(), "q", 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := chunks[0]
	if got.ChunkID != sr.ChunkID {
		t.Errorf("ChunkID: want %d, got %d", sr.ChunkID, got.ChunkID)
	}
	if got.DocumentID != sr.DocumentID {
		t.Errorf("DocumentID: want %d, got %d", sr.DocumentID, got.DocumentID)
	}
	if got.Path != sr.Path {
		t.Errorf("Path: want %q, got %q", sr.Path, got.Path)
	}
	if got.Title != sr.Title {
		t.Errorf("Title: want %q, got %q", sr.Title, got.Title)
	}
	if got.ChunkIndex != sr.ChunkIndex {
		t.Errorf("ChunkIndex: want %d, got %d", sr.ChunkIndex, got.ChunkIndex)
	}
	if got.Text != sr.Text {
		t.Errorf("Text: want %q, got %q", sr.Text, got.Text)
	}
	if got.Score != sr.Score {
		t.Errorf("Score: want %f, got %f", sr.Score, got.Score)
	}
	wantCitation := fmt.Sprintf("%s §%d", sr.Path, sr.ChunkIndex)
	if got.Citation != wantCitation {
		t.Errorf("Citation: want %q, got %q", wantCitation, got.Citation)
	}
}
