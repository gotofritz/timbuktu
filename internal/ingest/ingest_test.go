package ingest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// ── test doubles ─────────────────────────────────────────────────────────────

type mockExtractor struct {
	text string
	err  error
}

func (m *mockExtractor) ExtractFile(_ context.Context, _ string) (string, error) {
	return m.text, m.err
}

type mockEmbedder struct {
	dim int
	err error
}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, m.dim)
		for j := range vec {
			vec[j] = float32(i) + 0.1
		}
		out[i] = vec
	}
	return out, nil
}

func (m *mockEmbedder) Dimension() int { return m.dim }

// ── helpers ───────────────────────────────────────────────────────────────────

func openTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newIngester(t *testing.T, db *storage.DB, ext ingest.FileExtractor, emb ingest.Embedder) *ingest.Ingester {
	t.Helper()
	sqlDB := db.DB()
	return ingest.NewIngester(
		storage.NewDocumentRepo(sqlDB),
		storage.NewChunkRepo(sqlDB),
		storage.NewMetadataRepo(sqlDB),
		ext,
		&chunking.Chunker{Size: 50, Overlap: 0},
		emb,
		nil, // progress: discard
	)
}

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestIngester_newFile(t *testing.T) {
	db := openTestDB(t)
	ext := &mockExtractor{text: strings.Repeat("word ", 200)} // enough for multiple chunks
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.md", "# Hello\nThis is content.")

	res := ing.IngestFile(context.Background(), path, ingest.Options{})

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Skipped {
		t.Fatal("expected not skipped")
	}
	if res.Chunks == 0 {
		t.Fatal("expected non-zero chunks")
	}

	// verify chunks in DB
	sqlDB := db.DB()
	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count); err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	if count != res.Chunks {
		t.Errorf("DB chunks=%d want %d", count, res.Chunks)
	}
}

func TestIngester_skipUnchanged(t *testing.T) {
	db := openTestDB(t)
	ext := &mockExtractor{text: "some content here for testing"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.txt", "hello world content text")

	// first ingest
	r1 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r1.Err != nil {
		t.Fatalf("first ingest error: %v", r1.Err)
	}

	// second ingest — same file, same SHA256
	r2 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r2.Err != nil {
		t.Fatalf("second ingest error: %v", r2.Err)
	}
	if !r2.Skipped {
		t.Error("expected second ingest to be skipped")
	}
}

func TestIngester_reindexChanged(t *testing.T) {
	db := openTestDB(t)
	ext := &mockExtractor{text: "initial content for the document"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.txt", "version one content")

	r1 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r1.Err != nil {
		t.Fatalf("first ingest: %v", r1.Err)
	}
	chunksAfterFirst := r1.Chunks

	// overwrite with different content → different SHA256
	if err := os.WriteFile(path, []byte("version two is a completely different document now"), 0o644); err != nil {
		t.Fatal(err)
	}
	ext.text = "version two different content"

	r2 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r2.Err != nil {
		t.Fatalf("second ingest: %v", r2.Err)
	}
	if r2.Skipped {
		t.Error("expected re-index, not skip")
	}

	// chunks in DB should reflect new document only
	sqlDB := db.DB()
	var total int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	// old chunks deleted, new ones inserted
	_ = chunksAfterFirst
	if total != r2.Chunks {
		t.Errorf("DB has %d chunks, want %d (new only)", total, r2.Chunks)
	}
}

func TestIngester_forceFlag(t *testing.T) {
	db := openTestDB(t)
	ext := &mockExtractor{text: "document content stays same throughout testing here"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.txt", "content that does not change at all")

	r1 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r1.Err != nil {
		t.Fatalf("first ingest: %v", r1.Err)
	}

	// Force=true → re-ingest even though SHA256 unchanged
	r2 := ing.IngestFile(context.Background(), path, ingest.Options{Force: true})
	if r2.Err != nil {
		t.Fatalf("force ingest: %v", r2.Err)
	}
	if r2.Skipped {
		t.Error("force=true: expected re-ingest, not skip")
	}
}

func TestIngester_extractorError(t *testing.T) {
	db := openTestDB(t)
	ext := &mockExtractor{err: fmt.Errorf("extraction failed: corrupted file")}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "bad.pdf", "not a real pdf")

	res := ing.IngestFile(context.Background(), path, ingest.Options{})

	if res.Err == nil {
		t.Fatal("expected error from extractor failure")
	}
	// no partial chunks in DB
	sqlDB := db.DB()
	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 chunks after extractor error, got %d", count)
	}
}

func TestIngester_dirWalk(t *testing.T) {
	db := openTestDB(t)
	ext := &mockExtractor{text: "document text content for testing purposes here"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb)

	dir := t.TempDir()
	writeTempFile(t, dir, "a.md", "markdown content")
	writeTempFile(t, dir, "b.txt", "plain text content")
	writeTempFile(t, dir, "c.pdf", "pdf content bytes")
	writeTempFile(t, dir, "skip.exe", "should be ignored")
	writeTempFile(t, dir, "skip.go", "also ignored")

	results := ing.IngestDir(context.Background(), dir, ingest.Options{})

	supported := 0
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("unexpected error for %s: %v", r.Path, r.Err)
		}
		supported++
	}
	if supported != 3 {
		t.Errorf("expected 3 supported files ingested, got %d", supported)
	}
}
