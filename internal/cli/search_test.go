package cli_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func TestFindCommand_returnsResults(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := home + "/.tbuk/config.yaml"

	db, err := storage.Open(home + "/.tbuk/tbuk.sqlite")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	ctx := context.Background()
	docs := storage.NewDocumentRepo(db.DB())
	meta := storage.NewMetadataRepo(db.DB())
	chunks := storage.NewChunkRepo(db.DB())
	doc := &storage.Document{Path: "/design.md", SHA256: "d1", Title: "Design", MimeType: "text/plain"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}
	if err := meta.Set(ctx, doc.ID, "tag", "design"); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	if err := chunks.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: strings.Repeat("word ", 40), TokenCount: 10},
	}); err != nil {
		t.Fatalf("insert chunk: %v", err)
	}
	_ = db.Close()

	if err := runCLI("--config", cfgPath, "find", "tag=design"); err != nil {
		t.Fatalf("find text: %v", err)
	}
	if err := runCLI("--config", cfgPath, "find", "tag=design", "--format", "json", "--limit", "1"); err != nil {
		t.Fatalf("find json: %v", err)
	}
}

func TestSearchCommand_missingArg(t *testing.T) {
	err := runCLI("search")
	if err == nil {
		t.Fatal("expected error for missing query argument")
	}
}

func TestFindCommand_noArgs(t *testing.T) {
	err := runCLI("find")
	if err == nil {
		t.Fatal("expected error for missing key=value arguments")
	}
}

func TestSearchCommand_emptyDB(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	// search on empty DB should succeed (0 results)
	err := runCLI("--config", cfgPath, "search", "hello world", "--mode", "keyword")
	if err != nil {
		t.Fatalf("search on empty DB: %v", err)
	}
}

func TestSearchCommand_jsonFormat(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	err := runCLI("--config", cfgPath, "search", "hello", "--mode", "keyword", "--format", "json")
	if err != nil {
		t.Fatalf("search json format: %v", err)
	}
}

func TestSearchCommand_invalidMode(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	err := runCLI("--config", cfgPath, "search", "hello", "--mode", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestFindCommand_emptyDB(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	err := runCLI("--config", cfgPath, "find", "lang=go")
	if err != nil {
		t.Fatalf("find on empty DB: %v", err)
	}
}

func TestFindCommand_badFormat(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	err := runCLI("--config", cfgPath, "find", "lang=go", "--format", "xml")
	if err == nil {
		t.Fatal("expected error for bad format")
	}
	if !strings.Contains(err.Error(), "format") {
		t.Errorf("error should mention 'format', got: %v", err)
	}
}

// Hybrid scores are RRF sums (~0.03 max), not 0–1 cosine values, so a
// 0–1 --min-score silently filters everything. The command must warn on
// stderr when --min-score is combined with the default hybrid mode (P1-16).
func TestSearchCommand_hybridMinScoreWarns(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	err := runCLI("--config", cfgPath, "search", "hello", "--mode", "keyword", "--min-score", "0.7")

	_ = w.Close()
	os.Stderr = oldStderr
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "min-score") {
		t.Errorf("expected a min-score scale warning on stderr, got: %q", out)
	}
}

// Vector mode uses 0–1 cosine scores, so a 0–1 --min-score is correct there
// and must NOT trigger the warning.
func TestSearchCommand_vectorMinScoreNoWarn(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	// keyword avoids needing a live embedder; the warning is scoped to hybrid.
	_ = runCLI("--config", cfgPath, "search", "hello", "--mode", "keyword", "--min-score", "0")

	_ = w.Close()
	os.Stderr = oldStderr
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	if strings.Contains(out, "min-score") {
		t.Errorf("did not expect a warning when --min-score is 0, got: %q", out)
	}
}

func TestTruncatePreview_shortUnchanged(t *testing.T) {
	s := "café"
	if got := cli.TruncatePreview(s, 120); got != s {
		t.Errorf("TruncatePreview(%q) = %q, want unchanged", s, got)
	}
}

func TestTruncatePreview_multibyteStaysValid(t *testing.T) {
	// 200 accented runes; byte length far exceeds 120, so naive text[:120]
	// would slice mid-rune. Truncation must stay valid UTF-8.
	s := strings.Repeat("é", 200)
	got := cli.TruncatePreview(s, 120)
	if !utf8.ValidString(got) {
		t.Errorf("truncated preview is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "...")); n != 120 {
		t.Errorf("truncated to %d runes, want 120", n)
	}
}

// `tbuk search` reads its query as an expression (issue #136): the user typed
// it, so the punctuation and the operators in it are meant. `tbuk ask` does
// not — that path sends a natural-language question, which has no operators in
// it and must stay lenient.
func TestSearchCommand_readsOperators(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	seedTwoForms(t, filepath.Join(home, ".tbuk", "tbuk.sqlite"))

	out := captureStdout(t, func() {
		if err := runCLI("--config", cfgPath, "search", "--mode", "keyword",
			"register -main_consumption"); err != nil {
			t.Fatalf("search: %v", err)
		}
	})
	if strings.Contains(out, "/under.md") {
		t.Errorf("the excluded form came back:\n%s", out)
	}
	if !strings.Contains(out, "/spaced.md") {
		t.Errorf("want the space form, got:\n%s", out)
	}
}

func TestSearchCommand_readsQuotedPhrases(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	seedTwoForms(t, filepath.Join(home, ".tbuk", "tbuk.sqlite"))

	out := captureStdout(t, func() {
		if err := runCLI("--config", cfgPath, "search", "--mode", "keyword",
			`"main consumption"`); err != nil {
			t.Fatalf("search: %v", err)
		}
	})
	if strings.Contains(out, "/apart.md") {
		t.Errorf("a phrase must not match the words apart:\n%s", out)
	}
	if !strings.Contains(out, "/spaced.md") {
		t.Errorf("want the phrase as written, got:\n%s", out)
	}
}

// seedTwoForms indexes the underscore form, the space form, and a chunk
// carrying both words but not adjacent — enough for an exclusion and a phrase
// to be told apart from the lenient reading of the same input.
func seedTwoForms(t *testing.T, dbPath string) {
	t.Helper()
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	docs := storage.NewDocumentRepo(db.DB())
	chunks := storage.NewChunkRepo(db.DB())
	for _, tc := range []struct{ path, text string }{
		{"/under.md", "the main_consumption register is read hourly"},
		{"/spaced.md", "the main consumption register is read hourly"},
		{"/apart.md", "consumption of the main register"},
	} {
		doc := &storage.Document{Path: tc.path, SHA256: tc.path, Title: tc.path, MimeType: "text/plain"}
		if err := docs.Create(ctx, doc); err != nil {
			t.Fatalf("create doc: %v", err)
		}
		if err := chunks.BulkInsert(ctx, []*storage.Chunk{
			{DocumentID: doc.ID, ChunkIndex: 0, Text: tc.text, SearchText: tc.text, TokenCount: 8},
		}); err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
// printSearchResults writes to the process's stdout rather than the command's
// output writer, so a pipe is the only way to read it back.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func TestDoctorCommand_showsSearch(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	// Capture stdout to verify Search section is present.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runCLI("--config", cfgPath, "doctor")

	_ = w.Close()
	os.Stdout = oldStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(output, "Search") {
		t.Errorf("doctor output missing Search section:\n%s", output)
	}
}
