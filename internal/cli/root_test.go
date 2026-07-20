package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An invalid config must fail every command fast via the root
// PersistentPreRunE, with a message that points at the config (P1-17).
func TestRoot_invalidConfigFailsFast(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	// overlap >= size: chunks would never advance.
	content := "chunking:\n  size: 100\n  overlap: 100\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCLI("--config", cfgPath, "search", "hello", "--mode", "keyword")
	if err == nil {
		t.Fatal("expected invalid config to fail the command")
	}
	if !strings.Contains(err.Error(), "invalid config") {
		t.Errorf("error = %v, want it to mention 'invalid config'", err)
	}
}
