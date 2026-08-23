// Package importer reads a tar archive written by `tbuk export` and takes from
// it the two things a knowledge base can be rebuilt from on any machine: the
// archived source files under raw/, and the database, copied aside to be read
// as a manifest of what those files were.
//
// It deliberately takes nothing else. A config describes the machine it lives
// on — its paths, providers and models — so adopting another machine's config
// is never right; the extracted-text cache is re-derivable from the raw bytes;
// and prompt templates belong to the machine that runs them. Embeddings in the
// archived database are not read at all: they are re-derived locally, because
// vectors made by a different model cannot be searched here.
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

	"github.com/gotofritz/timbuktu/internal/export"
)

// Canonical archive-relative names, matching what a default `tbuk export`
// produces. Data paths in the exported config are commented out, so the archive
// always uses these default names regardless of where the source stored each
// component.
const (
	dbName      = "tbuk.sqlite"
	rawRoot     = "raw"
	promptsRoot = "prompts"
)

// Options controls what Extract writes and where.
type Options struct {
	// RawDir is where the archive's raw/ entries are written, keeping any
	// sub-path below raw/. Empty writes none of them — enough for a dry run,
	// which still learns what the archive holds from Result.RawNames.
	RawDir string
	// DBDest is where the archive's database (and its WAL/SHM sidecars) is
	// written, for the caller to open as a manifest. This is a path the caller
	// owns — a temp file, never a live knowledge base — so an existing file
	// there is overwritten. Empty skips the database.
	DBDest string
	// PromptsDir is where the archive's prompt templates are written, keeping
	// their <name>/... layout. Empty writes none of them — again enough for a
	// dry run, which learns the names from Result.PromptNames. Existing files
	// there are overwritten, so point this at a directory you own and decide
	// per template what to keep: whether a template should replace one already
	// installed is the caller's policy, not this package's.
	PromptsDir string
}

// Result reports what the archive held and what was written.
type Result struct {
	// Written lists the destinations actually written. A raw copy already in
	// place is not among them.
	Written []string
	// RawNames lists every raw entry's path below raw/, whether or not it was
	// written, so a caller can tell which documents the archive can supply.
	RawNames []string
	// PromptNames lists each prompt template the archive holds, once, whether
	// or not it was written. A template is a directory below prompts/, so a
	// loose file directly under prompts/ belongs to none and is left out.
	PromptNames []string
	// DBPath is where the archive's database was written, empty when the
	// archive carried none or Options.DBDest was empty.
	DBPath string
}

// Extract reads the tar archive in r, writing its raw/ entries under
// opts.RawDir, its prompt templates under opts.PromptsDir, and its database to
// opts.DBDest. Every other entry — the config, the extracted-text cache,
// anything unrecognised — is read past and discarded.
//
// A raw copy already present is left untouched: raw names are content-addressed
// (<sha256><ext>), so the same name means the same bytes. Entries that would
// escape their destination via an absolute path or `..` are rejected.
func Extract(r io.Reader, opts Options) (Result, error) {
	var res Result
	// A seekable source can be checked for completeness before a single file is
	// written: an archive cut at a block boundary reads back as a clean EOF, and
	// importing the entries that survived would leave a knowledge base silently
	// missing the rest. A stream that cannot seek is read as it arrives.
	if rs, ok := r.(io.ReadSeeker); ok {
		head, err := peekHead(rs)
		if err != nil {
			return res, fmt.Errorf("read archive: %w", err)
		}
		if n, err := export.CheckComplete(rs); err != nil {
			// Same classification the streaming path uses: a stray .tar.gz is not
			// a truncated archive, whatever the tar reader says.
			return res, archiveError(err, n, head)
		}
		if _, err := rs.Seek(0, io.SeekStart); err != nil {
			return res, fmt.Errorf("rewind archive: %w", err)
		}
	}

	// Keep the opening bytes: if the stream turns out not to be a tar archive,
	// they identify what the user actually pointed us at.
	br := bufio.NewReader(r)
	peeked, _ := br.Peek(headSize) // a short read is itself diagnostic
	// Peek returns a window into the reader's own buffer, which later reads
	// refill; these bytes outlive those reads, so they have to be a copy.
	head := append([]byte(nil), peeked...)

	tr := tar.NewReader(br)
	entries := 0
	// Set while an entry's body has been left unread: tar.Next consumes it on the
	// way to the next header, so a failure there belongs to that entry, which is
	// then not one of the complete ones.
	bodyPending := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if bodyPending && errors.Is(err, io.ErrUnexpectedEOF) {
				entries--
			}
			return res, archiveError(err, entries, head)
		}
		entries++
		bodyPending = true
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(hdr.Name)
		if !safeEntryName(name) {
			return res, fmt.Errorf("unsafe archive entry %q", hdr.Name)
		}

		if rest, ok := rawRel(name); ok {
			// Recorded whether or not it is written, so a dry run still learns
			// which documents the archive can supply.
			res.RawNames = append(res.RawNames, rest)
		}
		if tmpl, ok := promptName(name); ok && !contains(res.PromptNames, tmpl) {
			res.PromptNames = append(res.PromptNames, tmpl)
		}

		dest, skipIfPresent, want := destFor(name, opts)
		if !want {
			continue // config, extracted text, prompts, anything else: not ours
		}
		consumed, err := writeFile(dest, tr, skipIfPresent, &res)
		if err != nil {
			return res, entryError(err, entries, head)
		}
		if name == dbName {
			res.DBPath = dest
		}
		bodyPending = !consumed
	}
	if entries == 0 {
		return res, archiveError(errNoEntries, entries, head)
	}
	return res, nil
}

// destFor maps a slash-separated archive entry to where Extract should put it.
// want is false for every entry this package deliberately ignores.
// skipIfPresent marks the content-addressed raw copies, where an existing file
// is the same file; the database destination is the caller's own temp path and
// is always written.
func destFor(name string, opts Options) (dest string, skipIfPresent, want bool) {
	switch name {
	case dbName, dbName + "-wal", dbName + "-shm":
		if opts.DBDest == "" {
			return "", false, false
		}
		return opts.DBDest + strings.TrimPrefix(name, dbName), false, true
	}
	if rest, ok := rawRel(name); ok {
		if opts.RawDir == "" {
			return "", false, false
		}
		return filepath.Join(opts.RawDir, filepath.FromSlash(rest)), true, true
	}
	if rest, ok := promptRel(name); ok {
		if opts.PromptsDir == "" {
			return "", false, false
		}
		return filepath.Join(opts.PromptsDir, filepath.FromSlash(rest)), false, true
	}
	return "", false, false
}

// rawRel returns an entry's path below raw/, and whether it is a raw entry.
func rawRel(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, rawRoot+"/")
	return rest, ok && rest != ""
}

// promptRel returns an entry's path below prompts/, and whether it belongs to a
// template. A file directly under prompts/ has no template to belong to.
func promptRel(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, promptsRoot+"/")
	if !ok || !strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

// promptName returns the template an entry belongs to — the first path segment
// below prompts/ — and whether it belongs to one at all.
func promptName(name string) (string, bool) {
	rest, ok := promptRel(name)
	if !ok {
		return "", false
	}
	first, _, _ := strings.Cut(rest, "/")
	return first, first != ""
}

// contains reports whether list already holds want.
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// peekHead reads the opening bytes used for format sniffing and rewinds.
func peekHead(rs io.ReadSeeker) ([]byte, error) {
	head := make([]byte, headSize)
	n, err := io.ReadFull(rs, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return head[:n], nil
}

// headSize is how many opening bytes are kept for format sniffing — one tar
// block, enough to cover every magic number in formatOf.
const headSize = 512

var errNoEntries = errors.New("no entries")

// archiveError turns a tar-level failure into a message that says what is wrong
// with the file the user named. entries is how many entries were read before
// the failure, and head the archive's opening bytes.
//
// Nothing about the file is established yet while entries is 0, so those cases
// are classified first: a file shorter than one tar block fails with
// io.ErrUnexpectedEOF exactly as a truncated archive does, and calling a stray
// .tar.gz "truncated" sends the user looking for a broken copy that does not
// exist.
func archiveError(err error, entries int, head []byte) error {
	if entries == 0 {
		if e := openingError(err, head); e != nil {
			return e
		}
	}
	switch {
	case errors.Is(err, export.ErrIncomplete):
		return fmt.Errorf("read archive: %w; the copy or download is incomplete", err)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("read archive: archive ends mid-entry after %d complete entries; the copy or download is incomplete", entries)
	case errors.Is(err, tar.ErrHeader):
		return fmt.Errorf("read archive: archive is corrupt at entry %d; re-run `tbuk export` to produce a new archive: %w", entries+1, err)
	default:
		return fmt.Errorf("read archive: %w", err)
	}
}

// openingError classifies a failure on the archive's very first entry, where
// the file may well not be a tar archive at all. It returns nil when the
// failure is better described by the generic cases.
func openingError(err error, head []byte) error {
	switch {
	case len(head) == 0:
		return fmt.Errorf("read archive: file is empty; re-run `tbuk export` to produce a new archive")
	case errors.Is(err, errNoEntries):
		return fmt.Errorf("read archive: archive contains no entries; re-run `tbuk export` to produce a new archive")
	}
	if format := formatOf(head); format != "" {
		return fmt.Errorf("read archive: not a tar archive — the file looks like %s data; `tbuk export` writes an uncompressed .tar, so decompress it first", format)
	}
	if len(head) < headSize {
		return fmt.Errorf("read archive: not a tar archive — the file is %d bytes, too short to hold a tar header; expected a file written by `tbuk export`", len(head))
	}
	if errors.Is(err, tar.ErrHeader) {
		return fmt.Errorf("read archive: not a tar archive; expected a file written by `tbuk export`: %w", err)
	}
	return nil
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

// writeFile copies the current archive entry to dest, creating parent dirs.
// With skipIfPresent an existing destination is left intact and not recorded as
// written; otherwise dest is overwritten. Written files are owner-only (0o600).
// The bool reports whether the entry's body was read, which the caller needs to
// attribute a later short read to the right entry.
func writeFile(dest string, r io.Reader, skipIfPresent bool, res *Result) (bool, error) {
	if skipIfPresent {
		switch _, err := os.Stat(dest); {
		case err == nil:
			return false, nil
		case !os.IsNotExist(err):
			return false, fmt.Errorf("stat %s: %w", dest, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return false, fmt.Errorf("create dir for %s: %w", dest, err)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("create %s: %w", dest, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return true, fmt.Errorf("write %s: %w", dest, err)
	}
	if err := f.Close(); err != nil {
		return true, fmt.Errorf("close %s: %w", dest, err)
	}
	res.Written = append(res.Written, dest)
	return true, nil
}
