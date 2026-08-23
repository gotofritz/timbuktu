# Subplan 18: Source Abstraction + Joplin Ingestor

Lands roadmap **#18** (ingestor / `Source` abstraction) and **#19** (Joplin
ingestor) from `docs/plans/next-steps.md`, plus the storage half of **#23**
(lineage columns) that #18 requires. Evernote `.enex` (#20) and YouTube (#21)
are explicitly out of scope here — they ride the same seam in later subplans.

## Goal

Decouple ingestion from the local filesystem. Today the pipeline assumes a
path: `Ingester.IngestFile(ctx, path, opts)` hashes a file on disk,
`FileExtractor.ExtractFile(ctx, path)` re-opens it, `IngestDir` walks a
directory, and dedup is keyed on `documents.path`. A Joplin note is an HTTP API
response, not a file. Introduce a `Source` that *yields* documents (stable id,
content, native metadata, `source_uri`) so non-file origins plug in upstream of
the chunk → embed → store tail, which is reused unchanged.

Success = the existing file behaviour is preserved bit-for-bit (same chunks,
same rows, existing tests green) while Joplin notes ingest through the same
`Ingester`.

> **Amended after #130.** `tbuk reindex` and the rewritten `tbuk import` landed
> after this plan was written, and both re-read a document's content from the
> raw archive. That whole chain assumes a document is a file at a path and
> derives its type from the filename extension — assumptions a Joplin note
> breaks. See "Raw archive, reindex and import" below; the deliverables and
> tests here already include the amendments.

## Deliverables

- New `internal/source` package: `Source` / `SourceDoc` types, `FileSource`,
  `JoplinSource`.
- `Ingester.IngestSource(ctx, src, opts) []Result`; `IngestFile` / `IngestDir`
  reduced to thin wrappers over `FileSource` (backward-compatible signatures).
- Migration adding `source_type` + `source_uri` to `documents`, backfilling
  existing rows as `('file', path)`.
- Dedup re-keyed on the source's stable identity, not the OS path.
- Per-source metadata: each `Source` emits its own native metadata; the
  Ingester stops hard-coding `filename`/`extension`/`dir`.
- `sources.joplin` config block; `tbuk ingest --source joplin`.
- Raw archiving from a `SourceDoc`'s content stream rather than a file path,
  named for the source's own extension (see below).
- Content type carried on the document row instead of re-derived from the
  path, so `reindex` and `import` can re-read a non-file document.
- `SourceDoc`s record their archived copy in `documents.raw_path` (the column
  exists; `IngestSource` has to populate it).
- Unit tests: `FileSource` regression parity, `JoplinSource` against an
  `httptest` Joplin API, dedup + migration coverage. TDD, table-driven.

## Raw archive, reindex and import

A file document can always be re-read from disk. A Joplin note cannot: once
ingested, the copy under `raw/` is the only local copy of it that exists. That
makes the raw archive load-bearing for non-file sources, and three places in
the current code assume it is fed by a filesystem path.

1. **`Ingester.copySourceToRaw(path, sha)` opens a file.** `IngestSource` has to
   archive from `doc.Open(ctx)` instead — the same content it is about to hash
   and extract, teed to `rawDir/<sha256><ext>`. The extension comes from the
   `SourceDoc`, not from a path: files keep theirs, and a source that has none
   supplies one from its MIME (`text/markdown` → `.md`), so the archived copy
   stays openable and self-describing.

2. **`reindex` resolves content from `documents.raw_path`, falling back to
   `rawDir/<sha256><ext>` and then `documents.path`.** The `raw_path` column
   landed after this amendment was first written and takes most of this problem
   away: a `SourceDoc` simply records where its archived copy went, and no
   extension is ever derived from a `joplin://` identity. What remains is that
   `IngestSource` must *set* it — the paragraph below describes the failure if
   it does not. `documents.path`
   is `NOT NULL UNIQUE`, so a note has to store something there — `joplin://<id>`
   is the only sensible value — and `filepath.Ext("joplin://abc")` is `""`. So
   reindex looks for an extension-less `raw/<sha256>`, fails, falls back to
   `os.Stat("joplin://abc")`, fails again, and reports *"skipped: no archived
   copy and stored path unreadable"*. Every note would be stranded by the next
   embedding-model change — precisely the failure reindex exists to repair.
   Recording `raw_path` at ingest is what avoids this; the derivation is only a
   fallback for rows that predate the column, and no non-file document should
   ever rely on it.

3. **Extraction dispatches on the filename.** `preprocess.Extract` calls
   `DetectMIME(path)`, so an extension-less or `joplin://` name yields
   `application/octet-stream` and `"preprocess: no extractor for MIME type"` —
   even when the raw copy is found. `SourceDoc.MIME` is already persisted as
   `documents.mime_type`; `reindex` (and `import`) must extract with *that*,
   falling back to `DetectMIME` only when the row predates it. This is the
   single change that makes non-file documents re-readable at all.

`tbuk import` already reads `raw_path` from the archive's manifest, so it
inherits the fix; what remains there is its conflict key, `documents.path`,
which should become `source_uri` — for files the two are equal, so file
behaviour is unchanged.

## Package Layout

```
internal/source/
  source.go        ← Source, SourceDoc; content Open() closure
  file.go          ← FileSource: wraps the current path walk + preprocess
  joplin.go        ← JoplinSource: Joplin Data API client (paged /notes)
  source_test.go   ← shared fakes/helpers
  file_test.go
  joplin_test.go   ← httptest-mocked Joplin API

internal/ingest/
  ingester.go      ← add IngestSource(); IngestFile/IngestDir delegate to FileSource
```

## Interfaces

```go
package source

// SourceDoc is one ingestable unit produced by a Source. Open() defers reading
// content until the Ingester needs it (a note body, a file's bytes), so a
// Source can enumerate identities cheaply and skip unchanged docs before I/O.
type SourceDoc struct {
    SourceURI string            // stable identity: abs path | joplin://<id> | https://youtu.be/<v>
    Title     string
    MIME      string            // drives extractor dispatch + cleaning choice
    Metadata  map[string]string // native metadata (notebook, tags, created, dir...)
    Open      func(ctx context.Context) (io.ReadCloser, error)
}

// Source yields SourceDocs lazily. Go 1.26 range-over-func keeps memory O(1)
// in the corpus and lets ctx cancellation stop mid-enumeration.
type Source interface {
    Name() string                                    // "file", "joplin"
    Docs(ctx context.Context) iter.Seq2[SourceDoc, error]
}
```

`FileSource{Root string}` reproduces `IngestDir`'s walk + `supportedExts`
filter; each entry becomes a `SourceDoc{SourceURI: absPath, MIME: DetectMIME,
Open: os.Open, Metadata: filename/extension/dir}`. A single-file ingest is a
`FileSource` over one path.

`JoplinSource{BaseURL, Token string}` pages `GET /notes?fields=id,title,body,
updated_time,parent_id&token=...`, resolves `parent_id`→notebook title and
`GET /notes/:id/tags` for tags, and emits
`SourceDoc{SourceURI: "joplin://"+id, MIME: "text/markdown", Title: title,
Metadata: {notebook, tags, updated_time}, Open: body-as-reader}`.

## Ingester Changes

```go
func (ing *Ingester) IngestSource(ctx context.Context, src source.Source, opts Options) []Result
```

Per `SourceDoc`:

1. `docs.GetByURI(ctx, doc.SourceURI)` (new repo method; `GetByPath` becomes a
   thin alias while `path == source_uri` for files).
2. Read content via `doc.Open(ctx)` → `preprocess.NewExtractor(doc.MIME).
   Extract(ctx, r)` (reader-based; drops the path dependency in
   `readOrExtract`). Extracted-text cache still keyed by content SHA256.
3. `sha := HashReader(extracted)` → dedup: same SHA + `!Force` → `Skipped`.
4. Archive the content to `rawDir/<sha256><ext>` (unless `opts.NoRaw`), from
   the same stream — for a non-file source this copy is the only one that will
   exist locally.
5. chunk → embed → `ReplaceForDocument` — **unchanged**, including the atomic
   swap and dimension guard.
6. Persist `source_type = src.Name()`, `source_uri = doc.SourceURI`, and
   `doc.Metadata` (user-set keys still survive via `Set` upsert). The
   hard-coded `filename/extension/mime/dir` block moves into `FileSource`.

The hard-coded `filename/extension/mime/dir` metadata block moves into
`FileSource`. Note it now has a second caller — `Ingester.ReindexDocument`
writes it too — so both paths have to take their keys from the source.

`IngestFile(ctx, path, opts)` = `IngestSource(ctx, FileSource{Root: path},
opts)[0]`; `IngestDir` = `IngestSource` over a dir-rooted `FileSource`. Callers
and the CLI integration test are untouched.

## Migration

Append to the `migrations` slice in `storage/migrate.go` (one txn, crash-safe):

```sql
ALTER TABLE documents ADD COLUMN source_type TEXT NOT NULL DEFAULT 'file';
ALTER TABLE documents ADD COLUMN source_uri  TEXT NOT NULL DEFAULT '';
UPDATE documents SET source_uri = path WHERE source_uri = '';
CREATE UNIQUE INDEX idx_documents_source_uri ON documents(source_uri);
```

`path` stays the on-disk identity for files (`update`/`delete <path>` keep
working); `source_uri` is the generic key non-file sources dedup on. For files
the two are equal, so existing rows and the `path` UNIQUE constraint are
untouched. A non-file document stores its URI in `path` as well, since the
column is `NOT NULL UNIQUE`.

Issue [#122](https://github.com/gotofritz/timbuktu/issues/122) (re-key legacy
relative-path documents) overlaps here: the `source_uri = path` backfill
inherits whatever those rows hold. Landing #122 first keeps this migration from
carrying the problem into a second column.

## Config

```yaml
sources:
  joplin:
    base_url: http://localhost:41184   # Joplin Web Clipper / Data API
    token_env: JOPLIN_TOKEN            # token read from env, never stored in config
```

`Config.Validate` gains: if `--source joplin` is used, `base_url` non-empty and
the env var is set. Absent `sources` block = file-only behaviour (no breakage).

## CLI

```
tbuk ingest <path|dir>            # unchanged (implicit --source file)
tbuk ingest --source joplin       # ingest all Joplin notes via the Data API
    [--force] [--verbose]
```

Progress output reuses the existing `[n/N] <uri> → k chunks` writer; the URI
column shows `joplin://<id>` for notes.

## Error Handling

- Per-doc errors are collected into `[]Result` (like `IngestDir`), not fatal —
  one bad note doesn't abort the run.
- Joplin API: bounded-body error surfacing like the LLM/embedding adapters;
  wrap transient 429/5xx in the shared retry helper (mirror `embeddings/
  retry.go`). Auth failure (401) fails fast with the provider's message.
- `ctx` cancellation stops enumeration mid-stream (range-over-func returns).

## Tests

- `TestFileSource_parityWithIngestDir` — same files → identical chunks/rows as
  the current path (the key regression gate).
- `TestJoplinSource_mapsNote` — httptest note JSON → expected `SourceDoc`
  (uri, title, mime, notebook/tags metadata).
- `TestJoplinSource_paging` — multi-page `/notes` fully enumerated.
- `TestIngestSource_dedupByURI` — re-ingest unchanged note → `Skipped`;
  changed body → re-index.
- `TestMigration_sourceColumns` — pre-migration DB backfills `('file', path)`.
- `TestIngestSource_metadataFromSource` — file vs joplin emit their own keys;
  user-set keys survive.
- `TestIngestSource_archivesFromStream` — a source with no file on disk still
  lands a `raw/<sha256><ext>` copy, named from its MIME.
- `TestReindex_nonFileDocument` — a `joplin://` document re-embeds from its raw
  copy rather than being skipped as unresolvable (the regression this plan's
  amendment exists to prevent).
- `TestReindex_extractsByStoredMIME` — an extension-less archived copy is
  extracted via `documents.mime_type`, not `DetectMIME`.
- httptest for the API, `:memory:` SQLite, mocked embedder — no new external
  test deps.

## Dependencies

Depends on: storage (migration), preprocess (`NewExtractor` reader path already
exists), ingest, chunking, embeddings — all present. Pulls the `source_type`/
`source_uri` columns of #23 forward. No new third-party dependencies; the
Joplin client is stdlib `net/http` + `encoding/json`.

## PR Scope

Two PRs, both behind the eval-free, deterministic file-parity gate:

1. **Abstraction + FileSource + migration** — pure refactor, zero behaviour
   change, existing integration test green. Reviewable in isolation. Includes
   the raw-archive-from-stream and extract-by-stored-MIME changes: without
   them PR2's notes are ingestable but not re-indexable.
2. **JoplinSource + config + CLI `--source`** — the first non-file consumer,
   proving the seam.

## Acceptance

- `make check-ci` green (coverage ≥ 85% per package, incl. new `source`).
- Ingesting a directory yields byte-identical chunks/rows before and after PR1.
- `tbuk ingest --source joplin` populates the KB; `tbuk search`/`ask` cite
  `joplin://<id>`; re-running skips unchanged notes.
- `tbuk reindex` re-embeds Joplin notes from the raw archive — no document is
  reported as unresolvable purely for being non-file.
- `docs/initial-context.md` updated (new `source` package + `documents` columns
  affect architecture/boundaries) and this plan archived in the same PR.
