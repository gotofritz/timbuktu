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

type claudeProvider struct {
	baseURL   string
	model     string
	maxTokens int
	apiKey    string
	client    *http.Client
}

func newClaudeProvider(cfg *config.LLMConfig) (*claudeProvider, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("llm: ANTHROPIC_API_KEY not set")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if err := config.ValidateKeyedBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("llm claude: %w", err)
	}
	return &claudeProvider{
		baseURL:   baseURL,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		apiKey:    key,
		client:    &http.Client{},
	}, nil
}

func (p *claudeProvider) Chat(ctx context.Context, messages []Message, opts ...CallOptions) (<-chan Token, error) {
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

	// Build messages array for Anthropic API (system separate from messages).
	type apiMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var apiMessages []apiMsg
	var systemPrompt string
	for _, m := range messages {
		if m.Role == RoleSystem {
			systemPrompt = m.Content
		} else {
			apiMessages = append(apiMessages, apiMsg{Role: string(m.Role), Content: m.Content})
		}
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
	if systemPrompt != "" {
		reqBody["system"] = systemPrompt
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("claude: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("claude: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := errorMessage(resp)
		_ = resp.Body.Close()
		return nil, &LLMError{
			Provider:   "claude",
			StatusCode: resp.StatusCode,
			Message:    msg,
		}
	}

	ch := make(chan Token)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		scanner := bufio.NewScanner(resp.Body)
		var eventType string

		for scanner.Scan() {
			line := scanner.Text()
			field, value := parseSSELine(line)

			switch field {
			case "event":
				eventType = value
			case "data":
				switch eventType {
				case "content_block_delta":
					var payload struct {
						Delta struct {
							Text string `json:"text"`
						} `json:"delta"`
					}
					if err := json.Unmarshal([]byte(value), &payload); err != nil {
						sendToken(ctx, ch, Token{Error: fmt.Errorf("claude: parse delta: %w", err)})
						return
					}
					if !sendToken(ctx, ch, Token{Text: payload.Delta.Text}) {
						return
					}

				case "message_stop":
					sendToken(ctx, ch, Token{Done: true})
					return

				case "error":
					var payload struct {
						Error struct {
							Message string `json:"message"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(value), &payload); err != nil {
						sendToken(ctx, ch, Token{Error: fmt.Errorf("claude: parse error event: %w", err)})
					} else {
						sendToken(ctx, ch, Token{Error: fmt.Errorf("claude: %s", payload.Error.Message)})
					}
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			sendToken(ctx, ch, Token{Error: fmt.Errorf("claude: scan: %w", err)})
		}
	}()

	return ch, nil
}
