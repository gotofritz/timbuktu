package preprocess

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaxFileSize is the largest file (in bytes) Extract will read. A stray or
// hostile multi-GB file is rejected before any bytes are read, so extraction
// cannot exhaust memory. Adjustable by callers before extracting.
var MaxFileSize int64 = 100 << 20 // 100 MB

// Extract opens path, detects its MIME type, extracts plain text, and
// computes its SHA256. Returns (text, mime, sha256, error). Files larger than
// MaxFileSize are rejected, and a panic from an extractor (e.g. the PDF parser
// on a malformed file) is recovered into an error rather than crashing.
func Extract(ctx context.Context, path string) (text, mime, sha string, err error) {
	mime = DetectMIME(path)
	ext, err := NewExtractor(mime)
	if err != nil {
		return "", mime, "", fmt.Errorf("preprocess.Extract: %w", err)
	}
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > MaxFileSize {
		return "", mime, "", fmt.Errorf(
			"preprocess.Extract: file too large: %d bytes exceeds limit of %d bytes",
			info.Size(), MaxFileSize)
	}
	sha, err = HashFile(path)
	if err != nil {
		return "", mime, "", fmt.Errorf("preprocess.Extract hash: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", mime, sha, fmt.Errorf("preprocess.Extract open: %w", err)
	}
	defer func() { _ = f.Close() }()
	text, err = safeExtract(ctx, ext, f)
	if err != nil {
		return "", mime, sha, fmt.Errorf("preprocess.Extract extract: %w", err)
	}
	return text, mime, sha, nil
}

// safeExtract runs ext.Extract, converting any panic into an error so one
// malformed document cannot crash the whole ingest run. The PDF backend
// (github.com/ledongthuc/pdf) is known to panic on some malformed inputs.
func safeExtract(ctx context.Context, ext Extractor, r io.Reader) (text string, err error) {
	defer func() {
		if p := recover(); p != nil {
			text = ""
			err = fmt.Errorf("preprocess: recovered from panic in extractor: %v", p)
		}
	}()
	return ext.Extract(ctx, r)
}

// ExtractToFile extracts text from srcPath and saves it to
// outputDir/<sha256>.txt. Creates outputDir if needed.
// Returns the path of the saved file.
func ExtractToFile(ctx context.Context, srcPath, outputDir string) (string, error) {
	text, _, sha, err := Extract(ctx, srcPath)
	if err != nil {
		return "", fmt.Errorf("preprocess.ExtractToFile: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return "", fmt.Errorf("preprocess.ExtractToFile mkdir: %w", err)
	}
	outPath := filepath.Join(outputDir, sha+".txt")
	if err := os.WriteFile(outPath, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("preprocess.ExtractToFile write: %w", err)
	}
	return outPath, nil
}
