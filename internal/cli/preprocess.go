package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/preprocess"
)

func newPreprocessCmd() *cobra.Command {
	var (
		dryRun    bool
		outputDir string
	)
	cmd := &cobra.Command{
		Use:   "preprocess <path>",
		Short: "Extract text from a document and save to the extracted-text store",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := args[0]
			if dryRun {
				return PreviewExtracted(path, os.Stdout)
			}
			dir := outputDir
			if dir == "" {
				dir = Config().Preprocess.OutputDir
			}
			savedPath, err := SaveExtracted(path, dir)
			if err != nil {
				return err
			}
			fmt.Printf("Extracted: %s\n", savedPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print extracted text to stdout without saving")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "directory for extracted text files (default: preprocess.output_dir from config)")
	return cmd
}

// PreviewExtracted extracts text from path and writes a human-readable
// summary to w. Nothing is saved to disk.
func PreviewExtracted(path string, w io.Writer) error {
	text, mime, sha, err := preprocess.Extract(context.Background(), path)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "=== %s ===\n", path)
	_, _ = fmt.Fprintf(w, "MIME:   %s\n", mime)
	_, _ = fmt.Fprintf(w, "SHA256: %s\n", sha)
	_, _ = fmt.Fprintf(w, "Length: %d chars\n", len(text))
	_, _ = fmt.Fprintf(w, "\n%s\n", text)
	return nil
}

// SaveExtracted extracts text from path and saves it to outputDir/<sha256>.txt.
// Returns the path of the saved file.
func SaveExtracted(path, outputDir string) (string, error) {
	return preprocess.ExtractToFile(context.Background(), path, outputDir)
}
