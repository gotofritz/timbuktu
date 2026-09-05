package main

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// oldSchema is the chunks table and index as they were before #138: one text
// column, and an FTS5 index built from it.
const oldSchema = `
CREATE TABLE chunks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id  INTEGER NOT NULL,
    chunk_index  INTEGER NOT NULL,
    text         TEXT    NOT NULL,
    token_count  INTEGER NOT NULL DEFAULT 0,
    embedding    BLOB,
    UNIQUE(document_id, chunk_index)
);
CREATE VIRTUAL TABLE chunks_fts USING fts5(text, content='chunks', content_rowid='id');
CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;
CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
END;
CREATE TRIGGER chunks_au AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
    INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
END;
`

func openOldDB(t *testing.T, chunks ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("old schema: %v", err)
	}
	for i, text := range chunks {
		if _, err := db.Exec(
			`INSERT INTO chunks(document_id,chunk_index,text) VALUES(1,?,?)`, i, text); err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
	}
	return db
}

func matches(t *testing.T, db *sql.DB, term string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH ?`, term).Scan(&n); err != nil {
		t.Fatalf("MATCH %q: %v", term, err)
	}
	return n
}

func TestAddSearchText_backfillsAndRebuilds(t *testing.T) {
	db := openOldDB(t,
		"Plain prose about the meter.",
		"```go\n// read the meter\nfunc readMeter(id int) error { return nil }\n```",
	)

	n, err := addSearchText(db)
	if err != nil {
		t.Fatalf("addSearchText: %v", err)
	}
	if n != 2 {
		t.Errorf("backfilled %d chunks, want 2", n)
	}

	var searchText string
	if err := db.QueryRow(`SELECT search_text FROM chunks WHERE chunk_index=1`).Scan(&searchText); err != nil {
		t.Fatalf("read search_text: %v", err)
	}
	if strings.Contains(searchText, "func") {
		t.Errorf("code chunk not reduced: %q", searchText)
	}

	// The rebuilt index has to answer on the reduced encoding — the split form
	// of an identifier is the whole point — and the faithful text must not be
	// what is indexed any more.
	if got := matches(t, db, "meter"); got != 2 {
		t.Errorf("MATCH meter returned %d rows, want 2", got)
	}
	if got := matches(t, db, "readMeter"); got != 1 {
		t.Errorf("MATCH readMeter returned %d rows, want 1", got)
	}
}

// The triggers have to come back, or the next ingest writes chunks the index
// never hears about.
func TestAddSearchText_reinstatesTriggers(t *testing.T) {
	db := openOldDB(t, "First note.")
	if _, err := addSearchText(db); err != nil {
		t.Fatalf("addSearchText: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO chunks(document_id,chunk_index,text,search_text) VALUES(1,9,'later','laterterm')`,
	); err != nil {
		t.Fatalf("insert after migration: %v", err)
	}
	if got := matches(t, db, "laterterm"); got != 1 {
		t.Errorf("insert trigger did not fire: MATCH laterterm returned %d rows, want 1", got)
	}
}

// Running it twice must not fail: a half-finished run is retried, not
// unrecoverable.
func TestAddSearchText_idempotent(t *testing.T) {
	db := openOldDB(t, "A note about meters.")
	if _, err := addSearchText(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := addSearchText(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := matches(t, db, "meters"); got != 1 {
		t.Errorf("MATCH meters returned %d rows, want 1", got)
	}
}

// A chunk of pure syntax reduces to nothing; it keeps its text rather than
// dropping out of the index entirely.
func TestAddSearchText_keepsChunksThatReduceToNothing(t *testing.T) {
	db := openOldDB(t, "```\n{ } ( )\n```")
	if _, err := addSearchText(db); err != nil {
		t.Fatalf("addSearchText: %v", err)
	}
	var searchText string
	if err := db.QueryRow(`SELECT search_text FROM chunks`).Scan(&searchText); err != nil {
		t.Fatalf("read search_text: %v", err)
	}
	if strings.TrimSpace(searchText) == "" {
		t.Error("chunk left with empty search_text")
	}
}
