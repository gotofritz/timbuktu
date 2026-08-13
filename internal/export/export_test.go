package export_test

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/export"
)

// readTar reads every regular-file entry from a tar stream into name→content.
func readTar(t *testing.T, r io.Reader) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read entry %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = data
	}
	return out
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func TestCreate_archivesConfigAndDataFolders(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)

	writeFile(t, cfg.Database.Path, []byte("SQLITE-BYTES"))
	writeFile(t, filepath.Join(cfg.Preprocess.OutputDir, "abc.txt"), []byte("extracted text"))
	writeFile(t, filepath.Join(cfg.Ingest.RawDir, "abc.pdf"), []byte("%PDF raw"))
	writeFile(t, filepath.Join(cfg.Prompts.Dir, "qa", "manifest.yaml"), []byte("name: qa"))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	entries := readTar(t, &buf)

	want := map[string]string{
		"tbuk.sqlite":              "SQLITE-BYTES",
		"extracted/abc.txt":        "extracted text",
		"raw/abc.pdf":              "%PDF raw",
		"prompts/qa/manifest.yaml": "name: qa",
	}
	for name, content := range want {
		got, ok := entries[name]
		if !ok {
			t.Errorf("archive missing entry %q; have %v", name, keys(entries))
			continue
		}
		if string(got) != content {
			t.Errorf("entry %q = %q, want %q", name, got, content)
		}
	}

	cfgEntry, ok := entries["config.yaml"]
	if !ok {
		t.Fatalf("archive missing config.yaml; have %v", keys(entries))
	}
	if !strings.Contains(string(cfgEntry), "# path:") {
		t.Errorf("archived config.yaml should comment data paths, got:\n%s", cfgEntry)
	}
}

func TestCreate_usesForwardSlashNames(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	writeFile(t, filepath.Join(cfg.Prompts.Dir, "qa", "system.tmpl"), []byte("x"))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for name := range readTar(t, &buf) {
		if strings.Contains(name, "\\") {
			t.Errorf("archive name must use forward slashes, got %q", name)
		}
	}
}

func TestCreate_skipsMissingComponents(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create on empty KB: %v", err)
	}
	entries := readTar(t, &buf)
	if _, ok := entries["config.yaml"]; !ok {
		t.Errorf("config.yaml expected even for an empty KB; have %v", keys(entries))
	}
	if len(entries) != 1 {
		t.Errorf("empty KB should archive only config.yaml, got %v", keys(entries))
	}
}

func TestCreate_disabledRawDirSkipped(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	cfg.Ingest.RawDir = "" // archiving disabled
	writeFile(t, cfg.Database.Path, []byte("db"))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for name := range readTar(t, &buf) {
		if strings.HasPrefix(name, "raw/") {
			t.Errorf("disabled raw dir must not be archived, got %q", name)
		}
	}
}

func TestCreate_includesSQLiteSidecars(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	writeFile(t, cfg.Database.Path, []byte("main"))
	writeFile(t, cfg.Database.Path+"-wal", []byte("wal"))
	writeFile(t, cfg.Database.Path+"-shm", []byte("shm"))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	entries := readTar(t, &buf)
	for _, name := range []string{"tbuk.sqlite", "tbuk.sqlite-wal", "tbuk.sqlite-shm"} {
		if _, ok := entries[name]; !ok {
			t.Errorf("archive missing SQLite sidecar %q; have %v", name, keys(entries))
		}
	}
}

func TestCreate_componentOutsideRootUsesBasename(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	cfg.Database.Path = filepath.Join(other, "external.sqlite")
	cfg.Preprocess.OutputDir = filepath.Join(other, "cache")
	writeFile(t, cfg.Database.Path, []byte("db"))
	writeFile(t, filepath.Join(cfg.Preprocess.OutputDir, "z.txt"), []byte("t"))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	entries := readTar(t, &buf)
	if _, ok := entries["external.sqlite"]; !ok {
		t.Errorf("outside-root db should be archived by basename; have %v", keys(entries))
	}
	if _, ok := entries["cache/z.txt"]; !ok {
		t.Errorf("outside-root dir should keep its basename prefix; have %v", keys(entries))
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCreate_writerErrorPropagates(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	if err := export.Create(failWriter{}, cfg, root); err == nil {
		t.Fatal("expected error when the archive writer fails")
	}
}

func TestCreate_dbPathStatErrorPropagates(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "afile")
	writeFile(t, file, []byte("x"))
	cfg := config.DefaultsForRoot(root)
	cfg.Database.Path = filepath.Join(file, "nested.sqlite") // parent is a file → ENOTDIR

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err == nil {
		t.Fatal("expected error when a component path cannot be stat'd")
	}
}

func TestCreate_dirComponentStatErrorPropagates(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notadir")
	writeFile(t, file, []byte("x"))
	cfg := config.DefaultsForRoot(root)
	cfg.Prompts.Dir = filepath.Join(file, "sub") // parent is a file → ENOTDIR

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err == nil {
		t.Fatal("expected error when a directory component cannot be stat'd")
	}
}

func TestCreate_fileWhereDirExpectedIsArchived(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	promptsFile := filepath.Join(root, "prompts") // a regular file, not a dir
	writeFile(t, promptsFile, []byte("oops"))
	cfg.Prompts.Dir = promptsFile

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := readTar(t, &buf)["prompts"]; !ok {
		t.Error("a file configured where a directory is expected should be archived as 'prompts'")
	}
}

func TestVerify_acceptsArchiveFromCreate(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	writeFile(t, cfg.Database.Path, []byte("db"))
	writeFile(t, filepath.Join(cfg.Preprocess.OutputDir, "a.txt"), []byte("text"))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := export.Verify(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerify_rejectsCorruptArchive(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	writeFile(t, cfg.Database.Path, []byte("db"))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	data := buf.Bytes()
	// Flip a byte in the database entry's header block: the stored checksum no
	// longer matches, which is exactly how a damaged archive presents itself.
	// LastIndex: the exported config mentions the path too, in its body.
	at := bytes.LastIndex(data, []byte("tbuk.sqlite"))
	if at < 0 {
		t.Fatal("archive missing the database entry")
	}
	data[at] ^= 0xff

	if err := export.Verify(bytes.NewReader(data)); err == nil {
		t.Fatal("expected Verify to reject a corrupt archive")
	}
}

func TestVerify_rejectsTruncatedArchive(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	writeFile(t, cfg.Database.Path, bytes.Repeat([]byte("d"), 4096))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	half := buf.Bytes()[:buf.Len()/2]

	if err := export.Verify(bytes.NewReader(half)); err == nil {
		t.Fatal("expected Verify to reject a truncated archive")
	}
}

func TestVerify_rejectsArchiveWithoutConfig(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "tbuk.sqlite", Mode: 0o600, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("db")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	err := export.Verify(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("expected Verify to reject an archive without config.yaml")
	}
	if !strings.Contains(err.Error(), export.ConfigName) {
		t.Errorf("error should name the missing entry, got %q", err)
	}
}

// countingSeeker reports how many bytes were actually read, so the test can
// tell a header-only walk from a full re-read.
type countingSeeker struct {
	rs   io.ReadSeeker
	read int64
}

func (c *countingSeeker) Read(p []byte) (int, error) {
	n, err := c.rs.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *countingSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.rs.Seek(offset, whence)
}

// Verify must not stream entry bodies when the reader can seek: archive/tar
// skips over them, which is what keeps verification cheap on a large export.
func TestVerify_skipsEntryBodiesOnASeeker(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultsForRoot(root)
	writeFile(t, cfg.Database.Path, bytes.Repeat([]byte("d"), 1<<20))
	writeFile(t, filepath.Join(cfg.Preprocess.OutputDir, "a.txt"), bytes.Repeat([]byte("t"), 1<<20))

	var buf bytes.Buffer
	if err := export.Create(&buf, cfg, root); err != nil {
		t.Fatalf("Create: %v", err)
	}
	archive := buf.Bytes()

	counter := &countingSeeker{rs: bytes.NewReader(archive)}
	if err := export.Verify(counter); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Headers, the small config entry and a probe byte per skipped body only —
	// nothing close to the 2 MiB of file data in the archive.
	if counter.read > int64(len(archive))/4 {
		t.Errorf("Verify read %d of %d bytes; entry bodies should be seeked over", counter.read, len(archive))
	}
}
