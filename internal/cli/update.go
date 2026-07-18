package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/embeddings"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func newUpdateCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "update <path>",
		Short: "Re-ingest a file if its content has changed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := Config()
			db, err := storage.Open(cfg.Database.Path)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = db.Close() }()
			sqlDB := db.DB()

			emb, err := embeddings.NewEmbedder(cfg.Embedding)
			if err != nil {
				return fmt.Errorf("embedder: %w", err)
			}

			docs := storage.NewDocumentRepo(sqlDB)
			ing := ingest.NewIngester(
				docs,
				storage.NewChunkRepo(sqlDB),
				storage.NewMetadataRepo(sqlDB),
				&ingest.DefaultFileExtractor{},
				&chunking.Chunker{Size: cfg.Chunking.Size, Overlap: cfg.Chunking.Overlap},
				emb,
				cfg.Preprocess.OutputDir,
				os.Stdout,
			)

			return RunUpdate(cmd.Context(), cmd.OutOrStdout(), ing, docs, args[0], force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "re-ingest even if file is unchanged")
	return cmd
}

// RunUpdate re-ingests a single file if its SHA256 has changed (or force=true).
// Exported for testing.
func RunUpdate(ctx context.Context, out io.Writer, ing *ingest.Ingester, _ *storage.DocumentRepo, path string, force bool) error {
	res := ing.IngestFile(ctx, path, ingest.Options{Force: force})
	if res.Err != nil {
		return res.Err
	}

	if res.Skipped {
		fmt.Fprintf(out, "Skipped: %s unchanged\n", path) //nolint:errcheck
		return nil
	}

	fmt.Fprintf(out, "Updated: %s (%d chunks)\n", path, res.Chunks) //nolint:errcheck
	return nil
}
