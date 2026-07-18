# Subplan 09: Document Management & Stats

## Goal

Implement `tbuk delete`, `tbuk update`, and `tbuk stats` commands. This is the
final subplan for the POC; it closes out the CLI surface from the original plan.

## Deliverables

- `tbuk delete <path>` — remove document + cascade-delete chunks/metadata
- `tbuk update <path>` — re-ingest if SHA256 changed, skip otherwise
- `tbuk stats` — summary of knowledge base contents
- Unit tests for all three command paths

## Package Layout

No new packages. All logic lives in `internal/ingest` (update reuses `Ingester`)
and `internal/storage`. Commands added to `internal/cli/`.

```
internal/cli/
  delete.go    ← tbuk delete
  update.go    ← tbuk update
  stats.go     ← tbuk stats
```

## `tbuk delete`

```
tbuk delete <path> [--yes]
```

Flow:
1. `docs.GetByPath(path)` — error if not found
2. Prompt for confirmation unless `--yes` flag set
3. `docs.Delete(id)` — ON DELETE CASCADE removes chunks, metadata, FTS entries

Output:
```
Deleted /docs/README.md (4 chunks removed)
```

## `tbuk update`

```
tbuk update <path> [--force]
```

Thin wrapper over `Ingester.IngestFile(path, Options{Force: force})`.

- SHA256 unchanged → print "Skipped: /path unchanged"
- SHA256 changed → re-index, print "Updated: /path (old: 4 chunks → new: 6 chunks)"
- `--force` → re-index unconditionally

## `tbuk stats`

```
tbuk stats [--format text|json]
```

Query:
```sql
SELECT
    COUNT(*)             AS total_documents,
    SUM(chunk_count)     AS total_chunks,
    SUM(embedded_count)  AS embedded_chunks,
    SUM(size_bytes)      AS total_size_bytes
FROM (
    SELECT
        d.id,
        COUNT(c.id) AS chunk_count,
        COUNT(c.embedding) AS embedded_count,
        LENGTH(GROUP_CONCAT(c.text)) AS size_bytes
    FROM documents d
    LEFT JOIN chunks c ON c.document_id = d.id
    GROUP BY d.id
);
```

Text output:
```
Knowledge Base Stats
────────────────────
Documents   : 12
Chunks      : 148
Embedded    : 148 / 148 (100%)
Approx size : 412 KB
DB path     : ~/.tbuk/tbuk.sqlite
DB size     : 8.2 MB
```

JSON output: flat object with same fields as snake_case keys.

## Tests

- `TestDeleteCommand_found` — deletes document and chunks, prints confirmation
- `TestDeleteCommand_notFound` — error message, exit code 1
- `TestUpdateCommand_unchanged` — same SHA256 → skip message
- `TestUpdateCommand_changed` — different SHA256 → re-index, new chunk count
- `TestUpdateCommand_force` — force flag re-ingests even if unchanged
- `TestStatsCommand_empty` — zero documents → stats show zeros
- `TestStatsCommand_populated` — correct counts after ingestion

## Dependencies

None. Uses packages from earlier subplans only.

## PR Scope

One PR. Final PR for the POC. After merge, archive `docs/plans/00-poc.md` and
all `01-09-*.md` subplans to `docs/archive/` with the conventional filename.

## QA Checklist

Before merging this PR, run:
```
task qa
```
or manually:
```
make check-ci
```

Verify:
- [ ] All tests pass
- [ ] Coverage ≥ 85 %
- [ ] `tbuk init && tbuk ingest testdata/ && tbuk ask "test"` end-to-end smoke test
- [ ] No lint warnings
