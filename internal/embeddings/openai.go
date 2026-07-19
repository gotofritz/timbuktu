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

type openAIEmbedder struct {
	baseURL   string
	model     string
	apiKey    string
	dimension int
	client    *http.Client
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
		baseURL:   baseURL,
		model:     cfg.Model,
		apiKey:    key,
		dimension: cfg.Dimension,
		client:    &http.Client{},
	}, nil
}

func (o *openAIEmbedder) Dimension() int { return o.dimension }

func (o *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model": o.model,
		"input": texts,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &EmbedError{
			Provider:   "openai",
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
		return nil, fmt.Errorf("openai: decode response: %w", err)
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
