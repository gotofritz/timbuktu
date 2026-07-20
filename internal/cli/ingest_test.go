package cli_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func openMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.DB()
}

func TestIngestCommand_missingArg(t *testing.T) {
	err := runCLI("ingest")
	if err == nil {
		t.Fatal("expected error for missing path argument")
	}
}

func TestIngestCommand_nonExistentPath(t *testing.T) {
	err := runCLI("ingest", "/no/such/path/ever/exists")
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

func TestDoctorCommand_showsCounts(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	if err := runCLI("--config", cfgPath, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	// doctor must not error; it will show counts (0/0) from the empty DB
	if err := runCLI("--config", cfgPath, "doctor"); err != nil {
		t.Errorf("doctor: %v", err)
	}
}

func TestPrintFileResult_error(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	r := ingest.Result{Path: "/a.txt", Err: fmt.Errorf("boom")}
	err := cli.PrintFileResult(r, &outBuf, &errBuf)
	if err == nil {
		t.Fatal("expected error returned")
	}
	if !strings.Contains(errBuf.String(), "boom") {
		t.Errorf("error not in output: %s", errBuf.String())
	}
}

// Single-file success must print a one-line result unconditionally.
func TestPrintFileResult_success_printsChunks(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	r := ingest.Result{Path: "/a.txt", Chunks: 4}
	if err := cli.PrintFileResult(r, &outBuf, &errBuf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := outBuf.String()
	if !strings.Contains(out, "/a.txt") || !strings.Contains(out, "4 chunks embedded") {
		t.Errorf("want '<path> → 4 chunks embedded', got: %q", out)
	}
}

// Single-file skip must print (unconditionally, no --verbose needed).
func TestPrintFileResult_skipped_prints(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	r := ingest.Result{Path: "/a.txt", Skipped: true}
	if err := cli.PrintFileResult(r, &outBuf, &errBuf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(outBuf.String(), "skipped (unchanged)") {
		t.Errorf("want skipped message, got: %q", outBuf.String())
	}
}

func TestPrintDirResults_mixed(t *testing.T) {
	results := []ingest.Result{
		{Path: "/a.md", Chunks: 3},
		{Path: "/b.txt", Skipped: true},
		{Path: "/c.pdf", Err: fmt.Errorf("corrupt")},
	}
	var outBuf, errBuf bytes.Buffer
	err := cli.PrintDirResults(results, true, &outBuf, &errBuf)
	if err == nil {
		t.Fatal("expected error for failed file")
	}
	out := outBuf.String()
	if !strings.Contains(out, "3 chunks") {
		t.Errorf("missing chunk count in output: %s", out)
	}
	if !strings.Contains(out, "skipped") {
		t.Errorf("missing skipped in output: %s", out)
	}
	if !strings.Contains(errBuf.String(), "corrupt") {
		t.Errorf("error not in errBuf: %s", errBuf.String())
	}
}

func TestPrintDirResults_allOK(t *testing.T) {
	results := []ingest.Result{
		{Path: "/a.md", Chunks: 2},
		{Path: "/b.txt", Chunks: 1},
	}
	var outBuf, errBuf bytes.Buffer
	err := cli.PrintDirResults(results, false, &outBuf, &errBuf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(outBuf.String(), "Done:") {
		t.Errorf("missing Done summary: %s", outBuf.String())
	}
}
