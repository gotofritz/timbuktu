package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
)

func TestPreviewExtracted_containsText(t *testing.T) {
	path := writeTempFile(t, "hello.md", "# Title\n\nSome content here.")

	var buf bytes.Buffer
	if err := cli.PreviewExtracted(path, &buf); err != nil {
		t.Fatalf("PreviewExtracted: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "MIME:") {
		t.Errorf("output missing MIME line; got %q", out)
	}
	if !strings.Contains(out, "SHA256:") {
		t.Errorf("output missing SHA256 line; got %q", out)
	}
}

func TestPreviewExtracted_missingFile(t *testing.T) {
	var buf bytes.Buffer
	err := cli.PreviewExtracted("/no/such/file.txt", &buf)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestPreviewExtracted_unknownMIME(t *testing.T) {
	path := writeTempFile(t, "doc.xyz", "content")
	var buf bytes.Buffer
	err := cli.PreviewExtracted(path, &buf)
	if err == nil {
		t.Error("expected error for unknown MIME type")
	}
}

func TestSaveExtracted_savesToDir(t *testing.T) {
	path := writeTempFile(t, "hello.txt", "plain text content here")
	outDir := t.TempDir()

	savedPath, err := cli.SaveExtracted(path, outDir)
	if err != nil {
		t.Fatalf("SaveExtracted: %v", err)
	}
	if _, err := os.Stat(savedPath); err != nil {
		t.Errorf("saved file not found: %v", err)
	}
	data, _ := os.ReadFile(savedPath)
	if !strings.Contains(string(data), "plain text content here") {
		t.Errorf("saved file missing content; got %q", string(data))
	}
}

func TestPreprocessCommand_savesFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	if err := runCLI("--config", cfgPath, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	srcFile := writeTempFile(t, "doc.txt", "some text content to preprocess")
	outDir := t.TempDir()

	err := runCLI("--config", cfgPath, "preprocess", srcFile, "--output-dir", outDir)
	if err != nil {
		t.Fatalf("preprocess: %v", err)
	}
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 1 {
		t.Errorf("expected 1 file in outDir, got %d", len(entries))
	}
}

func TestPreprocessCommand_dryRun(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	if err := runCLI("--config", cfgPath, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	srcFile := writeTempFile(t, "doc.txt", "dry run content here")
	outDir := t.TempDir()

	err := runCLI("--config", cfgPath, "preprocess", srcFile, "--dry-run", "--output-dir", outDir)
	if err != nil {
		t.Fatalf("preprocess --dry-run: %v", err)
	}
	// dry-run must not write to outDir
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Errorf("dry-run wrote files to outDir: %d files", len(entries))
	}
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
