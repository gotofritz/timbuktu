package eval_test

import (
	"os"
	"testing"
)

// writeFile writes content to path, failing the test if it cannot.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
