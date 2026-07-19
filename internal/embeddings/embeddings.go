package embeddings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gotofritz/timbuktu/internal/config"
)

// errorMessage reads up to 2 KB of an error response body and returns it as the
// message, falling back to the HTTP status text when the body is empty, so the
// provider's own error text ("input too long", "model not found") survives.
func errorMessage(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if msg := strings.TrimSpace(string(body)); msg != "" {
		return msg
	}
	return http.StatusText(resp.StatusCode)
}

// Embedder produces embedding vectors for text.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimension() int
}

// EmbedError carries provider-level HTTP error details.
type EmbedError struct {
	Provider   string
	StatusCode int
	Message    string
}

func (e *EmbedError) Error() string {
	return fmt.Sprintf("embed %s: HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

// AsEmbedError unwraps err into target if it is or wraps an *EmbedError.
func AsEmbedError(err error, target **EmbedError) bool {
	return errors.As(err, target)
}

// NewEmbedder returns an Embedder selected by cfg.Provider.
func NewEmbedder(cfg config.EmbeddingConfig) (Embedder, error) {
	switch cfg.Provider {
	case "llama":
		return newLlamaEmbedder(cfg), nil
	case "ollama":
		return newOllamaEmbedder(cfg), nil
	case "openai":
		return newOpenAIEmbedder(cfg)
	default:
		return nil, fmt.Errorf("embeddings: unknown provider %q", cfg.Provider)
	}
}
