package ingest

import (
	"context"
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

// Ingester orchestrates the preprocessing → chunking → embedding → storage pipeline.
type Ingester struct {
	docs      *storage.DocumentRepo
	chunks    *storage.ChunkRepo
	meta      *storage.MetadataRepo
	extractor FileExtractor
	chunker   *chunking.Chunker
	embedder  Embedder
	progress  io.Writer
}

// NewIngester constructs an Ingester with the given dependencies.
func NewIngester(
	docs *storage.DocumentRepo,
	chunks *storage.ChunkRepo,
	meta *storage.MetadataRepo,
	extractor FileExtractor,
	chunker *chunking.Chunker,
	embedder Embedder,
	progress io.Writer,
) *Ingester {
	if progress == nil {
		progress = io.Discard
	}
	return &Ingester{
		docs:      docs,
		chunks:    chunks,
		meta:      meta,
		extractor: extractor,
		chunker:   chunker,
		embedder:  embedder,
		progress:  progress,
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

// IngestFile ingests a single file. Each call is atomic — all chunks are
// committed together or none are stored.
func (ing *Ingester) IngestFile(ctx context.Context, path string, opts Options) Result {
	sha, err := preprocess.HashFile(path)
	if err != nil {
		return Result{Path: path, Err: fmt.Errorf("ingest: hash %s: %w", path, err)}
	}

	existing, err := ing.docs.GetByPath(ctx, path)
	if err == nil {
		// document exists
		if existing.SHA256 == sha && !opts.Force {
			return Result{Path: path, Skipped: true}
		}
		// delete old chunks/metadata before re-indexing
		if err := ing.chunks.DeleteByDocument(ctx, existing.ID); err != nil {
			return Result{Path: path, Err: fmt.Errorf("ingest: delete old chunks: %w", err)}
		}
	}

	text, err := ing.extractor.ExtractFile(ctx, path)
	if err != nil {
		return Result{Path: path, Err: fmt.Errorf("ingest: extract %s: %w", path, err)}
	}

	rawChunks := ing.chunker.Split(text)
	if len(rawChunks) == 0 {
		rawChunks = nil
	}

	// embed in batches
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

	// upsert document record
	mime := preprocess.DetectMIME(path)
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	if existing != nil {
		existing.SHA256 = sha
		existing.MimeType = mime
		if err := ing.docs.Update(ctx, existing); err != nil {
			return Result{Path: path, Err: fmt.Errorf("ingest: update doc: %w", err)}
		}
		for _, c := range storageChunks {
			c.DocumentID = existing.ID
		}
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
		for _, c := range storageChunks {
			c.DocumentID = doc.ID
		}
	}

	if len(storageChunks) > 0 {
		if err := ing.chunks.BulkInsert(ctx, storageChunks); err != nil {
			return Result{Path: path, Err: fmt.Errorf("ingest: store chunks: %w", err)}
		}
	}

	_, _ = fmt.Fprintf(ing.progress, "%s → %d chunks embedded\n", path, len(storageChunks))
	return Result{Path: path, Chunks: len(storageChunks)}
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
	mime := preprocess.DetectMIME(path)
	ext, err := preprocess.NewExtractor(mime)
	if err != nil {
		return "", fmt.Errorf("extractor for %s: %w", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return ext.Extract(ctx, f)
}
