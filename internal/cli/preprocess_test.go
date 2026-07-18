package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
)

func TestRunPreprocess_text_output(t *testing.T) {
	path := writeTempFile(t, "hello.md", "# Title\n\nSome content here.")

	var buf bytes.Buffer
	if err := cli.RunPreprocess(path, "text", &buf); err != nil {
		t.Fatalf("RunPreprocess: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "MIME:") {
		t.Errorf("output missing MIME line; got %q", out)
	}
	if !strings.Contains(out, "SHA256:") {
		t.Errorf("output missing SHA256 line; got %q", out)
	}
	if !strings.Contains(out, "Chunks:") {
		t.Errorf("output missing Chunks line; got %q", out)
	}
}

func TestRunPreprocess_json_output(t *testing.T) {
	path := writeTempFile(t, "hello.txt", "Plain text content.")

	var buf bytes.Buffer
	if err := cli.RunPreprocess(path, "json", &buf); err != nil {
		t.Fatalf("RunPreprocess: %v", err)
	}

	var result struct {
		Path   string `json:"path"`
		MIME   string `json:"mime"`
		SHA256 string `json:"sha256"`
		Chunks []struct {
			Index  int    `json:"index"`
			Tokens int    `json:"tokens"`
			Text   string `json:"text"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if result.MIME != "text/plain" {
		t.Errorf("MIME = %q, want %q", result.MIME, "text/plain")
	}
	if result.SHA256 == "" {
		t.Error("SHA256 empty")
	}
	if len(result.Chunks) == 0 {
		t.Error("expected at least one chunk")
	}
}

func TestRunPreprocess_missing_file(t *testing.T) {
	var buf bytes.Buffer
	err := cli.RunPreprocess("/no/such/file.txt", "text", &buf)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestRunPreprocess_unknown_mime(t *testing.T) {
	path := writeTempFile(t, "doc.xyz", "content")
	var buf bytes.Buffer
	err := cli.RunPreprocess(path, "text", &buf)
	if err == nil {
		t.Error("expected error for unknown MIME type")
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
