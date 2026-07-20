package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Chunk is one text segment of a document.
type Chunk struct {
	ID         int64
	DocumentID int64
	ChunkIndex int
	Text       string
	TokenCount int
	Embedding  []float32 // nil if not yet embedded
}

// ChunkRepo provides typed access to the chunks table.
type ChunkRepo struct{ db *sql.DB }

// NewChunkRepo returns a ChunkRepo backed by db.
func NewChunkRepo(db *sql.DB) *ChunkRepo { return &ChunkRepo{db: db} }

// Count returns the total number of chunks.
func (r *ChunkRepo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&n); err != nil {
		return 0, fmt.Errorf("ChunkRepo.Count: %w", err)
	}
	return n, nil
}

// BulkInsert inserts chunks in a single transaction and sets each ID.
func (r *ChunkRepo) BulkInsert(ctx context.Context, chunks []*Chunk) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ChunkRepo.BulkInsert begin: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO chunks(document_id,chunk_index,text,token_count,embedding)
         VALUES(?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("ChunkRepo.BulkInsert prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, c := range chunks {
		var blob []byte
		if c.Embedding != nil {
			blob = Float32SliceToBlob(c.Embedding)
		}
		res, err := stmt.ExecContext(ctx, c.DocumentID, c.ChunkIndex, c.Text, c.TokenCount, blob)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("ChunkRepo.BulkInsert exec: %w", err)
		}
		c.ID, _ = res.LastInsertId()
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ChunkRepo.BulkInsert commit: %w", err)
	}
	return nil
}

// ReplaceForDocument deletes the document's existing chunks and inserts the
// given ones in a single transaction, so a re-ingest never leaves the index
// in a partially-updated state. Each inserted chunk's ID is set.
func (r *ChunkRepo) ReplaceForDocument(ctx context.Context, documentID int64, chunks []*Chunk) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ChunkRepo.ReplaceForDocument begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id=?`, documentID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("ChunkRepo.ReplaceForDocument delete: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO chunks(document_id,chunk_index,text,token_count,embedding)
         VALUES(?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("ChunkRepo.ReplaceForDocument prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, c := range chunks {
		var blob []byte
		if c.Embedding != nil {
			blob = Float32SliceToBlob(c.Embedding)
		}
		res, err := stmt.ExecContext(ctx, c.DocumentID, c.ChunkIndex, c.Text, c.TokenCount, blob)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("ChunkRepo.ReplaceForDocument exec: %w", err)
		}
		c.ID, _ = res.LastInsertId()
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ChunkRepo.ReplaceForDocument commit: %w", err)
	}
	return nil
}

// EmbeddingDimension returns the dimension (float count) of the embeddings
// stored in the chunks table, derived from the blob byte length. found is
// false when no embedded chunks exist. Chunks belonging to excludeDocID are
// ignored (pass 0 to consider all); this lets a re-ingest compare new
// embeddings against the rest of the index without matching the document's own
// soon-to-be-replaced chunks. It returns an error if the stored embeddings do
// not all share one dimension — an already-corrupt index.
func (r *ChunkRepo) EmbeddingDimension(ctx context.Context, excludeDocID int64) (dim int, found bool, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT length(embedding) FROM chunks
         WHERE embedding IS NOT NULL AND (?1 = 0 OR document_id != ?1)`, excludeDocID)
	if err != nil {
		return 0, false, fmt.Errorf("ChunkRepo.EmbeddingDimension: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var dims []int
	for rows.Next() {
		var byteLen int
		if err := rows.Scan(&byteLen); err != nil {
			return 0, false, fmt.Errorf("ChunkRepo.EmbeddingDimension scan: %w", err)
		}
		dims = append(dims, byteLen/4)
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("ChunkRepo.EmbeddingDimension rows: %w", err)
	}

	switch len(dims) {
	case 0:
		return 0, false, nil
	case 1:
		return dims[0], true, nil
	default:
		return 0, false, fmt.Errorf(
			"ChunkRepo.EmbeddingDimension: stored embeddings have inconsistent dimensions %v", dims)
	}
}

// DeleteByDocument removes all chunks for the given document ID.
func (r *ChunkRepo) DeleteByDocument(ctx context.Context, documentID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM chunks WHERE document_id=?`, documentID)
	if err != nil {
		return fmt.Errorf("ChunkRepo.DeleteByDocument: %w", err)
	}
	return nil
}

// ListByDocument returns all chunks for a document ordered by chunk_index.
func (r *ChunkRepo) ListByDocument(ctx context.Context, documentID int64) ([]*Chunk, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id,document_id,chunk_index,text,token_count,embedding
         FROM chunks WHERE document_id=? ORDER BY chunk_index`, documentID)
	if err != nil {
		return nil, fmt.Errorf("ChunkRepo.ListByDocument: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var chunks []*Chunk
	for rows.Next() {
		var c Chunk
		var blob []byte
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.ChunkIndex, &c.Text, &c.TokenCount, &blob); err != nil {
			return nil, fmt.Errorf("ChunkRepo scan: %w", err)
		}
		if blob != nil {
			c.Embedding, _ = BlobToFloat32Slice(blob)
		}
		chunks = append(chunks, &c)
	}
	return chunks, rows.Err()
}
