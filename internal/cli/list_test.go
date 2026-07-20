package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func seedListDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	docs := storage.NewDocumentRepo(db.DB())
	chunks := storage.NewChunkRepo(db.DB())

	d1 := &storage.Document{Path: "/a.md", SHA256: "s1", Title: "Alpha", MimeType: "text/markdown"}
	d2 := &storage.Document{Path: "/b.md", SHA256: "s2", Title: "Beta", MimeType: "text/markdown"}
	for _, d := range []*storage.Document{d1, d2} {
		if err := docs.Create(ctx, d); err != nil {
			t.Fatalf("create doc: %v", err)
		}
	}
	// d1 gets 2 chunks, d2 gets none.
	if err := chunks.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: d1.ID, ChunkIndex: 0, Text: "one", TokenCount: 1},
		{DocumentID: d1.ID, ChunkIndex: 1, Text: "two", TokenCount: 1},
	}); err != nil {
		t.Fatalf("insert chunks: %v", err)
	}
	return db
}

func TestRunList_text(t *testing.T) {
	db := seedListDB(t)
	var buf bytes.Buffer
	if err := cli.RunList(&buf, db.DB(), 0, "text"); err != nil {
		t.Fatalf("RunList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"/a.md", "Alpha", "/b.md", "Beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// d1's chunk count (2) must appear.
	if !strings.Contains(out, "2") {
		t.Errorf("expected chunk count 2 in output:\n%s", out)
	}
}

func TestRunList_json(t *testing.T) {
	db := seedListDB(t)
	var buf bytes.Buffer
	if err := cli.RunList(&buf, db.DB(), 0, "json"); err != nil {
		t.Fatalf("RunList: %v", err)
	}
	var items []struct {
		Path   string `json:"path"`
		Title  string `json:"title"`
		Chunks int64  `json:"chunks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &items); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Path != "/a.md" || items[0].Chunks != 2 {
		t.Errorf("item0 = %+v, want /a.md with 2 chunks", items[0])
	}
	if items[1].Chunks != 0 {
		t.Errorf("item1 chunks = %d, want 0", items[1].Chunks)
	}
}

func TestRunList_limit(t *testing.T) {
	db := seedListDB(t)
	var buf bytes.Buffer
	if err := cli.RunList(&buf, db.DB(), 1, "json"); err != nil {
		t.Fatalf("RunList: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &items); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("limit=1: want 1 item, got %d", len(items))
	}
}

func TestRunList_empty(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var buf bytes.Buffer
	if err := cli.RunList(&buf, db.DB(), 0, "text"); err != nil {
		t.Fatalf("RunList: %v", err)
	}
	if !strings.Contains(strings.ToLower(buf.String()), "no documents") {
		t.Errorf("empty DB should report no documents, got: %q", buf.String())
	}
}

func TestListCommand_badFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	err := runCLI("--config", home+"/.tbuk/config.yaml", "list", "--format", "xml")
	if err == nil {
		t.Fatal("expected error for bad format")
	}
	if !strings.Contains(err.Error(), "format") {
		t.Errorf("want format error, got %v", err)
	}
}
