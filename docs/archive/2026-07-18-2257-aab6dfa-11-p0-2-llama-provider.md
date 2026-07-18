# Subplan 11 — P0-2: llama.cpp LLM provider (+ P1-13 docs alignment)

**Status: Implemented.** Extracted from `docs/plans/11-poc-hardening.md`
(PR 2 of the phasing table). The remaining phases stay in the active plan.

## Problem

`config.Defaults()` sets `llm.provider: llama`, but `llm.NewLLM` only accepted
`claude | openai | ollama`. Out of the box, `tbuk ask` failed with
`llm: unknown provider "llama"`. Subplan 05 dropped the originally-required
llama.cpp provider without reconciling the default config or the docs.

## Fix (delivered)

llama.cpp's server exposes an OpenAI-compatible `/v1/chat/completions`
endpoint, so `llama` is backed by the existing `openAIProvider` instead of a
duplicated SSE loop:

- Added a `name` field to `openAIProvider` and made the `Authorization` header
  conditional on a non-empty `apiKey`, so one adapter serves both hosted OpenAI
  (bearer required) and local llama.cpp (no key).
- `newLlamaProvider` requires no API key and defaults `base_url` to
  `http://localhost:8080`.
- Provider name propagates into `LLMError` and wrapped stream errors
  (`llama: ...` vs `openai: ...`).

## Docs alignment (P1-13, delivered)

- README config comment: dropped the non-existent `voyage` embedder
  (`embedding.provider: llama | ollama | openai`).
- `docs/initial-context.md`: added the `llama` LLM provider row and its
  `http://localhost:8080` default; listed `llama` in the `llm/` package summary.

Doctor is unchanged: its `/health` + `/v1/models` probes are llama.cpp
conventions and already correct for the default `llama` provider. The
hosted-provider probe fix is P1-12 (a later PR).

## Tests (written first)

- `TestFactory_llamaNoAuth` — factory returns a provider for `llama` with no
  env vars.
- `TestFactory_llamaDefaultBaseURL` — empty `base_url` falls back to
  `http://localhost:8080`.
- `TestLlamaProvider_stream` — streams against a mock OpenAI-format SSE server
  and asserts no `Authorization` header is sent.
- `TestLlamaProvider_serverError` — a non-200 surfaces an `*LLMError` whose
  `Provider` is `llama`.

Confirmed failing (`llm: unknown provider "llama"`) before implementation.

## Files touched

- `internal/llm/llm.go` — factory `llama` case.
- `internal/llm/openai.go` — `name` field, optional bearer token,
  `newLlamaProvider`, provider-named errors.
- `internal/llm/llm_test.go` — llama tests.
- `README.md`, `docs/initial-context.md` — docs alignment.

`make check-ci` green; total coverage 86.1%.
