package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/llm"
)

// --- helpers ---

// claudeSSEHandler returns a handler that streams the given SSE events.
func claudeSSEHandler(events []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprint(w, e) //nolint:errcheck
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func openAISSEHandler(events []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprint(w, e) //nolint:errcheck
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func ollamaStreamHandler(lines []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, l := range lines {
			fmt.Fprintln(w, l) //nolint:errcheck
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// collectTokens drains a token channel into a slice.
func collectTokens(ch <-chan llm.Token) []llm.Token {
	var out []llm.Token
	for t := range ch {
		out = append(out, t)
	}
	return out
}

// --- Claude tests ---

func TestClaudeProvider_stream(t *testing.T) {
	events := []string{
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	srv := httptest.NewServer(claudeSSEHandler(events))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	cfg := &config.LLMConfig{
		Provider:  "claude",
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}

	ch, err := provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	tokens := collectTokens(ch)
	if len(tokens) != 3 {
		t.Fatalf("want 3 tokens, got %d", len(tokens))
	}
	if tokens[0].Text != "Hello" {
		t.Errorf("token 0: want %q, got %q", "Hello", tokens[0].Text)
	}
	if tokens[1].Text != " world" {
		t.Errorf("token 1: want %q, got %q", " world", tokens[1].Text)
	}
	if !tokens[2].Done {
		t.Errorf("last token: want Done=true")
	}
	if tokens[2].Error != nil {
		t.Errorf("last token: unexpected error %v", tokens[2].Error)
	}
}

func TestClaudeProvider_error_event(t *testing.T) {
	events := []string{
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"overloaded\"}}\n\n",
	}
	srv := httptest.NewServer(claudeSSEHandler(events))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	cfg := &config.LLMConfig{
		Provider:  "claude",
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}

	ch, err := provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	tokens := collectTokens(ch)
	if len(tokens) == 0 {
		t.Fatal("expected at least one token with error")
	}
	last := tokens[len(tokens)-1]
	if last.Error == nil {
		t.Errorf("want error token, got %+v", last)
	}
}

func TestClaudeProvider_missingKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := &config.LLMConfig{
		Provider:  "claude",
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 100,
	}
	_, err := llm.NewLLM(cfg)
	if err == nil {
		t.Fatal("expected error when ANTHROPIC_API_KEY missing")
	}
}

func TestClaudeProvider_callOptions(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		events := []string{
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprint(w, e) //nolint:errcheck
		}
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	cfg := &config.LLMConfig{
		Provider:  "claude",
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}

	opts := llm.CallOptions{Model: "claude-opus-4-8", MaxTokens: 512, Temperature: 0.5}
	ch, err := provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	}, opts)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	collectTokens(ch)

	if gotBody["model"] != "claude-opus-4-8" {
		t.Errorf("model: want claude-opus-4-8, got %v", gotBody["model"])
	}
	if gotBody["max_tokens"] != float64(512) {
		t.Errorf("max_tokens: want 512, got %v", gotBody["max_tokens"])
	}
}

// --- OpenAI tests ---

func TestOpenAIProvider_stream(t *testing.T) {
	events := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	srv := httptest.NewServer(openAISSEHandler(events))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	cfg := &config.LLMConfig{
		Provider:  "openai",
		Model:     "gpt-4o",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}

	ch, err := provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	tokens := collectTokens(ch)
	if len(tokens) != 3 {
		t.Fatalf("want 3 tokens, got %d", len(tokens))
	}
	if tokens[0].Text != "Hi" {
		t.Errorf("token 0: want %q, got %q", "Hi", tokens[0].Text)
	}
	if tokens[1].Text != " there" {
		t.Errorf("token 1: want %q, got %q", " there", tokens[1].Text)
	}
	if !tokens[2].Done {
		t.Errorf("last token: want Done=true")
	}
}

func TestOpenAIProvider_missingKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	cfg := &config.LLMConfig{
		Provider:  "openai",
		Model:     "gpt-4o",
		MaxTokens: 100,
	}
	_, err := llm.NewLLM(cfg)
	if err == nil {
		t.Fatal("expected error when OPENAI_API_KEY missing")
	}
}

// --- Ollama tests ---

func TestOllamaProvider_stream(t *testing.T) {
	lines := []string{
		`{"message":{"content":"Hey"},"done":false}`,
		`{"message":{"content":" you"},"done":false}`,
		`{"message":{"content":""},"done":true}`,
	}
	srv := httptest.NewServer(ollamaStreamHandler(lines))
	defer srv.Close()

	cfg := &config.LLMConfig{
		Provider:  "ollama",
		Model:     "llama3",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}

	ch, err := provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	tokens := collectTokens(ch)
	if len(tokens) != 3 {
		t.Fatalf("want 3 tokens, got %d", len(tokens))
	}
	if tokens[0].Text != "Hey" {
		t.Errorf("token 0: want %q, got %q", "Hey", tokens[0].Text)
	}
	if tokens[1].Text != " you" {
		t.Errorf("token 1: want %q, got %q", " you", tokens[1].Text)
	}
	if !tokens[2].Done {
		t.Errorf("last token: want Done=true")
	}
}

// --- Factory tests ---

func TestFactory_unknownProvider(t *testing.T) {
	cfg := &config.LLMConfig{
		Provider: "unknown-provider",
	}
	_, err := llm.NewLLM(cfg)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestFactory_ollamaNoAuth(t *testing.T) {
	// Ollama requires no env var — factory should succeed.
	cfg := &config.LLMConfig{
		Provider:  "ollama",
		Model:     "llama3",
		MaxTokens: 100,
		BaseURL:   "http://localhost:11434",
	}
	_, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("unexpected error for ollama: %v", err)
	}
}

func TestFactory_ollamaDefaultBaseURL(t *testing.T) {
	// No BaseURL → provider uses default localhost:11434.
	cfg := &config.LLMConfig{
		Provider: "ollama",
		Model:    "llama3",
	}
	_, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("unexpected error for ollama without BaseURL: %v", err)
	}
}

// --- LLMError tests ---

func TestLLMError_format(t *testing.T) {
	e := &llm.LLMError{Provider: "claude", StatusCode: 429, Message: "rate limited"}
	want := "llm claude: HTTP 429: rate limited"
	if e.Error() != want {
		t.Errorf("want %q, got %q", want, e.Error())
	}
}

func TestAsLLMError(t *testing.T) {
	e := &llm.LLMError{Provider: "openai", StatusCode: 500, Message: "server error"}
	wrapped := fmt.Errorf("wrap: %w", e)
	var target *llm.LLMError
	if !llm.AsLLMError(wrapped, &target) {
		t.Fatal("AsLLMError should return true for wrapped LLMError")
	}
	if target.StatusCode != 500 {
		t.Errorf("StatusCode: want 500, got %d", target.StatusCode)
	}
}

// --- OpenAI additional tests ---

func TestOpenAIProvider_serverError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	cfg := &config.LLMConfig{
		Provider:  "openai",
		Model:     "gpt-4o",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	_, err = provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	})
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	var llmErr *llm.LLMError
	if !llm.AsLLMError(err, &llmErr) {
		t.Fatalf("want *LLMError, got %T: %v", err, err)
	}
	if llmErr.StatusCode != 500 {
		t.Errorf("StatusCode: want 500, got %d", llmErr.StatusCode)
	}
}

func TestOpenAIProvider_callOptions(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		events := []string{"data: [DONE]\n\n"}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprint(w, e) //nolint:errcheck
		}
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	cfg := &config.LLMConfig{
		Provider:  "openai",
		Model:     "gpt-4o",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}

	opts := llm.CallOptions{Model: "gpt-4-turbo", MaxTokens: 256}
	ch, err := provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	}, opts)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	collectTokens(ch)

	if gotBody["model"] != "gpt-4-turbo" {
		t.Errorf("model: want gpt-4-turbo, got %v", gotBody["model"])
	}
	if gotBody["max_tokens"] != float64(256) {
		t.Errorf("max_tokens: want 256, got %v", gotBody["max_tokens"])
	}
}

// --- Ollama additional tests ---

func TestOllamaProvider_serverError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &config.LLMConfig{
		Provider:  "ollama",
		Model:     "llama3",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	_, err = provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	})
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	var llmErr *llm.LLMError
	if !llm.AsLLMError(err, &llmErr) {
		t.Fatalf("want *LLMError, got %T: %v", err, err)
	}
	if llmErr.StatusCode != 500 {
		t.Errorf("StatusCode: want 500, got %d", llmErr.StatusCode)
	}
}

// --- Claude system prompt test ---

func TestClaudeProvider_systemPrompt(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		events := []string{
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprint(w, e) //nolint:errcheck
		}
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	cfg := &config.LLMConfig{
		Provider:  "claude",
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}

	ch, err := provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Content: "be brief"},
		{Role: llm.RoleUser, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	collectTokens(ch)

	if gotBody["system"] != "be brief" {
		t.Errorf("system: want 'be brief', got %v", gotBody["system"])
	}
}

// --- Ollama callOptions test ---

func TestOllamaProvider_callOptions(t *testing.T) {
	var gotBody map[string]any
	lines := []string{
		`{"message":{"content":"ok"},"done":false}`,
		`{"message":{"content":""},"done":true}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, l := range lines {
			fmt.Fprintln(w, l) //nolint:errcheck
		}
	}))
	defer srv.Close()

	cfg := &config.LLMConfig{
		Provider: "ollama",
		Model:    "llama3",
		BaseURL:  srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}

	opts := llm.CallOptions{Model: "mistral"}
	ch, err := provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	}, opts)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	collectTokens(ch)

	if gotBody["model"] != "mistral" {
		t.Errorf("model: want mistral, got %v", gotBody["model"])
	}
}

// --- Claude HTTP error test ---

func TestClaudeProvider_serverError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "overloaded", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	cfg := &config.LLMConfig{
		Provider:  "claude",
		Model:     "claude-haiku-4-5-20251001",
		MaxTokens: 100,
		BaseURL:   srv.URL,
	}
	provider, err := llm.NewLLM(cfg)
	if err != nil {
		t.Fatalf("NewLLM: %v", err)
	}
	_, err = provider.Chat(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
	})
	if err == nil {
		t.Fatal("expected error from 503 response")
	}
	var llmErr *llm.LLMError
	if !llm.AsLLMError(err, &llmErr) {
		t.Fatalf("want *LLMError, got %T: %v", err, err)
	}
	if llmErr.StatusCode != 503 {
		t.Errorf("StatusCode: want 503, got %d", llmErr.StatusCode)
	}
}
