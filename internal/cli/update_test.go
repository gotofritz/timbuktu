package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

type stubExtractor struct{ text string }

func (s *stubExtractor) ExtractFile(_ context.Context, _ string) (string, error) {
	return s.text, nil
}

type stubEmbedder struct{}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2}
	}
	return out, nil
}

func (s *stubEmbedder) Dimension() int { return 2 }

func TestUpdateCommand_missingArg(t *testing.T) {
	err := runCLI("update")
	if err == nil {
		t.Fatal("expected error for missing path argument")
	}
}

func TestUpdateCommand_nonExistentFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := home + "/.tbuk/config.yaml"
	err := runCLI("--config", cfgPath, "update", "/no/such/file.md")
	if err == nil {
		t.Error("expected error updating non-existent file")
	}
}

func TestRunUpdate_unchanged(t *testing.T) {
	sqlDB := openMemoryDB(t)
	docs := storage.NewDocumentRepo(sqlDB)
	chunks := storage.NewChunkRepo(sqlDB)
	meta := storage.NewMetadataRepo(sqlDB)

	// Write a real temp file so SHA256 can be computed
	dir := t.TempDir()
	filePath := filepath.Join(dir, "doc.md")
	content := "hello world"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Pre-ingest so SHA256 is already recorded
	ing := ingest.NewIngester(
		docs, chunks, meta,
		&stubExtractor{text: content},
		&chunking.Chunker{Size: 100, Overlap: 10},
		&stubEmbedder{},
		t.TempDir(),
	)
	ctx := context.Background()
	res := ing.IngestFile(ctx, filePath, ingest.Options{})
	if res.Err != nil {
		t.Fatalf("pre-ingest: %v", res.Err)
	}

	var out bytes.Buffer
	err := cli.RunUpdate(ctx, &out, ing, docs, filePath, false)
	if err != nil {
		t.Fatalf("RunUpdate: %v", err)
	}

	if !strings.Contains(out.String(), "Skipped") {
		t.Errorf("expected 'Skipped' in output, got: %s", out.String())
	}
}

func TestRunUpdate_changed(t *testing.T) {
	sqlDB := openMemoryDB(t)
	docs := storage.NewDocumentRepo(sqlDB)
	chunks := storage.NewChunkRepo(sqlDB)
	meta := storage.NewMetadataRepo(sqlDB)
	extractDir := t.TempDir()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(filePath, []byte("version one content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ing := ingest.NewIngester(
		docs, chunks, meta,
		&stubExtractor{text: "version one content"},
		&chunking.Chunker{Size: 100, Overlap: 10},
		&stubEmbedder{},
		extractDir,
	)
	ctx := context.Background()

	res := ing.IngestFile(ctx, filePath, ingest.Options{})
	if res.Err != nil {
		t.Fatalf("pre-ingest v1: %v", res.Err)
	}

	// Change file content so SHA256 differs
	if err := os.WriteFile(filePath, []byte("version two content, longer for new chunks"), 0o644); err != nil {
		t.Fatalf("write file v2: %v", err)
	}

	var out bytes.Buffer
	err := cli.RunUpdate(ctx, &out, ing, docs, filePath, false)
	if err != nil {
		t.Fatalf("RunUpdate: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Updated") {
		t.Errorf("expected 'Updated' in output, got: %s", output)
	}
}

func TestRunUpdate_force(t *testing.T) {
	sqlDB := openMemoryDB(t)
	docs := storage.NewDocumentRepo(sqlDB)
	chunks := storage.NewChunkRepo(sqlDB)
	meta := storage.NewMetadataRepo(sqlDB)

	dir := t.TempDir()
	filePath := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(filePath, []byte("unchanged content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ing := ingest.NewIngester(
		docs, chunks, meta,
		&stubExtractor{text: "unchanged content"},
		&chunking.Chunker{Size: 100, Overlap: 10},
		&stubEmbedder{},
		t.TempDir(),
	)
	ctx := context.Background()

	res := ing.IngestFile(ctx, filePath, ingest.Options{})
	if res.Err != nil {
		t.Fatalf("pre-ingest: %v", res.Err)
	}

	var out bytes.Buffer
	err := cli.RunUpdate(ctx, &out, ing, docs, filePath, true)
	if err != nil {
		t.Fatalf("RunUpdate force: %v", err)
	}

	if !strings.Contains(out.String(), "Updated") {
		t.Errorf("expected 'Updated' in output with --force, got: %s", out.String())
	}
}
