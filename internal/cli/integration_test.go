package cli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/preprocess"
)

// TestCLI_endToEnd drives the assembled binary through the root command —
// init → ingest (real .md fixture, DefaultFileExtractor) → search → meta
// set/list → stats → delete — with only the embedding server faked (httptest).
// Every other seam is production wiring, which the per-package unit tests never
// exercise together.
func TestCLI_endToEnd(t *testing.T) {
	const dim = 4

	// Fake llama embedding server: one fixed unit vector per request.
	embSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]float32{"embedding": {1, 0, 0, 0}})
	}))
	defer embSrv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	// filepath.Abs (NormalizePath) needs a stable CWD; use a fixture inside HOME.
	fixture := filepath.Join(home, "notes.md")
	writeFile(t, fixture, "# Title\n\nThe quick brown fox jumps over the lazy dog.\n")

	// init writes ~/.tbuk with the default config; overwrite it so the embedder
	// points at the fake server and the dimension matches the fake vectors.
	mustRun(t, "init")
	writeConfig(t, filepath.Join(home, ".tbuk", "config.yaml"), config.Config{
		Database:   config.DatabaseConfig{Path: filepath.Join(home, ".tbuk", "tbuk.sqlite")},
		LLM:        config.LLMConfig{Provider: "llama", MaxTokens: 2048},
		Embedding:  config.EmbeddingConfig{Provider: "llama", Dimension: dim, BaseURL: embSrv.URL},
		Chunking:   config.ChunkingConfig{Size: 800, Overlap: 100},
		Preprocess: config.PreprocessConfig{OutputDir: filepath.Join(home, ".tbuk", "extracted")},
		Ingest:     config.IngestConfig{EmbedConcurrency: 2},
	})

	// ingest the real fixture through the production DefaultFileExtractor.
	if out := mustRun(t, "ingest", fixture); !strings.Contains(out, "chunk") {
		t.Fatalf("ingest output = %q, want it to mention chunks", out)
	}

	if out := mustRun(t, "search", "--mode", "vector", "fox"); !strings.Contains(out, "notes.md") {
		t.Fatalf("search output = %q, want it to reference the ingested doc", out)
	}

	mustRun(t, "meta", "set", fixture, "topic=animals")
	if out := mustRun(t, "meta", "list", fixture); !strings.Contains(out, "animals") {
		t.Fatalf("meta list output = %q, want the set value", out)
	}

	if out := mustRun(t, "stats"); !strings.Contains(out, "Documents") {
		t.Fatalf("stats output = %q, want a Documents line", out)
	}

	if out := mustRun(t, "delete", "--yes", fixture); !strings.Contains(out, "Deleted") {
		t.Fatalf("delete output = %q, want confirmation", out)
	}

	// After delete the knowledge base is empty.
	if out := mustRun(t, "list"); !strings.Contains(out, "No documents") {
		t.Fatalf("list after delete = %q, want empty-KB message", out)
	}
}

// TestIngestCommand_archivesRawSource drives the real ingest command end to end
// and asserts the on-ingest raw archive: a plain ingest copies the source into
// ~/.tbuk/raw, and --no-raw suppresses the copy. Only the embedding server is
// faked; the rawDir wiring (config → app → ingester) is production code.
func TestIngestCommand_archivesRawSource(t *testing.T) {
	const dim = 4
	embSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]float32{"embedding": {1, 0, 0, 0}})
	}))
	defer embSrv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	rawDir := filepath.Join(home, ".tbuk", "raw")

	mustRun(t, "init")
	writeConfig(t, filepath.Join(home, ".tbuk", "config.yaml"), config.Config{
		Database:   config.DatabaseConfig{Path: filepath.Join(home, ".tbuk", "tbuk.sqlite")},
		LLM:        config.LLMConfig{Provider: "llama", MaxTokens: 2048},
		Embedding:  config.EmbeddingConfig{Provider: "llama", Dimension: dim, BaseURL: embSrv.URL},
		Chunking:   config.ChunkingConfig{Size: 800, Overlap: 100},
		Preprocess: config.PreprocessConfig{OutputDir: filepath.Join(home, ".tbuk", "extracted")},
		Ingest:     config.IngestConfig{EmbedConcurrency: 2, RawDir: rawDir},
	})

	// Plain ingest → source archived under raw/<sha>.md.
	kept := filepath.Join(home, "kept.md")
	writeFile(t, kept, "# Kept\n\nArchive me into raw.\n")
	mustRun(t, "ingest", kept)

	keptSHA, err := preprocess.HashFile(kept)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rawDir, keptSHA+".md")); err != nil {
		t.Fatalf("expected raw copy for ingested file: %v", err)
	}

	// --no-raw → no archive copy.
	skipped := filepath.Join(home, "skipped.md")
	writeFile(t, skipped, "# Skipped\n\nDo not archive me.\n")
	mustRun(t, "ingest", "--no-raw", skipped)

	skipSHA, err := preprocess.HashFile(skipped)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rawDir, skipSHA+".md")); !os.IsNotExist(err) {
		t.Fatalf("--no-raw still created a raw copy (stat err = %v)", err)
	}
}

// TestExecute_success covers the exported Execute wrapper on a non-erroring
// command (it must not call os.Exit).
func TestExecute_success(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldArgs := os.Args
	os.Args = []string{"tbuk", "version"}
	defer func() { os.Args = oldArgs }()

	// version needs no config file; Load falls back to defaults. A panic or
	// os.Exit here would fail the test process.
	cli.Execute()
}

// TestExecute_exitCodeOnError verifies the Execute wrapper exits non-zero when
// the root command returns an error. Uses the re-exec-self subprocess pattern
// because Execute calls os.Exit, which would terminate the test binary.
func TestExecute_exitCodeOnError(t *testing.T) {
	if os.Getenv("TBUK_EXECUTE_CRASH") == "1" {
		os.Args = []string{"tbuk", "no-such-command"}
		cli.Execute() // must os.Exit(1)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestExecute_exitCodeOnError$") //nolint:gosec
	cmd.Env = append(os.Environ(), "TBUK_EXECUTE_CRASH=1")
	err := cmd.Run()

	var exitErr *exec.ExitError
	if err == nil {
		t.Fatal("Execute on a bad command: want non-zero exit, got success")
	}
	if !asExitError(err, &exitErr) {
		t.Fatalf("want *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("exit code = %d, want 1", exitErr.ExitCode())
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// mustRun executes one root command with args, capturing stdout+stderr (some
// commands print via fmt.Println straight to os.Stdout, so cobra's SetOut is
// not enough), and fails the test on error.
func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runRoot(t, args...)
	if err != nil {
		t.Fatalf("run %v: %v\noutput:\n%s", args, err, out)
	}
	return out
}

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	root := cli.New()
	root.SetArgs(args)
	root.SetOut(w)
	root.SetErr(w)
	runErr := root.ExecuteContext(context.Background())

	_ = w.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, readErr := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if readErr != nil {
			break
		}
	}
	_ = r.Close()
	return string(buf), runErr
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeConfig(t *testing.T, path string, cfg config.Config) {
	t.Helper()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	writeFile(t, path, string(data))
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
