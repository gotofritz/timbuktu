package cli_test

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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

// buildImportArchive writes a tar with the given entries and returns its path.
func buildImportArchive(t *testing.T, entries map[string]string) string {
	t.Helper()
	arcPath := filepath.Join(t.TempDir(), "kb.tar")
	f, err := os.Create(arcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	tw := tar.NewWriter(f)
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		data := []byte(entries[name])
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return arcPath
}

func readImported(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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
	if err := cli.RunImport(&out, arc, config.DefaultsForRoot(dest), dest, cfgPath, false, false, false); err != nil {
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
	err := cli.RunImport(&out, filepath.Join(dest, "nope.tar"), config.DefaultsForRoot(dest), dest, filepath.Join(dest, "config.yaml"), false, false, false)
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
	if err := cli.RunImport(&out, arc, config.DefaultsForRoot(dest), dest, cfgPath, true, false, false); err != nil {
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

// The config is a machine's own settings, so it is protected separately from
// the data: each has its own force flag and neither implies the other.
func TestRunImport_forceFlagsAreIndependent(t *testing.T) {
	arc := buildImportArchive(t, map[string]string{
		"config.yaml": "chunking:\n    size: 111\n",
		"tbuk.sqlite": "NEW-DB",
	})

	cases := []struct {
		name                   string
		forceConfig, forceData bool
		wantConfig, wantDB     string
	}{
		{name: "no flags keep both", wantConfig: "OLD-CONFIG", wantDB: "OLD-DB"},
		{name: "force-config takes the archive config only", forceConfig: true, wantConfig: "size: 111", wantDB: "OLD-DB"},
		{name: "force-data replaces data only", forceData: true, wantConfig: "OLD-CONFIG", wantDB: "NEW-DB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cfgPath := filepath.Join(root, "config.yaml")
			dbPath := filepath.Join(root, "tbuk.sqlite")
			for path, content := range map[string]string{cfgPath: "OLD-CONFIG", dbPath: "OLD-DB"} {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var out bytes.Buffer
			if err := cli.RunImport(&out, arc, config.DefaultsForRoot(root), root, cfgPath, false, tc.forceConfig, tc.forceData); err != nil {
				t.Fatalf("RunImport: %v", err)
			}
			if got := readImported(t, cfgPath); !strings.Contains(got, tc.wantConfig) {
				t.Errorf("config = %q, want it to contain %q", got, tc.wantConfig)
			}
			if got := readImported(t, dbPath); got != tc.wantDB {
				t.Errorf("db = %q, want %q", got, tc.wantDB)
			}
		})
	}
}

func TestImportCommand_exposesBothForceFlags(t *testing.T) {
	cmd := cli.New()
	var importCmd *cobra.Command
	for _, c := range cmd.Commands() {
		if c.Name() == "import" {
			importCmd = c
		}
	}
	if importCmd == nil {
		t.Fatal("import command not found")
	}
	for _, name := range []string{"force-config", "force-data"} {
		if importCmd.Flags().Lookup(name) == nil {
			t.Errorf("import should expose --%s", name)
		}
	}
	if importCmd.Flags().Lookup("force") != nil {
		t.Error("the coarse --force flag should be gone: it overwrote config and data together")
	}
}

// Data must land where the config that will be in effect after the import says.
// Keeping the target's config and then restoring into the root's default
// folders leaves a knowledge base whose config points at one place and whose
// data sits in another.
func TestRunImport_placesDataWhereThePreservedConfigPoints(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("MINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultsForRoot(root)
	cfg.Database.Path = filepath.Join(other, "custom.sqlite")
	cfg.Preprocess.OutputDir = filepath.Join(other, "cache")

	arc := buildImportArchive(t, map[string]string{
		"config.yaml":       "chunking:\n    size: 111\n",
		"tbuk.sqlite":       "NEW-DB",
		"extracted/abc.txt": "extracted",
	})

	var out bytes.Buffer
	if err := cli.RunImport(&out, arc, cfg, root, cfgPath, false, false, true); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if got := readImported(t, cfg.Database.Path); got != "NEW-DB" {
		t.Errorf("db at the configured path = %q, want NEW-DB", got)
	}
	if got := readImported(t, filepath.Join(cfg.Preprocess.OutputDir, "abc.txt")); got != "extracted" {
		t.Errorf("extracted file not placed under the configured OutputDir, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "tbuk.sqlite")); !os.IsNotExist(err) {
		t.Errorf("nothing should be written to the root default while that config is preserved, stat err = %v", err)
	}
	if got := readImported(t, cfgPath); got != "MINE\n" {
		t.Errorf("config = %q, want it preserved", got)
	}
	if !strings.Contains(out.String(), "--force-config") {
		t.Errorf("output should say the existing config was kept, got %q", out.String())
	}
}

// Adopting the archive's config re-homes the data under the root, since that is
// what the adopted config resolves to.
func TestRunImport_forceConfigRehomesDataUnderRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("MINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultsForRoot(root)
	cfg.Database.Path = filepath.Join(other, "custom.sqlite")

	arc := buildImportArchive(t, map[string]string{
		"config.yaml": "chunking:\n    size: 111\n",
		"tbuk.sqlite": "NEW-DB",
	})

	var out bytes.Buffer
	if err := cli.RunImport(&out, arc, cfg, root, cfgPath, false, true, true); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if got := readImported(t, filepath.Join(root, "tbuk.sqlite")); got != "NEW-DB" {
		t.Errorf("db = %q, want it re-homed under the root", got)
	}
	if got := readImported(t, cfgPath); !strings.Contains(got, "size: 111") {
		t.Errorf("config = %q, want the archive's", got)
	}
}
