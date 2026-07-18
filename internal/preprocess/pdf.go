package preprocess

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

type pdfExtractor struct{}

func (e *pdfExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("pdf: read: %w", err)
	}
	br := bytes.NewReader(data)
	reader, err := pdf.NewReader(br, int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf: open: %w", err)
	}

	var sb strings.Builder
	for i := 1; i <= reader.NumPage(); i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			return "", fmt.Errorf("pdf: page %d: %w", i, err)
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(text)
	}
	return strings.TrimSpace(sb.String()), nil
}
