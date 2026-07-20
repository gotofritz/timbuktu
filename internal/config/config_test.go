package config_test

import (
	"os"
	"path/filepath"
	"strings"
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
	if cfg.LLM.Provider != "llama" {
		t.Errorf("llm.provider: want llama, got %s", cfg.LLM.Provider)
	}
	if cfg.Embedding.Provider != "llama" {
		t.Errorf("embedding.provider: want llama, got %s", cfg.Embedding.Provider)
	}
	if cfg.Embedding.Dimension != 768 {
		t.Errorf("embedding.dimension: want 768, got %d", cfg.Embedding.Dimension)
	}
	// base_url must be empty in defaults so each provider factory resolves its
	// own default — otherwise switching provider silently keeps localhost:8080.
	if cfg.LLM.BaseURL != "" {
		t.Errorf("llm.base_url: want empty (provider resolves), got %q", cfg.LLM.BaseURL)
	}
	if cfg.Embedding.BaseURL != "" {
		t.Errorf("embedding.base_url: want empty (provider resolves), got %q", cfg.Embedding.BaseURL)
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
	if cfg.LLM.Provider != "llama" {
		t.Errorf("llm.provider should keep default, got %s", cfg.LLM.Provider)
	}
}

// A typo'd or unknown config key must fail loudly rather than being silently
// dropped so the default quietly wins (P1-18).
func TestLoad_unknownKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// chunk_size is a typo for chunking.size — must not be silently ignored.
	if err := os.WriteFile(path, []byte("chunking:\n  chunk_size: 512\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown config key, got nil")
	}
	if !strings.Contains(err.Error(), "chunk_size") {
		t.Errorf("error should name the offending key, got: %v", err)
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

func TestConfig_Validate(t *testing.T) {
	base := config.Defaults()

	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string // substring; "" means expect no error
	}{
		{"defaults valid", func(*config.Config) {}, ""},
		{"zero chunk size", func(c *config.Config) { c.Chunking.Size = 0 }, "size"},
		{"negative chunk size", func(c *config.Config) { c.Chunking.Size = -1 }, "size"},
		{"negative overlap", func(c *config.Config) { c.Chunking.Overlap = -5 }, "overlap"},
		{"overlap equals size", func(c *config.Config) { c.Chunking.Size = 200; c.Chunking.Overlap = 200 }, "overlap"},
		{"overlap exceeds size", func(c *config.Config) { c.Chunking.Size = 100; c.Chunking.Overlap = 150 }, "overlap"},
		{"zero max_tokens", func(c *config.Config) { c.LLM.MaxTokens = 0 }, "max_tokens"},
		{"zero dimension", func(c *config.Config) { c.Embedding.Dimension = 0 }, "dimension"},
		{"unknown llm provider", func(c *config.Config) { c.LLM.Provider = "gpt5" }, "llm provider"},
		{"unknown embedding provider", func(c *config.Config) { c.Embedding.Provider = "word2vec" }, "embedding provider"},
		{"claude not valid embedder", func(c *config.Config) { c.Embedding.Provider = "claude" }, "embedding provider"},
		{"empty db path", func(c *config.Config) { c.Database.Path = "" }, "database"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("Validate() = nil, want error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("Validate() = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateKeyedBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https remote ok", "https://api.openai.com", false},
		{"https with port ok", "https://example.com:8443", false},
		{"http loopback localhost ok", "http://localhost:8080", false},
		{"http loopback 127.0.0.1 ok", "http://127.0.0.1:1234", false},
		{"http loopback ipv6 ok", "http://[::1]:8080", false},
		{"http remote host rejected", "http://api.example.com", true},
		{"http remote ip rejected", "http://10.0.0.5:8080", true},
		{"empty rejected", "", true},
		{"malformed rejected", "://nope", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateKeyedBaseURL(tc.url)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateKeyedBaseURL(%q) = nil, want error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateKeyedBaseURL(%q) = %v, want nil", tc.url, err)
			}
		})
	}
}
