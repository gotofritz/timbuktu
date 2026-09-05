package main

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

// schemaWith is the chunks table and its triggers, with the index created by
// the caller's DDL: the shapes a knowledge base can carry before this script
// runs — the FTS5 default of before #136, or the tokenchars '_-' of #136.
const schemaWith = `
CREATE TABLE chunks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    document_id  INTEGER NOT NULL,
    chunk_index  INTEGER NOT NULL,
    text         TEXT    NOT NULL,
    search_text  TEXT    NOT NULL DEFAULT '',
    token_count  INTEGER NOT NULL DEFAULT 0,
    embedding    BLOB,
    UNIQUE(document_id, chunk_index)
);
%s
CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;
CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
END;
CREATE TRIGGER chunks_au AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
    INSERT INTO chunks_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;
`

// defaultIndex is the index as it was before #136: the FTS5 default tokenizer,
// where '_' and '-' are separators.
const defaultIndex = `CREATE VIRTUAL TABLE chunks_fts USING fts5(search_text, content='chunks', content_rowid='id');`

// hyphenIndex is the index #136 created: '-' a token character alongside '_',
// which locks hyphenated English into one term (issue #143).
const hyphenIndex = `CREATE VIRTUAL TABLE chunks_fts USING fts5(search_text, content='chunks', ` +
	`content_rowid='id', tokenize="unicode61 tokenchars '_-'");`

func openOldDB(t *testing.T, chunks ...string) *sql.DB {
	t.Helper()
	return openDB(t, defaultIndex, chunks...)
}

func openDB(t *testing.T, index string, chunks ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(fmt.Sprintf(schemaWith, index)); err != nil {
		t.Fatalf("schema: %v", err)
	}
	for i, text := range chunks {
		if _, err := db.Exec(
			`INSERT INTO chunks(document_id,chunk_index,text,search_text) VALUES(1,?,?,?)`,
			i, text, text); err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
	}
	return db
}

func matches(t *testing.T, db *sql.DB, match string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH ?`, match).Scan(&n); err != nil {
		t.Fatalf("match %s: %v", match, err)
	}
	return n
}

// Before the rebuild the index cannot tell the two forms apart; after it, an
// exact term and an exclusion both mean one form.
func TestRetokenize_separatesTheForms(t *testing.T) {
	db := openOldDB(t,
		"the main_consumption register is read hourly",
		"the main consumption register is read hourly")

	if got := matches(t, db, `"main_consumption"`); got != 2 {
		t.Fatalf("before: want the old index to match both rows, got %d", got)
	}

	if err := retokenize(db); err != nil {
		t.Fatalf("retokenize: %v", err)
	}

	if got := matches(t, db, `"main_consumption"`); got != 1 {
		t.Errorf("exact term matched %d rows, want 1", got)
	}
	if got := matches(t, db, `("main" OR "consumption") NOT ("main_consumption")`); got != 1 {
		t.Errorf("exclusion matched %d rows, want 1", got)
	}
}

// The triggers live on chunks, not on chunks_fts, so they survive the drop and
// keep the rebuilt index in sync with later writes.
func TestRetokenize_triggersStillSyncTheIndex(t *testing.T) {
	db := openOldDB(t, "the main consumption register")
	if err := retokenize(db); err != nil {
		t.Fatalf("retokenize: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO chunks(document_id,chunk_index,text,search_text) VALUES(1,99,?,?)`,
		"a later check-ci note", "a later check-ci note"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := matches(t, db, `"check-ci"`); got != 1 {
		t.Errorf("want the new row reachable, got %d matches", got)
	}
}

// The index #136 left behind is the other input this script has to fix: '-'
// was a token character in it, so hyphenated English was one token and the
// words it is made of could not reach it (issue #143). After the rebuild they
// can, and the underscore form is still told apart from the words apart.
func TestRetokenize_freesHyphenatedProse(t *testing.T) {
	db := openDB(t, hyphenIndex,
		"we track long-term costs day-to-day",
		"the main_consumption register is read hourly",
		"the main consumption register is read hourly")

	if got := matches(t, db, `"long" OR "term"`); got != 0 {
		t.Fatalf("before: want the #136 index to hide the compound's words, got %d", got)
	}

	if err := retokenize(db); err != nil {
		t.Fatalf("retokenize: %v", err)
	}

	if got := matches(t, db, `"long" OR "term"`); got != 1 {
		t.Errorf("the compound's words matched %d rows, want 1", got)
	}
	if got := matches(t, db, `"long-term"`); got != 1 {
		t.Errorf("the compound as written matched %d rows, want 1", got)
	}
	if got := matches(t, db, `"main_consumption"`); got != 1 {
		t.Errorf("exact term matched %d rows, want 1 — '_' still holds a term together", got)
	}
}

// Re-running is a no-op rather than an error: the script is throwaway and gets
// run twice by anyone who is not sure whether it took.
func TestRetokenize_isRepeatable(t *testing.T) {
	db := openOldDB(t, "the main_consumption register")
	for i := range 2 {
		if err := retokenize(db); err != nil {
			t.Fatalf("retokenize run %d: %v", i+1, err)
		}
	}
	if got := matches(t, db, `"main_consumption"`); got != 1 {
		t.Errorf("want 1 match after two runs, got %d", got)
	}
}
