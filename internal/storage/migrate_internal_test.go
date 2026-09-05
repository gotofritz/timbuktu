package storage

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openRawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A second migration applies in its own transaction and records its version.
func TestRunMigrations_secondMigrationApplies(t *testing.T) {
	db := openRawDB(t)
	migs := []migration{
		{1, schemaSQL},
		{2, `CREATE TABLE IF NOT EXISTS extra (id INTEGER PRIMARY KEY);`},
	}
	if err := runMigrations(db, migs); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	var maxV int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&maxV); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if maxV != 2 {
		t.Errorf("max version = %d, want 2", maxV)
	}
	// Re-running is a no-op (both versions already recorded).
	if err := runMigrations(db, migs); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}

// A closed DB surfaces the bootstrap error rather than panicking.
func TestRunMigrations_bootstrapError(t *testing.T) {
	db := openRawDB(t)
	_ = db.Close()
	if err := runMigrations(db, migrations); err == nil {
		t.Fatal("expected error running migrations on a closed DB")
	}
}

// If the version-record insert fails, the whole migration transaction rolls
// back — the schema change does not survive without its recorded version.
func TestRunMigrations_recordInsertFailureRollsBack(t *testing.T) {
	db := openRawDB(t)
	// Pre-create schema_migrations so the bootstrap's IF NOT EXISTS is a no-op,
	// with a CHECK that rejects version >= 2 — the record INSERT for v2 fails
	// while its migration SQL succeeds.
	if _, err := db.Exec(`CREATE TABLE schema_migrations (
        version INTEGER PRIMARY KEY CHECK (version < 2),
        applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	migs := []migration{
		{1, schemaSQL},
		{2, `CREATE TABLE IF NOT EXISTS extra (id INTEGER PRIMARY KEY);`},
	}
	err := runMigrations(db, migs)
	if err == nil {
		t.Fatal("expected error from rejected version record")
	}

	// The v2 schema change must not have leaked out of the rolled-back tx.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='extra'`).Scan(&n); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if n != 0 {
		t.Errorf("rolled-back migration left table 'extra' behind")
	}
}

// A failing migration rolls back: neither its schema change nor its version
// record survive, so the transaction wrapping holds.
func TestRunMigrations_failedMigrationRollsBack(t *testing.T) {
	db := openRawDB(t)
	migs := []migration{
		{1, schemaSQL},
		{2, `THIS IS NOT VALID SQL;`},
	}
	err := runMigrations(db, migs)
	if err == nil {
		t.Fatal("expected error from invalid migration SQL")
	}
	if !strings.Contains(err.Error(), "v2") {
		t.Errorf("error should name the failing version, got %v", err)
	}

	// v2 must NOT be recorded — the transaction rolled back.
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version=2`).Scan(&count); err != nil {
		t.Fatalf("read: %v", err)
	}
	if count != 0 {
		t.Errorf("failed migration left version 2 recorded (%d)", count)
	}
}

// The schema is created in one step, so a fresh knowledge base has every
// column from the start — there is no older shape to upgrade from.
func TestRunMigrations_createsTheWholeSchemaAtOnce(t *testing.T) {
	db := openRawDB(t)
	if err := runMigrations(db, migrations); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO documents(path,sha256,title,mime_type,raw_path,created_at,updated_at)
         VALUES('/notes/a.md','abc','A','text/markdown','abc.md','t','t')`); err != nil {
		t.Fatalf("insert with every column: %v", err)
	}

	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("recorded version = %d, want %d", version, schemaVersion)
	}
}

// A knowledge base created by the build that applied this schema in two steps
// already records this version, so it opens untouched rather than being
// rejected as newer than the binary understands.
func TestRunMigrations_acceptsADatabaseFromTheTwoStepBuild(t *testing.T) {
	db := openRawDB(t)
	if err := runMigrations(db, migrations); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// That build recorded version 1 as well; the extra row must not read as
	// "newer than supported".
	if _, err := db.Exec(
		`INSERT INTO schema_migrations(version,applied_at) VALUES(1,'t')`); err != nil {
		t.Fatalf("seed the older version row: %v", err)
	}

	if err := runMigrations(db, migrations); err != nil {
		t.Fatalf("reopening a two-step knowledge base: %v", err)
	}
}

// The index has to keep '_' and '-' inside a token, or nothing downstream can
// tell main_consumption from the same words written apart (issue #136).
func TestRunMigrations_indexKeepsPunctuationInsideTokens(t *testing.T) {
	db := openRawDB(t)
	if err := runMigrations(db, migrations); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	seed := func(id int, text string) {
		t.Helper()
		if _, err := db.Exec(
			`INSERT INTO documents(id,path,sha256,title,mime_type,created_at,updated_at)
			 VALUES(?,?, 'x','t','text/markdown','t','t')`, id, text); err != nil {
			t.Fatalf("insert document: %v", err)
		}
		if _, err := db.Exec(
			`INSERT INTO chunks(document_id,chunk_index,text,search_text) VALUES(?,0,?,?)`,
			id, text, text); err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}
	seed(1, "the main_consumption register")
	seed(2, "the main consumption register")

	matches := func(match string) []string {
		t.Helper()
		rows, err := db.Query(`SELECT search_text FROM chunks_fts WHERE chunks_fts MATCH ? ORDER BY rowid`, match)
		if err != nil {
			t.Fatalf("match %s: %v", match, err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, s)
		}
		return out
	}

	if got := matches(`"main_consumption"`); len(got) != 1 || !strings.Contains(got[0], "main_consumption") {
		t.Errorf(`"main_consumption" matched %v, want the underscore form only`, got)
	}
	if got := matches(`("main" OR "consumption") NOT ("main_consumption")`); len(got) != 1 ||
		strings.Contains(got[0], "main_consumption") {
		t.Errorf("negation matched %v, want the space form only", got)
	}
}

// The tokenizer is what an exact term, a phrase and an exclusion mean, so
// doctor has to be able to tell the current one from every shape an older
// knowledge base can carry.
func TestReadFTSTokenizer(t *testing.T) {
	db := openRawDB(t)
	if err := runMigrations(db, migrations); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	got, err := ReadFTSTokenizer(db)
	if err != nil {
		t.Fatalf("ReadFTSTokenizer: %v", err)
	}
	if got != FTSTokenizer {
		t.Errorf("a current schema reports %q, want %q", got, FTSTokenizer)
	}

	tests := []struct {
		name string
		ddl  string
		want string
	}{
		{
			"before #136: the FTS5 default, no tokenize clause",
			`CREATE VIRTUAL TABLE chunks_fts USING fts5(search_text, content='chunks', content_rowid='id')`,
			"",
		},
		{
			"#136: '-' a token character too, hyphenated prose locked in one term",
			`CREATE VIRTUAL TABLE chunks_fts USING fts5(search_text, content='chunks', content_rowid='id', ` +
				`tokenize="unicode61 tokenchars '_-'")`,
			`unicode61 tokenchars '_-'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, stmt := range []string{`DROP TABLE chunks_fts`, tt.ddl} {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}
			got, err := ReadFTSTokenizer(db)
			if err != nil {
				t.Fatalf("ReadFTSTokenizer: %v", err)
			}
			if got != tt.want {
				t.Errorf("reported %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadFTSTokenizer_noIndex(t *testing.T) {
	got, err := ReadFTSTokenizer(openRawDB(t))
	if err != nil {
		t.Fatalf("ReadFTSTokenizer: %v", err)
	}
	if got != "" {
		t.Errorf("a database with no index reported %q, want no tokenizer", got)
	}
}
