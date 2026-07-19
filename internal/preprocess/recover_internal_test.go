package preprocess

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type panickingExtractor struct{}

func (panickingExtractor) Extract(context.Context, io.Reader) (string, error) {
	panic("simulated parser index-out-of-range")
}

// safeExtract must convert a panic from an extractor (e.g. the PDF parser on a
// malformed file) into an error, so a single bad file cannot crash the whole
// ingest run.
func TestSafeExtract_recoversPanic(t *testing.T) {
	text, err := safeExtract(context.Background(), panickingExtractor{}, strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected error from panicking extractor, got nil")
	}
	if text != "" {
		t.Errorf("text = %q, want empty on panic", text)
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("error = %v, want it to mention panic", err)
	}
}

// Extract must reject a file larger than MaxFileSize before reading it, so a
// stray multi-GB file cannot exhaust memory.
func TestExtract_rejectsOversizeFile(t *testing.T) {
	orig := MaxFileSize
	MaxFileSize = 8 // bytes
	t.Cleanup(func() { MaxFileSize = orig })

	f := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(f, []byte("this content exceeds eight bytes"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, _, err := Extract(context.Background(), f)
	if err == nil {
		t.Fatal("expected oversize error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "too large") {
		t.Errorf("error = %v, want it to mention the file is too large", err)
	}
}

// A file within the limit still extracts normally.
func TestExtract_allowsFileWithinLimit(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ok.md")
	if err := os.WriteFile(f, []byte("hello world"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	text, _, _, err := Extract(context.Background(), f)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(text, "hello world") {
		t.Errorf("text = %q, want it to contain the content", text)
	}
}
