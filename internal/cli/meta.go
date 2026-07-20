package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/storage"
)

func newMetaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "meta",
		Short: "Attach and inspect document metadata",
	}
	cmd.AddCommand(newMetaSetCmd())
	cmd.AddCommand(newMetaListCmd())
	return cmd
}

func newMetaSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <path> <key=value>...",
		Short: "Set metadata key=value pairs on a document",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			docs, meta, closeDB, err := openMetaRepos(cmd)
			if err != nil {
				return err
			}
			defer closeDB()
			return RunMetaSet(cmd.Context(), cmd.OutOrStdout(), docs, meta, args[0], args[1:])
		},
	}
}

func newMetaListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <path>",
		Short: "List all metadata for a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			docs, meta, closeDB, err := openMetaRepos(cmd)
			if err != nil {
				return err
			}
			defer closeDB()
			return RunMetaList(cmd.Context(), cmd.OutOrStdout(), docs, meta, args[0])
		},
	}
}

// openMetaRepos opens the configured database and returns the repos plus a
// close function.
func openMetaRepos(cmd *cobra.Command) (*storage.DocumentRepo, *storage.MetadataRepo, func(), error) {
	cfg := configFrom(cmd)
	db, err := storage.Open(cfg.Database.Path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open database: %w", err)
	}
	sqlDB := db.DB()
	return storage.NewDocumentRepo(sqlDB), storage.NewMetadataRepo(sqlDB),
		func() { _ = db.Close() }, nil
}

// RunMetaSet resolves path to a document and sets each key=value pair.
// Exported for testing.
func RunMetaSet(ctx context.Context, out io.Writer, docs *storage.DocumentRepo, meta *storage.MetadataRepo, path string, pairs []string) error {
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
	for _, kv := range pairs {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid pair %q: must be key=value", kv)
		}
		if err := meta.Set(ctx, doc.ID, key, value); err != nil {
			return fmt.Errorf("set metadata: %w", err)
		}
		fmt.Fprintf(out, "%s=%s\n", key, value) //nolint:errcheck
	}
	return nil
}

// RunMetaList resolves path to a document and prints its metadata.
// Exported for testing.
func RunMetaList(ctx context.Context, out io.Writer, docs *storage.DocumentRepo, meta *storage.MetadataRepo, path string) error {
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
	entries, err := meta.List(ctx, doc.ID)
	if err != nil {
		return fmt.Errorf("list metadata: %w", err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "No metadata for", path) //nolint:errcheck
		return nil
	}
	for _, m := range entries {
		fmt.Fprintf(out, "%s=%s\n", stripControl(m.Key), stripControl(m.Value)) //nolint:errcheck
	}
	return nil
}
