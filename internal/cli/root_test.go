package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
)

// ExecuteContext must thread the caller's context through to commands, so a
// signal-cancelled context reaches the ctx-plumbed pipeline (P1-19).
func TestExecuteContext_threadsContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmd := cli.New()
	cmd.SetArgs([]string{"version"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext(version): %v", err)
	}
}

// The global --root flag must thread through to the composition root so a data
// command opens its database beneath the chosen root, not ~/.tbuk (issue #95).
func TestRoot_rootFlagRoutesDataUnderRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(t.TempDir(), "kb")
	if err := runCLI("--root", root, "init"); err != nil {
		t.Fatalf("init --root: %v", err)
	}
	if err := runCLI("--root", root, "list"); err != nil {
		t.Fatalf("list --root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tbuk.sqlite")); err != nil {
		t.Errorf("expected database under custom root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".tbuk", "tbuk.sqlite")); !os.IsNotExist(err) {
		t.Errorf("--root must not create a database under ~/.tbuk, stat err = %v", err)
	}
}

// An invalid config must fail every command fast via the root
// PersistentPreRunE, with a message that points at the config (P1-17).
func TestRoot_invalidConfigFailsFast(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	// overlap >= size: chunks would never advance.
	content := "chunking:\n  size: 100\n  overlap: 100\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCLI("--config", cfgPath, "search", "hello", "--mode", "keyword")
	if err == nil {
		t.Fatal("expected invalid config to fail the command")
	}
	if !strings.Contains(err.Error(), "invalid config") {
		t.Errorf("error = %v, want it to mention 'invalid config'", err)
	}
}
