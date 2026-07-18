# Subplan 05: LLM Providers

## Goal

Define the streaming `LLM` interface and implement providers for Claude, OpenAI,
and Ollama (llama.cpp-compatible API). Provider selected from config.

## Deliverables

- `LLM` interface with streaming token channel
- `Message` and `Token` types
- Claude provider (Anthropic Messages API, SSE streaming)
- OpenAI provider (chat completions, SSE streaming)
- Ollama provider (generate/chat endpoint, streaming JSON lines)
- Config-driven factory `NewLLM(cfg)`
- Unit tests with mock HTTP server

## Package Layout

```
internal/llm/
  llm.go            ← LLM interface, Message, Token, Role consts
  claude.go         ← ClaudeProvider
  openai.go         ← OpenAIProvider
  ollama.go         ← OllamaProvider
  stream.go         ← SSE parser, JSON-lines reader helpers
  llm_test.go
```

## Interface

```go
type Role string

const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
)

type Message struct {
    Role    Role
    Content string
}

type Token struct {
    Text  string
    Done  bool  // true on last token; Text may be empty
    Error error // non-nil on stream error
}

type LLM interface {
    // Chat sends messages and streams tokens back on the returned channel.
    // Channel is closed after the last Token (Done=true or Error!=nil).
    Chat(ctx context.Context, messages []Message) (<-chan Token, error)
}
```

## Claude Provider

Endpoint: `POST https://api.anthropic.com/v1/messages`

Streaming: `"stream": true` → SSE `content_block_delta` events.

Config:
```yaml
llm:
  provider: claude
  model: claude-haiku-4-5-20251001   # default; template can override
  max_tokens: 4096
```

Auth: `x-api-key: $ANTHROPIC_API_KEY`

Parse SSE events:
- `content_block_delta` → emit `Token{Text: delta.delta.text}`
- `message_stop` → emit `Token{Done: true}`
- `error` event → emit `Token{Error: ...}`

## OpenAI Provider

Endpoint: `POST https://api.openai.com/v1/chat/completions`

Streaming: `"stream": true` → SSE with `data: {"choices":[{"delta":{"content":"..."}}]}`

Auth: `Authorization: Bearer $OPENAI_API_KEY`

`[DONE]` sentinel → emit `Token{Done: true}`.

## Ollama Provider

Endpoint: `POST http://localhost:11434/api/chat`

Streaming: JSON lines, each `{"message":{"content":"..."},"done":false}`.

Final line has `"done":true`.

No auth required by default.

## Factory

```go
func NewLLM(cfg *config.LLMConfig) (LLM, error)
```

## Temperature & Max Tokens

Stored in config; template manifests can override per-request via `CallOptions`:

```go
type CallOptions struct {
    Model       string
    Temperature float64
    MaxTokens   int
}
```

`Chat` signature extended:
```go
Chat(ctx context.Context, messages []Message, opts ...CallOptions) (<-chan Token, error)
```

Variadic so callers without overrides compile without changes.

## Tests

- `TestClaudeProvider_stream` — mock SSE server, verify tokens received in order
- `TestClaudeProvider_error_event` — SSE error event → Token.Error set
- `TestOpenAIProvider_stream` — mock SSE, verify DONE handling
- `TestOllamaProvider_stream` — mock JSON-lines server
- `TestFactory_unknownProvider` — returns error

## Dependencies

No new packages beyond stdlib (`bufio`, `net/http`, `encoding/json`).

## PR Scope

One PR. Depends on Subplan 01 (config). No storage, no embeddings.
