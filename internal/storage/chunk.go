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
