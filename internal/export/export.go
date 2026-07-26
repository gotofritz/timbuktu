// Package export writes a portable snapshot of a tbuk knowledge base as a tar
// archive: the config (with machine-specific data paths commented out) plus the
// database, extracted-text store, raw archive and prompt templates that exist
// on disk. An import re-homes each component under its own root.
package export

import (
	"archive/tar"
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
	if info.IsDir() {
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
