// Package export writes a portable snapshot of a tbuk knowledge base as a tar
// archive: the config (with machine-specific data paths commented out) plus the
// database, extracted-text store, raw archive and prompt templates that exist
// on disk. An import re-homes each component under its own root.
package export

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gotofritz/timbuktu/internal/config"
)

// ConfigName is the archive entry holding the exported config.
const ConfigName = "config.yaml"

// Create writes a tar archive of the knowledge base described by cfg to w.
//
// The archive holds config.yaml (paths commented out for portability) followed
// by every data component that exists on disk: the database file — with its
// SQLite -wal/-shm sidecars when present, so a snapshot taken in WAL mode
// restores every committed write — the extracted-text store, the raw archive
// and the prompt templates. Each entry is named relative to root, or by its
// basename when the source lives outside root, so `..` never escapes the
// archive. Missing components (a path that does not exist, or a disabled config
// value such as raw_dir="") are skipped without error, so exporting a
// freshly-initialised knowledge base still succeeds.
func Create(w io.Writer, cfg config.Config, root string) error {
	tw := tar.NewWriter(w)

	cfgYAML, err := config.ExportYAML(cfg)
	if err != nil {
		return fmt.Errorf("render export config: %w", err)
	}
	if err := writeEntry(tw, ConfigName, []byte(cfgYAML)); err != nil {
		return err
	}

	// The database file and any WAL/SHM sidecars — a live SQLite database in
	// WAL mode keeps recent commits in the -wal file until a checkpoint.
	if cfg.Database.Path != "" {
		for _, p := range []string{cfg.Database.Path, cfg.Database.Path + "-wal", cfg.Database.Path + "-shm"} {
			if err := addFile(tw, p, archiveName(p, root)); err != nil {
				return err
			}
		}
	}

	for _, dir := range []string{cfg.Preprocess.OutputDir, cfg.Ingest.RawDir, cfg.Prompts.Dir} {
		if err := addDir(tw, dir, root); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	return nil
}

// blockSize is the tar block size; entry bodies are padded up to it, and an
// archive is terminated by two zero blocks.
const blockSize = 512

// Verify reads rs as a tar archive and reports whether it is a complete,
// well-formed export: CheckComplete passes and the config entry is present.
//
// It takes an io.ReadSeeker rather than an io.Reader because the walk seeks from
// header to header — verifying a multi-gigabyte archive costs little more than
// reading its headers — and because completeness cannot be established without
// knowing the file's length (see CheckComplete).
//
// Callers use it to catch a damaged archive at export time, before it is handed
// to a user who would otherwise only discover the damage on import.
func Verify(rs io.ReadSeeker) error {
	sawConfig := false
	if _, err := walkHeaders(rs, func(hdr *tar.Header) {
		if hdr.Name == ConfigName {
			sawConfig = true
		}
	}); err != nil {
		return fmt.Errorf("verify archive: %w", err)
	}
	if !sawConfig {
		return fmt.Errorf("verify archive: missing %s entry", ConfigName)
	}
	return nil
}

// ErrIncomplete reports an archive shorter than the entries it declares.
var ErrIncomplete = errors.New("archive is truncated")

// CheckComplete reports whether rs holds a whole tar archive: every header
// parses, and the file is long enough for each entry body it declares plus the
// two zero blocks that terminate a tar. It returns how many entries are intact,
// which callers use to say where a damaged archive went wrong.
//
// Each entry's extent is checked against the file's length rather than inferred
// from a read failure, which keeps the count honest: an entry whose body is cut
// short is not counted. Bodies are never read — the walk seeks from one header
// to the next — so its cost is O(entries), not O(archive size).
//
// The length comparison is also what catches the case a walk cannot: a tar cut
// at a block boundary, with its terminating blocks gone, reads back as a clean
// EOF, indistinguishable from a complete archive, and its surviving entries
// would be restored as if nothing were missing.
func CheckComplete(rs io.ReadSeeker) (int, error) {
	return walkHeaders(rs, nil)
}

// walkHeaders visits every entry header, checking as it goes that the archive
// is long enough to hold each declared body and the end-of-archive marker. It
// returns the number of intact entries. visit may be nil.
func walkHeaders(rs io.ReadSeeker, visit func(*tar.Header)) (int, error) {
	size, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}

	entries := 0
	var offset int64   // start of the next header block
	var declared int64 // end of the furthest entry body seen
	for {
		if _, err := rs.Seek(offset, io.SeekStart); err != nil {
			return entries, err
		}
		// A fresh reader per header. It parses this header — including any PAX or
		// GNU extension blocks that precede it — and stops at the body, so the
		// walk reads header blocks and nothing else. Seeking to the next header
		// ourselves is what makes that true: reusing one reader would leave it to
		// skip the body on the way to the next entry.
		hdr, err := tar.NewReader(rs).Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return entries, err
		}
		body, err := rs.Seek(0, io.SeekCurrent)
		if err != nil {
			return entries, err
		}
		// Whether this entry's body fits is a separate question from whether the
		// archive was terminated: a body that is whole counts as an intact entry
		// even when the file stops right after it.
		end := body + padded(hdr.Size)
		if end > size {
			return entries, fmt.Errorf("%w: %d bytes present, %d needed for the body of %q",
				ErrIncomplete, size, end, hdr.Name)
		}
		if visit != nil {
			visit(hdr)
		}
		entries++
		offset = end
		declared = end
	}
	// Two zero blocks terminate a tar. Without them the file was cut at a block
	// boundary, which reads back as a clean EOF.
	if want := declared + 2*blockSize; size < want {
		return entries, fmt.Errorf("%w: %d bytes present, %d needed for its entries and the "+
			"end-of-archive marker", ErrIncomplete, size, want)
	}
	return entries, nil
}

// padded rounds an entry size up to a whole number of tar blocks.
func padded(n int64) int64 {
	return (n + blockSize - 1) / blockSize * blockSize
}

// archiveName maps a source path to its archive entry name: relative to root
// when it lives under root, otherwise its basename. Names always use forward
// slashes, the tar convention.
func archiveName(path, root string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, path); err == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

// addDir archives every regular file under dir. Entry names are prefixed with
// dir's own archive name so an outside-root directory keeps its grouping (e.g.
// extracted/abc.txt) rather than flattening. An empty or missing dir is a
// no-op.
func addDir(tw *tar.Writer, dir, root string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return addFile(tw, dir, archiveName(dir, root))
	}
	prefix := archiveName(dir, root)
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return fmt.Errorf("relativise %s: %w", p, err)
		}
		return addFile(tw, p, prefix+"/"+filepath.ToSlash(rel))
	})
}

// addFile copies path into the archive under name. A missing file is skipped
// without error; other stat/open failures propagate.
func addFile(tw *tar.Writer, path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	// Only regular files are archivable content. Directories, sockets, devices
	// and named pipes are skipped — opening a pipe blocks until a writer appears,
	// which would hang the export.
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Mode:     0o600,
		Size:     info.Size(),
		ModTime:  info.ModTime(),
	}); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	// Copy exactly the declared size: capping at info.Size() keeps the stream
	// consistent if the file grows mid-copy, and CopyN surfaces a short read if
	// it shrinks, rather than writing a corrupt archive.
	if _, err := io.CopyN(tw, f, info.Size()); err != nil {
		return fmt.Errorf("tar write %s: %w", name, err)
	}
	return nil
}

// writeEntry writes an in-memory byte slice as a single archive entry.
func writeEntry(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Mode:     0o600,
		Size:     int64(len(data)),
	}); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("tar write %s: %w", name, err)
	}
	return nil
}
