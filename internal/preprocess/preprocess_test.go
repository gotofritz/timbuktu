package preprocess_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/preprocess"
)

// ── DetectMIME ────────────────────────────────────────────────────────────────

func TestDetectMIME(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"doc.md", "text/markdown"},
		{"doc.markdown", "text/markdown"},
		{"doc.html", "text/html"},
		{"doc.htm", "text/html"},
		{"doc.txt", "text/plain"},
		{"doc.pdf", "application/pdf"},
		{"doc.xyz", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := preprocess.DetectMIME(tt.path)
			if got != tt.want {
				t.Errorf("DetectMIME(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ── NewExtractor ──────────────────────────────────────────────────────────────

func TestNewExtractor_unknown_mime(t *testing.T) {
	_, err := preprocess.NewExtractor("application/unknown")
	if err == nil {
		t.Error("expected error for unknown MIME type, got nil")
	}
}

// ── MarkdownExtractor ─────────────────────────────────────────────────────────

func TestMarkdownExtractor_strips_frontmatter(t *testing.T) {
	input := "---\ntitle: Test\nauthor: Alice\n---\n\nHello world."
	got := mustExtract(t, "text/markdown", input)
	if strings.Contains(got, "title:") {
		t.Errorf("frontmatter not stripped; got %q", got)
	}
	if !strings.Contains(got, "Hello world.") {
		t.Errorf("body missing; got %q", got)
	}
}

func TestMarkdownExtractor_strips_code_fence_markers(t *testing.T) {
	input := "Text.\n\n```go\nfmt.Println(\"hello\")\n```\n\nMore text."
	got := mustExtract(t, "text/markdown", input)
	if strings.Contains(got, "```") {
		t.Errorf("code fence markers not stripped; got %q", got)
	}
	if !strings.Contains(got, `fmt.Println("hello")`) {
		t.Errorf("code content missing; got %q", got)
	}
}

func TestMarkdownExtractor_strips_inline_markup(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bold_asterisk", "Hello **world**.", "Hello world."},
		{"italic_underscore", "Hello _world_.", "Hello world."},
		{"inline_code", "Hello `world`.", "Hello world."},
		{"heading", "# Hello World", "Hello World"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.TrimSpace(mustExtract(t, "text/markdown", tt.input))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ── HTMLExtractor ─────────────────────────────────────────────────────────────

func TestHTMLExtractor_strips_tags(t *testing.T) {
	input := "<h1>Hello</h1><p>World</p>"
	got := mustExtract(t, "text/html", input)
	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Errorf("HTML tags not stripped; got %q", got)
	}
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World") {
		t.Errorf("text content missing; got %q", got)
	}
}

func TestHTMLExtractor_decodes_entities(t *testing.T) {
	input := "<p>Hello &amp; World &lt;3&gt;</p>"
	got := mustExtract(t, "text/html", input)
	if strings.Contains(got, "&amp;") || strings.Contains(got, "&lt;") || strings.Contains(got, "&gt;") {
		t.Errorf("entities not decoded; got %q", got)
	}
	if !strings.Contains(got, "&") {
		t.Errorf("decoded & missing; got %q", got)
	}
}

// ── PlainTextExtractor ────────────────────────────────────────────────────────

func TestPlainTextExtractor_passthrough(t *testing.T) {
	input := "Hello, world!\nLine two."
	got := mustExtract(t, "text/plain", input)
	if got != input {
		t.Errorf("got %q, want %q", got, input)
	}
}

// ── PDFExtractor ──────────────────────────────────────────────────────────────

func TestPDFExtractor_extracts_text(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Skip("testdata/sample.pdf not found; skipping PDF extraction test")
	}
	ex, err := preprocess.NewExtractor("application/pdf")
	if err != nil {
		t.Fatalf("NewExtractor: %v", err)
	}
	got, err := ex.Extract(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.TrimSpace(got) == "" {
		t.Error("expected non-empty text from PDF")
	}
}

// ── SHA256 ────────────────────────────────────────────────────────────────────

func TestHashReader_known_input(t *testing.T) {
	input := "hello world"
	h := sha256.Sum256([]byte(input))
	want := fmt.Sprintf("%x", h)

	got, err := preprocess.HashReader(strings.NewReader(input))
	if err != nil {
		t.Fatalf("HashReader: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHashFile_known_file(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "hash-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	content := "known content"
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256([]byte(content))
	want := fmt.Sprintf("%x", h)

	got, err := preprocess.HashFile(f.Name())
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ── Extract ───────────────────────────────────────────────────────────────────

func TestExtract_markdown(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "*.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("# Title\n\nHello world.")
	_ = f.Close()

	text, mime, sha, err := preprocess.Extract(context.Background(), f.Name())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if mime != "text/markdown" {
		t.Errorf("mime = %q, want text/markdown", mime)
	}
	if sha == "" {
		t.Error("sha empty")
	}
	if !strings.Contains(text, "Hello world.") {
		t.Errorf("text missing content; got %q", text)
	}
}

func TestExtract_unknownMIME(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "*.xyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	_, _, _, err = preprocess.Extract(context.Background(), f.Name())
	if err == nil {
		t.Error("expected error for unknown MIME type")
	}
}

// ── ExtractToFile ─────────────────────────────────────────────────────────────

func TestExtractToFile_savesText(t *testing.T) {
	src, err := os.CreateTemp(t.TempDir(), "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = src.WriteString("plain text content here")
	_ = src.Close()

	outDir := t.TempDir()
	savedPath, err := preprocess.ExtractToFile(context.Background(), src.Name(), outDir)
	if err != nil {
		t.Fatalf("ExtractToFile: %v", err)
	}
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !strings.Contains(string(data), "plain text content here") {
		t.Errorf("saved file missing content; got %q", string(data))
	}
}

func TestExtractToFile_filenameIsSHA256(t *testing.T) {
	src, err := os.CreateTemp(t.TempDir(), "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	content := "deterministic content"
	_, _ = src.WriteString(content)
	_ = src.Close()

	h := sha256.Sum256([]byte(content))
	wantSHA := fmt.Sprintf("%x", h)

	outDir := t.TempDir()
	savedPath, err := preprocess.ExtractToFile(context.Background(), src.Name(), outDir)
	if err != nil {
		t.Fatalf("ExtractToFile: %v", err)
	}
	base := filepath.Base(savedPath)
	if base != wantSHA+".txt" {
		t.Errorf("filename = %q, want %q", base, wantSHA+".txt")
	}
}

func TestExtractToFile_createsOutputDir(t *testing.T) {
	src, err := os.CreateTemp(t.TempDir(), "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = src.WriteString("some content")
	_ = src.Close()

	outDir := filepath.Join(t.TempDir(), "nested", "extracted")
	_, err = preprocess.ExtractToFile(context.Background(), src.Name(), outDir)
	if err != nil {
		t.Fatalf("ExtractToFile: %v", err)
	}
	if _, err := os.Stat(outDir); os.IsNotExist(err) {
		t.Error("output dir not created")
	}
}

func TestHashFile_missing(t *testing.T) {
	if _, err := preprocess.HashFile(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Error("want error for missing file")
	}
}

func TestExtract_missingFile(t *testing.T) {
	// Unknown extension resolves to a known MIME, but the file is absent, so
	// HashFile must surface the error.
	if _, _, _, err := preprocess.Extract(context.Background(), filepath.Join(t.TempDir(), "gone.txt")); err == nil {
		t.Error("want error for missing file")
	}
}

func TestExtractToFile_missingSource(t *testing.T) {
	if _, err := preprocess.ExtractToFile(context.Background(), filepath.Join(t.TempDir(), "gone.txt"), t.TempDir()); err == nil {
		t.Error("want error for missing source")
	}
}

func TestExtractToFile_mkdirFails(t *testing.T) {
	src, err := os.CreateTemp(t.TempDir(), "*.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = src.WriteString("body")
	_ = src.Close()

	// Point outputDir under an existing regular file so MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(blocker, "sub")
	if _, err := preprocess.ExtractToFile(context.Background(), src.Name(), outDir); err == nil {
		t.Error("want error when output dir cannot be created")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustExtract(t *testing.T, mime, content string) string {
	t.Helper()
	ex, err := preprocess.NewExtractor(mime)
	if err != nil {
		t.Fatalf("NewExtractor(%q): %v", mime, err)
	}
	out, err := ex.Extract(context.Background(), strings.NewReader(content))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return out
}
