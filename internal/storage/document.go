package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned by lookups (GetByPath, GetBySHA256) when no row
// matches. It wraps sql.ErrNoRows so callers can distinguish a genuine
// "does not exist" from a transient DB error with errors.Is.
var ErrNotFound = fmt.Errorf("storage: document not found: %w", sql.ErrNoRows)

// Document represents a source file tracked in the database.
type Document struct {
	ID        int64
	Path      string
	SHA256    string
	Title     string
	MimeType  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DocumentRepo provides typed access to the documents table.
type DocumentRepo struct{ db *sql.DB }

// NewDocumentRepo returns a DocumentRepo backed by db.
func NewDocumentRepo(db *sql.DB) *DocumentRepo { return &DocumentRepo{db: db} }

// Create inserts doc and sets doc.ID, doc.CreatedAt, doc.UpdatedAt.
func (r *DocumentRepo) Create(ctx context.Context, doc *Document) error {
	now := time.Now().UTC()
	doc.CreatedAt = now
	doc.UpdatedAt = now
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO documents(path,sha256,title,mime_type,created_at,updated_at)
         VALUES(?,?,?,?,?,?)`,
		doc.Path, doc.SHA256, doc.Title, doc.MimeType,
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("DocumentRepo.Create: %w", err)
	}
	doc.ID, _ = res.LastInsertId()
	return nil
}

// GetByPath returns the document with the given path, or an error if not found.
func (r *DocumentRepo) GetByPath(ctx context.Context, path string) (*Document, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id,path,sha256,title,mime_type,created_at,updated_at
         FROM documents WHERE path=?`, path)
	return scanDocument(row)
}

// GetBySHA256 returns the first document matching the hash.
func (r *DocumentRepo) GetBySHA256(ctx context.Context, sha string) (*Document, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id,path,sha256,title,mime_type,created_at,updated_at
         FROM documents WHERE sha256=?`, sha)
	return scanDocument(row)
}

// Update persists changes to an existing document and refreshes UpdatedAt.
func (r *DocumentRepo) Update(ctx context.Context, doc *Document) error {
	doc.UpdatedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx,
		`UPDATE documents SET path=?,sha256=?,title=?,mime_type=?,updated_at=? WHERE id=?`,
		doc.Path, doc.SHA256, doc.Title, doc.MimeType,
		doc.UpdatedAt.Format(time.RFC3339), doc.ID,
	)
	if err != nil {
		return fmt.Errorf("DocumentRepo.Update: %w", err)
	}
	return nil
}

// Delete removes the document (and cascades to chunks/metadata).
func (r *DocumentRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM documents WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("DocumentRepo.Delete: %w", err)
	}
	return nil
}

// List returns all documents ordered by id.
func (r *DocumentRepo) List(ctx context.Context) ([]*Document, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id,path,sha256,title,mime_type,created_at,updated_at
         FROM documents ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("DocumentRepo.List: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var docs []*Document
	for rows.Next() {
		doc, err := scanDocumentRow(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func scanDocument(row *sql.Row) (*Document, error) {
	var d Document
	var createdAt, updatedAt string
	err := row.Scan(&d.ID, &d.Path, &d.SHA256, &d.Title, &d.MimeType, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("DocumentRepo scan: %w", err)
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	d.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &d, nil
}

func scanDocumentRow(rows *sql.Rows) (*Document, error) {
	var d Document
	var createdAt, updatedAt string
	err := rows.Scan(&d.ID, &d.Path, &d.SHA256, &d.Title, &d.MimeType, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("DocumentRepo scan: %w", err)
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	d.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &d, nil
}
