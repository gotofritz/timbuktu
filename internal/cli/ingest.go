package cli

import (
	"database/sql"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/embeddings"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func newIngestCmd() *cobra.Command {
	var (
		force   bool
		verbose bool
	)

	cmd := &cobra.Command{
		Use:   "ingest <path>",
		Short: "Index a file or directory into the knowledge base",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd)
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

			ing := ingest.NewIngester(
				storage.NewDocumentRepo(sqlDB),
				storage.NewChunkRepo(sqlDB),
				storage.NewMetadataRepo(sqlDB),
				&ingest.DefaultFileExtractor{},
				&chunking.Chunker{
					Size:    cfg.Chunking.Size,
					Overlap: cfg.Chunking.Overlap,
				},
				emb,
				cfg.Preprocess.OutputDir,
				os.Stdout,
			)

			path, err := NormalizePath(args[0])
			if err != nil {
				return fmt.Errorf("resolve path %s: %w", args[0], err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				return fmt.Errorf("stat %s: %w", path, err)
			}

			opts := ingest.Options{Force: force}
			if fi.IsDir() {
				results := ing.IngestDir(cmd.Context(), path, opts)
				return printDirResults(results, verbose)
			}
			res := ing.IngestFile(cmd.Context(), path, opts)
			return printFileResult(res, verbose)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "re-ingest even if file is unchanged")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "show skipped files")
	return cmd
}

func printFileResult(r ingest.Result, verbose bool) error {
	return PrintFileResult(r, verbose, os.Stderr)
}

// PrintFileResult writes a single-file result to errW and returns r.Err.
func PrintFileResult(r ingest.Result, verbose bool, errW io.Writer) error {
	if r.Err != nil {
		_, _ = fmt.Fprintf(errW, "error: %v\n", r.Err)
		return r.Err
	}
	if r.Skipped && verbose {
		fmt.Printf("%s → skipped (unchanged)\n", r.Path)
	}
	return nil
}

func printDirResults(results []ingest.Result, verbose bool) error {
	return PrintDirResults(results, verbose, os.Stdout, os.Stderr)
}

// PrintDirResults writes progress lines to outW and errors to errW.
func PrintDirResults(results []ingest.Result, verbose bool, outW, errW io.Writer) error {
	total := len(results)
	ingested, skipped, errs := 0, 0, 0
	for i, r := range results {
		prefix := fmt.Sprintf("[%d/%d]", i+1, total)
		if r.Err != nil {
			_, _ = fmt.Fprintf(errW, "%s %s → error: %v\n", prefix, r.Path, r.Err)
			errs++
			continue
		}
		if r.Skipped {
			skipped++
			if verbose {
				_, _ = fmt.Fprintf(outW, "%s %s → skipped (unchanged)\n", prefix, r.Path)
			}
			continue
		}
		_, _ = fmt.Fprintf(outW, "%s %s → %d chunks embedded\n", prefix, r.Path, r.Chunks)
		ingested++
	}
	_, _ = fmt.Fprintf(outW, "Done: %d ingested, %d skipped, %d errors\n", ingested, skipped, errs)
	if errs > 0 {
		return fmt.Errorf("%d file(s) failed to ingest", errs)
	}
	return nil
}

// CountDocuments returns the total number of documents in the database.
func CountDocuments(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&n)
	return n, err
}

// CountChunks returns the total number of chunks in the database.
func CountChunks(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n)
	return n, err
}
