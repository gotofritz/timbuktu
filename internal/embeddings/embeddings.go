package embeddings

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotofritz/timbuktu/internal/config"
)

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
