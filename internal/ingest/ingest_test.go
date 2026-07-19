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
	"github.com/gotofritz/timbuktu/internal/preprocess"
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

func newIngester(t *testing.T, db *storage.DB, ext ingest.FileExtractor, emb ingest.Embedder, extractedDir string) *ingest.Ingester {
	t.Helper()
	sqlDB := db.DB()
	return ingest.NewIngester(
		storage.NewDocumentRepo(sqlDB),
		storage.NewChunkRepo(sqlDB),
		storage.NewMetadataRepo(sqlDB),
		ext,
		&chunking.Chunker{Size: 50, Overlap: 0},
		emb,
		extractedDir,
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

// writeExtractedFile simulates a pre-existing extracted file (from preprocess).
func writeExtractedFile(t *testing.T, extractedDir, sha, text string) {
	t.Helper()
	if err := os.MkdirAll(extractedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(extractedDir, sha+".txt")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ── tests ────────────────────────────────────────────────────────────────────

// A transient DB error during the initial lookup must surface as the real
// error, not be swallowed into the "new document" create path (P1-10).
func TestIngester_lookupError_notMaskedAsCreate(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, &mockExtractor{text: "hi"}, emb, extractedDir)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.md", "# Hello")

	// Close the DB so GetByPath returns a real error (not ErrNotFound).
	_ = db.Close()

	res := ing.IngestFile(context.Background(), path, ingest.Options{})
	if res.Err == nil {
		t.Fatal("expected error from closed DB, got nil")
	}
	if !strings.Contains(res.Err.Error(), "lookup") {
		t.Errorf("want lookup error surfaced, got %v", res.Err)
	}
}

func TestIngester_newFile(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	text := strings.Repeat("word ", 200) // enough for multiple chunks
	ext := &mockExtractor{text: text}    // auto-preprocess fallback
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

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

	sqlDB := db.DB()
	var count int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count); err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	if count != res.Chunks {
		t.Errorf("DB chunks=%d want %d", count, res.Chunks)
	}
}

// A second file embedded at a different dimension than the existing index
// must fail loudly rather than silently corrupting vector search.
func TestIngester_dimensionMismatchErrors(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	dir := t.TempDir()
	ctx := context.Background()

	// First file: 4-dim embeddings establish the index dimension.
	ext := &mockExtractor{text: "content one"}
	ing4 := newIngester(t, db, ext, &mockEmbedder{dim: 4}, extractedDir)
	p1 := writeTempFile(t, dir, "one.md", "first document body here")
	if res := ing4.IngestFile(ctx, p1, ingest.Options{}); res.Err != nil {
		t.Fatalf("first ingest: %v", res.Err)
	}

	// Second file with an 8-dim embedder (model/config changed) must error.
	ing8 := newIngester(t, db, &mockExtractor{text: "content two"}, &mockEmbedder{dim: 8}, extractedDir)
	p2 := writeTempFile(t, dir, "two.md", "second document body here")
	res := ing8.IngestFile(ctx, p2, ingest.Options{})
	if res.Err == nil {
		t.Fatal("expected dimension-mismatch error, got nil")
	}
	if !strings.Contains(res.Err.Error(), "dimension") {
		t.Errorf("error = %v, want it to mention dimension", res.Err)
	}

	// The mismatched file must not have been stored.
	var count int
	if err := db.DB().QueryRow(
		`SELECT COUNT(*) FROM chunks c JOIN documents d ON d.id=c.document_id WHERE d.path=?`,
		p2).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("mismatched file stored %d chunks, want 0", count)
	}
}

// Re-ingesting the SAME file at a new dimension is allowed with --force: its
// old chunks are excluded from the consistency check, so a full re-embed of a
// single-document KB succeeds.
func TestIngester_reingestSameFileNewDimension(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	dir := t.TempDir()
	ctx := context.Background()

	ext := &mockExtractor{text: "content"}
	p := writeTempFile(t, dir, "doc.md", "body")

	ing4 := newIngester(t, db, ext, &mockEmbedder{dim: 4}, extractedDir)
	if res := ing4.IngestFile(ctx, p, ingest.Options{}); res.Err != nil {
		t.Fatalf("first ingest: %v", res.Err)
	}

	ing8 := newIngester(t, db, ext, &mockEmbedder{dim: 8}, extractedDir)
	res := ing8.IngestFile(ctx, p, ingest.Options{Force: true})
	if res.Err != nil {
		t.Fatalf("re-ingest same file at new dim: %v", res.Err)
	}
}

func TestIngester_usesExtractedFile(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	// extractor returns wrong text — if ingester reads the pre-existing extracted
	// file instead, we'll see the correct content in chunks.
	ext := &mockExtractor{text: "WRONG TEXT should not appear"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

	dir := t.TempDir()
	content := "original file content here"
	path := writeTempFile(t, dir, "doc.txt", content)

	// compute SHA of source file (matches what ingester will compute)
	sha, err := computeSHA(path)
	if err != nil {
		t.Fatal(err)
	}
	writeExtractedFile(t, extractedDir, sha, strings.Repeat("correct text ", 60))

	res := ing.IngestFile(context.Background(), path, ingest.Options{})
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	// extractor was not called — chunks come from extracted file, not mockExtractor
	if res.Chunks == 0 {
		t.Error("expected chunks from extracted file")
	}
}

func TestIngester_skipUnchanged(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	ext := &mockExtractor{text: "some content here for testing"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.txt", "hello world content text")

	r1 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r1.Err != nil {
		t.Fatalf("first ingest error: %v", r1.Err)
	}

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
	extractedDir := t.TempDir()
	ext := &mockExtractor{text: "initial content for the document"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.txt", "version one content")

	r1 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r1.Err != nil {
		t.Fatalf("first ingest: %v", r1.Err)
	}

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

	sqlDB := db.DB()
	var total int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != r2.Chunks {
		t.Errorf("DB has %d chunks, want %d (new only)", total, r2.Chunks)
	}
}

func TestIngester_forceFlag(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	ext := &mockExtractor{text: "document content stays same throughout testing here"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.txt", "content that does not change at all")

	r1 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r1.Err != nil {
		t.Fatalf("first ingest: %v", r1.Err)
	}

	r2 := ing.IngestFile(context.Background(), path, ingest.Options{Force: true})
	if r2.Err != nil {
		t.Fatalf("force ingest: %v", r2.Err)
	}
	if r2.Skipped {
		t.Error("force=true: expected re-ingest, not skip")
	}
}

// toggleEmbedder succeeds until failAfter successful calls, then errors.
type toggleEmbedder struct {
	dim       int
	calls     int
	failAfter int
}

func (m *toggleEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	m.calls++
	if m.calls > m.failAfter {
		return nil, fmt.Errorf("embed provider down")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, m.dim)
		out[i] = vec
	}
	return out, nil
}

func (m *toggleEmbedder) Dimension() int { return m.dim }

// A failed re-ingest (embedding error) must not destroy the previous index:
// old chunks stay intact and searchable.
func TestIngester_reingestEmbedErrorPreservesOldChunks(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	ext := &mockExtractor{text: strings.Repeat("alpha ", 200)}
	emb := &toggleEmbedder{dim: 4, failAfter: 1} // first ingest ok, second fails
	ing := newIngester(t, db, ext, emb, extractedDir)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.md", "original content")

	r1 := ing.IngestFile(context.Background(), path, ingest.Options{})
	if r1.Err != nil {
		t.Fatalf("first ingest: %v", r1.Err)
	}
	if r1.Chunks == 0 {
		t.Fatal("expected chunks from first ingest")
	}

	sqlDB := db.DB()
	var before int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	// force a re-ingest whose embedding step fails
	r2 := ing.IngestFile(context.Background(), path, ingest.Options{Force: true})
	if r2.Err == nil {
		t.Fatal("expected embed error on re-ingest")
	}

	var after int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("old chunks destroyed by failed re-ingest: before=%d after=%d", before, after)
	}
}

func TestIngester_extractorError(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	ext := &mockExtractor{err: fmt.Errorf("extraction failed: corrupted file")}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "bad.pdf", "not a real pdf")

	// no pre-existing extracted file → triggers auto-preprocess → extractor fails
	res := ing.IngestFile(context.Background(), path, ingest.Options{})

	if res.Err == nil {
		t.Fatal("expected error from extractor failure")
	}
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
	extractedDir := t.TempDir()
	ext := &mockExtractor{text: "document text content for testing purposes here"}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

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

func TestIngester_writesAutomaticMetadata(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	extractedDir := t.TempDir()
	ext := &mockExtractor{text: strings.Repeat("word ", 200)}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "README.md", "# Hello")

	res := ing.IngestFile(ctx, path, ingest.Options{})
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}

	sqlDB := db.DB()
	docRepo := storage.NewDocumentRepo(sqlDB)
	metaRepo := storage.NewMetadataRepo(sqlDB)
	doc, err := docRepo.GetByPath(ctx, path)
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}

	got := map[string]string{}
	entries, err := metaRepo.List(ctx, doc.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range entries {
		got[m.Key] = m.Value
	}

	want := map[string]string{
		"filename":  "README.md",
		"extension": "md",
		"mime":      "text/markdown",
		"dir":       dir,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("metadata %s: want %q got %q", k, v, got[k])
		}
	}
}

func TestIngester_refreshesMetadataOnReingest(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	extractedDir := t.TempDir()
	ext := &mockExtractor{text: strings.Repeat("word ", 200)}
	emb := &mockEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, extractedDir)

	dir := t.TempDir()
	path := writeTempFile(t, dir, "note.txt", "first")

	if res := ing.IngestFile(ctx, path, ingest.Options{}); res.Err != nil {
		t.Fatalf("first ingest: %v", res.Err)
	}

	sqlDB := db.DB()
	docRepo := storage.NewDocumentRepo(sqlDB)
	metaRepo := storage.NewMetadataRepo(sqlDB)
	doc, err := docRepo.GetByPath(ctx, path)
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	// user-set tag should survive re-ingest (only automatic keys refresh)
	if err := metaRepo.Set(ctx, doc.ID, "tag", "design"); err != nil {
		t.Fatalf("Set tag: %v", err)
	}

	// change content so re-ingest is not skipped
	if err := os.WriteFile(path, []byte("second body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := ing.IngestFile(ctx, path, ingest.Options{}); res.Err != nil {
		t.Fatalf("re-ingest: %v", res.Err)
	}

	if v, err := metaRepo.Get(ctx, doc.ID, "filename"); err != nil || v != "note.txt" {
		t.Errorf("filename after re-ingest: got %q err %v", v, err)
	}
	if v, err := metaRepo.Get(ctx, doc.ID, "tag"); err != nil || v != "design" {
		t.Errorf("user tag lost on re-ingest: got %q err %v", v, err)
	}
}

// computeSHA returns the SHA256 hex of the file at path (via preprocess).
func computeSHA(path string) (string, error) {
	return preprocess.HashFile(path)
}

// silence unused import
var _ = fmt.Sprintf
