// Package importer restores a knowledge base from a tar archive written by
// `tbuk export`. It is the inverse of internal/export: each archived component
// (database, extracted-text store, raw archive, prompt templates) is written
// back under a target root, either re-homed at the root's default locations or
// merged into the folders named by an existing target config.
package importer

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
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
	// Keep the opening bytes: if the stream turns out not to be a tar archive,
	// they identify what the user actually pointed us at.
	br := bufio.NewReader(r)
	head, _ := br.Peek(headSize) // a short read is itself diagnostic

	tr := tar.NewReader(br)
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, archiveError(err, entries, head)
		}
		entries++
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
				return res, entryError(err, entries, head)
			}
			continue
		}

		dest, ok := destFor(name, opts.Config)
		if !ok {
			dest = filepath.Join(opts.Root, filepath.FromSlash(name))
		}
		if err := writeFile(dest, tr, opts.Force, &res); err != nil {
			return res, entryError(err, entries, head)
		}
	}
	if entries == 0 {
		return res, archiveError(errNoEntries, entries, head)
	}
	return res, nil
}

// headSize is how many opening bytes are kept for format sniffing — one tar
// block, enough to cover every magic number in formatOf.
const headSize = 512

var errNoEntries = errors.New("no entries")

// archiveError turns a tar-level failure into a message that says what is wrong
// with the file the user named. entries is how many entries were read before
// the failure, and head the archive's opening bytes.
func archiveError(err error, entries int, head []byte) error {
	switch {
	case errors.Is(err, errNoEntries) && len(head) == 0:
		return fmt.Errorf("read archive: file is empty; re-run `tbuk export` to produce a new archive")
	case errors.Is(err, errNoEntries):
		return fmt.Errorf("read archive: archive contains no entries; re-run `tbuk export` to produce a new archive")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("read archive: archive ends mid-entry after %d complete entries; the copy or download is incomplete", entries)
	case errors.Is(err, tar.ErrHeader) && entries == 0:
		if format := formatOf(head); format != "" {
			return fmt.Errorf("read archive: not a tar archive — the file looks like %s data; `tbuk export` writes an uncompressed .tar, so decompress it first: %w", format, err)
		}
		return fmt.Errorf("read archive: not a tar archive; expected a file written by `tbuk export`: %w", err)
	case errors.Is(err, tar.ErrHeader):
		return fmt.Errorf("read archive: archive is corrupt at entry %d; re-run `tbuk export` to produce a new archive: %w", entries+1, err)
	default:
		return fmt.Errorf("read archive: %w", err)
	}
}

// entryError reports a failure while copying an entry out. A short read means
// the archive itself is cut off; anything else is a problem with the
// destination and is passed through unchanged.
func entryError(err error, entries int, head []byte) error {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return archiveError(io.ErrUnexpectedEOF, entries-1, head)
	}
	return err
}

// formatOf names the archive format the opening bytes belong to, or "" when
// they match no format tbuk recognises.
func formatOf(head []byte) string {
	for _, f := range []struct {
		magic []byte
		name  string
	}{
		{[]byte{0x1f, 0x8b}, "gzip"},
		{[]byte("PK\x03\x04"), "zip"},
		{[]byte{0x28, 0xb5, 0x2f, 0xfd}, "zstd"},
		{[]byte("BZh"), "bzip2"},
		{[]byte{0xfd, '7', 'z', 'X', 'Z', 0x00}, "xz"},
		{[]byte("SQLite format 3\x00"), "SQLite database"},
	} {
		if bytes.HasPrefix(head, f.magic) {
			return f.name
		}
	}
	return ""
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
