package cli

import "path/filepath"

// NormalizePath resolves path to an absolute, cleaned form so the same file is
// keyed identically no matter how the user spelled it — relative, "./",
// "../", trailing slash, or already absolute. Applied at the ingest/update/
// delete boundary so a document ingested via one spelling is found by another.
func NormalizePath(path string) (string, error) {
	return filepath.Abs(path)
}
