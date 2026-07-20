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
		{1, migration001},
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
		{1, migration001},
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
		{1, migration001},
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
