package storage

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps sql.DB with lifecycle management.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at the given path.
// Runs migrations before returning.
//
// Pragmas are set in the DSN so every pooled connection inherits them: SQLite
// pragmas are per-connection, and database/sql maintains a pool, so a pragma
// run once via db.Exec would leave every other pooled connection with
// foreign_keys=0 (breaking cascade deletes).
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		return nil, fmt.Errorf("storage.Open: %w", err)
	}
	if path == ":memory:" {
		// A second pooled connection to :memory: opens a *different* empty
		// database, so pin the pool to a single connection.
		db.SetMaxOpenConns(1)
	}
	if err := RunMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

// dsnFor builds a modernc.org/sqlite DSN that applies the required pragmas to
// every pooled connection.
func dsnFor(path string) string {
	const pragmas = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		return path + "?" + pragmas
	}
	return "file:" + path + "?_pragma=journal_mode(WAL)&" + pragmas
}

// DB returns the underlying *sql.DB.
func (d *DB) DB() *sql.DB { return d.db }

// Close closes the underlying connection.
func (d *DB) Close() error { return d.db.Close() }
