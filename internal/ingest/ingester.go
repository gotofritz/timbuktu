package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/preprocess"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// FileExtractor extracts plain text from a file at the given path.
// Used as auto-preprocess fallback when no extracted file exists.
type FileExtractor interface {
	ExtractFile(ctx context.Context, path string) (string, error)
}

// Embedder produces embedding vectors for a batch of texts.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimension() int
}

// Options controls ingestion behaviour.
type Options struct {
	Force bool // re-ingest even if SHA256 unchanged
}

// Result describes the outcome of ingesting a single file.
type Result struct {
	Path    string
	Skipped bool
	Chunks  int
	Err     error
}

// Ingester orchestrates the preprocess → chunk → embed → store pipeline.
type Ingester struct {
	docs         *storage.DocumentRepo
	chunks       *storage.ChunkRepo
	meta         *storage.MetadataRepo
	extractor    FileExtractor // auto-preprocess fallback when extracted file missing
	chunker      *chunking.Chunker
	embedder     Embedder
	extractedDir string // directory for extracted text files (<sha256>.txt)
	progress     io.Writer
}

// NewIngester constructs an Ingester with the given dependencies.
func NewIngester(
	docs *storage.DocumentRepo,
	chunks *storage.ChunkRepo,
	meta *storage.MetadataRepo,
	extractor FileExtractor,
	chunker *chunking.Chunker,
	embedder Embedder,
	extractedDir string,
	progress io.Writer,
) *Ingester {
	if progress == nil {
		progress = io.Discard
	}
	return &Ingester{
		docs:         docs,
		chunks:       chunks,
		meta:         meta,
		extractor:    extractor,
		chunker:      chunker,
		embedder:     embedder,
		extractedDir: extractedDir,
		progress:     progress,
	}
}

const embedBatchSize = 16

// supportedExts lists extensions IngestDir will process.
var supportedExts = map[string]bool{
	".md":   true,
	".txt":  true,
	".pdf":  true,
	".html": true,
	".htm":  true,
}

// IngestFile ingests a single file. Reads extracted text from
// extractedDir/<sha256>.txt; auto-preprocesses if that file is missing.
func (ing *Ingester) IngestFile(ctx context.Context, path string, opts Options) Result {
	sha, err := preprocess.HashFile(path)
	if err != nil {
		return Result{Path: path, Err: fmt.Errorf("ingest: hash %s: %w", path, err)}
	}

	existing, err := ing.docs.GetByPath(ctx, path)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return Result{Path: path, Err: fmt.Errorf("ingest: lookup %s: %w", path, err)}
	}
	if existing != nil && existing.SHA256 == sha && !opts.Force {
		return Result{Path: path, Skipped: true}
	}

	text, err := ing.readOrExtract(ctx, path, sha)
	if err != nil {
		return Result{Path: path, Err: err}
	}

	rawChunks := ing.chunker.Split(text)

	var storageChunks []*storage.Chunk
	for i := 0; i < len(rawChunks); i += embedBatchSize {
		end := i + embedBatchSize
		if end > len(rawChunks) {
			end = len(rawChunks)
		}
		batch := rawChunks[i:end]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Text
		}
		vecs, err := ing.embedder.Embed(ctx, texts)
		if err != nil {
			return Result{Path: path, Err: fmt.Errorf("ingest: embed batch %d: %w", i/embedBatchSize, err)}
		}
		for j, c := range batch {
			storageChunks = append(storageChunks, &storage.Chunk{
				ChunkIndex: c.Index,
				Text:       c.Text,
				TokenCount: c.TokenCount,
				Embedding:  vecs[j],
			})
		}
	}

	// Guard against a changed embedding model/config silently corrupting the
	// index: new vectors must match the dimension of the rest of the KB. The
	// document's own soon-to-be-replaced chunks are excluded so a full re-ingest
	// of a single-document base still works.
	if newDim := firstEmbeddingDim(storageChunks); newDim > 0 {
		var excludeID int64
		if existing != nil {
			excludeID = existing.ID
		}
		existingDim, found, err := ing.chunks.EmbeddingDimension(ctx, excludeID)
		if err != nil {
			return Result{Path: path, Err: fmt.Errorf("ingest: check embedding dimension: %w", err)}
		}
		if found && existingDim != newDim {
			return Result{Path: path, Err: fmt.Errorf(
				"ingest: %s: embedding dimension mismatch: knowledge base uses %d-dim vectors "+
					"but the current embedding model/config produces %d — clear the database and "+
					"re-ingest, or restore the previous embedding configuration",
				path, existingDim, newDim)}
		}
	}

	mime := preprocess.DetectMIME(path)
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	var docID int64
	if existing != nil {
		existing.SHA256 = sha
		existing.MimeType = mime
		if err := ing.docs.Update(ctx, existing); err != nil {
			return Result{Path: path, Err: fmt.Errorf("ingest: update doc: %w", err)}
		}
		docID = existing.ID
	} else {
		doc := &storage.Document{
			Path:     path,
			SHA256:   sha,
			Title:    title,
			MimeType: mime,
		}
		if err := ing.docs.Create(ctx, doc); err != nil {
			return Result{Path: path, Err: fmt.Errorf("ingest: create doc: %w", err)}
		}
		docID = doc.ID
	}
	for _, c := range storageChunks {
		c.DocumentID = docID
	}

	// Replace old chunks with the new ones atomically: delete + insert in one
	// transaction, only after extraction and embedding have succeeded, so a
	// failed re-ingest never destroys the previous index.
	if err := ing.chunks.ReplaceForDocument(ctx, docID, storageChunks); err != nil {
		return Result{Path: path, Err: fmt.Errorf("ingest: store chunks: %w", err)}
	}

	if err := ing.writeAutoMetadata(ctx, docID, path, mime); err != nil {
		return Result{Path: path, Err: err}
	}

	_, _ = fmt.Fprintf(ing.progress, "%s → %d chunks embedded\n", path, len(storageChunks))
	return Result{Path: path, Chunks: len(storageChunks)}
}

// firstEmbeddingDim returns the length of the first non-empty embedding, or 0
// if none of the chunks carry an embedding.
func firstEmbeddingDim(chunks []*storage.Chunk) int {
	for _, c := range chunks {
		if len(c.Embedding) > 0 {
			return len(c.Embedding)
		}
	}
	return 0
}

// writeAutoMetadata (re)writes the automatic per-document metadata keys.
// User-set keys are left untouched (Set upserts by key).
func (ing *Ingester) writeAutoMetadata(ctx context.Context, docID int64, path, mime string) error {
	auto := map[string]string{
		"filename":  filepath.Base(path),
		"extension": strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		"mime":      mime,
		"dir":       filepath.Dir(path),
	}
	for k, v := range auto {
		if err := ing.meta.Set(ctx, docID, k, v); err != nil {
			return fmt.Errorf("ingest: write metadata %s: %w", k, err)
		}
	}
	return nil
}

// readOrExtract returns extracted text from extractedDir/<sha>.txt.
// If the file doesn't exist, calls extractor and saves the result.
func (ing *Ingester) readOrExtract(ctx context.Context, path, sha string) (string, error) {
	extractedPath := filepath.Join(ing.extractedDir, sha+".txt")
	data, err := os.ReadFile(extractedPath)
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("ingest: read extracted file: %w", err)
	}

	// auto-preprocess: extract and save for future use
	text, extractErr := ing.extractor.ExtractFile(ctx, path)
	if extractErr != nil {
		return "", fmt.Errorf("ingest: auto-preprocess %s: %w", path, extractErr)
	}
	if err := os.MkdirAll(ing.extractedDir, 0o700); err != nil {
		return "", fmt.Errorf("ingest: mkdir extractedDir: %w", err)
	}
	if err := os.WriteFile(extractedPath, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("ingest: save extracted: %w", err)
	}
	return text, nil
}

// IngestDir walks dir recursively, ingesting all supported file types.
func (ing *Ingester) IngestDir(ctx context.Context, dir string, opts Options) []Result {
	var results []Result
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !supportedExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		r := ing.IngestFile(ctx, path, opts)
		results = append(results, r)
		return nil
	})
	return results
}

// DefaultFileExtractor detects MIME type from the path and delegates to
// the appropriate preprocess backend.
type DefaultFileExtractor struct{}

// ExtractFile opens path, detects its MIME type, and extracts plain text.
func (d *DefaultFileExtractor) ExtractFile(ctx context.Context, path string) (string, error) {
	text, _, _, err := preprocess.Extract(ctx, path)
	return text, err
}
