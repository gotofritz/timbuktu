package search

import (
	"context"
	"fmt"
	"strings"
)

// Metadata returns all chunks from documents matching all key=value filters (AND logic).
// Returns an empty slice when filters is empty.
func (s *Searcher) Metadata(ctx context.Context, filters map[string]string) ([]SearchResult, error) {
	if len(filters) == 0 {
		return nil, nil
	}

	joinSQL, args := metadataFilterJoins(filters, "d")

	query := `SELECT DISTINCT c.id, c.document_id, c.chunk_index, c.text, d.path, d.title
	          FROM documents d ` + joinSQL + `
	          JOIN chunks c ON c.document_id = d.id
	          ORDER BY d.id, c.chunk_index`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ChunkID, &r.DocumentID, &r.ChunkIndex, &r.Text, &r.Path, &r.Title); err != nil {
			return nil, err
		}
		r.Source = "metadata"
		results = append(results, r)
	}
	return results, rows.Err()
}

// metadataFilterJoins builds one JOIN per filter against the metadata table,
// keyed on docAlias.id, so a query can AND-restrict to documents matching all
// key=value pairs. Returns the JOIN SQL fragment and the ordered bind args
// (key, value per filter, in the same order the JOINs appear). Returns
// ("", nil) when filters is empty. The alias/column names are generated, never
// caller-supplied, so the interpolation is injection-safe.
func metadataFilterJoins(filters map[string]string, docAlias string) (string, []any) {
	if len(filters) == 0 {
		return "", nil
	}
	joins := make([]string, 0, len(filters))
	args := make([]any, 0, len(filters)*2)
	i := 0
	for k, v := range filters {
		alias := fmt.Sprintf("m%d", i)
		joins = append(joins, fmt.Sprintf(
			"JOIN metadata %[1]s ON %[1]s.document_id = %[2]s.id AND %[1]s.key = ? AND %[1]s.value = ?",
			alias, docAlias))
		args = append(args, k, v)
		i++
	}
	return strings.Join(joins, " "), args
}
