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
	"github.com/gotofritz/timbuktu/internal/eval"
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
	setHome(t, home)
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
		Eval:       config.EvalConfig{Dir: filepath.Join(home, ".tbuk", "eval")},
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

	// eval, in keyword mode: the whole retrieval stage runs with no embedding
	// call at all, which is the claim that makes an eval affordable in CI.
	writeFile(t, filepath.Join(home, ".tbuk", "eval", "smoke.yaml"),
		"version: 1\nname: smoke\ncases:\n"+
			"  - id: fox\n    query: fox\n    relevant:\n      - path: notes.md\n"+
			"  - id: unlabelled\n    query: anything\n    answer: a reference answer\n")

	out := mustRun(t, "eval", "--mode", "keyword", "--format", "json")
	var report eval.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("eval --format json did not produce a report: %v\n%s", err, out)
	}
	if report.Set != "smoke" || report.Run.Mode != "keyword" {
		t.Fatalf("report = %+v", report)
	}
	if report.Overall.Hit != 1 || report.Overall.Cases != 1 {
		t.Fatalf("overall = %+v, want the one labelled case scored and hit", report.Overall)
	}
	// The generation-only case is skipped rather than counted as a zero.
	if len(report.Skipped) != 1 || report.Skipped[0].ID != "unlabelled" {
		t.Fatalf("skipped = %+v, want the unlabelled case named", report.Skipped)
	}

	// The same run again, diffed against itself, is all zeroes — the shape
	// `--baseline` is for.
	baseline := filepath.Join(home, "before.json")
	writeFile(t, baseline, out)
	if diffed := mustRun(t, "eval", "--mode", "keyword", "--baseline", baseline); !strings.Contains(diffed, "+0.00") {
		t.Fatalf("eval --baseline output = %q, want deltas", diffed)
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
	setHome(t, home)
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

// TestCLI_conversationEndToEnd drives a whole thread through the assembled root
// command — init → ingest → ask --session → ask -c → session show → chat →
// session delete — with only the embedding and chat endpoints faked. It is the
// path a person actually walks, and the one place the store, the planner, the
// REPL and the session commands are exercised against each other.
func TestCLI_conversationEndToEnd(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	srv := fakeLLMServer(t)

	mustRun(t, "init")
	writeConfig(t, filepath.Join(home, ".tbuk", "config.yaml"), config.Config{
		Database:   config.DatabaseConfig{Path: filepath.Join(home, ".tbuk", "tbuk.sqlite")},
		LLM:        config.LLMConfig{Provider: "llama", MaxTokens: 256, BaseURL: srv.URL},
		Embedding:  config.EmbeddingConfig{Provider: "llama", Dimension: 4, BaseURL: srv.URL},
		Chunking:   config.ChunkingConfig{Size: 800, Overlap: 100},
		Preprocess: config.PreprocessConfig{OutputDir: filepath.Join(home, ".tbuk", "extracted")},
		Ingest:     config.IngestConfig{EmbedConcurrency: 1},
		Prompts:    config.PromptsConfig{Dir: filepath.Join(home, ".tbuk", "prompts")},
		Session:    config.SessionConfig{HistoryTurns: 6},
	})

	fixture := filepath.Join(home, "go.md")
	writeFile(t, fixture, "# Go\n\nSlices grow by doubling their capacity. Maps rehash when they fill.\n")
	mustRun(t, "ingest", fixture)

	mustRun(t, "ask", "--session", "go", "how do slices grow?")
	mustRun(t, "ask", "-c", "and maps?")
	mustRunStdin(t, "and channels?\n/exit\n", "chat", "--session", "go")

	if out := mustRun(t, "session", "list"); !strings.Contains(out, "go") {
		t.Fatalf("session list = %q, want the thread the ask created", out)
	}

	// --verbose shows the query retrieval ran, which is where the thread being
	// folded into a follow-up becomes visible.
	out := mustRun(t, "session", "show", "go", "--verbose")
	for _, want := range []string{"how do slices grow?", "and maps?", "and channels?", "go.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("session show is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "[query] how do slices grow? and maps?") {
		t.Fatalf("the follow-up did not retrieve on the folded query:\n%s", out)
	}

	// Plain Enter is not a yes, and the thread survives it.
	if out := mustRunStdin(t, "\n", "session", "delete", "go"); !strings.Contains(out, "Aborted") {
		t.Fatalf("session delete without confirmation = %q", out)
	}
	if out := mustRun(t, "session", "show", "go"); !strings.Contains(out, "and channels?") {
		t.Fatalf("the aborted delete lost the thread:\n%s", out)
	}

	mustRun(t, "session", "delete", "go", "--yes")
	if out := mustRun(t, "session", "list"); !strings.Contains(out, "No conversation threads") {
		t.Fatalf("session list after delete = %q", out)
	}
	// Turns go with the thread, by the schema's cascade.
	if out := mustRun(t, "doctor"); !strings.Contains(out, "0 threads") {
		t.Fatalf("doctor after delete = %q, want no threads left", out)
	}
}

// TestCLI_fixtureCorpusEval tests eval over the fixture corpus in keyword mode
// (no vectors needed) and verifies the metrics. This is the regression test for
// retrieval ranking changes in CI.
func TestCLI_fixtureCorpusEval(t *testing.T) {
	const dim = 4
	embSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]float32{"embedding": {1, 0, 0, 0}})
	}))
	defer embSrv.Close()

	home := t.TempDir()
	setHome(t, home)

	mustRun(t, "init")
	writeConfig(t, filepath.Join(home, ".tbuk", "config.yaml"), config.Config{
		Database:   config.DatabaseConfig{Path: filepath.Join(home, ".tbuk", "tbuk.sqlite")},
		LLM:        config.LLMConfig{Provider: "llama", MaxTokens: 2048},
		Embedding:  config.EmbeddingConfig{Provider: "llama", Dimension: dim, BaseURL: embSrv.URL},
		Chunking:   config.ChunkingConfig{Size: 800, Overlap: 100},
		Preprocess: config.PreprocessConfig{OutputDir: filepath.Join(home, ".tbuk", "extracted")},
		Ingest:     config.IngestConfig{EmbedConcurrency: 2},
		Eval:       config.EvalConfig{Dir: filepath.Join(home, ".tbuk", "eval")},
	})

	// Ingest fixture corpus.
	// The fixture is embedded in the binary at internal/eval/testdata/corpus/
	// For now, copy the real fixture files for testing.
	corpusDir := filepath.Join(home, "corpus")
	writeFile(t, filepath.Join(corpusDir, "slices.md"),
		"# Go Slices\n\nWhen you append to a slice, Go reallocates the underlying array when len == cap.\nThe capacity is roughly doubled.\n")
	writeFile(t, filepath.Join(corpusDir, "maps.md"),
		"# Go Maps\n\nMaps grow by reallocating buckets and re-hashing all entries.\n")

	mustRun(t, "ingest", corpusDir)

	// Write label set for the fixture corpus, including a follow-up case with gold_query.
	writeFile(t, filepath.Join(home, ".tbuk", "eval", "fixture.yaml"),
		"version: 1\nname: fixture-corpus\ncases:\n"+
			"  - id: slices\n    query: slices\n    relevant:\n      - path: slices.md\n"+
			"  - id: maps-followup\n    query: and maps?\n    gold_query: how do maps grow?\n    relevant:\n      - path: maps.md\n")

	// Eval in keyword mode: free, deterministic, no embedding needed.
	out := mustRun(t, "eval", "--mode", "keyword", "--format", "json")
	var report eval.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("eval --format json failed: %v\n%s", err, out)
	}

	if report.Set != "fixture-corpus" {
		t.Fatalf("report.Set = %q, want fixture-corpus", report.Set)
	}
	if report.Run.Mode != "keyword" {
		t.Fatalf("report.Run.Mode = %q, want keyword", report.Run.Mode)
	}
	// Both cases should hit their labels.
	if report.Overall.Cases != 2 || report.Overall.Hit < 1 {
		t.Fatalf("overall = %+v, want 2 cases with at least 1 hit", report.Overall)
	}

	// Eval in hybrid mode: vectors come from the httptest embedder (frozen),
	// deterministic, and we can use it for regression testing.
	out = mustRun(t, "eval", "--mode", "hybrid", "--format", "json")
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("eval hybrid --format json failed: %v\n%s", err, out)
	}
	if report.Run.Mode != "hybrid" {
		t.Fatalf("hybrid report.Run.Mode = %q, want hybrid", report.Run.Mode)
	}
	// Hybrid should also score the cases (maybe differently than keyword).
	if report.Overall.Cases != 2 {
		t.Fatalf("hybrid overall.Cases = %d, want 2", report.Overall.Cases)
	}

	// Test gold ceiling: run with --gold to score against gold_query.
	// The gold query "how do maps grow?" should retrieve better than the follow-up "and maps?".
	out = mustRun(t, "eval", "--mode", "keyword", "--gold", "--format", "json")
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("eval --gold --format json failed: %v\n%s", err, out)
	}
	if report.Run.Rewrite != "gold" {
		t.Fatalf("gold report.Run.Rewrite = %q, want gold", report.Run.Rewrite)
	}
}

// TestExecute_success covers the exported Execute wrapper on a non-erroring
// command (it must not call os.Exit).
func TestExecute_success(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

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

// mustRunStdin is mustRun with a scripted stdin, for the commands that read one.
func mustRunStdin(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, err := runRootStdin(t, stdin, args...)
	if err != nil {
		t.Fatalf("run %v: %v\noutput:\n%s", args, err, out)
	}
	return out
}

func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runRootStdin(t, "", args...)
}

func runRootStdin(t *testing.T, stdin string, args ...string) (string, error) {
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
	root.SetIn(strings.NewReader(stdin))
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
