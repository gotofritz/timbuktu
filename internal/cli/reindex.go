package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/ingest"
)

func newReindexCmd() *cobra.Command {
	var (
		sourceDir string
		dryRun    bool
		verbose   bool
	)

	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Re-embed every indexed document from the raw archive",
		Long: "Reindex re-derives every indexed document's chunks and embeddings " +
			"using this machine's embedding configuration, reading each document's " +
			"content from the raw archive (raw/<sha256><ext>) rather than its " +
			"original path.\n\n" +
			"Use it after changing embedding.provider or embedding.model, which " +
			"changes the vector dimension and leaves the existing index unusable, or " +
			"after `tbuk restore` brings in a knowledge base built elsewhere. Because " +
			"the content comes from the archive, documents whose original files are " +
			"gone — restored from a snapshot, or since moved — are re-embedded too. A " +
			"document with no archived copy falls back to its stored path; one with " +
			"neither is reported and skipped, leaving the rest of the run unaffected.\n\n" +
			"Every targeted document is re-embedded: there is no unchanged-file check, " +
			"since the content is presumed unchanged and it is the embedding " +
			"configuration that moved.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := openApp(configFrom(cmd))
			if err != nil {
				return err
			}
			defer func() { _ = app.Close() }()

			ing, err := app.Ingester()
			if err != nil {
				return err
			}

			if sourceDir != "" {
				sourceDir, err = NormalizePath(sourceDir)
				if err != nil {
					return fmt.Errorf("resolve --source-dir: %w", err)
				}
			}

			return RunReindex(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), ing,
				ingest.ReindexOptions{SourceDir: sourceDir, DryRun: dryRun}, verbose)
		},
	}

	cmd.Flags().StringVar(&sourceDir, "source-dir", "",
		"resolve archived copies from this directory instead of ingest.raw_dir")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"list what would be re-embedded, and from where, without embedding anything")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false,
		"print a line per document; by default only problems and the summary are shown")
	return cmd
}

// RunReindex re-embeds every document in the knowledge base, writing failures
// to errW and a summary to outW.
//
// Only what the user has to act on is printed by default: a document skipped or
// failed, and the closing counts. A line per document turns one missing file
// into a needle in a haystack; verbose (or a dry run, whose whole point is the
// listing) prints them all.
//
// A document that resolves to no readable source is skipped, not failed: the
// run continues and the summary counts it, matching how a directory ingest
// carries on past one bad file. Only real failures make the command exit
// non-zero. Exported for testing.
func RunReindex(ctx context.Context, outW, errW io.Writer, ing *ingest.Ingester, opts ingest.ReindexOptions, verbose bool) error {
	reembedded, skipped, errs := 0, 0, 0
	listing := verbose || opts.DryRun

	opts.OnResult = func(index, total int, res ingest.ReindexResult) {
		prefix := fmt.Sprintf("[%d/%d]", index, total)
		switch {
		case res.Skipped:
			skipped++
			_, _ = fmt.Fprintf(outW, "%s %s → skipped: %s\n", prefix, res.Path, res.Reason)
		case res.Err != nil:
			errs++
			_, _ = fmt.Fprintf(errW, "%s %s → error: %v\n", prefix, res.Path, res.Err)
		case opts.DryRun:
			reembedded++
			_, _ = fmt.Fprintf(outW, "%s %s → would re-embed from %s\n", prefix, res.Path, describeSource(res))
		default:
			reembedded++
			if listing {
				_, _ = fmt.Fprintf(outW, "%s %s → %d chunks re-embedded from %s\n", prefix, res.Path, res.Chunks, describeSource(res))
			}
		}
	}

	results, err := ing.ReindexAll(ctx, opts)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		_, _ = fmt.Fprintln(outW, "No documents to reindex.")
		return nil
	}

	verb := "re-embedded"
	if opts.DryRun {
		verb = "would be re-embedded"
	}
	_, _ = fmt.Fprintf(outW, "Done: %d %s, %d skipped, %d errors\n", reembedded, verb, skipped, errs)
	if errs > 0 {
		return fmt.Errorf("%d document(s) failed to reindex", errs)
	}
	return nil
}

// describeSource names where a document's text came from, so the cache, the
// raw archive and the stored-path fallback are distinguishable in the output.
func describeSource(res ingest.ReindexResult) string {
	switch {
	case res.StaleText:
		return "text extracted by an older version (" + res.Source + ") — nothing left to re-extract from"
	case res.FromCache:
		return "already-extracted text (" + res.Source + ")"
	case res.FromRaw:
		return "the archive (" + res.Source + ")"
	default:
		return res.Source + " (no archived copy)"
	}
}
