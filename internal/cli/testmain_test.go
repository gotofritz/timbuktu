package cli_test

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

// A test turns this binary into a stand-in $EDITOR by setting these variables:
// TestMain reads them before the suite runs, and `template edit` inherits them
// when it launches the editor. The alternative — writing a /bin/sh script —
// does not run on Windows, and re-executing the test binary is already how
// TestExecute_exitCodeOnError drives a subprocess (issue #121).
const (
	// fakeEditorMarkEnv, when present, makes this process act as the editor:
	// it appends the variable's value to the file it is given and exits.
	fakeEditorMarkEnv = "TBUK_FAKE_EDITOR_MARK"
	// fakeEditorExitEnv overrides the editor's exit code, for the path where a
	// failing editor must propagate.
	fakeEditorExitEnv = "TBUK_FAKE_EDITOR_EXIT"
)

func TestMain(m *testing.M) {
	if mark, ok := os.LookupEnv(fakeEditorMarkEnv); ok {
		os.Exit(runFakeEditor(mark, os.Getenv(fakeEditorExitEnv), os.Args))
	}
	os.Exit(m.Run())
}

// runFakeEditor appends mark to the file named by the final argument — the path
// launchEditor appends to $EDITOR — and returns the process exit code.
func runFakeEditor(mark, exitCode string, args []string) int {
	if code, err := strconv.Atoi(exitCode); err == nil && code != 0 {
		return code
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "fake editor: no file argument")
		return 2
	}
	path := args[len(args)-1]
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake editor: %v\n", err)
		return 2
	}
	if _, err := f.WriteString(mark); err != nil {
		_ = f.Close()
		fmt.Fprintf(os.Stderr, "fake editor: %v\n", err)
		return 2
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "fake editor: %v\n", err)
		return 2
	}
	return 0
}

// fakeEditor returns an $EDITOR command that appends marker to the manifest it
// is opened on, so a test can prove the editor was launched against that file.
// The marker travels in the environment rather than on the command line
// because launchEditor splits $EDITOR on whitespace.
func fakeEditor(t *testing.T, marker string) string {
	t.Helper()
	t.Setenv(fakeEditorMarkEnv, marker)
	return os.Args[0]
}

// failingEditor returns an $EDITOR command that exits non-zero without editing
// anything — the portable stand-in for /bin/false.
func failingEditor(t *testing.T) string {
	t.Helper()
	t.Setenv(fakeEditorMarkEnv, "")
	t.Setenv(fakeEditorExitEnv, "1")
	return os.Args[0]
}
