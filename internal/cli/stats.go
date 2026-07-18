package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/storage"
)

func newStatsCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show knowledge base statistics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := Config()
			db, err := storage.Open(cfg.Database.Path)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = db.Close() }()
			return RunStats(cmd.OutOrStdout(), db.DB(), cfg.Database.Path, format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

type statsResult struct {
	TotalDocuments int64  `json:"total_documents"`
	TotalChunks    int64  `json:"total_chunks"`
	EmbeddedChunks int64  `json:"embedded_chunks"`
	TotalSizeBytes int64  `json:"total_size_bytes"`
	DBPath         string `json:"db_path"`
	DBSizeBytes    int64  `json:"db_size_bytes"`
}

// RunStats prints knowledge base statistics to out.
// Exported for testing.
func RunStats(out io.Writer, db *sql.DB, dbPath string, format string) error {
	const q = `
SELECT
    COUNT(*)             AS total_documents,
    COALESCE(SUM(chunk_count), 0)     AS total_chunks,
    COALESCE(SUM(embedded_count), 0)  AS embedded_chunks,
    COALESCE(SUM(size_bytes), 0)      AS total_size_bytes
FROM (
    SELECT
        d.id,
        COUNT(c.id)        AS chunk_count,
        COUNT(c.embedding) AS embedded_count,
        COALESCE(LENGTH(GROUP_CONCAT(c.text)), 0) AS size_bytes
    FROM documents d
    LEFT JOIN chunks c ON c.document_id = d.id
    GROUP BY d.id
)`

	var s statsResult
	err := db.QueryRow(q).Scan(&s.TotalDocuments, &s.TotalChunks, &s.EmbeddedChunks, &s.TotalSizeBytes)
	if err != nil {
		return fmt.Errorf("stats query: %w", err)
	}

	s.DBPath = dbPath
	if info, err := os.Stat(dbPath); err == nil {
		s.DBSizeBytes = info.Size()
	}

	if format == "json" {
		return json.NewEncoder(out).Encode(s)
	}

	embPct := "0%"
	if s.TotalChunks > 0 {
		embPct = fmt.Sprintf("%d%%", s.EmbeddedChunks*100/s.TotalChunks)
	}

	fmt.Fprintf(out, "\nKnowledge Base Stats\n")                                                                          //nolint:errcheck
	fmt.Fprintf(out, "────────────────────\n")                                                                            //nolint:errcheck
	fmt.Fprintf(out, "Documents   : %d\n", s.TotalDocuments)                                                              //nolint:errcheck
	fmt.Fprintf(out, "Chunks      : %d\n", s.TotalChunks)                                                                 //nolint:errcheck
	fmt.Fprintf(out, "Embedded    : %d / %d (%s)\n", s.EmbeddedChunks, s.TotalChunks, embPct)                            //nolint:errcheck
	fmt.Fprintf(out, "Approx size : %s\n", humanBytes(s.TotalSizeBytes))                                                  //nolint:errcheck
	fmt.Fprintf(out, "DB path     : %s\n", s.DBPath)                                                                      //nolint:errcheck
	fmt.Fprintf(out, "DB size     : %s\n", humanBytes(s.DBSizeBytes))                                                     //nolint:errcheck
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
