package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
)

func TestDefaults(t *testing.T) {
	cfg := config.Defaults()

	if cfg.Chunking.Size != 800 {
		t.Errorf("chunking size: want 800, got %d", cfg.Chunking.Size)
	}
	if cfg.Chunking.Overlap != 100 {
		t.Errorf("chunking overlap: want 100, got %d", cfg.Chunking.Overlap)
	}
	if cfg.LLM.Provider != "ollama" {
		t.Errorf("llm.provider: want ollama, got %s", cfg.LLM.Provider)
	}
	if cfg.Embedding.Provider != "ollama" {
		t.Errorf("embedding.provider: want ollama, got %s", cfg.Embedding.Provider)
	}
	if cfg.Embedding.Dimension != 768 {
		t.Errorf("embedding.dimension: want 768, got %d", cfg.Embedding.Dimension)
	}
}

func TestLoad_missingFile(t *testing.T) {
	cfg, err := config.Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg.Chunking.Size != 800 {
		t.Errorf("want default size 800, got %d", cfg.Chunking.Size)
	}
}

func TestLoad_partialYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	err := os.WriteFile(path, []byte("chunking:\n  size: 512\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Chunking.Size != 512 {
		t.Errorf("want 512, got %d", cfg.Chunking.Size)
	}
	// unset fields should keep defaults
	if cfg.Chunking.Overlap != 100 {
		t.Errorf("overlap should keep default 100, got %d", cfg.Chunking.Overlap)
	}
	if cfg.LLM.Provider != "ollama" {
		t.Errorf("llm.provider should keep default, got %s", cfg.LLM.Provider)
	}
}

func TestLoad_directoryPath(t *testing.T) {
	dir := t.TempDir()
	_, err := config.Load(dir) // passing a directory, not a file
	if err == nil {
		t.Fatal("expected error when path is a directory")
	}
}

func TestLoad_badYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	err := os.WriteFile(path, []byte("not: valid: yaml: ["), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	_, err = config.Load(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestDefaultPath_containsTbuk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := config.DefaultPath()
	if filepath.Base(path) != "config.yaml" {
		t.Errorf("want config.yaml basename, got %s", filepath.Base(path))
	}
	if filepath.Dir(filepath.Dir(path)) != home {
		t.Errorf("want path inside HOME/.tbuk/, got %s", path)
	}
}

func TestDefaultYAML_isValidYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(config.DefaultYAML()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("DefaultYAML not valid YAML: %v", err)
	}
	if cfg.Chunking.Size != 800 {
		t.Errorf("want default size 800, got %d", cfg.Chunking.Size)
	}
}

func TestLoad_fullYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `database:
  path: /tmp/test.sqlite
llm:
  provider: claude
  model: claude-haiku-4-5
  max_tokens: 2048
embedding:
  provider: openai
  model: text-embedding-3-small
  dimension: 1536
chunking:
  size: 600
  overlap: 80
`
	err := os.WriteFile(path, []byte(content), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Database.Path != "/tmp/test.sqlite" {
		t.Errorf("db path: want /tmp/test.sqlite, got %s", cfg.Database.Path)
	}
	if cfg.LLM.Provider != "claude" {
		t.Errorf("llm.provider: want claude, got %s", cfg.LLM.Provider)
	}
	if cfg.LLM.MaxTokens != 2048 {
		t.Errorf("llm.max_tokens: want 2048, got %d", cfg.LLM.MaxTokens)
	}
	if cfg.Embedding.Dimension != 1536 {
		t.Errorf("embedding.dimension: want 1536, got %d", cfg.Embedding.Dimension)
	}
	if cfg.Chunking.Size != 600 {
		t.Errorf("chunking.size: want 600, got %d", cfg.Chunking.Size)
	}
}
