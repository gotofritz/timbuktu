// Command retokenize-fts brings a knowledge base built before issue #136 up to
// the current schema: it drops chunks_fts and recreates it with the
// unicode61 tokenchars '_-' tokenizer, then rebuilds the index.
//
// Without it, '_' and '-' are separators in the stored index, so
// main_consumption and the same words written apart are one and the same term:
// an exact-term query, an exclusion and a phrase all answer as if the
// punctuation had never been typed. Nothing errors — the answers are just
// wrong.
//
// This is a throwaway (AGENTS.md, "proof of concept"): there is no versioned
// migration for the change, and this script exists only until the one
// knowledge base that needs it has been converted. Delete it then.
//
// Usage:
//
//	go run ./scripts/retokenize-fts ~/.tbuk/tbuk.sqlite
//
// No re-embedding and no re-ingest: chunks_fts is external-content
// (content='chunks'), so the index regenerates from the stored search_text
// column, and the vectors are untouched. Run scripts/add-search-text first if
// `tbuk doctor` also reports chunks.search_text missing.
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
		fmt.Fprintf(os.Stderr, "retokenize-fts: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Fprintf(os.Stderr, "retokenize-fts: open: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	if err := retokenize(db); err != nil {
		fmt.Fprintf(os.Stderr, "retokenize-fts: %v\n", err) //nolint:errcheck
		os.Exit(1)
	}
	fmt.Println("index rebuilt with tokenize=\"unicode61 tokenchars '_-'\"") //nolint:errcheck
}

// retokenize recreates chunks_fts under the new tokenizer and rebuilds it from
// chunks.search_text. It is safe to re-run: the table is dropped either way,
// and the rebuild reads the same column each time.
//
// The chunks_ai/chunks_ad/chunks_au triggers are left alone: they are defined
// on chunks, not on chunks_fts, and they name the same column before and
// after, so they survive the drop and keep writing into the new index.
func retokenize(db *sql.DB) error {
	if _, err := db.Exec(`DROP TABLE IF EXISTS chunks_fts`); err != nil {
		return fmt.Errorf("drop index: %w", err)
	}
	if _, err := db.Exec(`
		CREATE VIRTUAL TABLE chunks_fts USING fts5(
		    search_text,
		    content='chunks',
		    content_rowid='id',
		    tokenize="unicode61 tokenchars '_-'"
		)`); err != nil {
		return fmt.Errorf("recreate index: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild index: %w", err)
	}
	return nil
}
