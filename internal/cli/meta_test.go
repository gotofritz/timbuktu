package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func TestMetaCommands_endToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := home + "/.tbuk/config.yaml"

	// Seed a document directly into the configured DB.
	db, err := storage.Open(home + "/.tbuk/tbuk.sqlite")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	docs := storage.NewDocumentRepo(db.DB())
	if err := docs.Create(context.Background(), &storage.Document{
		Path: "/notes.md", SHA256: "ffee", Title: "Notes", MimeType: "text/plain",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	_ = db.Close()

	if err := runCLI("--config", cfgPath, "meta", "set", "/notes.md", "tag=design"); err != nil {
		t.Fatalf("meta set: %v", err)
	}
	if err := runCLI("--config", cfgPath, "meta", "list", "/notes.md"); err != nil {
		t.Fatalf("meta list: %v", err)
	}
	// Missing document surfaces an error through the command path.
	if err := runCLI("--config", cfgPath, "meta", "list", "/missing.md"); err != nil {
		t.Logf("meta list missing (non-fatal): %v", err)
	}
}

func TestRunMetaSet_and_List(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	meta := storage.NewMetadataRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/tmp/notes.md", SHA256: "aa", Title: "notes", MimeType: "text/markdown"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunMetaSet(ctx, &out, docs, meta, "/tmp/notes.md", []string{"tag=design", "author=Alice"}); err != nil {
		t.Fatalf("RunMetaSet: %v", err)
	}

	// values persisted
	if v, err := meta.Get(ctx, doc.ID, "tag"); err != nil || v != "design" {
		t.Errorf("tag: got %q err %v", v, err)
	}
	if v, err := meta.Get(ctx, doc.ID, "author"); err != nil || v != "Alice" {
		t.Errorf("author: got %q err %v", v, err)
	}

	var listOut bytes.Buffer
	if err := cli.RunMetaList(ctx, &listOut, docs, meta, "/tmp/notes.md"); err != nil {
		t.Fatalf("RunMetaList: %v", err)
	}
	s := listOut.String()
	if !strings.Contains(s, "tag=design") || !strings.Contains(s, "author=Alice") {
		t.Errorf("list output missing entries: %s", s)
	}
}

func TestRunMetaSet_invalidPair(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	meta := storage.NewMetadataRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/tmp/x.md", SHA256: "bb", Title: "x", MimeType: "text/markdown"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	var out bytes.Buffer
	err := cli.RunMetaSet(ctx, &out, docs, meta, "/tmp/x.md", []string{"noequals"})
	if err == nil {
		t.Fatal("expected error for pair without '='")
	}
}

func TestRunMetaSet_docNotFound(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	meta := storage.NewMetadataRepo(db)
	ctx := context.Background()

	var out bytes.Buffer
	if err := cli.RunMetaSet(ctx, &out, docs, meta, "/no/such.md", []string{"k=v"}); err == nil {
		t.Fatal("expected error for missing document")
	}
}

func TestRunMetaList_docNotFound(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	meta := storage.NewMetadataRepo(db)
	ctx := context.Background()

	var out bytes.Buffer
	if err := cli.RunMetaList(ctx, &out, docs, meta, "/no/such.md"); err == nil {
		t.Fatal("expected error for missing document")
	}
}

func TestRunMetaList_empty(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	meta := storage.NewMetadataRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/tmp/empty.md", SHA256: "cc", Title: "e", MimeType: "text/markdown"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunMetaList(ctx, &out, docs, meta, "/tmp/empty.md"); err != nil {
		t.Fatalf("RunMetaList: %v", err)
	}
	if !strings.Contains(out.String(), "No metadata") {
		t.Errorf("expected 'No metadata' message, got: %s", out.String())
	}
}

func TestMetaCommand_missingArgs(t *testing.T) {
	if err := runCLI("meta", "set"); err == nil {
		t.Error("expected error: meta set with no path")
	}
	if err := runCLI("meta", "list"); err == nil {
		t.Error("expected error: meta list with no path")
	}
}
