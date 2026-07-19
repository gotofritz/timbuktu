# Subplan 11 — PR 7: Stream cancellation, error bodies, not-found errors

Archived portion of `docs/plans/11-poc-hardening.md`. Covers P1-8, P1-9, and
P1-10, implemented together as hardening PR 7. Implementation commit `3e976ac`.

---

## P1-8 — LLM stream goroutines can leak / calls uncancellable

**Problem.** The claude/openai/ollama stream goroutines sent on an unbuffered
channel with no `select` on `ctx.Done()`. When the consumer stopped reading —
`RunAsk` returns on a mid-stream error without draining — the goroutine blocked
on send forever, leaking it and holding the HTTP body open. `RunAsk` also used
`context.Background()`, so Ctrl-C could not interrupt retrieval or streaming.

**Fix.**
- Added `sendToken(ctx, ch, tok) bool` in `internal/llm/stream.go`: selects on
  `ctx.Done()` and returns `false` to tell the goroutine to stop. Every send in
  `claude.go`, `openai.go`, and `ollama.go` goes through it.
- `RunAsk` gained a leading `ctx context.Context` parameter, wraps it in
  `context.WithCancel`, `defer cancel()`, and uses that context for both
  retrieval and the chat call. The `ask` command wires `cmd.Context()` through.

**Tests.** `TestOpenAIProvider_streamCancellation_noLeak` (goroutine-count delta:
read one token, cancel, assert the count returns to baseline);
`TestRunAsk_cancelsStreamContextOnExit` (chat captures its context; after an
early return the captured context is cancelled).

## P1-9 — Provider HTTP errors discarded the response body

**Problem.** All LLM/embedding adapters mapped non-200 to
`http.StatusText(code)` (literally "Bad Request"), discarding the API's own
message ("model not found", "context length exceeded", rate-limit details).

**Fix.** A package-local `errorMessage(resp)` helper in `internal/llm` and
`internal/embeddings` reads up to ~2 KB of the body (`io.LimitReader`) into
`LLMError`/`EmbedError.Message`, falling back to the status text only when the
body is empty.

**Tests.** `*_errorBodyIncluded` for openai/claude/ollama LLM adapters and
openai/llama/ollama embedders assert the error string contains the body text.

## P1-10 — "Not found" conflated with real DB errors

**Problem.** `Ingester.IngestFile` and `RunDelete` treated *any* `GetByPath`
error as "does not exist". A transient DB error routed ingest into the create
path (later failing with a confusing UNIQUE violation) and made delete report
"document not found" for a real I/O failure.

**Fix.** `DocumentRepo.GetByPath`/`GetBySHA256` return the sentinel
`storage.ErrNotFound` (wrapping `sql.ErrNoRows`) when no row matches. `scanDocument`
maps `sql.ErrNoRows` to it. `IngestFile` and both delete paths branch with
`errors.Is(err, storage.ErrNotFound)`; anything else is surfaced as the real error.

**Tests.** `TestDocumentRepo_GetByPath_NotFound` / `_GetBySHA256_NotFound` assert
`errors.Is(err, storage.ErrNotFound)` (and `sql.ErrNoRows`);
`TestIngester_lookupError_notMaskedAsCreate` and
`TestRunDelete_dbError_notReportedAsNotFound` assert a closed DB surfaces the
real error.

---

## QA

`make check-ci` green: `golangci-lint` 0 issues, build clean, total coverage 85.8%.
