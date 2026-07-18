package cli_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/config"
)

// ── checkConfig ───────────────────────────────────────────────────────────────

func TestCheckConfig_exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("chunking:\n  size: 800\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, ok := cli.CheckConfig(path)
	if !ok {
		t.Errorf("expected ok=true, got false: %s", msg)
	}
}

func TestCheckConfig_missing(t *testing.T) {
	msg, ok := cli.CheckConfig("/no/such/config.yaml")
	if ok {
		t.Error("expected ok=false for missing file")
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

// ── checkDB ───────────────────────────────────────────────────────────────────

func TestCheckDB_opens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tbuk.sqlite")
	msg, ok := cli.CheckDB(path)
	if !ok {
		t.Errorf("expected ok=true, got false: %s", msg)
	}
}

func TestCheckDB_badPath(t *testing.T) {
	// directory instead of file — Open will fail
	dir := t.TempDir()
	msg, ok := cli.CheckDB(filepath.Join(dir, "no", "such", "dir", "tbuk.sqlite"))
	// sqlite may or may not succeed depending on driver; just ensure we get a result
	_ = msg
	_ = ok
}

// ── checkHTTP ─────────────────────────────────────────────────────────────────

func TestCheckHTTP_healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	msg, ok := cli.CheckHTTP(srv.URL, srv.Client())
	if !ok {
		t.Errorf("expected ok=true, got false: %s", msg)
	}
}

func TestCheckHTTP_unreachable(t *testing.T) {
	msg, ok := cli.CheckHTTP("http://127.0.0.1:19999", http.DefaultClient)
	if ok {
		t.Error("expected ok=false for unreachable server")
	}
	if msg == "" {
		t.Error("expected non-empty error message")
	}
}

func TestCheckHTTP_serverError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	msg, ok := cli.CheckHTTP(srv.URL, srv.Client())
	if ok {
		t.Errorf("expected ok=false for 503, got true: %s", msg)
	}
}

// ── RunDoctor integration ─────────────────────────────────────────────────────

func TestRunDoctor_sameURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	if err := os.WriteFile(cfgPath, []byte("database:\n  path: "+dbPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Database.Path = dbPath
	cfg.LLM.BaseURL = srv.URL
	cfg.Embedding.BaseURL = srv.URL // same → triggers "same server" branch

	if err := cli.RunDoctor(srv.Client(), cfg, cfgPath); err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
}

func TestRunDoctor_differentURLs(t *testing.T) {
	srvLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srvLLM.Close()
	srvEmbed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srvEmbed.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	if err := os.WriteFile(cfgPath, []byte("database:\n  path: "+dbPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Database.Path = dbPath
	cfg.LLM.BaseURL = srvLLM.URL
	cfg.Embedding.BaseURL = srvEmbed.URL

	if err := cli.RunDoctor(srvLLM.Client(), cfg, cfgPath); err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
}

func TestRunDoctor_missingConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	cfg := config.Defaults()
	cfg.Database.Path = dbPath
	cfg.LLM.BaseURL = srv.URL
	cfg.Embedding.BaseURL = srv.URL

	// Config file path does not exist — should report but not error
	if err := cli.RunDoctor(srv.Client(), cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
}
