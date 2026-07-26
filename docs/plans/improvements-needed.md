# Repo Assessment — Improvements Needed (2026-07-26)

Assessment of timbuktu across architectural soundness, code quality,
maintainability, security, and ease of use. Each improvement below is a short
description of a step, ranked with **MoSCoW** (Must / Should / Could / Won't).
This is a summary plan only — no fixes are made here. When an item is picked
up, it gets its own numbered subplan under `docs/plans/`.

## Overall verdict

The repo is in very good shape. Architecture is clean and layered
(dependencies point inward, providers behind shared interfaces, single
composition root in `internal/cli/app.go`), the full test suite passes, CI is
thorough (race detector, cross-compile matrix, govulncheck with a sensible
gate policy, coverage ≥ 85%), and security posture is unusually strong for a
CLI of this size (tar path-traversal rejection on import, ANSI/OSC sanitizing
of document-derived terminal output, `0o600`/`0o700` data permissions, API-key
base-URL validation, atomic re-index). Documentation is excellent and current.

The items below are therefore refinements, not repairs.

---

## Must have

### M1. Context-window budget guard on `tbuk ask`
`ask` assembles retrieved chunks into the prompt with no check against the
model's context window; an oversized prompt only fails when the provider
rejects it (HTTP 4xx, already listed in README troubleshooting). Add a token
budget for the rendered prompt (config or manifest driven, building on the
existing `retrieval.max_tokens` trimming) so the failure is prevented locally
instead of diagnosed remotely. Already identified as a Quick Win in
`next-steps.md`; promoted here because it affects correctness of the core
command.

### M2. Scheduled vulnerability scan
`govulncheck` runs only on push/PR. In quiet periods, newly disclosed CVEs in
shipped dependencies (`ledongthuc/pdf` parses untrusted input; `x/net`;
`modernc.org/sqlite`) go undetected until the next commit. Add a weekly
`schedule:` trigger to the CI workflow's govulncheck job (or a small dedicated
workflow) reusing the existing gate policy.

---

## Should have

### S1. Better token estimation for non-ASCII text
`CountTokens = len(s)/4` counts **bytes**, and is calibrated for English. For
CJK and other multi-byte scripts the real token count is far higher per byte,
so chunks can exceed the embedding server's batch size (llama.cpp HTTP 500s)
and `retrieval.max_tokens` under-trims. Replace with a cheap
script-aware estimator (e.g. runes with a per-script weight) behind the
existing `CountTokens` seam; a full tokenizer is not needed.

### S2. Run tests on Windows in CI
Windows binaries are shipped, but CI only cross-compiles for Windows — tests
never run there (`os.UserHomeDir` reads `USERPROFILE`, which the HOME-based
fixtures don't set, per the comment in `ci.yml`). Make the test fixtures set
`USERPROFILE` alongside `HOME` and add `windows-latest` to the test matrix, so
path handling (a classic Windows-breakage area for a path-keyed document
store) is actually exercised on the platform users get binaries for.

### S3. Re-key legacy relative-path documents
Documents ingested before path normalization are keyed by their original
relative path (README "Paths & Unicode" caveat) and can double-index alongside
the absolute-path key. Add a migration (or a `doctor --fix` step) that
re-keys/merges legacy rows so the caveat and its foot-gun disappear.

---

## Could have

### C1. Shell completions
Cobra generates bash/zsh/fish completions nearly for free. Expose
`tbuk completion <shell>` (if hidden) and document installation in the README.
Cheap ease-of-use win for a CLI-first tool.

### C2. More extractors (docx, epub, source code)
Supported inputs stop at `.md`/`.txt`/`.pdf`/`.html`. docx and epub are common
personal-knowledge formats; both have small pure-Go parsing paths. Fits the
existing `Extractor` interface without architectural change. (Roadmap Quick
Win; sequencing after the in-flight Source abstraction, subplan 18, may be
natural.)

### C3. Document base_url and max_tokens in the sample config
The README's sample `config.yaml` omits `llm.base_url`, `llm.max_tokens`, and
`embedding.base_url`, though troubleshooting refers to them. Add them
(commented) to the sample and the `tbuk init` default YAML so users don't have
to discover keys from docs prose.

### C4. Retrieval quality evaluation harness
A tiny eval command (fixed query→expected-doc pairs over a fixture corpus,
reporting hit-rate/MRR) would let chunking/RRF/estimator changes (e.g. S1) be
tuned with evidence instead of anecdote. Deliberately small; the full
retrieval-quality cluster stays in `next-steps.md`.

### C5. ANN index (sqlite-vec) for large corpora
Vector search is an O(n) scan — a documented, sound trade-off below ~100k
chunks, and the two-phase top-K scan keeps memory at O(K). Revisit only if
real corpora approach that scale; the `Searcher` interface already isolates
the swap.

---

## Won't have (this cycle)

Deliberately excluded, matching the roadmap's "money pits" quadrant — the
architecture doesn't block any of them later:

- **Web / server mode** — local-first single-user CLI is the product.
- **Plugin system** — the interface seams (Extractor, Embedder, LLM, Source)
  already give extension points in-tree.
- **Encryption at rest** — `0o600`/`0o700` plus OS-level disk encryption is
  the right boundary for a single-user local tool.
- **Multi-user / sync** — export/import tar snapshots cover portability.
- **Source ingestor framework** — not skipped, already in flight as its own
  plan (`docs/plans/18-source-ingestors.md`); tracked there, not here.
