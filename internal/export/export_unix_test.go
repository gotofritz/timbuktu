//go:build unix

package export_test

import (
	"bytes"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/export"
)

// A named pipe inside a data folder must not be archived: opening one blocks
// until a writer appears, which would hang the export indefinitely.
func TestCreate_skipsNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	writeFile(t, filepath.Join(cfg.Ingest.RawDir, "a.pdf"), []byte("pdf"))
	fifo := filepath.Join(cfg.Ingest.RawDir, "pipe.pdf")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	done := make(chan error, 1)
	var buf bytes.Buffer
	go func() { done <- export.Create(&buf, cfg, root) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Create blocked on a named pipe")
	}

	names := readTar(t, &buf)
	if _, ok := names["raw/pipe.pdf"]; ok {
		t.Error("named pipe should not be archived")
	}
	if _, ok := names["raw/a.pdf"]; !ok {
		t.Errorf("regular file should still be archived, have %v", keys(names))
	}
}
