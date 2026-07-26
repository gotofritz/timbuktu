package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/export"
)

// writeArchive builds a real export archive of a source knowledge base and
// returns its path.
func writeArchive(t *testing.T, srcRoot string) string {
	t.Helper()
	cfg := config.DefaultsForRoot(srcRoot)
	if err := os.MkdirAll(filepath.Dir(cfg.Database.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Database.Path, []byte("SQLITE"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSrc(t, filepath.Join(cfg.Preprocess.OutputDir, "abc.txt"), "extracted")
	writeSrc(t, filepath.Join(cfg.Prompts.Dir, "qa", "manifest.yaml"), "name: qa")

	arcPath := filepath.Join(t.TempDir(), "kb.tar")
	f, err := os.Create(arcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := export.Create(f, cfg, srcRoot); err != nil {
		t.Fatalf("export.Create: %v", err)
	}
	return arcPath
}

func writeSrc(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunImport_nonMergeRestoresUnderRoot(t *testing.T) {
	arc := writeArchive(t, t.TempDir())
	dest := t.TempDir()
	cfgPath := filepath.Join(dest, "config.yaml")

	var out bytes.Buffer
	if err := cli.RunImport(&out, arc, config.DefaultsForRoot(dest), dest, cfgPath, false, false); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	for _, p := range []string{
		cfgPath,
		filepath.Join(dest, "tbuk.sqlite"),
		filepath.Join(dest, "extracted", "abc.txt"),
		filepath.Join(dest, "prompts", "qa", "manifest.yaml"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected restored file %s: %v", p, err)
		}
	}
	if !strings.Contains(out.String(), "Imported") {
		t.Errorf("expected an import summary, got %q", out.String())
	}
}

func TestRunImport_missingArchiveErrors(t *testing.T) {
	dest := t.TempDir()
	var out bytes.Buffer
	err := cli.RunImport(&out, filepath.Join(dest, "nope.tar"), config.DefaultsForRoot(dest), dest, filepath.Join(dest, "config.yaml"), false, false)
	if err == nil {
		t.Fatal("expected error for a missing archive")
	}
}

// Merge mode keeps the target's existing config and places data into the folders
// that config names.
func TestRunImport_mergeKeepsTargetConfig(t *testing.T) {
	arc := writeArchive(t, t.TempDir())
	dest := t.TempDir()
	cfgPath := filepath.Join(dest, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("SENTINEL-CONFIG\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := cli.RunImport(&out, arc, config.DefaultsForRoot(dest), dest, cfgPath, true, false); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if data, _ := os.ReadFile(cfgPath); string(data) != "SENTINEL-CONFIG\n" {
		t.Errorf("merge overwrote the target config: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dest, "tbuk.sqlite")); err != nil {
		t.Errorf("merge should still restore data: %v", err)
	}
}

func TestImportCommand_missingArg(t *testing.T) {
	if err := runCLI("import"); err == nil {
		t.Fatal("expected error for missing archive argument")
	}
}

func TestImportCommand_endToEnd(t *testing.T) {
	// Build an archive by initialising and exporting a source KB.
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
		t.Fatalf("expected one archive, got %v", matches)
	}

	// Import into a fresh root.
	dest := filepath.Join(t.TempDir(), "restored")
	if err := runCLI("--root", dest, "import", matches[0]); err != nil {
		t.Fatalf("import: %v", err)
	}
	for _, p := range []string{
		filepath.Join(dest, "config.yaml"),
		filepath.Join(dest, "prompts", "qa", "manifest.yaml"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected restored file %s: %v", p, err)
		}
	}
}
