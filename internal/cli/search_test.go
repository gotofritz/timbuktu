package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gotofritz/timbuktu/internal/cli"
)

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
	t.Setenv("HOME", home)
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
	t.Setenv("HOME", home)
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
	t.Setenv("HOME", home)
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
	t.Setenv("HOME", home)
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
	t.Setenv("HOME", home)
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

func TestDoctorCommand_showsSearch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
