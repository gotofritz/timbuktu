package embeddings_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/embeddings"
)

// --- helpers ---

func ollamaHandler(t *testing.T, responses [][][]float32, gotBodies *[][]string) http.HandlerFunc {
	t.Helper()
	call := 0
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if gotBodies != nil {
			*gotBodies = append(*gotBodies, req.Input)
		}
		if call >= len(responses) {
			t.Errorf("unexpected call %d", call)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		resp := map[string]any{"embeddings": responses[call]}
		call++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}
}

func openAIHandler(t *testing.T, vecs [][]float32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		data := make([]map[string]any, len(vecs))
		for i, v := range vecs {
			data[i] = map[string]any{"embedding": v, "index": i}
		}
		resp := map[string]any{"data": data}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}
}

// --- llama.cpp handler ---

func llamaHandler(t *testing.T, vecs [][]float32) http.HandlerFunc {
	return llamaHandlerFormat(t, vecs, false)
}

func llamaHandlerFormat(t *testing.T, vecs [][]float32, arrayFormat bool) http.HandlerFunc {
	t.Helper()
	call := 0
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("llama decode body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if call >= len(vecs) {
			t.Errorf("unexpected llama call %d", call)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		var resp any
		if arrayFormat {
			// newer llama.cpp: [{embedding: [[v1, v2, ...]]}]
			resp = []map[string]any{{"embedding": [][]float32{vecs[call]}}}
		} else {
			resp = map[string]any{"embedding": vecs[call]}
		}
		call++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}
}

// --- llama.cpp tests ---

func TestLlamaEmbedder_success(t *testing.T) {
	vecs := [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	srv := httptest.NewServer(llamaHandler(t, vecs))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider:  "llama",
		BaseURL:   srv.URL,
		Dimension: 3,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	got, err := emb.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	for i, row := range got {
		if len(row) != 3 {
			t.Errorf("row %d: want len 3, got %d", i, len(row))
		}
	}
	if emb.Dimension() != 3 {
		t.Errorf("Dimension: want 3, got %d", emb.Dimension())
	}
}

func TestLlamaEmbedder_arrayResponse(t *testing.T) {
	// newer llama.cpp versions return [{embedding:[...]}] instead of {embedding:[...]}
	vecs := [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	srv := httptest.NewServer(llamaHandlerFormat(t, vecs, true))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider:  "llama",
		BaseURL:   srv.URL,
		Dimension: 3,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	got, err := emb.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	for i, row := range got {
		if len(row) != 3 {
			t.Errorf("row %d: want len 3, got %d", i, len(row))
		}
	}
}

func TestLlamaEmbedder_serverError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider:  "llama",
		BaseURL:   srv.URL,
		Dimension: 768,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	_, err = emb.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	var embedErr *embeddings.EmbedError
	if !embeddings.AsEmbedError(err, &embedErr) {
		t.Fatalf("want *EmbedError, got %T: %v", err, err)
	}
	if embedErr.StatusCode != 500 {
		t.Errorf("StatusCode: want 500, got %d", embedErr.StatusCode)
	}
}

func TestLlamaEmbedder_perTextRequests(t *testing.T) {
	// llama.cpp /embedding is single-text; 3 texts → 3 HTTP calls
	vecs := [][]float32{{0.1}, {0.2}, {0.3}}
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req struct {
			Content string `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		resp := map[string]any{"embedding": vecs[callCount-1]}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider:  "llama",
		BaseURL:   srv.URL,
		Dimension: 1,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	got, err := emb.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 results, got %d", len(got))
	}
	if callCount != 3 {
		t.Errorf("want 3 HTTP calls (one per text), got %d", callCount)
	}
}

// --- Ollama tests ---

func TestOllamaEmbedder_success(t *testing.T) {
	want := [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	srv := httptest.NewServer(ollamaHandler(t, [][][]float32{want}, nil))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider:  "ollama",
		Model:     "nomic-embed-text",
		BaseURL:   srv.URL,
		Dimension: 3,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	got, err := emb.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	for i, row := range got {
		if len(row) != 3 {
			t.Errorf("row %d: want len 3, got %d", i, len(row))
		}
	}
	if emb.Dimension() != 3 {
		t.Errorf("Dimension: want 3, got %d", emb.Dimension())
	}
}

func TestOllamaEmbedder_batchSplit(t *testing.T) {
	// batch limit is 8; send 10 texts → expect 2 HTTP calls
	batch1 := make([][]float32, 8)
	batch2 := make([][]float32, 2)
	for i := range batch1 {
		batch1[i] = []float32{float32(i)}
	}
	for i := range batch2 {
		batch2[i] = []float32{float32(i + 8)}
	}

	var gotBodies [][]string
	srv := httptest.NewServer(ollamaHandler(t, [][][]float32{batch1, batch2}, &gotBodies))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider:  "ollama",
		Model:     "nomic-embed-text",
		BaseURL:   srv.URL,
		Dimension: 1,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	texts := make([]string, 10)
	for i := range texts {
		texts[i] = "text"
	}
	got, err := emb.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("want 10 results, got %d", len(got))
	}
	if len(gotBodies) != 2 {
		t.Errorf("want 2 HTTP calls, got %d", len(gotBodies))
	}
	if len(gotBodies[0]) != 8 {
		t.Errorf("first batch: want 8 texts, got %d", len(gotBodies[0]))
	}
	if len(gotBodies[1]) != 2 {
		t.Errorf("second batch: want 2 texts, got %d", len(gotBodies[1]))
	}
}

func TestOllamaEmbedder_serverError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider:  "ollama",
		Model:     "nomic-embed-text",
		BaseURL:   srv.URL,
		Dimension: 768,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	_, err = emb.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	var embedErr *embeddings.EmbedError
	if !embeddings.AsEmbedError(err, &embedErr) {
		t.Fatalf("want *EmbedError, got %T: %v", err, err)
	}
	if embedErr.StatusCode != 500 {
		t.Errorf("StatusCode: want 500, got %d", embedErr.StatusCode)
	}
}

func TestOllamaEmbedder_rateLimitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	cfg := config.EmbeddingConfig{
		Provider:  "ollama",
		Model:     "nomic-embed-text",
		BaseURL:   srv.URL,
		Dimension: 768,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	_, err = emb.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error from 429 response")
	}
	var embedErr *embeddings.EmbedError
	if !embeddings.AsEmbedError(err, &embedErr) {
		t.Fatalf("want *EmbedError, got %T: %v", err, err)
	}
	if embedErr.StatusCode != 429 {
		t.Errorf("StatusCode: want 429, got %d", embedErr.StatusCode)
	}
}

// --- OpenAI tests ---

func TestOpenAIEmbedder_success(t *testing.T) {
	want := [][]float32{{0.1, 0.2}, {0.3, 0.4}}
	srv := httptest.NewServer(openAIHandler(t, want))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	cfg := config.EmbeddingConfig{
		Provider:  "openai",
		Model:     "text-embedding-3-small",
		BaseURL:   srv.URL,
		Dimension: 2,
	}
	emb, err := embeddings.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	got, err := emb.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if emb.Dimension() != 2 {
		t.Errorf("Dimension: want 2, got %d", emb.Dimension())
	}
}

func TestOpenAIEmbedder_rejectsInsecureRemoteBaseURL(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	cfg := config.EmbeddingConfig{
		Provider:  "openai",
		Model:     "text-embedding-3-small",
		BaseURL:   "http://api.remote.example.com",
		Dimension: 2,
	}
	if _, err := embeddings.NewEmbedder(cfg); err == nil {
		t.Fatal("expected error for API key on non-HTTPS remote base_url")
	}
}

func TestOpenAIEmbedder_missingKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	cfg := config.EmbeddingConfig{
		Provider:  "openai",
		Model:     "text-embedding-3-small",
		Dimension: 1536,
	}
	_, err := embeddings.NewEmbedder(cfg)
	if err == nil {
		t.Fatal("expected error when OPENAI_API_KEY is missing")
	}
}

// --- error body tests (P1-9) ---

func TestOpenAIEmbedder_errorBodyIncluded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"input too long"}}`) //nolint:errcheck
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	emb, err := embeddings.NewEmbedder(config.EmbeddingConfig{
		Provider: "openai", Model: "m", BaseURL: srv.URL, Dimension: 2,
	})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	_, err = emb.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected error from 400")
	}
	if !strings.Contains(err.Error(), "input too long") {
		t.Errorf("error must include response body, got %v", err)
	}
}

func TestLlamaEmbedder_errorBodyIncluded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "model still loading") //nolint:errcheck
	}))
	defer srv.Close()

	emb, err := embeddings.NewEmbedder(config.EmbeddingConfig{
		Provider: "llama", BaseURL: srv.URL, Dimension: 3,
	})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	_, err = emb.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected error from 503")
	}
	if !strings.Contains(err.Error(), "model still loading") {
		t.Errorf("error must include response body, got %v", err)
	}
}

func TestOllamaEmbedder_errorBodyIncluded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"model not found"}`) //nolint:errcheck
	}))
	defer srv.Close()

	emb, err := embeddings.NewEmbedder(config.EmbeddingConfig{
		Provider: "ollama", Model: "m", BaseURL: srv.URL, Dimension: 2,
	})
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	_, err = emb.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected error from 404")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error must include response body, got %v", err)
	}
}

// --- factory tests ---

func TestFactory_unknownProvider(t *testing.T) {
	cfg := config.EmbeddingConfig{
		Provider:  "unknown-provider",
		Dimension: 768,
	}
	_, err := embeddings.NewEmbedder(cfg)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}
