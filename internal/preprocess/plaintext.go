package preprocess

import (
	"context"
	"io"
)

type plainTextExtractor struct{}

func (e *plainTextExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
