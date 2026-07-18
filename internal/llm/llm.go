package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotofritz/timbuktu/internal/config"
)

// Role identifies the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single turn in a conversation.
type Message struct {
	Role    Role
	Content string
}

// Token is a streamed piece of a response.
type Token struct {
	Text  string
	Done  bool  // true on last token
	Error error // non-nil on stream error
}

// CallOptions overrides per-request LLM parameters.
// A nil Temperature means "unset" — the provider default is used — so an
// explicit temperature of 0 is expressible.
type CallOptions struct {
	Model       string
	Temperature *float64
	MaxTokens   int
}

// LLM streams chat completions.
type LLM interface {
	// Chat sends messages and streams tokens back on the returned channel.
	// Channel is closed after the last Token (Done=true or Error!=nil).
	Chat(ctx context.Context, messages []Message, opts ...CallOptions) (<-chan Token, error)
}

// LLMError carries provider-level HTTP error details.
type LLMError struct {
	Provider   string
	StatusCode int
	Message    string
}

func (e *LLMError) Error() string {
	return fmt.Sprintf("llm %s: HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

// AsLLMError unwraps err into target if it is or wraps an *LLMError.
func AsLLMError(err error, target **LLMError) bool {
	return errors.As(err, target)
}

// NewLLM returns an LLM selected by cfg.Provider.
func NewLLM(cfg *config.LLMConfig) (LLM, error) {
	switch cfg.Provider {
	case "claude":
		return newClaudeProvider(cfg)
	case "llama":
		return newLlamaProvider(cfg), nil
	case "openai":
		return newOpenAIProvider(cfg)
	case "ollama":
		return newOllamaProvider(cfg), nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", cfg.Provider)
	}
}
