package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func TestNormalizePath_absoluteUnchanged(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "a.md")
	got, err := cli.NormalizePath(abs)
	if err != nil {
		t.Fatalf("NormalizePath: %v", err)
	}
	if got != abs {
		t.Errorf("NormalizePath(%q) = %q, want unchanged", abs, got)
	}
}

func TestNormalizePath_relativeResolvedAndCleaned(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := cli.NormalizePath("./sub/../notes.md")
	if err != nil {
		t.Fatalf("NormalizePath: %v", err)
	}
	want := filepath.Join(dir, "notes.md")
	if got != want {
		t.Errorf("NormalizePath = %q, want %q", got, want)
	}
}

// A file ingested via a relative path must be stored (and thus deletable)
// under its absolute path, so a later delete via an absolute path matches.
func TestRunDelete_normalizesRelativeToAbsolute(t *testing.T) {
	sqlDB := openMemoryDB(t)
	docs := storage.NewDocumentRepo(sqlDB)
	ctx := context.Background()

	dir := t.TempDir()
	abs := filepath.Join(dir, "notes.md")
	if err := docs.Create(ctx, &storage.Document{
		Path: abs, SHA256: "abc", Title: "notes", MimeType: "text/plain",
	}); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	// Delete using the relative spelling from the file's directory.
	t.Chdir(dir)
	var out bytes.Buffer
	if err := cli.RunDelete(ctx, &out, sqlDB, docs, "notes.md"); err != nil {
		t.Fatalf("RunDelete(relative): %v", err)
	}
	if _, err := docs.GetByPath(ctx, abs); err == nil {
		t.Error("expected document deleted via relative path")
	}
}

// Ingest via a relative path must key the document by its absolute path.
func TestRunUpdate_storesAbsolutePath(t *testing.T) {
	sqlDB := openMemoryDB(t)
	docs := storage.NewDocumentRepo(sqlDB)
	chunks := storage.NewChunkRepo(sqlDB)
	meta := storage.NewMetadataRepo(sqlDB)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte("hello world content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	ing := ingest.NewIngester(
		docs, chunks, meta,
		&stubExtractor{text: "hello world content"},
		&chunking.Chunker{Size: 100, Overlap: 10},
		&stubEmbedder{},
		t.TempDir(),
		nil,
	)
	ctx := context.Background()

	t.Chdir(dir)
	var out bytes.Buffer
	if err := cli.RunUpdate(ctx, &out, ing, docs, "doc.md", false); err != nil {
		t.Fatalf("RunUpdate(relative): %v", err)
	}

	abs := filepath.Join(dir, "doc.md")
	if _, err := docs.GetByPath(ctx, abs); err != nil {
		t.Errorf("expected document keyed by absolute path %q: %v", abs, err)
	}
}
