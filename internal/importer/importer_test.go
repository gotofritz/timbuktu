package importer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/importer"
)

// buildTar assembles an in-memory tar archive from name→content entries.
func buildTar(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Deterministic order keeps failures reproducible.
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		data := []byte(entries[name])
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     0o600,
			Size:     int64(len(data)),
		}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// standardArchive returns the entries a default `tbuk export` produces.
func standardArchive() map[string]string {
	return map[string]string{
		"config.yaml":              "llm:\n  provider: llama\n",
		"tbuk.sqlite":              "DBDATA",
		"extracted/abc.txt":        "extracted text",
		"raw/abc.pdf":              "%PDF raw",
		"prompts/qa/manifest.yaml": "name: qa",
	}
}

// Non-merge import re-homes every component under the target root and adopts the
// archive's config.yaml at ConfigDest.
func TestImport_nonMergeRehomesUnderRoot(t *testing.T) {
	root := t.TempDir()
	arc := buildTar(t, standardArchive())

	res, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:       root,
		Config:     config.DefaultsForRoot(root),
		ConfigDest: filepath.Join(root, "config.yaml"),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	want := map[string]string{
		filepath.Join(root, "config.yaml"):                    "llm:\n  provider: llama\n",
		filepath.Join(root, "tbuk.sqlite"):                    "DBDATA",
		filepath.Join(root, "extracted", "abc.txt"):           "extracted text",
		filepath.Join(root, "raw", "abc.pdf"):                 "%PDF raw",
		filepath.Join(root, "prompts", "qa", "manifest.yaml"): "name: qa",
	}
	for path, content := range want {
		if got := readFile(t, path); got != content {
			t.Errorf("%s = %q, want %q", path, got, content)
		}
		if !contains(res.Written, path) {
			t.Errorf("Written missing %q; have %v", path, res.Written)
		}
	}
}

// Merge import places data into the folders named by the target config and does
// not touch the target's config file.
func TestImport_mergeUsesTargetConfigFolders(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	cfg.Database.Path = filepath.Join(other, "mydb.sqlite")
	cfg.Preprocess.OutputDir = filepath.Join(other, "cache")

	arc := buildTar(t, standardArchive())
	res, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:       root,
		Config:     cfg,
		ConfigDest: "", // merge: keep the target config
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if got := readFile(t, cfg.Database.Path); got != "DBDATA" {
		t.Errorf("db at %s = %q, want DBDATA", cfg.Database.Path, got)
	}
	if got := readFile(t, filepath.Join(cfg.Preprocess.OutputDir, "abc.txt")); got != "extracted text" {
		t.Errorf("extracted file not placed under target OutputDir")
	}
	// prompts kept default under root.
	if got := readFile(t, filepath.Join(cfg.Prompts.Dir, "qa", "manifest.yaml")); got != "name: qa" {
		t.Errorf("prompts not placed under target Prompts.Dir")
	}
	// Merge must not write a config.yaml under the root.
	if _, err := os.Stat(filepath.Join(root, "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("merge should not write config.yaml, stat err = %v", err)
	}
	if contains(res.Written, filepath.Join(root, "config.yaml")) {
		t.Errorf("config.yaml should not be reported written in merge mode")
	}
}

// Existing destination files are left untouched without --force and recorded as
// skipped.
func TestImport_skipsExistingWithoutForce(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "tbuk.sqlite")
	if err := os.WriteFile(dbPath, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	arc := buildTar(t, standardArchive())

	res, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:       root,
		Config:     config.DefaultsForRoot(root),
		ConfigDest: filepath.Join(root, "config.yaml"),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := readFile(t, dbPath); got != "OLD" {
		t.Errorf("existing db overwritten: got %q, want OLD", got)
	}
	if !contains(res.Skipped, dbPath) {
		t.Errorf("Skipped missing %q; have %v", dbPath, res.Skipped)
	}
	if contains(res.Written, dbPath) {
		t.Errorf("skipped file must not be reported written")
	}
}

// --force overwrites existing destinations.
func TestImport_forceOverwrites(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "tbuk.sqlite")
	if err := os.WriteFile(dbPath, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	arc := buildTar(t, standardArchive())

	res, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:       root,
		Config:     config.DefaultsForRoot(root),
		ConfigDest: filepath.Join(root, "config.yaml"),
		Force:      true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := readFile(t, dbPath); got != "DBDATA" {
		t.Errorf("force should overwrite: got %q, want DBDATA", got)
	}
	if !contains(res.Written, dbPath) {
		t.Errorf("Written missing %q; have %v", dbPath, res.Written)
	}
}

// Non-merge writes the archive's config bytes verbatim to ConfigDest.
func TestImport_nonMergeWritesArchiveConfig(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "config.yaml")
	arc := buildTar(t, map[string]string{"config.yaml": "MARKER-CONFIG"})

	if _, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:       root,
		Config:     config.DefaultsForRoot(root),
		ConfigDest: dest,
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := readFile(t, dest); got != "MARKER-CONFIG" {
		t.Errorf("config = %q, want MARKER-CONFIG", got)
	}
}

// SQLite -wal/-shm sidecars map onto the target db path.
func TestImport_sidecarsMapToDbPath(t *testing.T) {
	root := t.TempDir()
	arc := buildTar(t, map[string]string{
		"tbuk.sqlite":     "main",
		"tbuk.sqlite-wal": "wal",
		"tbuk.sqlite-shm": "shm",
	})
	if _, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	for suffix, want := range map[string]string{"": "main", "-wal": "wal", "-shm": "shm"} {
		p := filepath.Join(root, "tbuk.sqlite") + suffix
		if got := readFile(t, p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
}

// Written files are owner-only (0o600).
func TestImport_writesOwnerOnlyPerms(t *testing.T) {
	root := t.TempDir()
	arc := buildTar(t, map[string]string{"tbuk.sqlite": "x"})
	if _, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "tbuk.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
}

// A path-traversal entry is rejected and never written outside the root.
func TestImport_rejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	arc := buildTar(t, map[string]string{"../evil.txt": "pwned"})
	_, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	})
	if err == nil {
		t.Fatal("expected error for a path-traversal archive entry")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); !os.IsNotExist(statErr) {
		t.Errorf("traversal entry escaped the root: %v", statErr)
	}
}

// An entry outside the four known components lands verbatim under the root.
func TestImport_unknownEntryFallsUnderRoot(t *testing.T) {
	root := t.TempDir()
	arc := buildTar(t, map[string]string{"notes/todo.txt": "hi"})
	if _, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "notes", "todo.txt")); got != "hi" {
		t.Errorf("unknown entry not placed under root: %q", got)
	}
}

// A disabled component (empty target path) falls back to the root.
func TestImport_disabledComponentFallsUnderRoot(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	cfg.Ingest.RawDir = "" // raw archiving disabled in the target config
	arc := buildTar(t, map[string]string{"raw/x.pdf": "raw"})
	if _, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:   root,
		Config: cfg,
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "raw", "x.pdf")); got != "raw" {
		t.Errorf("disabled raw component not re-homed under root: %q", got)
	}
}

// An absolute archive entry is rejected.
func TestImport_rejectsAbsoluteEntry(t *testing.T) {
	root := t.TempDir()
	arc := buildTar(t, map[string]string{"/etc/passwd": "root:x"})
	if _, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	}); err == nil {
		t.Fatal("expected error for an absolute archive entry")
	}
}

// Directory (non-regular) entries are ignored rather than written.
func TestImport_ignoresNonRegularEntries(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "prompts/qa/", Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(bytes.NewReader(buf.Bytes()), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "prompts", "qa")); !os.IsNotExist(err) {
		t.Errorf("directory entry should not create a path, stat err = %v", err)
	}
}

// A stat failure on the destination (a parent component that is a file, not a
// directory) propagates instead of being mistaken for "does not exist".
func TestImport_statErrorPropagates(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	arc := buildTar(t, map[string]string{"notes/todo.txt": "hi"}) // dest parent "notes" is a file
	if _, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	}); err == nil {
		t.Fatal("expected error when the destination cannot be stat'd")
	}
}

// A MkdirAll failure (parent path is a file) propagates.
func TestImport_mkdirErrorPropagates(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	arc := buildTar(t, map[string]string{"notes/todo.txt": "hi"})
	if _, err := importer.Import(bytes.NewReader(arc), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
		Force:  true, // skip the stat check so MkdirAll is the failing step
	}); err == nil {
		t.Fatal("expected error when the destination directory cannot be created")
	}
}

// A corrupt tar stream surfaces an error.
func TestImport_corruptArchiveErrors(t *testing.T) {
	root := t.TempDir()
	_, err := importer.Import(bytes.NewReader([]byte("not a tar archive at all")), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	})
	if err == nil {
		t.Fatal("expected error for a corrupt archive")
	}
}

func TestImport_corruptArchiveErrorNamesTarArchive(t *testing.T) {
	root := t.TempDir()
	_, err := importer.Import(bytes.NewReader(bytes.Repeat([]byte("x"), 2048)), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	})
	if err == nil {
		t.Fatal("expected error for a corrupt archive")
	}
	if !strings.Contains(err.Error(), "not a tar archive") {
		t.Errorf("error should say the file is not a tar archive, got %q", err)
	}
}

func TestImport_compressedArchiveErrorNamesFormat(t *testing.T) {
	cases := []struct {
		name  string
		magic []byte
		want  string
	}{
		{"gzip", []byte{0x1f, 0x8b, 0x08, 0x00}, "gzip"},
		{"zip", []byte("PK\x03\x04"), "zip"},
		{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd}, "zstd"},
		{"bzip2", []byte("BZh9"), "bzip2"},
		{"xz", []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}, "xz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			data := append(append([]byte{}, tc.magic...), bytes.Repeat([]byte("x"), 1024)...)
			_, err := importer.Import(bytes.NewReader(data), importer.Options{
				Root:   root,
				Config: config.DefaultsForRoot(root),
			})
			if err == nil {
				t.Fatal("expected error for a compressed archive")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should name the %s format, got %q", tc.want, err)
			}
		})
	}
}

func TestImport_truncatedArchiveErrorSaysTruncated(t *testing.T) {
	root := t.TempDir()
	data := buildTar(t, map[string]string{
		"config.yaml": "database:\n",
		"tbuk.sqlite": strings.Repeat("d", 4096),
	})
	_, err := importer.Import(bytes.NewReader(data[:len(data)/2]), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	})
	if err == nil {
		t.Fatal("expected error for a truncated archive")
	}
	if !strings.Contains(err.Error(), "ends mid-entry") {
		t.Errorf("error should say the archive ends mid-entry, got %q", err)
	}
}

func TestImport_emptyArchiveErrors(t *testing.T) {
	root := t.TempDir()
	_, err := importer.Import(bytes.NewReader(nil), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	})
	if err == nil {
		t.Fatal("expected error for an empty archive")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should say the archive is empty, got %q", err)
	}
}

func TestImport_archiveWithoutEntriesErrors(t *testing.T) {
	root := t.TempDir()
	// A well-formed tar holding nothing restores nothing: report it rather than
	// claiming a successful import of zero files.
	_, err := importer.Import(bytes.NewReader(buildTar(t, nil)), importer.Options{
		Root:   root,
		Config: config.DefaultsForRoot(root),
	})
	if err == nil {
		t.Fatal("expected error for an archive with no entries")
	}
	if !strings.Contains(err.Error(), "no entries") {
		t.Errorf("error should say the archive has no entries, got %q", err)
	}
}

// A file too small to hold a tar header must not be reported as a truncated
// archive: the tar reader returns io.ErrUnexpectedEOF for those too, but the
// user's file is a different kind of thing entirely.
func TestImport_shortNonTarFilesAreNotReportedAsTruncated(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{name: "short gzip names the format", data: gz.Bytes(), want: "gzip"},
		{name: "short text says it is too short", data: []byte("hello\n"), want: "too short"},
		{name: "short sqlite names the format", data: []byte("SQLite format 3\x00short"), want: "SQLite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := importer.Import(bytes.NewReader(tc.data), importer.Options{
				Root:   root,
				Config: config.DefaultsForRoot(root),
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got %q", tc.want, err)
			}
			if strings.Contains(err.Error(), "ends mid-entry") {
				t.Errorf("a short non-tar file is not a truncated archive, got %q", err)
			}
		})
	}
}
