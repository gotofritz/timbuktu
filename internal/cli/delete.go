package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/storage"
)

func newDeleteCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete <path>",
		Short: "Remove a document and all its chunks from the knowledge base",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd)
			db, err := storage.Open(cfg.Database.Path)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = db.Close() }()
			sqlDB := db.DB()
			out := cmd.OutOrStdout()
			docs := storage.NewDocumentRepo(sqlDB)

			if !yes {
				path, err := NormalizePath(args[0])
				if err != nil {
					return fmt.Errorf("resolve path %s: %w", args[0], err)
				}
				doc, lookupErr := docs.GetByPath(cmd.Context(), path)
				if lookupErr != nil {
					if errors.Is(lookupErr, storage.ErrNotFound) {
						return fmt.Errorf("document not found: %s", path)
					}
					return fmt.Errorf("look up %s: %w", path, lookupErr)
				}
				var n int
				_ = sqlDB.QueryRowContext(cmd.Context(), `SELECT COUNT(*) FROM chunks WHERE document_id=?`, doc.ID).Scan(&n)
				fmt.Fprintf(out, "Delete %s (%d chunks)? [y/N] ", args[0], n) //nolint:errcheck
				var answer string
				if _, err := fmt.Fscan(os.Stdin, &answer); err != nil || (answer != "y" && answer != "Y") {
					fmt.Fprintln(out, "Aborted.") //nolint:errcheck
					return nil
				}
			}

			return RunDelete(cmd.Context(), out, sqlDB, docs, cfg.Preprocess.OutputDir, args[0])
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation prompt")
	return cmd
}

// RunDelete removes a document (and its chunks via CASCADE) from the DB, and
// removes its extracted-text cache file (extractedDir/<sha>.txt) so no orphan
// lineage is left behind. Exported for testing.
func RunDelete(ctx context.Context, out io.Writer, db *sql.DB, docs *storage.DocumentRepo, extractedDir, path string) error {
	path, err := NormalizePath(path)
	if err != nil {
		return fmt.Errorf("resolve path %s: %w", path, err)
	}

	doc, err := docs.GetByPath(ctx, path)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("document not found: %s", path)
		}
		return fmt.Errorf("look up %s: %w", path, err)
	}

	var chunkCount int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE document_id=?`, doc.ID).Scan(&chunkCount)

	if err := docs.Delete(ctx, doc.ID); err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	// Best-effort cache cleanup: a missing file is not an error.
	if extractedDir != "" && doc.SHA256 != "" {
		cachePath := filepath.Join(extractedDir, doc.SHA256+".txt")
		if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove extracted cache %s: %w", cachePath, err)
		}
	}

	fmt.Fprintf(out, "Deleted %s (%d chunks removed)\n", path, chunkCount) //nolint:errcheck
	return nil
}
