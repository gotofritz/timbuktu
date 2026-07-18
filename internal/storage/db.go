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
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage.Open: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage.Open WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage.Open foreign_keys: %w", err)
	}
	if err := RunMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

// DB returns the underlying *sql.DB.
func (d *DB) DB() *sql.DB { return d.db }

// Close closes the underlying connection.
func (d *DB) Close() error { return d.db.Close() }
