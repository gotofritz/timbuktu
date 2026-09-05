# Subplan 33: Context-window budget guard on `tbuk ask`

Lands **M1** from `docs/plans/improvements-needed.md` and roadmap item **2**
from `docs/plans/next-steps.md` ([#141](../../../../issues/141)).

`retrieval.max_tokens` trimmed the retrieved chunks; nothing bounded the *whole*
prompt — system prompt + template + chunks + question — against the model's
context window. An oversized prompt was only caught remotely, as an HTTP 4xx
from the provider ("context length exceeded"), which the README already had to
document as a troubleshooting row. The fix is a local budget check that trims
and warns before the call.

## Design decisions

### 1. Two levels: config window, per-template override

| Source | Key | Meaning |
|---|---|---|
| `config.yaml` | `llm.context_tokens` (default `8192`) | the model's whole window, prompt + reply; `0` disables the guard |
| `manifest.yaml` | `context_tokens` (top level) | overrides the config for that template; absent = inherit |

The window belongs to the model, and a template can already pin a `model:` of
its own — so the override lives next to `model`/`max_tokens` at the manifest's
top level, not under `retrieval:` (which is about how much is fetched, not how
much the model can hold).

The prompt's share is `window − reserve`, where `reserve` is the template's
`max_tokens`, else `llm.max_tokens`: the reply has to fit the same window.
`Config.Validate` rejects a config window that does not exceed
`llm.max_tokens` — that config can never produce a prompt at all.

Default `8192` (guard on) rather than `0` (guard off): the sharp edge the issue
asks to remove is only removed if the guard is on out of the box. It is the
smallest window a current local model is usually served with, so it does not
trim what a default setup would have accepted, and `tbuk init` backfills the key
into an existing config via `FillMissingDefaults` — no migration script.

### 2. A ladder: compact, then drop, then fail

`fitToContext` in `internal/cli/ask.go` renders, measures, and while over budget:

1. **compacts** the retrieved text (`internal/squeeze`),
2. **drops** trailing — lowest-ranked — chunks one at a time,
3. **fails** locally when even a chunk-free prompt overflows, naming the knobs.

Compaction before dropping: text the model can still read beats a passage it can
no longer cite. Each rung warns on the diagnostics stream, and citations follow
what survived, so `Sources:` never lists a passage the model did not see.
`--require-context` aborts when the guard leaves no context at all — the answer
would otherwise come from the model's priors, which is exactly what that flag
exists to prevent.

### 3. `internal/squeeze`, a deliberately dumb compactor

Mirror of `internal/normalize`: normalize repairs the model's output, squeeze
compacts its input. It collapses repeated whitespace and blank lines and drops
articles and filler words (`the`, `a`, `really`, `basically`, …). Words that
carry any non-letter character are never touched, so identifiers, URLs and
inline code spans survive; fenced (``` / ~~~) and indented blocks pass through
byte-exact. Nothing that changes what a sentence claims — negations, modals,
quantifiers — is on the drop list: the chunks are the evidence the answer is
graded on.

It is lossy, so it runs on retrieved chunk text only, never on the question or
the template, and only when the prompt would otherwise overflow.

### 4. Estimation stays chars/4

The budget uses `chunking.CountTokens`, the same estimator as the chunker and
the `retrieval.max_tokens` trim. It undercounts non-ASCII text
([#120](../../../../issues/120) tracks a better one); the docs say to leave
headroom rather than set the window to the model's exact maximum. A second
estimator here would only be a second thing to be wrong.

## What shipped

- `internal/squeeze/` — `Text`, `Chunks`; 100% covered.
- `internal/config` — `LLMConfig.ContextTokens`, default, validation, commented
  default YAML, backfill through `FillMissingDefaults`.
- `internal/prompts` — `Manifest.ContextTokens`.
- `internal/cli/ask.go` — `WithContextBudget`, `fitToContext`, `promptTokens`;
  wired from `cfg.LLM` in `newAskCmd`.
- `internal/cli/doctor.go` — `context:` line (window and what it leaves for the
  prompt, or "guard off") and a `budgets:` line naming any template whose own
  `max_tokens` swallows its window.
- `internal/cli/context.go` — the cheatsheet gains both keys and the guard's
  warnings, so an agent reading it knows why chunks vanished.
- Docs: README (sample config, template manifest, a "Context budget" section,
  three troubleshooting rows), `docs/user-guide.md` (§5 config, §9 "Fitting the
  model's context window", §11 manifest keys), `docs/initial-context.md`
  (package map, config, RAG "Context budget").

## Verification

`make check-ci` — lint, build, coverage ≥ 85% per package.

Behaviour, against a scratch root:

```bash
tbuk --root /tmp/kb init                 # config carries context_tokens: 8192
tbuk --root /tmp/kb doctor               # LLM: context: … Prompts: budgets: …
tbuk context | grep context_tokens       # cheatsheet documents the guard
```

With a corpus ingested, lowering `llm.context_tokens` towards `max_tokens`
walks the ladder: compaction warning first, then the drop warning, then the
local error — with no HTTP call made in the last case.
