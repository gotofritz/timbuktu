package storage

import (
	"database/sql"
	"errors"
	"fmt"
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

var migrations = []migration{
	{1, migration001},
}

const migration001 = `
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
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS chunks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id  INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    chunk_index  INTEGER NOT NULL,
    text         TEXT    NOT NULL,
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

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    text,
    content='chunks',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;

CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
    INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;
`

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
