// Command add-sessions brings a knowledge base built before issue #157 up to
// the current schema: it creates the sessions and session_turns tables that
// `tbuk ask --session` records a conversation thread in.
//
// Without them a threaded ask fails with a raw "no such table: sessions"; with
// them nothing else changes, since no other command reads either table. There
// is nothing to backfill — a knowledge base that has never held a thread starts
// with none — and no re-embedding or re-ingest: documents, chunks and vectors
// are untouched.
//
// This is a throwaway (AGENTS.md, "proof of concept"): there is no versioned
// migration for the change, and this script exists only until the one knowledge
// base that needs it has been converted. Delete it then.
//
// Usage:
//
//	go run ./scripts/add-sessions ~/.tbuk/tbuk.sqlite
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <path to tbuk.sqlite>\n", os.Args[0]) //nolint:errcheck
		os.Exit(2)
	}
	path := os.Args[1]
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "add-sessions: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Fprintf(os.Stderr, "add-sessions: open: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	if err := addSessions(db); err != nil {
		fmt.Fprintf(os.Stderr, "add-sessions: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}
	fmt.Println("sessions and session_turns created; `tbuk ask --session NAME` can record a thread now") //nolint:errcheck
}

// sessionSchema mirrors the session half of storage.schemaSQL. Every statement
// is IF NOT EXISTS, so the script is safe to re-run.
const sessionSchema = `
CREATE TABLE IF NOT EXISTS sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,
    template   TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS session_turns (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_index INTEGER NOT NULL,
    question   TEXT    NOT NULL,
    query      TEXT    NOT NULL DEFAULT '',
    answer     TEXT    NOT NULL,
    citations  TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    UNIQUE(session_id, turn_index)
);

CREATE INDEX IF NOT EXISTS idx_session_turns_session
    ON session_turns(session_id, turn_index);
`

// addSessions creates the session tables if they are not already there.
func addSessions(db *sql.DB) error {
	if _, err := db.Exec(sessionSchema); err != nil {
		return fmt.Errorf("create session tables: %w", err)
	}
	return nil
}
