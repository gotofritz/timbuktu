package cli_test

import (
	"bytes"
	"context"
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

func TestRunDelete_found(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/tmp/test.md", SHA256: "aabbcc", Title: "test", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	var out bytes.Buffer
	err := cli.RunDelete(ctx, &out, db, docs, "/tmp/test.md")
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
	err := cli.RunDelete(ctx, &out, db, docs, "/no/such/file.md")
	if err == nil {
		t.Fatal("expected error for missing document")
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
	if err := cli.RunDelete(ctx, &out, db, docs, "/tmp/doc.md"); err != nil {
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
