package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
)

func TestContextCommand_prints(t *testing.T) {
	var buf bytes.Buffer
	cmd := cli.New()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"context"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("context command failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"tbuk",
		"ingest",
		"ask",
		"search",
		"config",
		"template",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("context output missing %q", want)
		}
	}
}

func TestContextCommand_noConfigRequired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("context"); err != nil {
		t.Fatalf("context failed without config: %v", err)
	}
}

func TestContextCommand_inRootHelp(t *testing.T) {
	var buf bytes.Buffer
	cmd := cli.New()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()
	if !strings.Contains(buf.String(), "context") {
		t.Error("root --help does not mention context command")
	}
}
