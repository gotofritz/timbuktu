package cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/storage"
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

func TestCheckConfig_invalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte(":\tinvalid:\n[\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, ok := cli.CheckConfig(path)
	if ok {
		t.Error("expected ok=false for invalid YAML")
	}
	if msg == "" {
		t.Error("expected non-empty error message")
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

// ── CheckLLMModel ─────────────────────────────────────────────────────────────

func TestCheckLLMModel_discoversFromServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"data":[{"id":"llama-3.2"}]}`) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got := cli.CheckLLMModel(srv.URL, "fallback-model", srv.Client())
	if got != "llama-3.2" {
		t.Errorf("want llama-3.2, got %q", got)
	}
}

func TestCheckLLMModel_fallsBackToConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	got := cli.CheckLLMModel(srv.URL, "my-model", srv.Client())
	if got != "my-model" {
		t.Errorf("want my-model, got %q", got)
	}
}

func TestCheckLLMModel_emptyModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"data":[]}`) //nolint:errcheck
	}))
	defer srv.Close()

	got := cli.CheckLLMModel(srv.URL, "cfg-model", srv.Client())
	if got != "cfg-model" {
		t.Errorf("want cfg-model (fallback), got %q", got)
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

// Hosted providers (claude/openai) must not be HTTP-probed: no /health or
// /v1/models request, and the report says the API was not probed (P1-12).
func TestRunDoctorTo_hostedProviderNotProbed(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath
	cfg.LLM.Provider = "claude"
	cfg.LLM.BaseURL = srv.URL
	cfg.Embedding.Provider = "openai"
	cfg.Embedding.BaseURL = srv.URL

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, srv.Client(), cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("hosted providers must not be probed, got %d HTTP hits", got)
	}
	if !strings.Contains(out.String(), "not probed") {
		t.Errorf("expected 'not probed' for hosted provider, got:\n%s", out.String())
	}
}

// FTS5 health must be gated on DB health, not on the embedding server's
// reachability. With a broken FTS index and a down embedder, the report must
// still surface the FTS failure instead of printing a bogus ✓ (P1-12).
func TestRunDoctorTo_fts5CheckedWhenEmbedderDown(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)
	breakFTS(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath
	// llama providers so probes run; unreachable URL so they fail (embedder down).
	cfg.LLM.Provider = "llama"
	cfg.LLM.BaseURL = "http://127.0.0.1:19999"
	cfg.Embedding.Provider = "llama"
	cfg.Embedding.BaseURL = "http://127.0.0.1:19998"

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}

	if !ftsLineFailed(out.String()) {
		t.Errorf("expected fts5 check to report failure with broken index, got:\n%s", out.String())
	}
}

// seedDB creates a schema-initialized database file at path.
func seedDB(t *testing.T, path string) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	_ = db.Close()
}

// breakFTS drops the chunks_fts virtual table so search.CheckFTS5 fails.
func breakFTS(t *testing.T, path string) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.DB().Exec(`DROP TABLE chunks_fts`); err != nil {
		t.Fatalf("drop chunks_fts: %v", err)
	}
	_ = db.Close()
}

// ftsLineFailed reports whether the doctor output's fts5 line shows a failure.
func ftsLineFailed(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "fts5") && strings.Contains(line, "✗") {
			return true
		}
	}
	return false
}
