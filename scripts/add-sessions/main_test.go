package main

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// openOldDB is a knowledge base from before sessions existed: documents and
// chunks, no session tables.
func openOldDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE documents (
        id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE)`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	return db
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		t.Fatalf("check %s: %v", name, err)
	}
	return n == 1
}

func TestAddSessions(t *testing.T) {
	db := openOldDB(t)
	if err := addSessions(db); err != nil {
		t.Fatalf("addSessions: %v", err)
	}
	for _, name := range []string{"sessions", "session_turns"} {
		if !hasTable(t, db, name) {
			t.Errorf("%s not created", name)
		}
	}

	// The tables are usable: a thread and a turn go in, and the cascade holds.
	if _, err := db.Exec(
		`INSERT INTO sessions(name,template,created_at,updated_at) VALUES('go','qa','t','t')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO session_turns(session_id,turn_index,question,query,answer,citations,created_at)
         VALUES(1,0,'q','q','a','','t')`); err != nil {
		t.Fatalf("insert turn: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO session_turns(session_id,turn_index,question,query,answer,citations,created_at)
         VALUES(1,0,'q','q','a','','t')`); err == nil {
		t.Error("duplicate turn index: want a constraint error, got nil")
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE id=1`); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	var turns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_turns`).Scan(&turns); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turns != 0 {
		t.Errorf("%d turns survived the cascade, want 0", turns)
	}
}

// Running twice is a no-op, so a half-finished run can simply be re-run.
func TestAddSessions_idempotent(t *testing.T) {
	db := openOldDB(t)
	if err := addSessions(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := addSessions(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestAddSessions_closedDB(t *testing.T) {
	db := openOldDB(t)
	_ = db.Close()
	if err := addSessions(db); err == nil {
		t.Error("want an error against a closed database, got nil")
	}
}
