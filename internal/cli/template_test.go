package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifest creates prompts/<name>/manifest.yaml under HOME and returns its
// path.
func writeManifest(t *testing.T, home, name, body string) string {
	t.Helper()
	dir := filepath.Join(home, ".tbuk", "prompts", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeEditor writes a shell script that appends marker to its first argument,
// and returns its path. It stands in for $EDITOR so a test can prove the editor
// was launched against the manifest path.
func fakeEditor(t *testing.T, marker string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-editor.sh")
	script := "#!/bin/sh\nprintf '%s' '" + marker + "' >> \"$1\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTemplateEdit_launchesEditorOnManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manifest := writeManifest(t, home, "qa", "name: qa\n")
	t.Setenv("EDITOR", fakeEditor(t, "EDITED"))

	if err := runCLI("template", "edit", "qa"); err != nil {
		t.Fatalf("template edit: %v", err)
	}

	got, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "EDITED") {
		t.Errorf("editor did not run against manifest; content = %q", got)
	}
}

func TestTemplateEdit_honorsConfiguredPromptsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A prompts root well outside ~/.tbuk/prompts: the command must find the
	// template here only if it reads prompts.dir from config, not the hardcoded
	// default.
	customRoot := filepath.Join(t.TempDir(), "myprompts")
	dir := filepath.Join(customRoot, "qa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifest, []byte("name: qa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("prompts:\n  dir: "+customRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", fakeEditor(t, "EDITED"))

	if err := runCLI("--config", cfgPath, "template", "edit", "qa"); err != nil {
		t.Fatalf("template edit: %v", err)
	}

	got, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "EDITED") {
		t.Errorf("editor did not run against configured-dir manifest; content = %q", got)
	}
}

func TestTemplateEdit_missingTemplateErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EDITOR", fakeEditor(t, "X"))

	if err := runCLI("template", "edit", "does-not-exist"); err == nil {
		t.Fatal("expected error for missing template, got nil")
	}
}

func TestTemplateEdit_editorFailurePropagates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeManifest(t, home, "qa", "name: qa\n")
	t.Setenv("EDITOR", "false") // exits non-zero

	if err := runCLI("template", "edit", "qa"); err == nil {
		t.Fatal("expected editor failure to propagate, got nil")
	}
}
