package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/preprocess"
)

func newPreprocessCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "preprocess <path>",
		Short: "Extract and chunk text from a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return RunPreprocess(args[0], format, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

// RunPreprocess extracts text from path, splits it into chunks, and writes
// the result to w in the requested format ("text" or "json").
func RunPreprocess(path, format string, w io.Writer) error {
	mime := preprocess.DetectMIME(path)

	ex, err := preprocess.NewExtractor(mime)
	if err != nil {
		return err
	}

	sha, err := preprocess.HashFile(path)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	text, err := ex.Extract(context.Background(), f)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	c := &chunking.Chunker{Size: 800, Overlap: 100}
	chunks := c.Split(text)

	switch format {
	case "json":
		type jsonChunk struct {
			Index  int    `json:"index"`
			Tokens int    `json:"tokens"`
			Text   string `json:"text"`
		}
		type output struct {
			Path   string      `json:"path"`
			MIME   string      `json:"mime"`
			SHA256 string      `json:"sha256"`
			Chunks []jsonChunk `json:"chunks"`
		}
		jc := make([]jsonChunk, len(chunks))
		for i, ch := range chunks {
			jc[i] = jsonChunk{Index: ch.Index, Tokens: ch.TokenCount, Text: ch.Text}
		}
		return json.NewEncoder(w).Encode(output{
			Path:   path,
			MIME:   mime,
			SHA256: sha,
			Chunks: jc,
		})
	default:
		_, _ = fmt.Fprintf(w, "=== %s ===\n", path)
		_, _ = fmt.Fprintf(w, "MIME:   %s\n", mime)
		_, _ = fmt.Fprintf(w, "SHA256: %s\n", sha)
		_, _ = fmt.Fprintf(w, "Chunks: %d\n", len(chunks))
		for _, ch := range chunks {
			_, _ = fmt.Fprintf(w, "\n--- Chunk %d (%d tokens) ---\n%s\n", ch.Index, ch.TokenCount, ch.Text)
		}
		return nil
	}
}
