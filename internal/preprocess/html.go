package preprocess

import (
	"context"
	"io"
	"strings"

	"golang.org/x/net/html"
)

type htmlExtractor struct{}

func (e *htmlExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	tok := html.NewTokenizer(r)
	var sb strings.Builder
	for {
		tt := tok.Next()
		switch tt {
		case html.ErrorToken:
			if tok.Err() == io.EOF {
				return strings.TrimSpace(sb.String()), nil
			}
			return "", tok.Err()
		case html.TextToken:
			sb.Write(tok.Text())
		}
	}
}
