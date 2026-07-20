package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
)

func appTestConfig(dbPath string) config.Config {
	return config.Config{
		Database:   config.DatabaseConfig{Path: dbPath},
		LLM:        config.LLMConfig{Provider: "llama", MaxTokens: 256},
		Embedding:  config.EmbeddingConfig{Provider: "llama", Dimension: 8},
		Chunking:   config.ChunkingConfig{Size: 100, Overlap: 10},
		Preprocess: config.PreprocessConfig{OutputDir: "out"},
	}
}

func TestOpenApp_opensAndCloses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	app, err := openApp(appTestConfig(dbPath))
	if err != nil {
		t.Fatalf("openApp: %v", err)
	}
	if app.DB() == nil {
		t.Fatal("DB() = nil")
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenApp_badPath_errors(t *testing.T) {
	// Parent directory does not exist, so sqlite cannot create the file.
	dbPath := filepath.Join(t.TempDir(), "missing-dir", "tbuk.sqlite")
	_, err := openApp(appTestConfig(dbPath))
	if err == nil {
		t.Fatal("openApp with unopenable path: want error, got nil")
	}
	if !strings.Contains(err.Error(), "open database") {
		t.Fatalf("error %q missing %q", err, "open database")
	}
}

func TestApp_Docs_memoized(t *testing.T) {
	app := mustOpenApp(t)
	first := app.Docs()
	if first == nil {
		t.Fatal("Docs() = nil")
	}
	if app.Docs() != first {
		t.Fatal("Docs() not memoized: returned different pointers")
	}
}

func TestApp_Embedder_memoizedAndReused(t *testing.T) {
	app := mustOpenApp(t)
	emb, err := app.Embedder()
	if err != nil {
		t.Fatalf("Embedder: %v", err)
	}
	if emb == nil {
		t.Fatal("Embedder() = nil")
	}
	emb2, err := app.Embedder()
	if err != nil {
		t.Fatalf("Embedder (2nd): %v", err)
	}
	if emb != emb2 {
		t.Fatal("Embedder() not memoized: returned different values")
	}
}

func TestApp_Embedder_badProvider_errors(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	cfg := appTestConfig(dbPath)
	cfg.Embedding.Provider = "nope"
	app, err := openApp(cfg)
	if err != nil {
		t.Fatalf("openApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if _, err := app.Embedder(); err == nil {
		t.Fatal("Embedder() with bad provider: want error, got nil")
	}
}

func TestApp_Ingester(t *testing.T) {
	app := mustOpenApp(t)
	ing, err := app.Ingester()
	if err != nil {
		t.Fatalf("Ingester: %v", err)
	}
	if ing == nil {
		t.Fatal("Ingester() = nil")
	}
}

func TestApp_Ingester_badEmbedder_errors(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	cfg := appTestConfig(dbPath)
	cfg.Embedding.Provider = "nope"
	app, err := openApp(cfg)
	if err != nil {
		t.Fatalf("openApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if _, err := app.Ingester(); err == nil {
		t.Fatal("Ingester() with bad embedder: want error, got nil")
	}
}

func TestApp_LLM(t *testing.T) {
	app := mustOpenApp(t)
	l, err := app.LLM()
	if err != nil {
		t.Fatalf("LLM: %v", err)
	}
	if l == nil {
		t.Fatal("LLM() = nil")
	}
}

func TestApp_LLM_badProvider_errors(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	cfg := appTestConfig(dbPath)
	cfg.LLM.Provider = "nope"
	app, err := openApp(cfg)
	if err != nil {
		t.Fatalf("openApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if _, err := app.LLM(); err == nil {
		t.Fatal("LLM() with bad provider: want error, got nil")
	}
}

func mustOpenApp(t *testing.T) *App {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	app, err := openApp(appTestConfig(dbPath))
	if err != nil {
		t.Fatalf("openApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}
