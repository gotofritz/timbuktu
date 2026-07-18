package storage

import (
	"database/sql"
	"fmt"
	"time"
)

var migrations = []struct {
	version int
	sql     string
}{
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
	// Bootstrap the migrations table itself.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
        version    INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return fmt.Errorf("storage.RunMigrations bootstrap: %w", err)
	}

	for _, m := range migrations {
		var exists int
		_ = db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version=?`, m.version).Scan(&exists)
		if exists > 0 {
			continue
		}
		if _, err := db.Exec(m.sql); err != nil {
			return fmt.Errorf("storage.RunMigrations v%d: %w", m.version, err)
		}
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,?)`,
			m.version, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("storage.RunMigrations record v%d: %w", m.version, err)
		}
	}
	return nil
}
