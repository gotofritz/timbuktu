package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
)

func runCLI(args ...string) error {
	cmd := cli.New()
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestInitCommand_createsDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	expected := []string{
		filepath.Join(home, ".tbuk"),
		filepath.Join(home, ".tbuk", "prompts", "qa"),
	}
	for _, dir := range expected {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("expected dir to exist: %s", dir)
		}
	}
}

func TestInitCommand_writesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if len(data) == 0 {
		t.Error("config file is empty")
	}
}

func TestInitCommand_idempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	// write sentinel to config
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	sentinel := "# sentinel\n"
	if err := os.WriteFile(cfgPath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCLI("init"); err != nil {
		t.Fatalf("second init failed: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sentinel {
		t.Error("second init overwrote existing config")
	}
}

func TestInitCommand_writesQATemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	for _, name := range []string{"manifest.yaml", "system.tmpl", "user.tmpl"} {
		path := filepath.Join(home, ".tbuk", "prompts", "qa", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected template file: %s", path)
		}
	}
}
