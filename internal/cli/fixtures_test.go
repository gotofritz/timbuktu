package cli_test

import (
	"path/filepath"
	"testing"
)

// setHome points the process at dir as the user's home directory for the
// duration of the test, so config.DefaultRoot resolves to dir/.tbuk.
//
// Both variables are needed: os.UserHomeDir reads $HOME on Unix and
// %USERPROFILE% on Windows. Setting only HOME left the Windows suite pointed
// at the real profile directory, which is why the test matrix used to stop at
// Linux and macOS (issue #121).
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// docPath spells p — written with forward slashes, the way a document path is
// written in a fixture — as an absolute path for this platform, so a row seeded
// with it matches what the command boundary looks up. NormalizePath is
// filepath.Abs, and on Windows that resolves "/tmp/a.md" against the current
// drive to "D:\\tmp\\a.md", which a row keyed by the literal never matches
// (issue #121). On Unix p comes back unchanged.
func docPath(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.FromSlash(p))
	if err != nil {
		t.Fatalf("abs %s: %v", p, err)
	}
	return abs
}
