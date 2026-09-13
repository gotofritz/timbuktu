# Subplan 14: HTTP ingest server + browser clipper

Lands roadmap **#14** (web / server mode) from `docs/plans/next-steps.md` —
which is listed there as a money pit, "only revisit on a deliberate scope
change". This is that change, and it is narrower than the item it reverses.

The driving use case is one gesture: **select text on a web page and get it
into the knowledge base**, optionally translated, summarised, or stripped of
code samples on the way in. That needs something listening on localhost while
the browser is open, and a browser extension to talk to it. It does not need a
web UI, and it does not need `ask` or `search` over HTTP.

The extension is modelled closely on [`gotofritz/anklipper`](https://github.com/gotofritz/anklipper),
which solves the same shape of problem — one gesture, a local HTTP daemon, a
sidebar to check what is about to be written — against Anki instead of against
a RAG index. Its `docs/initial-context.md` is the reference for the extension
half of this plan, and most of its decisions transfer unchanged.

## Scope

**In:** ingesting clips over HTTP, the transforms applied before ingest, the
extension that produces them, and the minimum security a localhost write
endpoint genuinely needs.

**Out, for now:** `search`, `ask`, `chat`, `stats` and every other read over
HTTP. They are the obvious next thing and the API is shaped so they slot in
under `/v1/` without moving anything, but nothing here builds them, tests them
or reserves handler code for them. A server that only writes is a much smaller
security surface than one that also answers questions about the corpus, and
this milestone should not pay for the larger one before there is a reason to.

**Also out:** any HTTP endpoint that ingests a *path*. The server never reads a
file the caller names. See "The local minimum", rule 6.

## Two questions settled first

Both were raised when this plan was commissioned, and the rest of the plan
depends on the answers.

### Should the server be its own app, with the CLI talking to it over HTTP?

**No.** `tbuk serve` is a subcommand of the existing binary, and the CLI keeps
talking to SQLite directly.

The server is a *second front door onto the same `internal/` packages*, the way
`ask` and `chat` are two front doors onto `RunAsk`. `openApp` already
centralises the dependency graph — DB, repos, embedder, ingester, LLM — so a
handler builds what it needs exactly as a cobra command does, and nothing in
`internal/` learns that HTTP exists.

What a split would cost, against what it would buy:

| | Cost |
|---|---|
| `tbuk ingest ./notes` | needs a daemon running, or a second code path that does not |
| Release | two artifacts, two version numbers, a compatibility matrix between them |
| The wire | becomes load-bearing for every CLI feature, not just for clips |
| SQLite | two processes writing one file, where today there is one |
| Buys | nothing, while the database is a local file |

The last row is the whole argument. A client/server split earns its keep when
the data is somewhere the client cannot reach. `database.path` is a file on the
same disk. Revisit if that ever stops being true — a remote or shared knowledge
base is exactly the deliberate scope change that would flip this.

Two processes *can* still meet: the server may be running while the user types
`tbuk ingest`. That is fine and stays fine — WAL is on, `busy_timeout` is
5000ms (`storage.dsnFor`), and the one long-running part of an ingest
(extraction and embedding) happens *before* `ReplaceForDocument` opens its
transaction, so the write window is short. The server keeps a single ingest
worker (decision 4) so it never contends with itself.

### Should the CLI be rewritten in node, to share code with the extension?

**No.** Measure what is actually shareable before paying for it:

- **The pipeline** — chunking, `searchtext.Reduce`, embedding, storage, the
  retrieval encoding — is the server's, in Go, and the extension never sees it.
- **File walking, `--root`, config, doctor, export/import** — the CLI's, and a
  browser extension cannot use any of it.
- **What is left** is the request and response shapes, the error taxonomy, and
  a `fetch` wrapper. A few hundred lines.

Rewriting a tested Go CLI — 21 commands, a suite that runs on three platforms —
to share a few hundred lines is a bad trade twice over: it throws away working
code (Agent Constraints: "avoid rewriting working code without reason") and it
moves the CLI *away* from the pipeline it drives, onto a wire that would then
have to carry everything.

Share the shapes instead, and let them be the only thing shared. **Decision 9**
says how: the Go side owns the types and emits golden JSON fixtures; the
extension repo parses those same fixtures in its own tests. Both sides break
when the shape drifts, which is the entire benefit a shared language would have
bought.

If a node CLI is ever wanted anyway — a `tbuk-clip` that pipes stdin to the
server, say — it is a thin client over the same endpoint and costs nothing to
add later. That is not the same as making it *the* CLI.

## Goal

```bash
tbuk serve                         # 127.0.0.1:7717, token printed on first run
tbuk serve --allow-origin moz-extension://<uuid>
tbuk ingest ./notes --transform summarise    # same transforms, no server needed
tbuk doctor                        # now reports the server's config and the transforms
```

and, in the browser: select, right-click **Clip to Timbuktu** (or `Alt+Shift+T`),
a sidebar opens with the selection, a transform picker, a language box and a
topics field; press **Ingest**; the clip is in the knowledge base and
`tbuk search` finds it.

Success =

1. every existing CLI command behaves exactly as it does today, with the same
   tests green;
2. a clip ingested over HTTP is indistinguishable, in the database, from one
   ingested from a file — same chunk shapes, same automatic metadata rules,
   same `reindex` behaviour;
3. `tbuk reindex` re-embeds a clipped document **without spending a single LLM
   call**, and produces the same text it produced the first time;
4. nothing the server accepts can make it read or write a file the caller
   named.

## Design decisions (and the alternatives rejected)

### 1. `internal/server/` holds HTTP and nothing else

Handlers, routing, the middleware chain, the wire types. It depends on
`internal/ingest`, `internal/transform`, `internal/storage` and
`internal/config`, and on nothing in `internal/cli`. `internal/cli/serve.go` is
the cobra seam that builds it, the way `internal/cli/reindex.go` is the seam
over `Ingester.ReindexAll`.

*Rejected:* handlers in `internal/cli`. The composition root lives there, but a
`net/http` handler is not a cobra command and putting it there makes `cli` the
package everything lands in.

### 2. A clip is ingested as content, not as a temporary file

`Ingester` is path-centric today: `IngestFile(ctx, path, opts)` hashes a file,
`FileExtractor.ExtractFile(ctx, path)` re-opens it. A clip is bytes in a
request body.

Add one narrow method:

```go
// ContentDoc is one ingestable unit that is not a file on disk. It is
// deliberately the subset of #18's source.SourceDoc that a clip needs, so
// that plan generalises this rather than replacing it.
type ContentDoc struct {
    URI      string            // stable identity; documents.path
    Title    string
    MIME     string            // drives extractor dispatch: text/html, text/markdown, text/plain
    Metadata map[string]string // written after the embed, user keys only
    Open     func(ctx context.Context) (io.ReadCloser, error)
}

func (ing *Ingester) IngestContent(ctx context.Context, doc ContentDoc, opts Options) Result
```

`IngestFile` is untouched. `IngestContent` reuses the whole tail — SHA256,
dedup, raw archive, chunk, `searchtext.Reduce`, embed, `ReplaceForDocument` —
and differs only at the head: the extractor is chosen from `doc.MIME` rather
than from `DetectMIME(path)`, and the raw copy is teed from the content stream
rather than copied from a path.

*Rejected:* writing the clip to a temp file and calling `IngestFile`. Three
lines cheaper and wrong in a way that compounds — `documents.path` would hold a
temp path that resolves to nothing, which is precisely the failure
`18-source-ingestors.md` documents for Joplin notes, and `reindex` would strand
every clip on the next embedding-model change.

*Rejected:* building #18's full `Source` seam here. It needs the `source_type`
/ `source_uri` columns, a dedup re-key, and per-source metadata — a plan of its
own, which exists. `IngestContent` is shaped to be the thing `IngestSource`
calls per document, so #18 inherits it instead of tripping over it.

### 3. A clip's identity is content-addressed, and includes its transform

`documents.path` is `NOT NULL UNIQUE` and a clip has no path. Use:

```
clip://<sha256 of the original clip bytes>/<transform chain, or "raw">
```

e.g. `clip://a1b2…/summarise+strip-code`, `clip://a1b2…/raw`.

Content-addressed, so re-clipping the same selection with the same transform
lands on the existing dedup path and skips — the behaviour a user re-clipping
a paragraph they already saved expects. Transform-qualified, so a summary and a
translation of one selection are two documents rather than a collision, which
they are: they say different things.

*Rejected:* the page URL as identity. Two clips from one long article would
collide, and a URL with a session token in it would fragment one page into
many.

*Rejected:* identity from the *post-transform* text. An LLM transform is not
deterministic, so re-clipping the same paragraph would produce a new document
every time and the dedup promise would be silently false.

### 4. Clips are queued, and the queue is durable

`POST /v1/clips` writes the job, answers `202 Accepted` with a job id, and a
single background worker does the ingest. `GET /v1/jobs/{id}` reports progress.

Two reasons, and only the second is about latency. An ingest with a transform
is a model call — five to thirty seconds locally, more on a cold model — and a
browser request that hangs that long is one the user will retry, producing two
clips. And **a clip is the only copy of itself**: the user has moved on, the
page may be gone, and a server restart between accept and ingest must not lose
it. That is anklipper's first rule — the draft is durable from the moment it
exists — applied one layer down.

Durability is a `clip_jobs` table in the knowledge base's own database, and the
original bytes go into the raw archive *before* the 202 is sent. On startup the
worker drains anything left `pending` or `running`. Per AGENTS.md's proof-of-
concept rule, the table is added by editing `schemaSQL` in place, not by a
versioned migration, and an existing knowledge base is brought forward by a
throwaway `scripts/add-clip-jobs/` that the PR names and a later PR deletes.
`storage.HasClipJobsTable` is how `tbuk doctor` spots a database that has not
had it run, following `HasSessionTables`.

*Rejected:* synchronous ingest. Simpler by a worker and a table, and it makes
the transform feature unusable through a browser.

*Rejected:* an in-memory queue. Free, and it loses the user's work on a
restart — the one failure that is not recoverable by trying again, because the
tab is closed.

*Rejected:* a spool directory of JSON files. A second durable store with its
own crash semantics, next to a database that already has them.

**One worker, deliberately.** Ingest is embed-bound, and the embedding server
is the bottleneck; `ingest.embed_concurrency` already parallelises *within* a
document. A second worker would add SQLite write contention and a second way to
hit the embedding server's batch limits, for throughput nobody clipping by hand
needs.

### 5. A transform is a prompt template applied before chunking

`internal/transform` renders a template over the clip's text and sends it to
the LLM; the completion is what gets ingested. Templates live under
`<root>/transforms/<name>/{manifest.yaml, system.tmpl, user.tmpl}` — the same
on-disk shape as `<root>/prompts/`, a different root, a different data struct:

```go
type Data struct {
    Text      string            // the clip, extracted to plain text
    Title     string
    URL       string
    Variables map[string]string // e.g. {"lang": "German"}
}
```

Builtins installed by `tbuk init`: **`translate`** (one variable, `lang`),
**`summarise`**, **`strip-code`**. Translation is not a separate mechanism — it
is a transform with a variable, which is what stops "translate" and "summarise"
needing two different pieces of API.

*Rejected:* reusing `<root>/prompts/` with a `kind:` key in the manifest.
`tbuk template list` would then list entries `tbuk ask --template` cannot
accept, and a template naming `{{ .Chunks }}` would load fine and fail at
render. Two directories make the two kinds impossible to confuse; the shared
thing is a file convention, not code, and abstracting over two eighty-line
loaders costs more than it saves.

*Rejected:* a hardcoded transform per endpoint. The user's list —
"summarise", "remove all code samples", "…" — is open-ended by construction.

**Chaining.** `transforms` is an ordered list; each one's output is the next
one's input, capped at `transform.max_chain` (default 2) because each link is a
model call and a chain of five is a bill nobody sanctioned. The chain is
recorded in metadata and in the document's identity (decision 3).

**Clips too long for the transform model** are refused with a named error the
extension shows, not silently truncated — a summary of the first half of a
document, presented as a summary, is the failure mode this project's context
guard exists to prevent. The budget is `llm.context_tokens` (or the transform
manifest's own `context_tokens`) minus its `max_tokens`, measured with
`chunking.CountTokens`, exactly as `fitToContext` does. Map-reduce over a long
clip is a later milestone if it is wanted.

### 6. Both the original and the ingested text are archived

- `raw/<sha-original><ext>` — the clip exactly as the browser sent it.
- `raw/<sha-ingested>.md` — the transform's output, which is what was chunked
  and embedded, and what `documents.raw_path` points at.

Metadata links them: `clip_original_sha`, plus `source_url`, `source_title`,
`clipped_at`, `transform` (the chain, joined), `transform_model`.

This is what makes success criterion 3 hold. `reindex` re-reads
`documents.raw_path`, so it re-reads the *transform's output* and re-embeds it
— no model call, no non-determinism, the same text as last time. A design where
raw held the original would make `reindex` either re-run the transform (an
expensive, non-deterministic repair) or index text the document never had.

The original is kept anyway, because it is the only copy and because a better
transform next year has nothing to re-run against without it.

*Note:* `source_url` belongs in the `source_uri` column that #18/#23 add. Until
that lands it is a metadata key, and #18 should migrate it. The automatic keys
`filename`/`extension`/`mime`/`dir` are derived from a path and are **not**
written for a clip; `readManifest`'s filter list in `import` is the precedent.

### 7. The CLI gets transforms too

`tbuk ingest <path> --transform summarise --transform-var lang=German`.

Not a courtesy: it is how the transform pipeline is tested and demonstrated
without HTTP in the picture, and it keeps the promise that the server adds a
front door rather than a feature only reachable through one. It also means the
interesting half of this plan is usable before the extension exists.

### 8. The wire is `/v1/`, JSON, and small

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/v1/health` | liveness plus what the extension's status strip needs |
| `GET` | `/v1/transforms` | the installed transforms and their variables, for the picker |
| `POST` | `/v1/clips` | accept a clip; `202` + `{"job_id": …}` |
| `GET` | `/v1/jobs/{id}` | `pending` \| `running` \| `done` \| `failed`, with the document path on `done` and a typed cause on `failed` |

```jsonc
// POST /v1/clips
{
  "url":   "https://example.com/article",
  "title": "How slices grow",
  "text":  "A slice grows when append finds len == cap…",  // required
  "html":  "<p>A slice grows…</p>",                        // optional; preferred when present
  "heading": "Growth",                                     // optional, from the nearest h1–h6
  "transforms": [{"name": "summarise"}],                   // optional, ordered
  "metadata": {"topic": "go"},                             // optional, user keys only
  "force": false
}
```

Every response is an object, never a bare value, and every failure is
`{"error": {"kind": "…", "message": "…"}}` with `kind` drawn from a closed
union — anklipper's rule, for anklipper's reason: a client that switches on
prose is a client that breaks when the prose improves. The kinds are
`bad-request`, `unsupported-media`, `too-long`, `transform-failed`,
`embed-failed`, `storage-failed`, `unauthorized`, `forbidden-origin`.

`html` preferred over `text` when both are present: the HTML fragment carries
paragraph structure that the flattened selection has lost, and
`preprocess.NewExtractor("text/html")` already knows what to do with it. `text`
stays required, because a page whose fragment cannot be read still produces a
usable clip from the flattened selection — anklipper's "a degraded card beats
no card, provided the degradation is visible".

### 9. One source of truth for the wire shapes, on the Go side

`internal/server/apitypes` holds the request and response structs and the error
kinds. `make api-fixtures` writes a golden JSON example of every shape into
`internal/server/testdata/contract/`; Go tests assert the structs round-trip
them, and the extension repo vendors the same files and parses them in vitest
against its hand-written `api.d.ts`.

Manual copy between repos, until it hurts. A generator, a shared package
registry or a monorepo are all answers to a problem four JSON shapes do not
have yet; a drifted shape failing a test in both repos is the property worth
buying, and this buys it for the price of a `cp`.

## Package layout

```
internal/server/
  server.go         ← New(Deps) *http.Server; routes, timeouts, shutdown
  middleware.go     ← token, origin/CORS, body cap, request id, recovery
  clips.go          ← POST /v1/clips
  jobs.go           ← GET  /v1/jobs/{id}
  transforms.go     ← GET  /v1/transforms
  health.go         ← GET  /v1/health
  queue.go          ← the worker: claim → transform → ingest → record
  apitypes/         ← the wire structs and the error union; no net/http import
  testdata/contract/

internal/transform/
  transform.go      ← Transform, Chain, Apply(ctx, chat, data) (string, error)
  templates.go      ← TemplateDir over <root>/transforms/, Load, List
  budget.go         ← the context check, over chunking.CountTokens
  builtin/          ← translate, summarise, strip-code (installed by init)

internal/ingest/
  content.go        ← IngestContent + ContentDoc

internal/storage/
  clipjob.go        ← ClipJobRepo: Enqueue, Claim, Complete, Fail, Get, Drain
  migrate.go        ← clip_jobs added to schemaSQL in place

internal/cli/
  serve.go          ← the cobra seam: RunServe(ctx, out, errW, cfg, opts)
  ingest.go         ← --transform / --transform-var
  doctor.go         ← a Server section and a Transforms section

scripts/add-clip-jobs/   ← throwaway; deleted once it has done its job
```

Config gains one block:

```yaml
server:
  addr: "127.0.0.1:7717"
  token: ""                 # generated by `tbuk init`; empty refuses to serve
  allowed_origins: []       # e.g. ["moz-extension://<uuid>"]
  max_body_bytes: 2097152
  job_retention_days: 30

transform:
  max_chain: 2
  timeout: 120s
```

`Config.Validate()` gains: a non-loopback `server.addr` without the explicit
opt-in flag is an error; `max_chain < 1` is an error; a malformed origin is an
error. Fail fast at the one config chokepoint, as every other setting does.

## The local minimum

The brief is minimum security for a local setup, **except for what is also a
real risk locally**. Three things are, and they are cheap:

1. **Loopback only.** `server.addr` must resolve to `127.0.0.1`/`::1`.
   Binding anywhere else needs `tbuk serve --bind-non-loopback`, which prints
   what that means before it starts. A knowledge base is personal data and the
   default must not put it on the office wifi by accident.

2. **A token, sent as `X-Tbuk-Token`.** Generated by `tbuk init`, stored in
   `config.yaml` (already `0o600`), never logged and never echoed in an error.
   This is not theatre: **any web page the user visits can POST to
   `http://127.0.0.1:7717`**. A `<form>` or a `fetch` with a simple content
   type is not blocked by CORS on the way *out* — CORS only stops the page
   *reading the reply*. Without a token, any site could write documents into
   the knowledge base while the user reads it. A custom header is the fix twice
   over: the attacker does not have the token, and requiring a custom header
   forces a preflight, which the origin check then refuses.

3. **An origin allowlist.** `allowed_origins` is exact-match, and `*` is never
   accepted — for the reason anklipper states about `webCorsOriginList`: web
   pages are the one class CORS does constrain, so widening the list is exactly
   how a visited site gets to drive the tool. `tbuk serve` prints the extension
   origin to paste when it sees a rejected preflight, so the setup step is
   self-documenting.

And four that are ordinary hygiene rather than a threat model:

4. **SQL injection stays impossible.** Every query is already parameterised,
   and the one place user input becomes SQL syntax — `internal/search/query.go`
   building an FTS5 `MATCH` expression — already double-quotes every operand
   and doubles embedded quotes. This plan adds no new SQL string building, and
   adds a test that a clip whose title is `"; DROP TABLE documents; --` ingests
   and retrieves as text. It is a cheap regression bar for a place where a
   later careless `fmt.Sprintf` would be invisible.

5. **Bounded everything.** `http.MaxBytesReader` at `max_body_bytes`,
   `ReadHeaderTimeout`, `WriteTimeout`, `IdleTimeout` on the `http.Server`
   (a server with none is the one `gosec` finding this repo would earn), a
   transform timeout, and a bounded read of any upstream error body — the
   pattern the LLM adapters already follow.

6. **No path in, ever.** The server takes clip *content*. It never takes a
   filename, a path, a `file://` URL or anything it would open, and it never
   fetches a URL the caller supplies. Both would turn a local write endpoint
   into a local file disclosure (straight into a searchable index) or an SSRF
   pivot onto whatever else listens on localhost — including the user's own
   embedding and LLM servers. `tbuk ingest <path>` stays a CLI-only capability,
   and the absence of a path-taking endpoint is pinned by a test that walks the
   registered routes.

7. **Untrusted text, treated as such.** Clipped text is a web page's, so:
   HTML is parsed and text-extracted, never stored as markup (a `<script>` body
   must contribute no text — a test); everything written to a terminal goes
   through the existing `sanitizeWriter`/`stripControl`; and the JSON encoder
   escapes control runes on the way out.

### What productionising would need

Not in scope, listed so the gap is a decision rather than an oversight. Each is
a thing to look at *when* the corresponding assumption stops holding.

**If it ever listens on more than loopback**
- Real authentication (the shared token is a single-user convenience, has no
  rotation, no expiry and no per-client identity) and TLS, since the token
  would otherwise cross a network in the clear.
- CSRF proper, rate limiting and connection limits — the single worker means a
  flood does not corrupt anything, but it does mean a queue nobody can drain.
- A reverse proxy's timeouts and body limits in front of these ones.

**If it ever serves more than one person**
- Authorization, per-user data roots, and the multi-tenancy question `storage`
  is explicitly not built for (roadmap #17).
- Audit logging — who ingested what, when — which is also a privacy decision,
  since the log would then hold page URLs.
- Secret management: `server.token` in a `0o600` config is right for one user
  on one laptop and wrong everywhere else.

**If the transforms ever matter more**
- **Prompt injection.** Clipped page text goes into an LLM prompt, so a page
  can carry "ignore your instructions and …". Locally the blast radius is a bad
  summary in one's own notes. It stops being that the moment a transform can
  call a tool, reach the network, or write outside the document it is
  transforming — which is the line to watch, not the injection itself.
- Cost controls and quotas, once a transform is not spending the user's own
  local model.

**Operationally**
- Observability (roadmap #31's opt-in OpenTelemetry covers `net/http` and
  `database/sql` with no source changes — this is the natural first consumer).
- Supply-chain review of the extension's dependency tree, and signed builds.
  `pnpm` lockfile pinning, `web-ext sign`, and the reproducibility of both.
- Backup/restore under a running server: `tbuk export` reads a live database,
  and the interaction with an in-flight ingest is untested.
- A `/v1/` deprecation policy, which a single-user tool does not have and a
  published extension eventually needs.

## The extension

A separate repository (`tbuk-clipper`), not a directory in this one. Its
toolchain is pnpm/WXT/Vitest/Svelte and its release flow is `web-ext sign`;
vendoring that inside a Go module makes `make check-ci` describe half the
project. The contract between them is decision 9's fixtures.

It is anklipper with the Anki adapter replaced. What transfers, essentially
unchanged:

- **Ports and adapters.** Every `browser.*` call behind a module in
  `src/platform/`, each with an in-memory fake; tests run against the fakes.
- **Typed messaging.** One discriminated union on `type`, every message
  answered with a `Result<T, E>`, an error taxonomy where `no-receiver` is a
  normal condition rather than a failure.
- **No state in module scope.** Firefox's background is an event page and
  Chrome's a service worker; both are unloaded when idle. Anything durable goes
  through `storage.local` from the moment it exists.
- **The draft is durable from the first keystroke**, debounced, flushed on
  submit and on `pagehide`, and a capture never overwrites a clip that is open
  — it waits in a second slot and the panel asks.
- **Capture is a content script, not the menu event.** `info.selectionText` is
  truncated and carries no surroundings; the content script supplies the HTML
  fragment and the nearest heading. Shadow roots, cross-origin frames and the
  built-in PDF viewer each become a named warning shown in the panel.
- **The sidebar opens inside the gesture's own task**, before anything is
  awaited, or both browsers refuse.
- **Permissions:** `activeTab`, `scripting`, `contextMenus`, `storage`,
  `sidePanel`, and one loopback host permission. Never `<all_urls>`; the
  content script is registered at runtime with no match patterns. On Firefox
  MV3 the host permission is not granted at install, so the panel's first state
  is a button that asks for it.

What is new, and it is a small list: the client is `/v1/` instead of
AnkiConnect; the panel's controls are a transform picker (populated from
`GET /v1/transforms`), a language box shown only when the chosen transform
declares a `lang` variable, and a free-text metadata field; and because ingest
is queued, the panel polls `GET /v1/jobs/{id}` and shows *clipping → ingesting
→ saved*, with the clip kept and a **Try again** button on any failure.

Milestone M5 below is the whole extension; it is sized like anklipper's M2–M8
and will want its own plan in its own repo.

## Milestones

Each is a PR. TDD throughout, per AGENTS.md: `_test.go` first, watch it fail,
minimal implementation, `make check-ci` before the PR.

**M1 — `tbuk serve`, and nothing it can do yet.**
Config block and validation, the cobra seam, loopback enforcement, token and
origin middleware, body cap and timeouts, `GET /v1/health`, graceful shutdown
on SIGINT/SIGTERM through the existing `signal.NotifyContext`. No ingest.
*Done when:* `curl` with the token gets health, without it gets `unauthorized`,
from a disallowed origin gets `forbidden-origin`, and the route-walk test
passes vacuously.

**M2 — `IngestContent`, and a clip that lands.**
`ingest.ContentDoc` + `IngestContent`, extractor dispatch on MIME, raw archive
from a stream, the `clip://` identity, clip metadata. `POST /v1/clips`
synchronous for this milestone only, no transforms.
*Done when:* a posted clip is a document `tbuk search` finds and `tbuk reindex`
re-embeds; `IngestFile`'s tests are untouched and green.

**M3 — the queue.**
`clip_jobs` in `schemaSQL`, `ClipJobRepo`, the single worker, `202` + `job_id`,
`GET /v1/jobs/{id}`, startup drain, retention, `scripts/add-clip-jobs/`,
`storage.HasClipJobsTable` and the doctor line that names the script.
*Done when:* a clip posted to a server killed mid-ingest is ingested by the
next start, proved by a test that does exactly that.

**M4 — transforms.**
`internal/transform`, the three builtins installed by `tbuk init`, the context
budget check, `GET /v1/transforms`, chaining to `max_chain`, the dual raw
archive and its provenance metadata, and `tbuk ingest --transform`.
*Done when:* `tbuk ingest ./x.md --transform translate --transform-var lang=German`
stores the German text with `transform=translate` in its metadata, and
`tbuk reindex` re-embeds it with no model call — asserted by a fake LLM that
fails the test if called.

**M5 — the extension.** Its own repo, its own plan, the contract fixtures from
M4's server.
*Done when:* select → right-click → panel → **Ingest** → `tbuk search` finds
it, on a real Firefox against a real `tbuk serve`.

**M6 — docs and the seams that describe it.**
`docs/initial-context.md` gains a Server section, a Transforms section, and the
`clip_jobs` row in Storage — AGENTS.md requires it in the same change as the
architecture it describes, so M1–M4 each carry their slice and M6 is the
consolidation. README gains a "Clipping from the browser" section.
`next-steps.md` #14 points here. `SECURITY.md` gains the local-minimum rules
and the productionisation list.

## Testing

Following the house patterns, with nothing new required:

- **Handlers:** `net/http/httptest`, table-driven over method, token, origin,
  body, and expected `kind`. No sockets beyond httptest, no real browser.
- **Storage:** `:memory:` SQLite for `ClipJobRepo`, including the claim/drain
  path under a simulated restart.
- **Transforms:** the `ChatFn` seam that `rewrite` and `eval` already use — a
  three-line fake. One test asserts the budget refusal, one asserts the chain
  order, one asserts `reindex` spends no call.
- **Contract:** every `apitypes` struct round-trips its golden fixture; a
  changed shape fails here first.
- **Route walk:** a test enumerates registered routes and fails on any handler
  that reads a caller-supplied path — rule 6, pinned rather than remembered.
- **Injection bars:** the `"; DROP TABLE documents; --` title, a `<script>`
  body contributing no text, a clip carrying ANSI escapes printing clean.
- **Integration:** one test in `internal/cli/integration_test.go`'s style —
  the real root command, `serve` on an ephemeral port, embedding and chat
  endpoints faked, `init → serve → POST clip → search → delete`.
- Coverage ≥ 85% per new package, as `make check-ci` enforces.

## Risks

**The transform is the only non-deterministic thing in the pipeline**, and it
runs before storage. Decision 6 confines the damage: what is stored is a
recorded artifact, `reindex` never re-runs the model, and the original survives
for a better transform later.

**A queue is a place for work to sit unseen.** Retention and a doctor line are
the whole mitigation, and they are enough for a queue one person feeds by hand.
If the failed-job count is ever routinely non-zero, that is evidence the error
taxonomy is wrong, not that the queue needs features.

**The extension is the larger half of this plan** and is in another repo, on
another toolchain, behind a signing flow. M1–M4 are independently useful —
`tbuk ingest --transform` and a `curl`-able endpoint — which is the hedge.

**This reverses a documented "don't".** `next-steps.md` #14 and
`initial-context.md`'s "no web UI" both stand, and this plan does not contradict
either as literally written: there is no web UI, nothing is served to a browser
but JSON, and the local-first constraint is strengthened by rules 1, 3 and 6
rather than weakened. The line that genuinely moves is "no server", and it
moves for one use case. If a later plan wants `/v1/ask`, that is a separate
scope change and it should have to argue for itself.
