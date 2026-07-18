# Subplan 04: Embedding Providers

## Goal

Define the `Embedder` interface and implement at least two providers (Ollama and
OpenAI). Provider is selected from config. No ingestion or search logic here.

## Deliverables

- `Embedder` interface in `internal/embeddings`
- Ollama provider (HTTP, no SDK)
- OpenAI provider (HTTP, no SDK)
- Config-driven factory `NewEmbedder(cfg)`
- Unit tests with HTTP mock server
- Integration test helpers (skipped unless `TBUK_INTEGRATION=1`)

## Package Layout

```
internal/embeddings/
  embeddings.go      ← Embedder interface, EmbedResult, factory
  ollama.go          ← OllamaEmbedder
  openai.go          ← OpenAIEmbedder
  embeddings_test.go ← mock HTTP server tests
```

## Interface

```go
type Embedder interface {
    // Embed returns one embedding vector per input text.
    // Batch size is provider-dependent; implementations may split internally.
    Embed(ctx context.Context, texts []string) ([][]float32, error)

    // Dimension returns the vector length this provider produces.
    Dimension() int
}
```

## Ollama Provider

Endpoint: `POST /api/embed`

```json
{"model":"nomic-embed-text","input":["text1","text2"]}
```

Response: `{"embeddings":[[0.1,0.2,...],[...]]}`

Config keys:
```yaml
embedding:
  provider: ollama
  model: nomic-embed-text
  base_url: http://localhost:11434
  dimension: 768
```

Dimension is **read from config** (not auto-detected) to avoid an extra round-trip.

## OpenAI Provider

Endpoint: `POST https://api.openai.com/v1/embeddings`

```json
{"model":"text-embedding-3-small","input":["text1","text2"]}
```

Auth: `Authorization: Bearer $OPENAI_API_KEY`

Config keys:
```yaml
embedding:
  provider: openai
  model: text-embedding-3-small
  dimension: 1536
```

API key read from env `OPENAI_API_KEY`; error if missing.

## Factory

```go
func NewEmbedder(cfg *config.EmbeddingConfig) (Embedder, error)
```

Returns `OllamaEmbedder` or `OpenAIEmbedder` based on `cfg.Provider`.
Unknown provider returns a typed error.

## Error Types

```go
type EmbedError struct {
    Provider   string
    StatusCode int
    Message    string
}
```

HTTP 429 → return `EmbedError` with `StatusCode=429` so callers can back off.

## Tests

- `TestOllamaEmbedder_success` — mock server returns valid embeddings
- `TestOllamaEmbedder_batchSplit` — input > batch limit triggers multiple requests
- `TestOllamaEmbedder_serverError` — 500 → error propagated
- `TestOpenAIEmbedder_success` — mock returns valid embeddings
- `TestOpenAIEmbedder_missingKey` — no env var → factory error
- `TestFactory_unknownProvider` — error with provider name

## Dependencies

No new packages — use `net/http`, `encoding/json` from stdlib.

## PR Scope

One PR. Depends on Subplan 01 (config). No storage or LLM.
