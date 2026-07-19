package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/embeddings"
	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func newSearchCmd() *cobra.Command {
	var (
		mode     string
		topK     int
		minScore float64
		format   string
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the knowledge base",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("invalid format %q: must be text or json", format)
			}
			cfg := Config()
			db, err := storage.Open(cfg.Database.Path)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = db.Close() }()

			var emb embeddings.Embedder
			if mode == "vector" || mode == "hybrid" {
				emb, err = embeddings.NewEmbedder(cfg.Embedding)
				if err != nil {
					return fmt.Errorf("embedder: %w", err)
				}
			}

			s := search.New(db.DB(), emb)
			opts := search.Options{TopK: topK, MinScore: minScore}

			var results []search.SearchResult
			switch mode {
			case "vector":
				results, err = s.Vector(cmd.Context(), args[0], opts)
			case "keyword":
				results, err = s.Keyword(cmd.Context(), args[0], opts)
			case "hybrid":
				results, err = s.Hybrid(cmd.Context(), args[0], opts)
			default:
				return fmt.Errorf("invalid mode %q: must be vector, keyword, or hybrid", mode)
			}
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}

			return printSearchResults(results, format)
		},
	}

	cmd.Flags().StringVar(&mode, "mode", "hybrid", "search mode: vector, keyword, or hybrid")
	cmd.Flags().IntVar(&topK, "top", 5, "number of results to return")
	cmd.Flags().Float64Var(&minScore, "min-score", 0, "minimum score threshold")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

func newFindCmd() *cobra.Command {
	var (
		limit  int
		format string
	)

	cmd := &cobra.Command{
		Use:   "find <key=value>...",
		Short: "Find documents by metadata filters",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("invalid format %q: must be text or json", format)
			}
			filters := make(map[string]string, len(args))
			for _, kv := range args {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid filter %q: must be key=value", kv)
				}
				filters[parts[0]] = parts[1]
			}

			cfg := Config()
			db, err := storage.Open(cfg.Database.Path)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = db.Close() }()

			s := search.New(db.DB(), nil)
			results, err := s.Metadata(cmd.Context(), filters)
			if err != nil {
				return fmt.Errorf("find: %w", err)
			}
			if limit > 0 && len(results) > limit {
				results = results[:limit]
			}
			return printSearchResults(results, format)
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "maximum results to return")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

func printSearchResults(results []search.SearchResult, format string) error {
	if format == "json" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return nil
	}
	for i, r := range results {
		fmt.Printf("[%d] score=%.4f  %s §%d\n", i+1, r.Score, r.Path, r.ChunkIndex)
		fmt.Printf("    %q\n\n", TruncatePreview(r.Text, 120))
	}
	return nil
}

// TruncatePreview shortens s to at most n runes, appending "..." when it was
// longer. It truncates on rune boundaries so multi-byte text stays valid UTF-8.
func TruncatePreview(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
