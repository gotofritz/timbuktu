package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Metadata is a key/value pair attached to a document.
type Metadata struct {
	DocumentID int64
	Key        string
	Value      string
}

// MetadataRepo provides typed access to the metadata table.
type MetadataRepo struct{ db *sql.DB }

// NewMetadataRepo returns a MetadataRepo backed by db.
func NewMetadataRepo(db *sql.DB) *MetadataRepo { return &MetadataRepo{db: db} }

// Set inserts or replaces the value for (documentID, key).
func (r *MetadataRepo) Set(ctx context.Context, documentID int64, key, value string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO metadata(document_id,key,value) VALUES(?,?,?)
         ON CONFLICT(document_id,key) DO UPDATE SET value=excluded.value`,
		documentID, key, value,
	)
	if err != nil {
		return fmt.Errorf("MetadataRepo.Set: %w", err)
	}
	return nil
}

// Get returns the value for (documentID, key), or error if not found.
func (r *MetadataRepo) Get(ctx context.Context, documentID int64, key string) (string, error) {
	var value string
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM metadata WHERE document_id=? AND key=?`, documentID, key,
	).Scan(&value)
	if err != nil {
		return "", fmt.Errorf("MetadataRepo.Get: %w", err)
	}
	return value, nil
}

// List returns all metadata entries for documentID, ordered by key.
func (r *MetadataRepo) List(ctx context.Context, documentID int64) ([]Metadata, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT document_id,key,value FROM metadata WHERE document_id=? ORDER BY key`,
		documentID,
	)
	if err != nil {
		return nil, fmt.Errorf("MetadataRepo.List: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Metadata
	for rows.Next() {
		var m Metadata
		if err := rows.Scan(&m.DocumentID, &m.Key, &m.Value); err != nil {
			return nil, fmt.Errorf("MetadataRepo.List scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("MetadataRepo.List: %w", err)
	}
	return out, nil
}

// Delete removes the entry for (documentID, key).
func (r *MetadataRepo) Delete(ctx context.Context, documentID int64, key string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM metadata WHERE document_id=? AND key=?`, documentID, key,
	)
	if err != nil {
		return fmt.Errorf("MetadataRepo.Delete: %w", err)
	}
	return nil
}
