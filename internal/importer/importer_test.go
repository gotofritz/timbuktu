package importer_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

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

// dest is a scratch destination pair: a raw directory and a manifest path,
// both under one temp dir the test owns.
type dest struct {
	dir      string
	rawDir   string
	manifest string
}

func newDest(t *testing.T) dest {
	t.Helper()
	dir := t.TempDir()
	return dest{
		dir:      dir,
		rawDir:   filepath.Join(dir, "raw"),
		manifest: filepath.Join(dir, "manifest.sqlite"),
	}
}

func (d dest) opts() importer.Options {
	return importer.Options{RawDir: d.rawDir, DBDest: d.manifest}
}

// ── what Extract takes, and what it leaves alone ─────────────────────────────

// The archive's raw copies land in the target raw directory, and its database
// lands at the caller's chosen path for reading as a manifest.
func TestExtract_writesRawFilesAndDatabase(t *testing.T) {
	d := newDest(t)

	res, err := importer.Extract(bytes.NewReader(buildTar(t, standardArchive())), d.opts())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	rawDest := filepath.Join(d.rawDir, "abc.pdf")
	if got := readFile(t, rawDest); got != "%PDF raw" {
		t.Errorf("raw copy = %q, want the archive's bytes", got)
	}
	if got := readFile(t, d.manifest); got != "DBDATA" {
		t.Errorf("manifest = %q, want the archive's database", got)
	}
	if res.DBPath != d.manifest {
		t.Errorf("DBPath = %q, want %q", res.DBPath, d.manifest)
	}
	if !contains(res.Written, rawDest) {
		t.Errorf("Written missing the raw copy; have %v", res.Written)
	}
	if !contains(res.RawNames, "abc.pdf") {
		t.Errorf("RawNames = %v, want it to list abc.pdf", res.RawNames)
	}
}

// A machine's config is its own: the archive's config.yaml is never written,
// whatever the target looks like. Neither is the extracted-text cache, which
// this machine re-derives. Templates go only where the caller asks for them,
// and these options ask for nowhere.
func TestExtract_neverWritesConfigOrExtractedCache(t *testing.T) {
	d := newDest(t)

	if _, err := importer.Extract(bytes.NewReader(buildTar(t, standardArchive())), d.opts()); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	for _, name := range []string{"config.yaml", "extracted", "prompts"} {
		if _, err := os.Stat(filepath.Join(d.dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should never be written, stat err = %v", name, err)
		}
	}
	walked := 0
	_ = filepath.Walk(d.dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			walked++
			rel, _ := filepath.Rel(d.dir, path)
			if rel != "manifest.sqlite" && !strings.HasPrefix(rel, "raw"+string(filepath.Separator)) {
				t.Errorf("unexpected file written: %s", rel)
			}
		}
		return nil
	})
	if walked == 0 {
		t.Fatal("nothing was written at all")
	}
}

// The WAL and SHM sidecars travel with the database: without them a manifest
// read can miss the most recent commits.
func TestExtract_writesDatabaseSidecars(t *testing.T) {
	d := newDest(t)
	arc := buildTar(t, map[string]string{
		"tbuk.sqlite":     "MAIN",
		"tbuk.sqlite-wal": "WAL",
		"tbuk.sqlite-shm": "SHM",
	})

	if _, err := importer.Extract(bytes.NewReader(arc), d.opts()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for suffix, want := range map[string]string{"": "MAIN", "-wal": "WAL", "-shm": "SHM"} {
		if got := readFile(t, d.manifest+suffix); got != want {
			t.Errorf("manifest%s = %q, want %q", suffix, got, want)
		}
	}
}

// An archive carrying no database has no manifest to import from; the caller
// needs to be able to tell.
func TestExtract_noDatabaseInArchive(t *testing.T) {
	d := newDest(t)
	arc := buildTar(t, map[string]string{"raw/only.md": "# raw"})

	res, err := importer.Extract(bytes.NewReader(arc), d.opts())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.DBPath != "" {
		t.Errorf("DBPath = %q, want empty", res.DBPath)
	}
	if !contains(res.RawNames, "only.md") {
		t.Errorf("RawNames = %v, want only.md", res.RawNames)
	}
}

// Raw names are content-addressed, so a copy already in place holds identical
// bytes: leave it, and do not report it as written.
func TestExtract_existingRawCopyIsLeftAlone(t *testing.T) {
	d := newDest(t)
	if err := os.MkdirAll(d.rawDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(d.rawDir, "abc.pdf")
	if err := os.WriteFile(existing, []byte("MINE"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := importer.Extract(bytes.NewReader(buildTar(t, standardArchive())), d.opts())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := readFile(t, existing); got != "MINE" {
		t.Errorf("existing raw copy was overwritten: %q", got)
	}
	if contains(res.Written, existing) {
		t.Errorf("an untouched copy should not be reported written; have %v", res.Written)
	}
	if !contains(res.RawNames, "abc.pdf") {
		t.Errorf("RawNames should list it regardless; have %v", res.RawNames)
	}
}

// A dry run reads the archive to learn what it holds without writing a thing.
func TestExtract_emptyRawDirWritesNoRawFiles(t *testing.T) {
	d := newDest(t)
	opts := d.opts()
	opts.RawDir = ""

	res, err := importer.Extract(bytes.NewReader(buildTar(t, standardArchive())), opts)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !contains(res.RawNames, "abc.pdf") {
		t.Errorf("RawNames = %v, want abc.pdf reported without writing it", res.RawNames)
	}
	if len(res.Written) != 1 || res.Written[0] != d.manifest {
		t.Errorf("Written = %v, want only the manifest", res.Written)
	}
}

// Nested raw entries keep their sub-path rather than colliding on basename.
func TestExtract_keepsRawSubPaths(t *testing.T) {
	d := newDest(t)
	arc := buildTar(t, map[string]string{"raw/nested/a.md": "# nested"})

	if _, err := importer.Extract(bytes.NewReader(arc), d.opts()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := readFile(t, filepath.Join(d.rawDir, "nested", "a.md")); got != "# nested" {
		t.Errorf("nested raw entry = %q", got)
	}
}

func TestExtract_writesOwnerOnlyPerms(t *testing.T) {
	d := newDest(t)

	if _, err := importer.Extract(bytes.NewReader(buildTar(t, standardArchive())), d.opts()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, p := range []string{filepath.Join(d.rawDir, "abc.pdf"), d.manifest} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s perm = %o, want 600", p, perm)
		}
	}
}

// ── hostile and malformed archives ───────────────────────────────────────────

// A path-traversal entry is rejected and never written outside the target.
func TestExtract_rejectsPathTraversal(t *testing.T) {
	d := newDest(t)
	arc := buildTar(t, map[string]string{"../evil.txt": "pwned"})

	if _, err := importer.Extract(bytes.NewReader(arc), d.opts()); err == nil {
		t.Fatal("expected error for a path-traversal archive entry")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(d.dir), "evil.txt")); !os.IsNotExist(err) {
		t.Errorf("traversal entry escaped: %v", err)
	}
}

// Traversal dressed up as a raw entry is rejected too.
func TestExtract_rejectsTraversalThroughRawPrefix(t *testing.T) {
	d := newDest(t)
	arc := buildTar(t, map[string]string{"raw/../../evil.md": "pwned"})

	if _, err := importer.Extract(bytes.NewReader(arc), d.opts()); err == nil {
		t.Fatal("expected error for a raw entry escaping via ..")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(d.dir), "evil.md")); !os.IsNotExist(err) {
		t.Errorf("traversal entry escaped: %v", err)
	}
}

// An absolute archive entry is rejected.
func TestExtract_rejectsAbsoluteEntry(t *testing.T) {
	d := newDest(t)
	arc := buildTar(t, map[string]string{"/etc/passwd": "root:x"})

	if _, err := importer.Extract(bytes.NewReader(arc), d.opts()); err == nil {
		t.Fatal("expected error for an absolute archive entry")
	}
}

// Directory (non-regular) entries are ignored rather than written.
func TestExtract_ignoresNonRegularEntries(t *testing.T) {
	d := newDest(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "raw/", Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := importer.Extract(bytes.NewReader(buf.Bytes()), d.opts()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, err := os.Stat(d.rawDir); !os.IsNotExist(err) {
		t.Errorf("directory entry should not create a path, stat err = %v", err)
	}
}

// A stat failure on a raw destination (its parent is a file, not a directory)
// propagates instead of being mistaken for "does not exist".
func TestExtract_statErrorPropagates(t *testing.T) {
	d := newDest(t)
	if err := os.WriteFile(d.rawDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	arc := buildTar(t, map[string]string{"raw/a.md": "# a"})

	if _, err := importer.Extract(bytes.NewReader(arc), d.opts()); err == nil {
		t.Fatal("expected error when the raw destination cannot be stat'd")
	}
}

// A MkdirAll failure (the manifest's parent is a file) propagates.
func TestExtract_mkdirErrorPropagates(t *testing.T) {
	d := newDest(t)
	opts := d.opts()
	opts.DBDest = filepath.Join(d.dir, "notadir", "manifest.sqlite")
	if err := os.WriteFile(filepath.Join(d.dir, "notadir"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := importer.Extract(bytes.NewReader(buildTar(t, standardArchive())), opts); err == nil {
		t.Fatal("expected error when the destination directory cannot be created")
	}
}

// A corrupt tar stream surfaces an error.
func TestExtract_corruptArchiveErrors(t *testing.T) {
	d := newDest(t)
	if _, err := importer.Extract(bytes.NewReader([]byte("not a tar archive at all")), d.opts()); err == nil {
		t.Fatal("expected error for a corrupt archive")
	}
}

func TestExtract_corruptArchiveErrorNamesTarArchive(t *testing.T) {
	d := newDest(t)
	_, err := importer.Extract(bytes.NewReader(bytes.Repeat([]byte("x"), 2048)), d.opts())
	if err == nil {
		t.Fatal("expected error for a corrupt archive")
	}
	if !strings.Contains(err.Error(), "not a tar archive") {
		t.Errorf("error should say the file is not a tar archive, got %q", err)
	}
}

func TestExtract_compressedArchiveErrorNamesFormat(t *testing.T) {
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
			d := newDest(t)
			data := append(append([]byte{}, tc.magic...), bytes.Repeat([]byte("x"), 1024)...)
			_, err := importer.Extract(bytes.NewReader(data), d.opts())
			if err == nil {
				t.Fatal("expected error for a compressed archive")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should name the %s format, got %q", tc.want, err)
			}
		})
	}
}

func TestExtract_truncatedArchiveErrorSaysTruncated(t *testing.T) {
	data := buildTar(t, map[string]string{
		"config.yaml": "database:\n",
		"tbuk.sqlite": strings.Repeat("d", 4096),
	})
	half := data[:len(data)/2]

	// Seekable: the pre-flight compares extents, so it can name the entry that
	// does not fit.
	_, err := importer.Extract(bytes.NewReader(half), newDest(t).opts())
	if err == nil {
		t.Fatal("expected error for a truncated archive")
	}
	if !strings.Contains(err.Error(), "truncated") || !strings.Contains(err.Error(), "tbuk.sqlite") {
		t.Errorf("error should say which entry is truncated, got %q", err)
	}

	// Non-seekable: the read loop reports how far it got instead.
	_, err = importer.Extract(nonSeeker{r: bytes.NewReader(half)}, newDest(t).opts())
	if err == nil {
		t.Fatal("expected error for a truncated archive")
	}
	if !strings.Contains(err.Error(), "ends mid-entry") {
		t.Errorf("error should say the archive ends mid-entry, got %q", err)
	}
}

func TestExtract_emptyArchiveErrors(t *testing.T) {
	_, err := importer.Extract(bytes.NewReader(nil), newDest(t).opts())
	if err == nil {
		t.Fatal("expected error for an empty archive")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should say the archive is empty, got %q", err)
	}
}

func TestExtract_archiveWithoutEntriesErrors(t *testing.T) {
	// A well-formed tar holding nothing restores nothing: report it rather than
	// claiming a successful import of zero files.
	_, err := importer.Extract(bytes.NewReader(buildTar(t, nil)), newDest(t).opts())
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
func TestExtract_shortNonTarFilesAreNotReportedAsTruncated(t *testing.T) {
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
			_, err := importer.Extract(bytes.NewReader(tc.data), newDest(t).opts())
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

// An archive cut at a block boundary reads back as a clean EOF, so the entries
// that survived would import as if nothing were missing. When the reader can
// seek, refuse it instead of importing half a knowledge base.
func TestExtract_rejectsArchiveTruncatedAtBlockBoundary(t *testing.T) {
	arc := buildTar(t, standardArchive())
	stripped := arc[:len(arc)-1024] // drop the end-of-archive marker

	res, err := importer.Extract(bytes.NewReader(stripped), newDest(t).opts())
	if err == nil {
		t.Fatal("expected an error for an archive with no end-of-archive marker")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error should say the archive is truncated, got %q", err)
	}
	if len(res.Written) > 0 {
		t.Errorf("nothing should be written from an incomplete archive, wrote %v", res.Written)
	}
}

// nonSeeker hides the seek capability of the underlying reader.
type nonSeeker struct{ r io.Reader }

func (n nonSeeker) Read(p []byte) (int, error) { return n.r.Read(p) }

// A stream that cannot seek keeps the old behaviour: entries are written as
// they arrive, since completeness cannot be established up front.
func TestExtract_nonSeekableStreamStillExtracts(t *testing.T) {
	d := newDest(t)

	res, err := importer.Extract(nonSeeker{r: bytes.NewReader(buildTar(t, standardArchive()))}, d.opts())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Written) == 0 {
		t.Error("expected entries to be written from a non-seekable stream")
	}
}

// On a non-seekable stream the count comes from the read loop. An entry whose
// body is skipped (a raw copy already in place) and then turns out to be
// truncated must not be counted as complete either.
func TestExtract_streamingCountExcludesTheTruncatedEntry(t *testing.T) {
	d := newDest(t)
	if err := os.MkdirAll(d.rawDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.rawDir, "bbb.md"), []byte("MINE"), 0o600); err != nil {
		t.Fatal(err)
	}
	// raw/bbb.md sorts after raw/aaa.md, so it is entry 2 and its body is
	// skipped because the copy already exists; cut inside that body.
	arc := buildTar(t, map[string]string{
		"raw/aaa.md": "# a",
		"raw/bbb.md": strings.Repeat("d", 4096),
	})
	cut := len(arc) - 1024 - 2048 // inside the second body

	_, err := importer.Extract(nonSeeker{r: bytes.NewReader(arc[:cut])}, d.opts())
	if err == nil {
		t.Fatal("expected an error for a truncated archive")
	}
	if !strings.Contains(err.Error(), "after 1 complete") {
		t.Errorf("only raw/aaa.md is complete, got %q", err)
	}
}

// ── prompt templates ─────────────────────────────────────────────────────────

// Templates are the user's own work and travel with the archive, into whatever
// directory the caller names.
func TestExtract_writesPromptTemplates(t *testing.T) {
	d := newDest(t)
	promptsDir := filepath.Join(d.dir, "prompts")
	opts := d.opts()
	opts.PromptsDir = promptsDir
	arc := buildTar(t, map[string]string{
		"prompts/qa/manifest.yaml":     "name: qa",
		"prompts/mine/manifest.yaml":   "name: mine",
		"prompts/mine/user.tmpl":       "{{ .Question }}",
		"prompts/mine/nested/extra.md": "extra",
	})

	res, err := importer.Extract(bytes.NewReader(arc), opts)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if got := readFile(t, filepath.Join(promptsDir, "mine", "user.tmpl")); got != "{{ .Question }}" {
		t.Errorf("template file = %q", got)
	}
	if got := readFile(t, filepath.Join(promptsDir, "mine", "nested", "extra.md")); got != "extra" {
		t.Errorf("nested template file = %q", got)
	}
	for _, name := range []string{"qa", "mine"} {
		if !contains(res.PromptNames, name) {
			t.Errorf("PromptNames = %v, want it to list %q", res.PromptNames, name)
		}
	}
	if len(res.PromptNames) != 2 {
		t.Errorf("PromptNames = %v, want each template named once", res.PromptNames)
	}
}

// A dry run learns which templates the archive holds without writing them.
func TestExtract_emptyPromptsDirWritesNoTemplates(t *testing.T) {
	d := newDest(t)
	arc := buildTar(t, map[string]string{"prompts/mine/manifest.yaml": "name: mine"})

	res, err := importer.Extract(bytes.NewReader(arc), d.opts()) // PromptsDir unset
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !contains(res.PromptNames, "mine") {
		t.Errorf("PromptNames = %v, want mine reported without writing it", res.PromptNames)
	}
	for _, w := range res.Written {
		if strings.Contains(w, "prompts") {
			t.Errorf("no template should be written, got %q", w)
		}
	}
}

// A loose file directly under prompts/ belongs to no template, so there is
// nothing to name it after: it is left out rather than invented into one.
func TestExtract_ignoresLooseFilesUnderPrompts(t *testing.T) {
	d := newDest(t)
	promptsDir := filepath.Join(d.dir, "prompts")
	opts := d.opts()
	opts.PromptsDir = promptsDir
	arc := buildTar(t, map[string]string{"prompts/stray.txt": "loose"})

	res, err := importer.Extract(bytes.NewReader(arc), opts)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.PromptNames) != 0 {
		t.Errorf("PromptNames = %v, want none", res.PromptNames)
	}
	if _, err := os.Stat(filepath.Join(promptsDir, "stray.txt")); !os.IsNotExist(err) {
		t.Errorf("loose file should not be written, stat err = %v", err)
	}
}

// Templates are overwritten where they land: the caller extracts them to a
// scratch directory it owns and decides what to keep.
func TestExtract_overwritesTemplatesInTheTargetDir(t *testing.T) {
	d := newDest(t)
	promptsDir := filepath.Join(d.dir, "prompts")
	if err := os.MkdirAll(filepath.Join(promptsDir, "qa"), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(promptsDir, "qa", "manifest.yaml")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := d.opts()
	opts.PromptsDir = promptsDir

	if _, err := importer.Extract(bytes.NewReader(buildTar(t, map[string]string{
		"prompts/qa/manifest.yaml": "name: qa",
	})), opts); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := readFile(t, stale); got != "name: qa" {
		t.Errorf("template = %q, want the archive's copy", got)
	}
}
