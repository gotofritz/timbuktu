// Command add-search-text brings a knowledge base built before issue #138 up to
// the current schema: it adds chunks.search_text, fills it with the reduced
// encoding of each chunk, and rebuilds the FTS5 index from that column instead
// of chunks.text.
//
// This is a throwaway (AGENTS.md, "proof of concept"): there is no versioned
// migration for the change, and this script exists only until the one knowledge
// base that needs it has been converted. Delete it then.
//
// Usage:
//
//	go run ./scripts/add-search-text ~/.tbuk/tbuk.sqlite
//
// Afterwards run `tbuk reindex`. The backfill can only reduce the text already
// stored, which was extracted before fences were kept, so it recovers the
// inline spans and nothing else; reindex re-extracts, re-chunks and re-embeds
// against the current encoding. Until it has run, the stored vectors are still
// the ones taken from the faithful text.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/gotofritz/timbuktu/internal/searchtext"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <path to tbuk.sqlite>\n", os.Args[0]) //nolint:errcheck
		os.Exit(2)
	}
	path := os.Args[1]
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "add-search-text: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Fprintf(os.Stderr, "add-search-text: open: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	n, err := addSearchText(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "add-search-text: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}
	fmt.Printf("%d chunks encoded; index rebuilt from search_text\n", n)        //nolint:errcheck
	fmt.Println("now run `tbuk reindex` to re-extract and re-embed the corpus") //nolint:errcheck
}

// addSearchText adds the column, backfills it and rebuilds the index, returning
// the number of chunks encoded. It is safe to re-run: each step checks for the
// state it would create.
func addSearchText(db *sql.DB) (int, error) {
	// Drop the triggers first: they reference the column being replaced, and
	// leaving them in place would have the backfill write into the old index.
	for _, name := range []string{"chunks_ai", "chunks_ad", "chunks_au"} {
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			return 0, fmt.Errorf("drop trigger %s: %w", name, err)
		}
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS chunks_fts`); err != nil {
		return 0, fmt.Errorf("drop index: %w", err)
	}

	has, err := hasSearchText(db)
	if err != nil {
		return 0, err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE chunks ADD COLUMN search_text TEXT NOT NULL DEFAULT ''`); err != nil {
			return 0, fmt.Errorf("add column: %w", err)
		}
	}

	n, err := backfill(db)
	if err != nil {
		return 0, err
	}

	if _, err := db.Exec(`
		CREATE VIRTUAL TABLE chunks_fts USING fts5(
		    search_text,
		    content='chunks',
		    content_rowid='id',
		    tokenize="unicode61 tokenchars '_'"
		);

		CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
		    INSERT INTO chunks_fts(rowid, search_text) VALUES (new.id, new.search_text);
		END;

		CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
		    INSERT INTO chunks_fts(chunks_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
		END;

		CREATE TRIGGER chunks_au AFTER UPDATE ON chunks BEGIN
		    INSERT INTO chunks_fts(chunks_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
		    INSERT INTO chunks_fts(rowid, search_text) VALUES (new.id, new.search_text);
		END;`); err != nil {
		return 0, fmt.Errorf("recreate index: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`); err != nil {
		return 0, fmt.Errorf("rebuild index: %w", err)
	}
	return n, nil
}

func hasSearchText(db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('chunks') WHERE name='search_text'`).Scan(&n); err != nil {
		return false, fmt.Errorf("inspect chunks: %w", err)
	}
	return n > 0, nil
}

// backfill writes the reduced encoding of every chunk. The rows are read whole
// before any is written: a personal knowledge base is small, and holding the
// read cursor open across the writes is not worth the trouble.
func backfill(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT id, text FROM chunks ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("read chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type encoded struct {
		id   int64
		text string
	}
	var all []encoded
	for rows.Next() {
		var e encoded
		if err := rows.Scan(&e.id, &e.text); err != nil {
			return 0, fmt.Errorf("scan chunk: %w", err)
		}
		reduced := searchtext.Reduce(e.text)
		if strings.TrimSpace(reduced) == "" {
			reduced = e.text // all syntax: better indexed badly than not at all
		}
		e.text = reduced
		all = append(all, e)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read chunks: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	stmt, err := tx.Prepare(`UPDATE chunks SET search_text=? WHERE id=?`)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range all {
		if _, err := stmt.Exec(e.text, e.id); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("encode chunk %d: %w", e.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(all), nil
}
