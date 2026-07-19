package embeddings

import (
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
)

// With an empty BaseURL, the llama and ollama embedder factories must resolve
// their own provider default rather than leaving the host blank (which sends
// requests to a schemeless "/embedding" URL).
func TestEmbedderFactories_resolveDefaultBaseURL(t *testing.T) {
	t.Run("llama defaults to localhost:8080", func(t *testing.T) {
		e := newLlamaEmbedder(config.EmbeddingConfig{BaseURL: ""})
		if e.baseURL != "http://localhost:8080" {
			t.Errorf("baseURL = %q, want http://localhost:8080", e.baseURL)
		}
	})
	t.Run("ollama defaults to localhost:11434", func(t *testing.T) {
		e := newOllamaEmbedder(config.EmbeddingConfig{BaseURL: ""})
		if e.baseURL != "http://localhost:11434" {
			t.Errorf("baseURL = %q, want http://localhost:11434", e.baseURL)
		}
	})
	t.Run("explicit BaseURL is respected", func(t *testing.T) {
		e := newLlamaEmbedder(config.EmbeddingConfig{BaseURL: "http://example:9000"})
		if e.baseURL != "http://example:9000" {
			t.Errorf("baseURL = %q, want the explicit value", e.baseURL)
		}
	})
}
