//go:build unix

package cli_test

import (
	"io/fs"
	"os"
	"testing"
)

// wantPerm asserts that path exists and carries exactly the POSIX permission
// bits in want.
//
// Windows has no such bits — a file created 0o600 stats back as 0o666 — so the
// non-unix build of this helper checks only that the path exists. That keeps
// these tests running on every platform instead of being skipped there
// (issue #121); the private-permissions guarantee itself is a Unix one.
func wantPerm(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s perms = %o, want %o", path, got, want)
	}
}
