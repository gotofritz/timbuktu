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

// These two live here because they turn on a distinction only Unix draws. A
// path whose parent is a file fails stat with ENOTDIR, which Create must
// propagate rather than read as "component absent". Windows answers the same
// stat with ERROR_PATH_NOT_FOUND, which Go maps to fs.ErrNotExist — so there
// the component genuinely is missing and skipping it is correct. The case
// cannot be constructed off Unix, which is why this is a build tag rather than
// a skip (issue #121).

func TestCreate_dbPathStatErrorPropagates(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "afile")
	writeFile(t, file, []byte("x"))
	cfg := config.DefaultsForRoot(root)
	cfg.Database.Path = filepath.Join(file, "nested.sqlite") // parent is a file → ENOTDIR

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err == nil {
		t.Fatal("expected error when a component path cannot be stat'd")
	}
}

func TestCreate_dirComponentStatErrorPropagates(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notadir")
	writeFile(t, file, []byte("x"))
	cfg := config.DefaultsForRoot(root)
	cfg.Prompts.Dir = filepath.Join(file, "sub") // parent is a file → ENOTDIR

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err == nil {
		t.Fatal("expected error when a directory component cannot be stat'd")
	}
}

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
