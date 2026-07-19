package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func TestDeleteCommand_missingArg(t *testing.T) {
	err := runCLI("delete")
	if err == nil {
		t.Fatal("expected error for missing path argument")
	}
}

func TestDeleteCommand_notFoundWithConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := home + "/.tbuk/config.yaml"
	err := runCLI("--config", cfgPath, "delete", "--yes", "/no/such/doc.md")
	if err == nil {
		t.Error("expected error deleting non-existent document")
	}
}

func TestConfirmYes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"plain enter defaults to no", "\n", false},
		{"empty EOF defaults to no", "", false},
		{"lowercase y", "y\n", true},
		{"uppercase Y", "Y\n", true},
		{"full yes", "yes\n", true},
		{"padded y", "  y  \n", true},
		{"explicit n", "n\n", false},
		{"no", "no\n", false},
		{"garbage", "maybe\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got := cli.ConfirmYes(strings.NewReader(tc.input), &out, "Delete? [y/N] ")
			if got != tc.want {
				t.Errorf("ConfirmYes(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if !strings.Contains(out.String(), "Delete?") {
				t.Errorf("prompt not written, out = %q", out.String())
			}
		})
	}
}

func TestRunDelete_found(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/tmp/test.md", SHA256: "aabbcc", Title: "test", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	var out bytes.Buffer
	err := cli.RunDelete(ctx, &out, db, docs, "", "/tmp/test.md")
	if err != nil {
		t.Fatalf("RunDelete: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Deleted") {
		t.Errorf("expected 'Deleted' in output, got: %s", output)
	}
	if !strings.Contains(output, "/tmp/test.md") {
		t.Errorf("expected path in output, got: %s", output)
	}

	// Document must be gone
	_, err = docs.GetByPath(ctx, "/tmp/test.md")
	if err == nil {
		t.Error("expected document to be deleted")
	}
}

func TestRunDelete_notFound(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	ctx := context.Background()

	var out bytes.Buffer
	err := cli.RunDelete(ctx, &out, db, docs, "", "/no/such/file.md")
	if err == nil {
		t.Fatal("expected error for missing document")
	}
}

// A real DB error must not be reported as "document not found" (P1-10).
func TestRunDelete_dbError_notReportedAsNotFound(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	ctx := context.Background()
	_ = db.Close()

	var out bytes.Buffer
	err := cli.RunDelete(ctx, &out, db, docs, "", "/tmp/whatever.md")
	if err == nil {
		t.Fatal("expected error from closed DB")
	}
	if strings.Contains(err.Error(), "document not found") {
		t.Errorf("real DB error must not be reported as not-found, got: %v", err)
	}
}

// Deleting a document must also remove its extracted-text cache file
// (extractedDir/<sha>.txt), which was previously left behind forever (P1-12).
func TestRunDelete_removesExtractedCacheFile(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	ctx := context.Background()

	extractedDir := t.TempDir()
	sha := "cafebabe"
	cachePath := filepath.Join(extractedDir, sha+".txt")
	if err := os.WriteFile(cachePath, []byte("extracted text"), 0o600); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	doc := &storage.Document{Path: "/tmp/cached.md", SHA256: sha, Title: "cached", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunDelete(ctx, &out, db, docs, extractedDir, "/tmp/cached.md"); err != nil {
		t.Fatalf("RunDelete: %v", err)
	}

	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Errorf("expected extracted cache file removed, stat err = %v", err)
	}
}

// A missing cache file must not turn delete into an error (best-effort cleanup).
func TestRunDelete_missingCacheFileIsNotAnError(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/tmp/nocache.md", SHA256: "0badf00d", Title: "nocache", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunDelete(ctx, &out, db, docs, t.TempDir(), "/tmp/nocache.md"); err != nil {
		t.Fatalf("RunDelete with no cache file should succeed: %v", err)
	}
}

func TestRunDelete_showsChunkCount(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	chunks := storage.NewChunkRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/tmp/doc.md", SHA256: "deadbeef", Title: "doc", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	err := chunks.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "chunk one", TokenCount: 2},
		{DocumentID: doc.ID, ChunkIndex: 1, Text: "chunk two", TokenCount: 2},
	})
	if err != nil {
		t.Fatalf("bulk insert: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunDelete(ctx, &out, db, docs, "", "/tmp/doc.md"); err != nil {
		t.Fatalf("RunDelete: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "2") {
		t.Errorf("expected chunk count '2' in output, got: %s", output)
	}
	if !strings.Contains(output, "chunk") {
		t.Errorf("expected 'chunk' in output, got: %s", output)
	}
}
