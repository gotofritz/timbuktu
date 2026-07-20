package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var (
		limit  int
		format string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List documents in the knowledge base",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("invalid format %q: must be text or json", format)
			}
			app, err := openApp(configFrom(cmd))
			if err != nil {
				return err
			}
			defer func() { _ = app.Close() }()
			return RunList(cmd.OutOrStdout(), app.DB(), limit, format)
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 0, "maximum documents to list (0 = all)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

type listItem struct {
	Path      string `json:"path"`
	Title     string `json:"title"`
	Chunks    int64  `json:"chunks"`
	UpdatedAt string `json:"updated_at"`
}

// RunList prints the documents in the knowledge base (path, title, chunk count,
// updated_at) to out. Exported for testing.
func RunList(out io.Writer, db *sql.DB, limit int, format string) error {
	q := `
SELECT d.path, d.title, COUNT(c.id) AS chunks, d.updated_at
FROM documents d
LEFT JOIN chunks c ON c.document_id = d.id
GROUP BY d.id
ORDER BY d.id`
	args := []any{}
	if limit > 0 {
		q += "\nLIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(q, args...)
	if err != nil {
		return fmt.Errorf("list query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []listItem
	for rows.Next() {
		var it listItem
		if err := rows.Scan(&it.Path, &it.Title, &it.Chunks, &it.UpdatedAt); err != nil {
			return fmt.Errorf("list scan: %w", err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list rows: %w", err)
	}

	if format == "json" {
		if items == nil {
			items = []listItem{}
		}
		return json.NewEncoder(out).Encode(items)
	}

	if len(items) == 0 {
		fmt.Fprintln(out, "No documents in the knowledge base.") //nolint:errcheck
		return nil
	}

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tTITLE\tCHUNKS\tUPDATED") //nolint:errcheck
	for _, it := range items {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", stripControl(it.Path), stripControl(it.Title), it.Chunks, it.UpdatedAt) //nolint:errcheck
	}
	return tw.Flush()
}
