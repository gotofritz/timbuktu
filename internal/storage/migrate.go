package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrSchemaTooNew is returned when the database records a schema version higher
// than this binary knows about — a DB written by a newer tbuk. Reading or
// writing it could corrupt data, so migration refuses and asks for an upgrade.
var ErrSchemaTooNew = errors.New("storage: database schema is newer than this tbuk supports")

type migration struct {
	version int
	sql     string
}

// schemaVersion is the version the one migration below records.
//
// It is 2 rather than 1 because an earlier build created this schema in two
// steps, and a knowledge base made by that build already records version 2 —
// numbering the single schema 2 lets those open untouched while a new one
// starts here. Nothing in the wild predates that, so there is no upgrade path
// to carry: a knowledge base either does not exist yet or already has this
// schema.
//
// schemaSQL is therefore edited in place rather than followed by a versioned
// migration (AGENTS.md, "proof of concept"): a new knowledge base gets the
// current shape, and an existing one is brought to it by a throwaway script
// under scripts/ — chunks.search_text was added that way, and the chunks_fts
// tokenizer changed that way.
const schemaVersion = 2

var migrations = []migration{
	{schemaVersion, schemaSQL},
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS documents (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    path       TEXT    NOT NULL UNIQUE,
    sha256     TEXT    NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    mime_type  TEXT    NOT NULL DEFAULT '',
    -- Where this document's archived copy lives, relative to the configured
    -- raw directory. Empty means none is known: ingested with --no-raw, or
    -- with the archive switched off.
    raw_path   TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS chunks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id  INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index  INTEGER NOT NULL,
    -- The chunk as written: what tbuk search prints and what tbuk ask feeds the
    -- model. Code is readable here, fences and all.
    text         TEXT    NOT NULL,
    -- The same chunk reduced for retrieval (internal/searchtext): prose as it
    -- stands, code as its comments, identifiers and split forms. This is what
    -- the FTS5 index below is built from and what the embedder is shown.
    search_text  TEXT    NOT NULL DEFAULT '',
    token_count  INTEGER NOT NULL DEFAULT 0,
    embedding    BLOB,
    UNIQUE(document_id, chunk_index)
);

CREATE TABLE IF NOT EXISTS metadata (
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    key         TEXT    NOT NULL,
    value       TEXT    NOT NULL,
    PRIMARY KEY (document_id, key)
);

-- tokenchars '_-' keeps '_' and '-' inside a token, so main_consumption and
-- check-ci index as one term each rather than as their parts. That is what lets
-- a query tell an exact term from the same words written apart, and what makes
-- an exclusion mean one form rather than both (issue #136). The split forms
-- searchtext.Reduce emits alongside each identifier are what keeps the loose
-- query reaching the code all the same.
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    search_text,
    content='chunks',
    content_rowid='id',
    tokenize="unicode61 tokenchars '_-'"
);

CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
END;

CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
    INSERT INTO chunks_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;
`

// HasSearchTextColumn reports whether chunks carries the search_text column.
//
// A knowledge base created before it existed opens and reads fine — its index
// is still there — but every ingest into it fails on the missing column, so
// something has to be able to ask. See scripts/ for the script that adds it.
func HasSearchTextColumn(db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('chunks') WHERE name='search_text'`,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("storage.HasSearchTextColumn: %w", err)
	}
	return n > 0, nil
}

// HasPunctuationTokenizer reports whether chunks_fts was created with the
// tokenchars tokenizer the query semantics depend on.
//
// A knowledge base built before it searches and ingests fine, but '_' and '-'
// are separators in its index, so an exact term, an exclusion and a phrase all
// collapse to the same bag of words. Nothing fails; the answers are just
// wrong, which is exactly the kind of thing doctor exists to say out loud. See
// scripts/ for the script that rebuilds the index.
func HasPunctuationTokenizer(db *sql.DB) (bool, error) {
	var ddl string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='chunks_fts'`,
	).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage.HasPunctuationTokenizer: %w", err)
	}
	return strings.Contains(ddl, "tokenchars"), nil
}

// RunMigrations applies any pending schema migrations.
func RunMigrations(db *sql.DB) error {
	return runMigrations(db, migrations)
}

// runMigrations applies migs against db. Split from RunMigrations so tests can
// exercise multi-migration and failure paths with custom lists.
func runMigrations(db *sql.DB, migs []migration) error {
	// Bootstrap the migrations table itself.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
        version    INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return fmt.Errorf("storage.RunMigrations bootstrap: %w", err)
	}

	// Refuse a database created by a newer tbuk: if its highest recorded version
	// exceeds the latest migration this binary carries, its schema may be one we
	// don't understand, and reading/writing it could corrupt data.
	var dbMax sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&dbMax); err != nil {
		return fmt.Errorf("storage.RunMigrations read version: %w", err)
	}
	latest := migs[len(migs)-1].version
	if dbMax.Valid && dbMax.Int64 > int64(latest) {
		return fmt.Errorf("%w: database is at version %d but this tbuk supports up to %d — upgrade tbuk",
			ErrSchemaTooNew, dbMax.Int64, latest)
	}

	for _, m := range migs {
		var exists int
		// Propagate the check error rather than swallowing it: a transient
		// failure must not read as "not applied" and re-run a migration.
		if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version=?`, m.version).Scan(&exists); err != nil {
			return fmt.Errorf("storage.RunMigrations check v%d: %w", m.version, err)
		}
		if exists > 0 {
			continue
		}

		// Apply the migration SQL and record its version in one transaction, so a
		// crash between the two can never leave the schema changed but unrecorded
		// (which would re-apply it on next start).
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("storage.RunMigrations begin v%d: %w", m.version, err)
		}
		if _, err := tx.Exec(m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storage.RunMigrations v%d: %w", m.version, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`,
			m.version, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("storage.RunMigrations record v%d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storage.RunMigrations commit v%d: %w", m.version, err)
		}
	}
	return nil
}
