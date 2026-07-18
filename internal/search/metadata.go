package search

import (
	"context"
	"strings"
)

// Metadata returns all chunks from documents matching all key=value filters (AND logic).
// Returns an empty slice when filters is empty.
func (s *Searcher) Metadata(ctx context.Context, filters map[string]string) ([]SearchResult, error) {
	if len(filters) == 0 {
		return nil, nil
	}

	// Build dynamic JOIN chain: one JOIN per filter.
	joins := make([]string, 0, len(filters))
	args := make([]any, 0, len(filters)*2+1)
	i := 0
	for k, v := range filters {
		alias := strings.NewReplacer("-", "_", ".", "_").Replace(k)
		joins = append(joins, "JOIN metadata m"+string(rune('0'+i))+" ON m"+string(rune('0'+i))+".document_id = d.id AND m"+string(rune('0'+i))+".key = ? AND m"+string(rune('0'+i))+".value = ?")
		_ = alias
		args = append(args, k, v)
		i++
	}

	query := `SELECT DISTINCT c.id, c.document_id, c.chunk_index, c.text, d.path, d.title
	          FROM documents d ` + strings.Join(joins, " ") + `
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
