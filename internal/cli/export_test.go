package cli_test

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/config"
)

var fixedNow = time.Date(2026, 7, 26, 15, 4, 5, 0, time.UTC)

// tarNames returns the entry names in the tar archive at path.
func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = f.Close() }()

	var names []string
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive %s: %v", path, err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestResolveExportTarget_dirGetsTimestampedName(t *testing.T) {
	dir := t.TempDir()
	got, err := cli.ResolveExportTarget(dir, fixedNow)
	if err != nil {
		t.Fatalf("ResolveExportTarget: %v", err)
	}
	want := filepath.Join(dir, "tbuk-export-20260726-150405.tar")
	if got != want {
		t.Errorf("dir target = %q, want %q", got, want)
	}
}

func TestResolveExportTarget_existingFileUsedAsIs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "snapshot.tar")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := cli.ResolveExportTarget(file, fixedNow)
	if err != nil {
		t.Fatalf("ResolveExportTarget: %v", err)
	}
	if got != file {
		t.Errorf("file target = %q, want %q", got, file)
	}
}

func TestResolveExportTarget_nonexistentTreatedAsFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new.tar")
	got, err := cli.ResolveExportTarget(target, fixedNow)
	if err != nil {
		t.Fatalf("ResolveExportTarget: %v", err)
	}
	if got != target {
		t.Errorf("nonexistent target = %q, want %q", got, target)
	}
}

func TestResolveExportTarget_trailingSlashNonexistentIsError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope") + string(os.PathSeparator)
	if _, err := cli.ResolveExportTarget(missing, fixedNow); err == nil {
		t.Fatal("expected error for a non-existent directory target")
	}
}

func TestRunExport_writesValidArchive(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	if err := os.WriteFile(cfg.Database.Path, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "out.tar")

	var out bytes.Buffer
	if err := cli.RunExport(strings.NewReader(""), &out, cfg, root, target, true); err != nil {
		t.Fatalf("RunExport: %v", err)
	}
	names := tarNames(t, target)
	if !containsName(names, "config.yaml") {
		t.Errorf("archive missing config.yaml, have %v", names)
	}
	if !containsName(names, "tbuk.sqlite") {
		t.Errorf("archive missing tbuk.sqlite, have %v", names)
	}
	if !strings.Contains(out.String(), target) {
		t.Errorf("expected success message naming %q, got %q", target, out.String())
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("archive perm = %o, want 600", perm)
	}
}

func TestRunExport_declineOverwriteAborts(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	target := filepath.Join(t.TempDir(), "exists.tar")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cli.RunExport(strings.NewReader("n\n"), &out, cfg, root, target, false); err != nil {
		t.Fatalf("RunExport: %v", err)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("expected 'Aborted' message, got %q", out.String())
	}
	data, _ := os.ReadFile(target)
	if string(data) != "ORIGINAL" {
		t.Errorf("declined overwrite should leave file intact, got %q", data)
	}
}

func TestRunExport_acceptOverwriteReplaces(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	target := filepath.Join(t.TempDir(), "exists.tar")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cli.RunExport(strings.NewReader("y\n"), &out, cfg, root, target, false); err != nil {
		t.Fatalf("RunExport: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) == "ORIGINAL" {
		t.Error("accepted overwrite should replace the file")
	}
	if !containsName(tarNames(t, target), "config.yaml") {
		t.Error("overwritten file should be a valid archive")
	}
}

func TestRunExport_forceSkipsPrompt(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	target := filepath.Join(t.TempDir(), "exists.tar")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Empty stdin: if --force did not skip the prompt, ConfirmYes would read EOF
	// and abort, leaving ORIGINAL in place.
	var out bytes.Buffer
	if err := cli.RunExport(strings.NewReader(""), &out, cfg, root, target, true); err != nil {
		t.Fatalf("RunExport: %v", err)
	}
	if strings.Contains(out.String(), "Overwrite") {
		t.Errorf("--force must not prompt, got %q", out.String())
	}
	if !containsName(tarNames(t, target), "config.yaml") {
		t.Error("--force should have written the archive")
	}
}

func TestRunExport_createsMissingParentDir(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	target := filepath.Join(t.TempDir(), "nested", "sub", "out.tar")

	var out bytes.Buffer
	if err := cli.RunExport(strings.NewReader(""), &out, cfg, root, target, true); err != nil {
		t.Fatalf("RunExport: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("expected archive at %q: %v", target, err)
	}
}

func TestExportCommand_missingArg(t *testing.T) {
	if err := runCLI("export"); err == nil {
		t.Fatal("expected error for missing path argument")
	}
}

func TestExportCommand_endToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	outDir := t.TempDir()
	if err := runCLI("export", "--force", outDir); err != nil {
		t.Fatalf("export: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "tbuk-export-*.tar"))
	if len(matches) != 1 {
		t.Fatalf("expected one timestamped archive, got %v", matches)
	}
	names := tarNames(t, matches[0])
	if !containsName(names, "config.yaml") {
		t.Errorf("end-to-end archive missing config.yaml, have %v", names)
	}
	if !containsName(names, "prompts/qa/manifest.yaml") {
		t.Errorf("end-to-end archive missing prompt templates, have %v", names)
	}
}
