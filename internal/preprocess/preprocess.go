package preprocess

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Extract opens path, detects its MIME type, extracts plain text, and
// computes its SHA256. Returns (text, mime, sha256, error).
func Extract(ctx context.Context, path string) (text, mime, sha string, err error) {
	mime = DetectMIME(path)
	ext, err := NewExtractor(mime)
	if err != nil {
		return "", mime, "", fmt.Errorf("preprocess.Extract: %w", err)
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
	text, err = ext.Extract(ctx, f)
	if err != nil {
		return "", mime, sha, fmt.Errorf("preprocess.Extract extract: %w", err)
	}
	return text, mime, sha, nil
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
