package config_test

import "testing"

// setHome points the process at dir as the user's home directory for the
// duration of the test, so DefaultRoot resolves to dir/.tbuk.
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
