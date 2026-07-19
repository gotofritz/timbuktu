package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gotofritz/timbuktu/internal/config"
)

const ollamaBatchSize = 8

type ollamaEmbedder struct {
	baseURL   string
	model     string
	dimension int
	client    *http.Client
}

func newOllamaEmbedder(cfg config.EmbeddingConfig) *ollamaEmbedder {
	return &ollamaEmbedder{
		baseURL:   cfg.BaseURL,
		model:     cfg.Model,
		dimension: cfg.Dimension,
		client:    &http.Client{},
	}
}

func (o *ollamaEmbedder) Dimension() int { return o.dimension }

func (o *ollamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var all [][]float32
	for start := 0; start < len(texts); start += ollamaBatchSize {
		end := start + ollamaBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := o.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
	}
	return all, nil
}

func (o *ollamaEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model": o.model,
		"input": texts,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &EmbedError{
			Provider:   "ollama",
			StatusCode: resp.StatusCode,
			Message:    errorMessage(resp),
		}
	}

	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	return result.Embeddings, nil
}
