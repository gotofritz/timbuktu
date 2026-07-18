package preprocess

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Extractor converts raw bytes of a document into plain text.
type Extractor interface {
	Extract(ctx context.Context, r io.Reader) (string, error)
}

// DetectMIME returns a MIME type based on the file extension of path.
func DetectMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".txt":
		return "text/plain"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

// NewExtractor returns an Extractor for the given MIME type.
func NewExtractor(mime string) (Extractor, error) {
	switch mime {
	case "text/markdown":
		return &markdownExtractor{}, nil
	case "text/html":
		return &htmlExtractor{}, nil
	case "text/plain":
		return &plainTextExtractor{}, nil
	case "application/pdf":
		return &pdfExtractor{}, nil
	default:
		return nil, fmt.Errorf("preprocess: no extractor for MIME type %q", mime)
	}
}
