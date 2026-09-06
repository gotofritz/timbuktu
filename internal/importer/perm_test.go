package importer_test

import (
	"io/fs"
	"os"
	"runtime"
	"testing"
)

// wantPerm asserts that path exists and, on Unix, carries exactly the
// permission bits in want.
//
// Windows has no POSIX mode bits: a file created 0o600 stats back as 0o666,
// and 0o700 directories as 0o777. Comparing them there would fail on every
// owner-only file the code writes. Existence is still checked on every
// platform, so the test does real work everywhere rather than being skipped
// off Unix (issue #121); the owner-only guarantee itself is a Unix one.
func wantPerm(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s perms = %o, want %o", path, got, want)
	}
}
