package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/gotofritz/timbuktu/internal/config"
)

// openAIProvider speaks the OpenAI-compatible /v1/chat/completions protocol.
// It backs both the hosted OpenAI provider (bearer token required) and the
// local llama.cpp provider (name "llama", no token).
type openAIProvider struct {
	name      string
	baseURL   string
	model     string
	maxTokens int
	apiKey    string // empty → no Authorization header
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
	if err := config.ValidateKeyedBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("llm openai: %w", err)
	}
	return &openAIProvider{
		name:      "openai",
		baseURL:   baseURL,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		apiKey:    key,
		client:    &http.Client{},
	}, nil
}

// newLlamaProvider targets a local llama.cpp server. Its OpenAI-compatible
// endpoint needs no API key, defaulting to http://localhost:8080.
func newLlamaProvider(cfg *config.LLMConfig) *openAIProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return &openAIProvider{
		name:      "llama",
		baseURL:   baseURL,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		apiKey:    "",
		client:    &http.Client{},
	}
}

func (p *openAIProvider) Chat(ctx context.Context, messages []Message, opts ...CallOptions) (<-chan Token, error) {
	model := p.model
	maxTokens := p.maxTokens
	var temperature *float64
	if len(opts) > 0 {
		o := opts[0]
		if o.Model != "" {
			model = o.Model
		}
		if o.MaxTokens > 0 {
			maxTokens = o.MaxTokens
		}
		if o.Temperature != nil {
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

	reqBody := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   apiMessages,
		"stream":     true,
	}
	if temperature != nil {
		reqBody["temperature"] = *temperature
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", p.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", p.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: do request: %w", p.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := errorMessage(resp)
		_ = resp.Body.Close()
		return nil, &LLMError{
			Provider:   p.name,
			StatusCode: resp.StatusCode,
			Message:    msg,
		}
	}

	ch := make(chan Token)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			field, value := parseSSELine(line)
			if field != "data" {
				continue
			}
			if value == "[DONE]" {
				sendToken(ctx, ch, Token{Done: true})
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
				sendToken(ctx, ch, Token{Error: fmt.Errorf("%s: parse chunk: %w", p.name, err)})
				return
			}
			if len(payload.Choices) > 0 {
				if !sendToken(ctx, ch, Token{Text: payload.Choices[0].Delta.Content}) {
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			sendToken(ctx, ch, Token{Error: fmt.Errorf("%s: scan: %w", p.name, err)})
		}
	}()

	return ch, nil
}
