package cli_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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
		"export",
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

// `tbuk context` is what an agent reads before running anything, so every flag
// it advertises has to exist. Renaming a flag and leaving this text behind
// hands agents a command that fails with "unknown flag".
func TestContextCommand_advertisesOnlyRealFlags(t *testing.T) {
	var out bytes.Buffer
	cmd := cli.New()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"context"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("context: %v", err)
	}

	byName := map[string]*cobra.Command{}
	for _, c := range cli.New().Commands() {
		byName[c.Name()] = c
	}

	// Lines look like: "tbuk import <archive>   restore … (--merge, --force-data)"
	line := regexp.MustCompile(`(?m)^tbuk (\w+)\b.*$`)
	flag := regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
	checked := 0
	for _, m := range line.FindAllStringSubmatch(out.String(), -1) {
		sub, ok := byName[m[1]]
		if !ok {
			continue // sub-subcommands (template list, meta set) are described inline
		}
		for _, f := range flag.FindAllStringSubmatch(m[0], -1) {
			name := f[1]
			if sub.Flags().Lookup(name) != nil || cli.New().PersistentFlags().Lookup(name) != nil {
				checked++
				continue
			}
			t.Errorf("context advertises --%s for %q, which has no such flag", name, m[1])
		}
	}
	if checked == 0 {
		t.Fatal("no flags were checked; the line pattern no longer matches the context text")
	}
}
