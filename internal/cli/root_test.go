package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
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
	setHome(t, home)

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
	setHome(t, home)

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

// With --config but no --root, the config file's own directory becomes the data
// root, so `--config <dir>/config.yaml` behaves like `--root <dir>`: data lands
// beside the config, never under ~/.tbuk (issue #96).
func TestRoot_configDirBecomesRootWithoutRootFlag(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	dir := filepath.Join(t.TempDir(), "kb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")

	if err := runCLI("--config", cfgPath, "list"); err != nil {
		t.Fatalf("list --config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tbuk.sqlite")); err != nil {
		t.Errorf("expected database beside config under %s: %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".tbuk", "tbuk.sqlite")); !os.IsNotExist(err) {
		t.Errorf("--config alone must not create a database under ~/.tbuk, stat err = %v", err)
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

// Usage is help for someone who typed the command wrong. A command that ran and
// then failed has nothing to do with usage, and printing it buries the error.
func TestExecute_usageOnlyForMisuse(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	// An unknown command points at --help rather than dumping usage; that is
	// still guidance for misuse, so it counts.
	cases := []struct {
		name      string
		args      []string
		wantUsage bool
	}{
		{name: "missing argument", args: []string{"search"}, wantUsage: true},
		{name: "unknown flag", args: []string{"--nope", "stats"}, wantUsage: true},
		{name: "unknown command", args: []string{"frobnicate"}, wantUsage: true},
		{
			name:      "runtime failure",
			args:      []string{"--config", cfgPath, "ingest", filepath.Join(home, "no-such-file.md")},
			wantUsage: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			cmd := cli.New()
			cmd.SetArgs(tc.args)
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected an error")
			}
			combined := out.String() + errOut.String()
			guided := strings.Contains(combined, "Usage:") || strings.Contains(combined, "--help' for usage")
			if got := guided; got != tc.wantUsage {
				t.Errorf("usage printed = %v, want %v; output:\n%s", got, tc.wantUsage, combined)
			}
		})
	}
}

// Cobra prints the error itself; printing it again in Execute doubles every
// failure.
func TestExecute_reportsAnErrorOnce(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}

	missing := filepath.Join(home, "no-such-file.md")
	var out, errOut bytes.Buffer
	cmd := cli.New()
	cmd.SetArgs([]string{"--config", filepath.Join(home, ".tbuk", "config.yaml"),
		"ingest", missing})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error")
	}
	if n := strings.Count(out.String()+errOut.String(), notFoundText(t, missing)); n != 1 {
		t.Errorf("error reported %d times, want once; output:\n%s", n, out.String()+errOut.String())
	}
}

// notFoundText returns the operating system's own wording for "this path does
// not exist" — "no such file or directory" on Unix, "The system cannot find the
// file specified." on Windows — so the count above matches what the error
// actually carries on the platform running the test.
func notFoundText(t *testing.T, path string) string {
	t.Helper()
	_, err := os.Stat(path)
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("stat %s: want a *fs.PathError, got %T: %v", path, err, err)
	}
	return pathErr.Err.Error()
}
