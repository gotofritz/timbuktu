package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/gotofritz/timbuktu/internal/config"
)

// openAIEmbedder speaks the OpenAI /v1/embeddings protocol. It backs the
// hosted openai provider (bearer token required) and the local mlx provider
// (no token; MLX_API_KEY optional).
type openAIEmbedder struct {
	name      string // provider name for error contexts ("openai" | "mlx")
	baseURL   string
	model     string
	apiKey    string // empty → no Authorization header
	dimension int
	client    *http.Client
	retry     retryPolicy
}

func newOpenAIEmbedder(cfg config.EmbeddingConfig) (*openAIEmbedder, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("embeddings: OPENAI_API_KEY not set")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	if err := config.ValidateKeyedBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("embeddings openai: %w", err)
	}
	return &openAIEmbedder{
		name:      "openai",
		baseURL:   baseURL,
		model:     cfg.Model,
		apiKey:    key,
		dimension: cfg.Dimension,
		client:    &http.Client{},
		retry:     defaultRetryPolicy(),
	}, nil
}

// newMLXEmbedder targets a local OpenAI-compatible MLX server's
// /v1/embeddings endpoint, defaulting to http://localhost:8080. No API key
// is required; when MLX_API_KEY is set it is sent as a Bearer token and the
// base URL must be HTTPS or loopback.
func newMLXEmbedder(cfg config.EmbeddingConfig) (*openAIEmbedder, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	key := os.Getenv("MLX_API_KEY")
	if key != "" {
		if err := config.ValidateKeyedBaseURL(baseURL); err != nil {
			return nil, fmt.Errorf("embeddings mlx: %w", err)
		}
	}
	return &openAIEmbedder{
		name:      "mlx",
		baseURL:   baseURL,
		model:     cfg.Model,
		apiKey:    key,
		dimension: cfg.Dimension,
		client:    &http.Client{},
		retry:     defaultRetryPolicy(),
	}, nil
}

func (o *openAIEmbedder) Dimension() int { return o.dimension }

func (o *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model": o.model,
		"input": texts,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", o.name, err)
	}

	newReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/v1/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%s: build request: %w", o.name, err)
		}
		req.Header.Set("Content-Type", "application/json")
		if o.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+o.apiKey)
		}
		return req, nil
	}

	resp, err := doWithRetry(ctx, o.client, o.retry, newReq)
	if err != nil {
		return nil, fmt.Errorf("%s: do request: %w", o.name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &EmbedError{
			Provider:   o.name,
			StatusCode: resp.StatusCode,
			Message:    errorMessage(resp),
		}
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", o.name, err)
	}

	// sort by index to maintain original order
	sort.Slice(result.Data, func(i, j int) bool {
		return result.Data[i].Index < result.Data[j].Index
	})

	out := make([][]float32, len(result.Data))
	for i, d := range result.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
