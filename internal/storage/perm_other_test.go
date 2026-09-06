//go:build !unix

package storage_test

import (
	"io/fs"
	"os"
	"testing"
)

// wantPerm checks only that path exists: see the unix build for why the
// permission bits are not asserted off Unix.
func wantPerm(t *testing.T, path string, _ fs.FileMode) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
}
