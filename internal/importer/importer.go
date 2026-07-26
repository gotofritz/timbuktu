// Package importer restores a knowledge base from a tar archive written by
// `tbuk export`. It is the inverse of internal/export: each archived component
// (database, extracted-text store, raw archive, prompt templates) is written
// back under a target root, either re-homed at the root's default locations or
// merged into the folders named by an existing target config.
package importer

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/export"
)

// Canonical archive-relative component names, matching what a default
// `tbuk export` produces. Data paths in the exported config are commented out,
// so the archive always uses these default names regardless of where the source
// stored each component.
const (
	dbName        = "tbuk.sqlite"
	extractedRoot = "extracted"
	rawRoot       = "raw"
	promptsRoot   = "prompts"
)

// Options controls where an archive's entries are written.
type Options struct {
	// Root is the target data root. Entries that do not match a known component
	// (or whose component is disabled in Config) are written verbatim under it.
	Root string
	// Config supplies the destination path for each component. In merge mode this
	// is the target's existing config; otherwise it is the root's defaults.
	Config config.Config
	// ConfigDest is where the archive's config.yaml is written. Empty means the
	// archive config is discarded and the target's existing config is kept
	// (merge mode).
	ConfigDest string
	// Force overwrites existing destination files. When false, an existing file
	// is left in place and recorded in Result.Skipped.
	Force bool
}

// Result reports the destinations written and skipped.
type Result struct {
	Written []string
	Skipped []string
}

// Import extracts the tar archive in r according to opts. Each entry is mapped
// to a destination under opts.Config's component paths (database, extracted
// store, raw archive, prompts) or, failing that, verbatim under opts.Root.
// Existing files are overwritten only with opts.Force, otherwise skipped.
// Entries that would escape the destination via an absolute path or `..` are
// rejected.
func Import(r io.Reader, opts Options) (Result, error) {
	var res Result
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(hdr.Name)
		if !safeEntryName(name) {
			return res, fmt.Errorf("unsafe archive entry %q", hdr.Name)
		}

		if name == export.ConfigName {
			if opts.ConfigDest == "" {
				continue // merge: keep the target's existing config
			}
			if err := writeFile(opts.ConfigDest, tr, opts.Force, &res); err != nil {
				return res, err
			}
			continue
		}

		dest, ok := destFor(name, opts.Config)
		if !ok {
			dest = filepath.Join(opts.Root, filepath.FromSlash(name))
		}
		if err := writeFile(dest, tr, opts.Force, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// destFor maps a slash-separated archive entry name to its destination under
// cfg's component paths. It returns ok=false when the entry belongs to no known
// component, or to one that is disabled (empty path) in cfg, so the caller can
// fall back to placing it verbatim under the root.
func destFor(name string, cfg config.Config) (string, bool) {
	switch name {
	case dbName:
		return cfg.Database.Path, cfg.Database.Path != ""
	case dbName + "-wal":
		return cfg.Database.Path + "-wal", cfg.Database.Path != ""
	case dbName + "-shm":
		return cfg.Database.Path + "-shm", cfg.Database.Path != ""
	}
	for _, c := range []struct{ prefix, base string }{
		{extractedRoot, cfg.Preprocess.OutputDir},
		{rawRoot, cfg.Ingest.RawDir},
		{promptsRoot, cfg.Prompts.Dir},
	} {
		rest, ok := strings.CutPrefix(name, c.prefix+"/")
		if !ok {
			continue
		}
		if c.base == "" {
			return "", false
		}
		return filepath.Join(c.base, filepath.FromSlash(rest)), true
	}
	return "", false
}

// safeEntryName reports whether a cleaned archive entry name stays within its
// destination — no absolute path and no `..` escape.
func safeEntryName(name string) bool {
	if name == "" || name == "." {
		return false
	}
	if name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	if path.IsAbs(name) || filepath.IsAbs(filepath.FromSlash(name)) {
		return false
	}
	return true
}

// writeFile copies the current archive entry to dest, creating parent dirs. An
// existing dest is overwritten only when force is set; otherwise it is left
// intact and recorded as skipped. Written files are owner-only (0o600).
func writeFile(dest string, r io.Reader, force bool, res *Result) error {
	if !force {
		switch _, err := os.Stat(dest); {
		case err == nil:
			res.Skipped = append(res.Skipped, dest)
			return nil
		case !os.IsNotExist(err):
			return fmt.Errorf("stat %s: %w", dest, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("create dir for %s: %w", dest, err)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
	}
	res.Written = append(res.Written, dest)
	return nil
}
