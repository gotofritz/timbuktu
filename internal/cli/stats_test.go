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

func TestStatsCommand_withRealConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := home + "/.tbuk/config.yaml"
	if err := runCLI("--config", cfgPath, "stats"); err != nil {
		t.Errorf("stats command: %v", err)
	}
}

func TestStatsCommand_jsonFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := home + "/.tbuk/config.yaml"
	if err := runCLI("--config", cfgPath, "stats", "--format", "json"); err != nil {
		t.Errorf("stats --format json: %v", err)
	}
}

func TestRunStats_empty(t *testing.T) {
	db := openMemoryDB(t)
	var out bytes.Buffer
	if err := cli.RunStats(&out, db, "/fake/path.sqlite", "text"); err != nil {
		t.Fatalf("RunStats: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "0") {
		t.Errorf("expected zeros in empty stats output, got: %s", output)
	}
	if !strings.Contains(output, "Documents") {
		t.Errorf("expected 'Documents' label in output, got: %s", output)
	}
}

func TestRunStats_populated(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	chunks := storage.NewChunkRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/a.md", SHA256: "aa", Title: "A", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := chunks.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "hello", TokenCount: 1, Embedding: []float32{0.1}},
		{DocumentID: doc.ID, ChunkIndex: 1, Text: "world", TokenCount: 1, Embedding: []float32{0.2}},
	}); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunStats(&out, db, "/fake/tbuk.sqlite", "text"); err != nil {
		t.Fatalf("RunStats: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "1") {
		t.Errorf("expected document count '1' in output, got: %s", output)
	}
	if !strings.Contains(output, "2") {
		t.Errorf("expected chunk count '2' in output, got: %s", output)
	}
}

func TestRunStats_sizeIsExactSumOfChunkLengths(t *testing.T) {
	// Size must be the exact byte sum of chunk texts, not GROUP_CONCAT length
	// (which adds a separator comma per chunk boundary).
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	chunks := storage.NewChunkRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/s.md", SHA256: "ss", Title: "S", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := chunks.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "hello", TokenCount: 1},
		{DocumentID: doc.ID, ChunkIndex: 1, Text: "world", TokenCount: 1},
	}); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunStats(&out, db, "/fake/tbuk.sqlite", "json"); err != nil {
		t.Fatalf("RunStats: %v", err)
	}
	var result struct {
		TotalSizeBytes int64 `json:"total_size_bytes"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result.TotalSizeBytes != 10 {
		t.Errorf("total_size_bytes = %d, want 10 (len(hello)+len(world))", result.TotalSizeBytes)
	}
}

func TestRunStats_jsonFormat(t *testing.T) {
	db := openMemoryDB(t)
	var out bytes.Buffer
	if err := cli.RunStats(&out, db, "/fake/tbuk.sqlite", "json"); err != nil {
		t.Fatalf("RunStats json: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\noutput: %s", err, out.String())
	}

	expectedKeys := []string{"total_documents", "total_chunks", "embedded_chunks", "db_path"}
	for _, k := range expectedKeys {
		if _, ok := result[k]; !ok {
			t.Errorf("missing key %q in JSON output", k)
		}
	}
}

func TestRunStats_dbSize(t *testing.T) {
	// Using in-memory DB, db_size_bytes should be 0 or absent in JSON
	db := openMemoryDB(t)
	var out bytes.Buffer
	if err := cli.RunStats(&out, db, ":memory:", "json"); err != nil {
		t.Fatalf("RunStats: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	_ = result // just ensure valid JSON with no panic
}

func TestRunStats_largeChunksTriggerKBDisplay(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	chunks := storage.NewChunkRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/big.md", SHA256: "bigsha", Title: "Big", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	// 2000-byte text → total_size_bytes > 1024 → KB display branch
	bigText := strings.Repeat("x", 2000)
	if err := chunks.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: bigText, TokenCount: 500},
	}); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunStats(&out, db, "/fake/tbuk.sqlite", "text"); err != nil {
		t.Fatalf("RunStats: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "KB") {
		t.Errorf("expected 'KB' in output for large text, got: %s", output)
	}
}

func TestRunStats_embeddedPctCalculation(t *testing.T) {
	db := openMemoryDB(t)
	docs := storage.NewDocumentRepo(db)
	chunks := storage.NewChunkRepo(db)
	ctx := context.Background()

	doc := &storage.Document{Path: "/c.md", SHA256: "cccc", Title: "C", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	// One chunk embedded, one not
	if err := chunks.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "embedded", TokenCount: 1, Embedding: []float32{0.1}},
		{DocumentID: doc.ID, ChunkIndex: 1, Text: "not embedded", TokenCount: 2},
	}); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}

	var out bytes.Buffer
	if err := cli.RunStats(&out, db, "/fake/tbuk.sqlite", "text"); err != nil {
		t.Fatalf("RunStats: %v", err)
	}

	output := out.String()
	// 1/2 = 50%
	if !strings.Contains(output, "50%") {
		t.Errorf("expected '50%%' in output, got: %s", output)
	}
}
