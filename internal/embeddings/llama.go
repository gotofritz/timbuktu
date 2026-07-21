package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gotofritz/timbuktu/internal/config"
)

type llamaEmbedder struct {
	baseURL   string
	dimension int
	client    *http.Client
}

func newLlamaEmbedder(cfg config.EmbeddingConfig) *llamaEmbedder {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return &llamaEmbedder{
		baseURL:   baseURL,
		dimension: cfg.Dimension,
		client:    &http.Client{},
	}
}

func (l *llamaEmbedder) Dimension() int { return l.dimension }

// Embed calls the native llama.cpp /embedding endpoint once per text.
func (l *llamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := l.embedOne(ctx, text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (l *llamaEmbedder) embedOne(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return nil, fmt.Errorf("llama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL+"/embedding", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llama: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &EmbedError{
			Provider:   "llama",
			StatusCode: resp.StatusCode,
			Message:    errorMessage(resp),
		}
	}

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("llama: decode response: %w", err)
	}
	// newer llama.cpp returns [{embedding:[[...]]}]; older returns {embedding:[...]}
	if len(raw) > 0 && raw[0] == '[' {
		var arr []struct {
			Embedding [][]float32 `json:"embedding"`
		}
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("llama: decode array response: %w", err)
		}
		if len(arr) == 0 || len(arr[0].Embedding) == 0 {
			return nil, fmt.Errorf("llama: empty array response")
		}
		return arr[0].Embedding[0], nil
	}
	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("llama: decode response: %w", err)
	}
	return result.Embedding, nil
}
