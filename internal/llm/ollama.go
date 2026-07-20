package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gotofritz/timbuktu/internal/config"
)

type ollamaProvider struct {
	baseURL   string
	model     string
	maxTokens int
	client    *http.Client
}

func newOllamaProvider(cfg *config.LLMConfig) *ollamaProvider {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &ollamaProvider{
		baseURL:   baseURL,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		client:    &http.Client{},
	}
}

func (p *ollamaProvider) Chat(ctx context.Context, messages []Message, opts ...CallOptions) (<-chan Token, error) {
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

	// Ollama takes generation parameters in a nested "options" object, not as
	// top-level fields (num_predict is its name for max output tokens). Without
	// this, config llm.max_tokens and manifest temperature/max_tokens are silent
	// no-ops on ollama while working for claude/openai/llama.
	options := map[string]any{}
	if maxTokens > 0 {
		options["num_predict"] = maxTokens
	}
	if temperature != nil {
		options["temperature"] = *temperature
	}

	reqBody := map[string]any{
		"model":    model,
		"messages": apiMessages,
		"stream":   true,
	}
	if len(options) > 0 {
		reqBody["options"] = options
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := errorMessage(resp)
		_ = resp.Body.Close()
		return nil, &LLMError{
			Provider:   "ollama",
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
			if line == "" {
				continue
			}
			var payload struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Done bool `json:"done"`
			}
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				sendToken(ctx, ch, Token{Error: fmt.Errorf("ollama: parse line: %w", err)})
				return
			}
			if payload.Done {
				sendToken(ctx, ch, Token{Done: true})
				return
			}
			if !sendToken(ctx, ch, Token{Text: payload.Message.Content}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			sendToken(ctx, ch, Token{Error: fmt.Errorf("ollama: scan: %w", err)})
		}
	}()

	return ch, nil
}
