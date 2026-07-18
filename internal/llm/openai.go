package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/gotofritz/timbuktu/internal/config"
)

type openAIProvider struct {
	baseURL   string
	model     string
	maxTokens int
	apiKey    string
	client    *http.Client
}

func newOpenAIProvider(cfg *config.LLMConfig) (*openAIProvider, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("llm: OPENAI_API_KEY not set")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	return &openAIProvider{
		baseURL:   baseURL,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		apiKey:    key,
		client:    &http.Client{},
	}, nil
}

func (p *openAIProvider) Chat(ctx context.Context, messages []Message, opts ...CallOptions) (<-chan Token, error) {
	model := p.model
	maxTokens := p.maxTokens
	temperature := 0.0
	if len(opts) > 0 {
		o := opts[0]
		if o.Model != "" {
			model = o.Model
		}
		if o.MaxTokens > 0 {
			maxTokens = o.MaxTokens
		}
		if o.Temperature != 0 {
			temperature = o.Temperature
		}
	}

	type apiMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	apiMessages := make([]apiMsg, 0, len(messages))
	for _, m := range messages {
		apiMessages = append(apiMessages, apiMsg{Role: string(m.Role), Content: m.Content})
	}

	body, err := json.Marshal(map[string]any{
		"model":       model,
		"max_tokens":  maxTokens,
		"messages":    apiMessages,
		"stream":      true,
		"temperature": temperature,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, &LLMError{
			Provider:   "openai",
			StatusCode: resp.StatusCode,
			Message:    http.StatusText(resp.StatusCode),
		}
	}

	ch := make(chan Token)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		scanner := sseScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			field, value := parseSSELine(line)
			if field != "data" {
				continue
			}
			if value == "[DONE]" {
				ch <- Token{Done: true}
				return
			}
			var payload struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(value), &payload); err != nil {
				ch <- Token{Error: fmt.Errorf("openai: parse chunk: %w", err)}
				return
			}
			if len(payload.Choices) > 0 {
				ch <- Token{Text: payload.Choices[0].Delta.Content}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- Token{Error: fmt.Errorf("openai: scan: %w", err)}
		}
	}()

	return ch, nil
}
