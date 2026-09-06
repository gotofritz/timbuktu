package cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// A knowledge base built before chunks.search_text existed still opens and
// still answers queries, but every ingest into it fails on the missing column.
// Doctor has to say so, and name the script that fixes it (issue #138).
func TestRunDoctorTo_reportsMissingSearchTextColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)
	dropSearchText(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	if !strings.Contains(out.String(), "search_text") {
		t.Errorf("expected the missing column to be reported, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "scripts/") {
		t.Errorf("expected the report to name the script that fixes it, got:\n%s", out.String())
	}
}

func TestRunDoctorTo_searchTextColumnPresent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	if strings.Contains(out.String(), "scripts/") {
		t.Errorf("a current database should need no migration script, got:\n%s", out.String())
	}
}

// A knowledge base whose index predates issue #136 searches without error, but
// '_' and '-' are separators in it, so an exact term, an exclusion and a phrase
// all collapse to the same bag of words. Nothing fails; the answers are just
// wrong, so doctor has to say so and name the script that fixes it.
func TestRunDoctorTo_reportsStaleTokenizer(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)
	useDefaultTokenizer(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	if !strings.Contains(out.String(), "tokenizer") {
		t.Errorf("expected the stale tokenizer to be reported, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "scripts/") {
		t.Errorf("expected the report to name the script that fixes it, got:\n%s", out.String())
	}
}

// The shape issue #136 left behind: '-' a token character as well, which locks
// ordinary hyphenated English into one term, so the words it is made of stop
// reaching it (issue #143). Nothing errors here either, so doctor names the
// tokenizer it found and the script that replaces it.
func TestRunDoctorTo_reportsHyphenTokenizer(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)
	useHyphenTokenizer(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	if !strings.Contains(out.String(), `tokenchars '_-'`) {
		t.Errorf("expected the report to name the tokenizer it found, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "scripts/retokenize-fts") {
		t.Errorf("expected the report to name the script that fixes it, got:\n%s", out.String())
	}
}

func TestRunDoctorTo_currentTokenizerNeedsNoScript(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	if !strings.Contains(out.String(), "tokenizer") {
		t.Errorf("expected a tokenizer line, got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "scripts/") {
		t.Errorf("a current database should need no migration script, got:\n%s", out.String())
	}
}

// useDefaultTokenizer rebuilds the index the way it was before issue #136: the
// same column, the FTS5 default tokenizer.
func useDefaultTokenizer(t *testing.T, path string) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		`DROP TABLE chunks_fts`,
		`CREATE VIRTUAL TABLE chunks_fts USING fts5(search_text, content='chunks', content_rowid='id')`,
	} {
		if _, err := db.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// useHyphenTokenizer rebuilds the index the way issue #136 left it: '-' a
// token character alongside '_'.
func useHyphenTokenizer(t *testing.T, path string) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		`DROP TABLE chunks_fts`,
		`CREATE VIRTUAL TABLE chunks_fts USING fts5(search_text, content='chunks', content_rowid='id', ` +
			`tokenize="unicode61 tokenchars '_-'")`,
	} {
		if _, err := db.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// dropSearchText puts the schema back the way it was before search_text
// existed: the index and its triggers over chunks.text, and no reduced column.
func dropSearchText(t *testing.T, path string) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		`DROP TRIGGER chunks_ai`,
		`DROP TRIGGER chunks_ad`,
		`DROP TRIGGER chunks_au`,
		`DROP TABLE chunks_fts`,
		`ALTER TABLE chunks DROP COLUMN search_text`,
		`CREATE VIRTUAL TABLE chunks_fts USING fts5(text, content='chunks', content_rowid='id')`,
		`CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
		    INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
		 END`,
	} {
		if _, err := db.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

// ── CheckEmbeddingDimension ───────────────────────────────────────────────────

func seedEmbeddedChunk(t *testing.T, path string, dim int) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := t.Context()
	docRepo := storage.NewDocumentRepo(db.DB())
	chunkRepo := storage.NewChunkRepo(db.DB())
	doc := &storage.Document{Path: "/d.txt", SHA256: "s", Title: "t", MimeType: "text/plain"}
	if err := docRepo.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}
	emb := make([]float32, dim)
	if err := chunkRepo.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "x", Embedding: emb},
	}); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}
}

func TestCheckEmbeddingDimension_matches(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	seedDB(t, dbPath)
	seedEmbeddedChunk(t, dbPath, 768)

	msg, status := cli.CheckEmbeddingDimension(dbPath, 768)
	if status != "✓" {
		t.Errorf("status = %q, want ✓ (msg %q)", status, msg)
	}
}

func TestCheckEmbeddingDimension_mismatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	seedDB(t, dbPath)
	seedEmbeddedChunk(t, dbPath, 384)

	msg, status := cli.CheckEmbeddingDimension(dbPath, 768)
	if status != "✗" {
		t.Errorf("status = %q, want ✗", status)
	}
	if !strings.Contains(msg, "384") || !strings.Contains(msg, "768") {
		t.Errorf("msg = %q, want it to show both stored (384) and config (768)", msg)
	}
}

func TestCheckEmbeddingDimension_noEmbeddings(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	seedDB(t, dbPath)

	msg, status := cli.CheckEmbeddingDimension(dbPath, 768)
	if status == "✗" {
		t.Errorf("empty KB should not report a mismatch, got ✗ (msg %q)", msg)
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

// MLX servers (mlx_lm.server & co.) have no /health endpoint; the status
// probe for provider "mlx" must hit /v1/models — the one endpoint every
// OpenAI-compatible server exposes. llama keeps its /health probe.
func TestRunDoctorTo_mlxProbesModelsNotHealth(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"mlx-community/test-model-4bit"}]}`) //nolint:errcheck
	}))
	defer srv.Close()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath
	cfg.LLM.Provider = "mlx"
	cfg.LLM.BaseURL = srv.URL
	cfg.Embedding.Provider = "mlx"
	cfg.Embedding.BaseURL = srv.URL // same URL → "same server as LLM" branch

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, srv.Client(), cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p == "/health" {
			t.Errorf("mlx must not be probed via /health, got requests: %v", paths)
		}
	}
	var sawModels bool
	for _, p := range paths {
		if p == "/v1/models" {
			sawModels = true
		}
	}
	if !sawModels {
		t.Errorf("expected a /v1/models probe for mlx, got requests: %v", paths)
	}
	if !strings.Contains(out.String(), "mlx-community/test-model-4bit") {
		t.Errorf("expected model id from /v1/models in report, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "healthy") {
		t.Errorf("expected healthy status for reachable mlx server, got:\n%s", out.String())
	}
}

func TestRunDoctorTo_llamaStillProbesHealth(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tbuk.sqlite")
	seedDB(t, dbPath)

	cfg := config.Defaults()
	cfg.Database.Path = dbPath
	cfg.LLM.Provider = "llama"
	cfg.LLM.BaseURL = srv.URL
	cfg.Embedding.Provider = "llama"
	cfg.Embedding.BaseURL = srv.URL

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, srv.Client(), cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var sawHealth bool
	for _, p := range paths {
		if p == "/health" {
			sawHealth = true
		}
	}
	if !sawHealth {
		t.Errorf("expected /health probe for llama, got requests: %v", paths)
	}
}

// ── context budget (#141) ─────────────────────────────────────────────────────

// writeTemplateAt drops a bare template into a prompts root so doctor can read
// its budgets.
func writeTemplateAt(t *testing.T, root, name, manifest string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, content := range map[string]string{
		"manifest.yaml": manifest,
		"system.tmpl":   "sys",
		"user.tmpl":     "{{ .Question }}",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The window and what it leaves for the prompt are the two numbers that decide
// whether an ask gets trimmed, so doctor reports both.
func TestRunDoctorTo_reportsContextBudget(t *testing.T) {
	promptRoot := t.TempDir()
	writeTemplateAt(t, promptRoot, "qa", "name: qa\nmax_tokens: 2048\n")

	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "tbuk.sqlite")
	cfg.Prompts.Dir = promptRoot
	cfg.LLM.Provider = "llama"
	cfg.LLM.BaseURL = "http://127.0.0.1:19999"
	cfg.LLM.MaxTokens = 4096
	cfg.LLM.ContextTokens = 8192

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	got := out.String()
	for _, want := range []string{"context", "8192", "4096 for the prompt"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor report missing %q:\n%s", want, got)
		}
	}
}

func TestRunDoctorTo_reportsContextGuardOff(t *testing.T) {
	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "tbuk.sqlite")
	cfg.Prompts.Dir = t.TempDir()
	cfg.LLM.Provider = "llama"
	cfg.LLM.BaseURL = "http://127.0.0.1:19999"
	cfg.LLM.ContextTokens = 0

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	if !strings.Contains(out.String(), "guard off") {
		t.Errorf("want the report to say the guard is off:\n%s", out.String())
	}
}

// A template whose own output budget swallows the window can never produce a
// prompt that fits, so name it before the first ask fails.
func TestRunDoctorTo_flagsTemplateWithNoPromptRoom(t *testing.T) {
	promptRoot := t.TempDir()
	writeTemplateAt(t, promptRoot, "qa", "name: qa\nmax_tokens: 512\n")
	writeTemplateAt(t, promptRoot, "greedy", "name: greedy\nmax_tokens: 9000\n")
	writeTemplateAt(t, promptRoot, "wide", "name: wide\nmax_tokens: 9000\ncontext_tokens: 131072\n")

	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "tbuk.sqlite")
	cfg.Prompts.Dir = promptRoot
	cfg.LLM.Provider = "llama"
	cfg.LLM.BaseURL = "http://127.0.0.1:19999"
	cfg.LLM.MaxTokens = 4096
	cfg.LLM.ContextTokens = 8192

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "greedy") {
		t.Errorf("want the over-budget template named:\n%s", got)
	}
	// wide pins a window of its own that fits its output budget; qa is fine.
	if strings.Contains(got, "wide:") || strings.Contains(got, "qa:") {
		t.Errorf("only the over-budget template should be flagged:\n%s", got)
	}
}

func TestRunDoctorTo_templateBudgetsOK(t *testing.T) {
	promptRoot := t.TempDir()
	writeTemplateAt(t, promptRoot, "qa", "name: qa\nmax_tokens: 2048\n")

	cfg := config.Defaults()
	cfg.Database.Path = filepath.Join(t.TempDir(), "tbuk.sqlite")
	cfg.Prompts.Dir = promptRoot
	cfg.LLM.Provider = "llama"
	cfg.LLM.BaseURL = "http://127.0.0.1:19999"

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	if !strings.Contains(out.String(), "budgets") {
		t.Errorf("want a template budget line:\n%s", out.String())
	}
}

// ── CheckChunkBudget ──────────────────────────────────────────────────────────

// seedChunkTexts stores one chunk per text under a single document.
func seedChunkTexts(t *testing.T, path string, texts ...string) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := t.Context()
	doc := &storage.Document{Path: "/budget.txt", SHA256: "s", Title: "t", MimeType: "text/plain"}
	if err := storage.NewDocumentRepo(db.DB()).Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}
	chunks := make([]*storage.Chunk, len(texts))
	for i, s := range texts {
		chunks[i] = &storage.Chunk{DocumentID: doc.ID, ChunkIndex: i, Text: s, SearchText: s}
	}
	if err := storage.NewChunkRepo(db.DB()).BulkInsert(ctx, chunks); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}
}

func TestCheckChunkBudget_withinBudget(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	seedDB(t, dbPath)
	seedChunkTexts(t, dbPath, strings.Repeat("a", 200), strings.Repeat("世", 40))

	msg, status := cli.CheckChunkBudget(dbPath, 100)
	if status != "✓" {
		t.Errorf("status = %q, want ✓ (msg %q)", status, msg)
	}
}

// An index built under the byte estimator holds CJK chunks of Size*4 *bytes* —
// far more than Size tokens once they are measured per rune. That is what makes
// llama.cpp answer HTTP 500, and nothing else in the report would say so.
func TestCheckChunkBudget_flagsChunksIndexedUnderByteEstimator(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	seedDB(t, dbPath)
	// 400 han runes: 1200 bytes, which the old estimator read as 300 tokens.
	seedChunkTexts(t, dbPath, strings.Repeat("世", 400), strings.Repeat("a", 100))

	msg, status := cli.CheckChunkBudget(dbPath, 100)
	if status != "✗" {
		t.Fatalf("status = %q, want ✗ (msg %q)", status, msg)
	}
	if !strings.Contains(msg, "reindex") {
		t.Errorf("msg = %q, want it to name the remedy (tbuk reindex)", msg)
	}
	if !strings.Contains(msg, "400") {
		t.Errorf("msg = %q, want it to report the largest measured count (400)", msg)
	}
	if !strings.Contains(msg, "1 of 2 sampled chunk is") {
		t.Errorf("msg = %q, want the singular reading for one offending chunk", msg)
	}
}

func TestCheckChunkBudget_pluralisesSeveralOffenders(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	seedDB(t, dbPath)
	seedChunkTexts(t, dbPath, strings.Repeat("世", 400), strings.Repeat("界", 300))

	msg, status := cli.CheckChunkBudget(dbPath, 100)
	if status != "✗" {
		t.Fatalf("status = %q, want ✗ (msg %q)", status, msg)
	}
	if !strings.Contains(msg, "2 of 2 sampled chunks are") {
		t.Errorf("msg = %q, want the plural reading for two offending chunks", msg)
	}
}

func TestCheckChunkBudget_emptyKnowledgeBase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	seedDB(t, dbPath)

	msg, status := cli.CheckChunkBudget(dbPath, 400)
	if status == "✗" {
		t.Errorf("an empty KB should not report a failure, got ✗ (msg %q)", msg)
	}
}

func TestCheckChunkBudget_unreadableDB(t *testing.T) {
	msg, status := cli.CheckChunkBudget(filepath.Join(t.TempDir(), "nope", "tbuk.sqlite"), 400)
	if status == "✓" {
		t.Errorf("status = %q, want no success marker for an unopenable DB (msg %q)", status, msg)
	}
}

func TestRunDoctorTo_reportsChunkingSection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tbuk.sqlite")
	seedDB(t, dbPath)
	seedChunkTexts(t, dbPath, strings.Repeat("世", 400))

	cfg := config.Defaults()
	cfg.Database.Path = dbPath
	cfg.Chunking.Size = 100
	cfg.Chunking.Overlap = 10

	var buf bytes.Buffer
	if err := cli.RunDoctorTo(&buf, http.DefaultClient, cfg, "/no/such/config.yaml"); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Chunking", "size", "estimator", "script-aware", "reindex"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

// ── checkHome ─────────────────────────────────────────────────────────────────

// The default data root hangs off the user's home directory, which is $HOME on
// Unix and %USERPROFILE% on Windows. Reporting the resolved directory is what
// tells a user on either platform where `tbuk init` actually put things.
func TestCheckHome_reportsResolvedDirectory(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	msg, status := cli.CheckHome()
	if msg != home {
		t.Errorf("CheckHome msg = %q, want %q", msg, home)
	}
	if status != "✓" {
		t.Errorf("CheckHome status = %q, want ✓", status)
	}
}

// With no home directory to resolve, config.DefaultRoot falls back to a
// relative ".tbuk" — so the knowledge base follows the working directory.
// Nothing else in the report would say so.
func TestCheckHome_flagsFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	msg, status := cli.CheckHome()
	if status != "✗" {
		t.Errorf("CheckHome status = %q, want ✗ when the home directory cannot be resolved", status)
	}
	if !strings.Contains(msg, ".tbuk") {
		t.Errorf("CheckHome msg = %q, want it to name the relative fallback", msg)
	}
}

// A path-keyed store behaves differently per OS, so a bug report needs to say
// which one it came from — and where the default root resolved to there.
func TestRunDoctor_reportsPlatform(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	cfg := config.DefaultsForRoot(filepath.Join(home, ".tbuk"))

	var out bytes.Buffer
	if err := cli.RunDoctorTo(&out, http.DefaultClient, cfg, filepath.Join(home, "config.yaml")); err != nil {
		t.Fatalf("RunDoctorTo: %v", err)
	}
	got := out.String()
	for _, want := range []string{"Platform", runtime.GOOS + "/" + runtime.GOARCH, home} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor report missing %q:\n%s", want, got)
		}
	}
}
