package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

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
			app, err := openApp(configFrom(cmd))
			if err != nil {
				return err
			}
			defer func() { _ = app.Close() }()

			ing, err := app.Ingester()
			if err != nil {
				return err
			}

			return RunUpdate(cmd.Context(), cmd.OutOrStdout(), ing, app.Docs(), args[0], force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "re-ingest even if file is unchanged")
	return cmd
}

// RunUpdate re-ingests a single file if its SHA256 has changed (or force=true).
// Exported for testing.
func RunUpdate(ctx context.Context, out io.Writer, ing *ingest.Ingester, _ *storage.DocumentRepo, path string, force bool) error {
	path, err := NormalizePath(path)
	if err != nil {
		return fmt.Errorf("resolve path %s: %w", path, err)
	}
	// update re-ingests one file; on a directory IngestFile fails deep in the
	// extractor with an opaque "is a directory" read error. Catch it here and
	// point the user at the command that does handle folders.
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		return fmt.Errorf("update takes a single file; use `tbuk ingest %s` for directories", path)
	}
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
