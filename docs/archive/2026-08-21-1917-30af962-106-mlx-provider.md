# Plan 106: MLX provider — Apple-silicon local inference, default over llama.cpp

Lands issue [#106](https://github.com/gotofritz/timbuktu/issues/106): add a
first-class `mlx` provider for both LLM and embeddings — targeting the family
of OpenAI-compatible servers that run [MLX](https://github.com/ml-explore/mlx)
models natively on Apple silicon — and promote it over llama.cpp as the
default local backend. The issue links [nativ](https://github.com/Blaizzy/nativ)
as *inspiration*; the provider is deliberately not tied to any one server app.

## Goal

```bash
# any OpenAI-compatible MLX server running on the Mac at :8080, tbuk fresh install:
tbuk init          # config.yaml now says provider: mlx for llm + embedding
tbuk doctor        # probes the server, reports the loaded model
tbuk ingest ./docs # embeds via POST /v1/embeddings
tbuk ask "…"       # streams via POST /v1/chat/completions
```

Success = a Mac user with an MLX server running needs zero config edits after
`tbuk init`; existing llama.cpp/ollama/hosted users keep working by setting
`provider: llama` (etc.) exactly as today; docs lead with the MLX path on
Apple silicon and keep llama.cpp as the cross-platform alternative.

## The MLX server landscape (research summary)

MLX itself is a framework, not a server. What tbuk talks to is one of several
servers that load MLX models and expose the same OpenAI-compatible HTTP
surface:

| Server | Chat | Embeddings | Default port | Notes |
|---|---|---|---|---|
| `mlx_lm.server` (ml-explore/mlx-lm) | ✓ `/v1/chat/completions` | — (chat only) | 8080 | `pip install mlx-lm`; the reference server |
| mlx-openai-server (cubist38) | ✓ | ✓ `/v1/embeddings` | 8000 | FastAPI; text + vision + embeddings |
| LM Studio (MLX engine) | ✓ | ✓ | 1234 | GUI app; serves MLX models on Apple silicon |
| nativ (Blaizzy) | ✓ | ✓ | 8080 | macOS app bundling mlx-vlm; optional Bearer API key; the issue's inspiration |

Common denominators tbuk can rely on:

- `POST /v1/chat/completions` with SSE streaming — byte-compatible with what
  the `llama` provider already speaks.
- `GET /v1/models` — universal (part of the OpenAI surface).
- `POST /v1/embeddings` with the OpenAI request/response shape — on the
  servers that do embeddings at all.
- Models named by Hugging Face repo id (`mlx-community/…`).
- No auth by default; some servers support an optional Bearer key.
- **Not** universal: `/health` (llama.cpp has it, several MLX servers don't).

So: **no new protocol code is required** — this is a factory + config + docs
change, a small refactor to let the OpenAI-shape embedder run keyless, and a
one-line doctor adjustment for the probe endpoint.

## Design decisions (and alternatives rejected)

1. **Provider name `mlx`, server-agnostic.** The config key names the
   ecosystem ("an OpenAI-compatible server fronting MLX models"), and the
   adapter works with any of the servers above. Docs recommend one concrete
   server for the walkthrough (Open question 1) and list the rest. (Rejected:
   naming a specific app — ties config vocabulary to one brand; renaming
   later breaks configs.)
2. **Reuse the existing OpenAI-compatible adapters, exactly as `llama` does
   for LLM.** `internal/llm/openai.go` already backs both `openai` (keyed)
   and `llama` (keyless) via one `openAIProvider` struct; `mlx` becomes a
   third constructor on the same struct. The embeddings side gets the
   mirror-image treatment: `openAIEmbedder` grows a `name` field and a
   conditional `Authorization` header so `mlx` can share it keyless (today it
   hardcodes `"openai"` in errors and always sends the header). (Rejected: a
   separate `mlx.go` adapter pair — duplicate SSE/JSON plumbing with no
   behavioural difference.)
3. **Optional `MLX_API_KEY` env var.** Most MLX servers run keyless, so the
   provider must not require a key (unlike `openai`, which hard-fails without
   one). When `MLX_API_KEY` is set we send it as a Bearer token and run the
   existing `config.ValidateKeyedBaseURL` check (https-or-loopback), matching
   the security posture of the other keyed providers. (Rejected: no key
   support — cheap to add now, and keyed/tailnet setups exist, e.g. nativ.)
4. **Default provider flips to `mlx` for both `llm` and `embedding`.** The
   issue asks for it, and the cost to non-Mac users is low: the default
   config has always presumed a local server the user must run; anyone on
   Linux/Windows edits one word (`provider: llama`) exactly as a Claude user
   edits it today. Bonus: because llama.cpp's server also speaks
   `/v1/chat/completions` and `/v1/embeddings` on the same default port, a
   `provider: mlx` config pointed at a llama.cpp server largely still works.
   (Rejected: platform-conditional default via `runtime.GOOS` — the config is
   documented as portable across machines, and a default that changes by OS
   makes `tbuk init` output non-reproducible and tests platform-dependent.)
5. **`base_url` default stays empty in config; the factory resolves
   `http://localhost:8080`.** Same rule as every other provider ("switching
   provider doesn't silently target a stale localhost URL"). 8080 matches
   `mlx_lm.server`, nativ, and llama.cpp; LM Studio / mlx-openai-server users
   set `base_url` once (docs show it).
6. **Doctor probes `GET /v1/models` for `mlx`, not `/health`.** `/health` is
   llama.cpp-specific; `/v1/models` is the one endpoint every
   OpenAI-compatible server has, and doctor already calls it for the model
   line. Small change: the local-provider status probe picks its path per
   provider (`llama`/`ollama` keep `/health`, `mlx` uses `/v1/models`).
   (Rejected: probing `/health` with a fallback — two requests to answer one
   question.)
7. **Embeddings support varies by server — the config already handles it.**
   `llm.base_url` and `embedding.base_url` are independent, so a
   chat-only server (`mlx_lm.server`) pairs fine with embeddings from another
   local server, or `embedding.provider: llama`/`ollama`. Docs call this out;
   the walkthrough recommends a server that does both.
8. **Embedding dimension stays a config value (default 768), no
   auto-detection.** Unchanged principle. Docs recommend a 768-dim MLX
   embedding model so the default keeps working (Open question 3).
9. **Retry policy: `mlx` embeddings use the shared `doWithRetry`** (comes free
   with the reused adapter, same as `ollama`). LLM streaming stays unretried,
   as for every provider.

## Package changes

```
internal/config/
  config.go           ← "mlx" in validLLMProviders + validEmbeddingProviders
                        (+ error strings); relativeDefaults() providers → "mlx";
                        base_url head-comment mentions mlx; chunking comment
                        gains "llama.cpp only" framing
  config_test.go      ← defaults/Validate/DefaultYAML expectations

internal/llm/
  openai.go           ← newMLXProvider(cfg): name "mlx", default base URL
                        http://localhost:8080, optional MLX_API_KEY (validated
                        via ValidateKeyedBaseURL when set)
  llm.go              ← factory case "mlx"
  llm_test.go         ← factory + streaming + auth-header + insecure-URL tests

internal/embeddings/
  openai.go           ← openAIEmbedder gains name field; Authorization header
                        only when apiKey != ""; error strings use the name
  embeddings.go       ← factory case "mlx" → newMLXEmbedder
  (new tests in embeddings_test.go + baseurl_internal_test.go)

internal/cli/
  doctor.go           ← per-provider status probe path (mlx → /v1/models);
                        comment strings updated
  context.go          ← --help text for llm.base_url mentions mlx
  doctor_test.go      ← doctor table gains an mlx case

docs/
  README.md, docs/user-guide.md, docs/initial-context.md  ← see Docs section
```

### LLM factory

```go
// newMLXProvider targets a local OpenAI-compatible MLX server
// (mlx_lm.server, LM Studio, mlx-openai-server, nativ, …).
// No API key required; MLX_API_KEY is sent as a Bearer token when set.
func newMLXProvider(cfg *config.LLMConfig) (*openAIProvider, error) {
    baseURL := cfg.BaseURL
    if baseURL == "" {
        baseURL = "http://localhost:8080"
    }
    key := os.Getenv("MLX_API_KEY")
    if key != "" {
        if err := config.ValidateKeyedBaseURL(baseURL); err != nil {
            return nil, fmt.Errorf("llm mlx: %w", err)
        }
    }
    return &openAIProvider{name: "mlx", baseURL: baseURL, model: cfg.Model,
        maxTokens: cfg.MaxTokens, apiKey: key, client: &http.Client{}}, nil
}
```

`NewLLM` gains `case "mlx"`. Errors and `LLMError.Provider` read `mlx` via the
existing `p.name` plumbing — no other adapter change.

### Embeddings factory

`openAIEmbedder` today: always sends `Authorization`, hardcodes `"openai"` in
`fmt.Errorf` contexts and `EmbedError.Provider`. Refactor (behaviour-neutral
for `openai`):

```go
type openAIEmbedder struct {
    name      string // "openai" | "mlx" — error contexts + EmbedError.Provider
    ...
}
// Embed: set the Authorization header only when o.apiKey != "".
```

```go
// newMLXEmbedder targets a local OpenAI-compatible MLX server,
// POST {base_url}/v1/embeddings. Keyless by default; MLX_API_KEY optional.
func newMLXEmbedder(cfg config.EmbeddingConfig) (*openAIEmbedder, error)
```

Same base-URL/key rules as the LLM side. `NewEmbedder` gains `case "mlx"`.

### Config

- `validLLMProviders`: `{claude, llama, mlx, openai, ollama}`;
  `validEmbeddingProviders`: `{llama, mlx, openai, ollama}`. Error strings
  updated to list `mlx`.
- `relativeDefaults()`: `LLM.Provider = "mlx"`, `Embedding.Provider = "mlx"`.
  Comments updated (base URL resolution note gains mlx → :8080).
- `defaultConfigNode()` head comments (what `tbuk init` writes):
  `base_url: leave empty to use the provider default` →
  `mlx/llama/openai-compatible → http://localhost:8080`.

### Doctor

```
LLM (mlx)
  url      http://localhost:8080
  status   healthy (HTTP 200)           ← GET /v1/models  (llama/ollama keep /health)
  model    mlx-community/Qwen3-8B-4bit  ← first id from /v1/models, as today
```

`isHostedProvider` is untouched (`mlx` is local); only the status-probe URL
becomes per-provider. The "same server as LLM" branch keeps working.

## Docs (the "promote over llama.cpp" half of the issue)

- **README.md**
  - Config sample: `provider: mlx    # mlx | llama | ollama | claude | openai`
    with a one-line comment: `mlx` = OpenAI-compatible MLX server on Apple
    silicon; `llama` = llama.cpp anywhere.
  - Architecture bullet lists mlx among adapters (`embeddings/`, `llm/`).
  - Troubleshooting: "LLM or embedding unreachable" row mentions starting the
    MLX server *or* llama.cpp; keep the llama.cpp batch-size row as
    llama-specific.
- **docs/user-guide.md** (largest edit)
  - "Fully local" path restructured: **Path A — Apple silicon Mac: MLX**
    becomes the recommended first path — install one server (walkthrough
    features the Open-question-1 pick; the others get a comparison table),
    pull a chat model and an embedding model by HF repo id, config needs
    nothing when the server sits on :8080. **Path B — any OS: llama.cpp**
    keeps the existing GGUF instructions verbatim, plus `provider: llama` in
    its config samples. Hybrid-with-Claude path notes embeddings can come
    from either local server.
  - Config-reference section: provider lists, model naming (HF repo ids for
    mlx), `MLX_API_KEY` note, mixed setups (chat-only MLX server + llama/
    ollama embeddings).
  - Keep the llama.cpp ubatch/chunk-size note, scoped to llama.cpp.
- **docs/initial-context.md** (per AGENTS.md, before merge)
  - Provider tables in *Embeddings* and *LLM* sections gain the `mlx` row
    (endpoint, default base URL, optional key).
  - Defaults line: `llm.provider=mlx`, `embedding.provider=mlx`; doctor
    probe-path note.
- `docs/plans/106-mlx-provider.md` → archived in the same PR.

## Testing (TDD, table-driven, ≥85% per package)

- **config:** `Validate` accepts `mlx` for both roles; unknown-provider error
  strings name it; `Defaults()`/`relativeDefaults` assert `mlx`;
  `DefaultYAML` golden output updated (provider lines + comments).
- **llm:** `TestFactory_mlxDefaultBaseURL` (mirror of the llama one); chat
  streaming happy path against `httptest` speaking OpenAI SSE (asserts no
  `Authorization` header by default); with `MLX_API_KEY` set (`t.Setenv`)
  the header is present and an insecure remote base URL is rejected
  (mirror `TestOpenAIProvider_rejectsInsecureRemoteBaseURL`); provider name
  `mlx` in `LLMError`.
- **embeddings:** mlx `Embed` happy path against `httptest` returning the
  OpenAI `{"data":[{"embedding":…,"index":…}]}` shape (order restored by
  index); no auth header by default / header with `MLX_API_KEY`; error paths
  produce `EmbedError.Provider == "mlx"`; default base URL case added to
  `baseurl_internal_test.go`; regression: `openai` embedder still sends its
  header and still errors without `OPENAI_API_KEY`.
- **cli:** doctor case with `provider: mlx` served by `httptest` asserts the
  status probe hits `/v1/models` (not `/health`) and the model line renders;
  llama case still probes `/health`; sweep existing tests that assert
  default-config provider strings (`doctor` output labels, `config` goldens)
  for the `llama` → `mlx` flip.
- Integration test keeps its explicit `provider: llama` fake-server config —
  it pins that the llama path stays working now that it's no longer the
  default.
- `make check-ci` before the PR.

## Rollout

One PR (`feat/work-with-mlx` branch), commits roughly:

1. `feat(llm): add mlx provider for OpenAI-compatible MLX servers`
2. `feat(embeddings): add mlx provider; share keyless OpenAI-shape adapter`
3. `feat(config)!: default llm+embedding provider to mlx` — **breaking-ish**:
   fresh `tbuk init` output changes; existing config files are untouched
   (their explicit `provider:` values win). `feat!:`/major vs `feat:`/minor
   bump — see Open questions.
4. `docs: promote MLX over llama.cpp; document mlx provider`
   (+ archive this plan)

Order matters only in that the default flip (3) must not land before the
provider exists (1–2); collapsing 1–3 into fewer commits is fine.

## Out of scope (deliberate)

- Anthropic-compatible `/v1/messages`, image/audio endpoints, `/metrics` that
  some MLX servers also expose — nothing in tbuk needs them.
- Auto-detecting the embedding dimension from the server — existing
  "dimension comes from config" principle unchanged.
- A `tbuk doctor` deep-probe that verifies the loaded model *type*
  (chat vs embedding) — nice later, not needed to land this.
- Whisper/audio via MLX — belongs to plan 21 (AV transcription), which can
  grow an mlx option once this provider exists.
- Managing/launching the MLX server from tbuk — tbuk talks HTTP, full stop.

## Open questions (answer before/while implementing)

1. **Which server does the user-guide walkthrough feature?** Candidates:
   `mlx_lm.server` (official mlx-lm, pip-installable, chat-only — needs a
   second server or provider for embeddings), mlx-openai-server (chat +
   embeddings in one), LM Studio (GUI, chat + embeddings), nativ (GUI,
   chat + embeddings, macOS 26+). A chat+embeddings single server makes the
   walkthrough simplest; owner preference decides.
2. **Empty `model` behaviour across servers.** llama.cpp ignores the field
   (one loaded model); some MLX servers require the HF repo id per request.
   Determines whether docs say "leave empty" or "set to the repo id" for
   `llm.model`/`embedding.model`. Verify on the chosen server; the plan's
   safe default is: docs tell mlx users to set both models explicitly.
3. **Recommended models for the docs.** A chat model and a **768-dim**
   embedding model from `mlx-community` (so the config's default dimension
   holds), e.g. `mlx-community/nomic-embed-text-v1.5` — verify availability
   on the chosen server.
4. **Version bump for the default flip.** `feat!:` (major) is honest about
   changed fresh-install behaviour, but pre-1.0 a plain `feat:` (minor) with
   a loud changelog line may be preferable. Owner's call.
5. **Windows/Linux first-run experience.** With `mlx` as default, `tbuk
   doctor` on a llama.cpp machine with an untouched config still mostly works
   (same port, compatible endpoints) but reports `LLM (mlx)`. Acceptable, or
   should doctor hint "set provider: llama"? Default plan: docs-only, no
   hint.
