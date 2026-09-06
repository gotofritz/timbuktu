package ingest_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/preprocess"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// writeStaleExtractedFile simulates text cached by an earlier extractor version.
func writeStaleExtractedFile(t *testing.T, extractedDir, sha, text string) {
	t.Helper()
	if err := os.MkdirAll(extractedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	older := preprocess.OlderCacheNames(sha)
	if err := os.WriteFile(filepath.Join(extractedDir, older[len(older)-1]), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ── test doubles ─────────────────────────────────────────────────────────────

// recordingExtractor returns text keyed by the path it is asked to extract, so
// a test can prove which file reindex actually read from. Paths with no entry
// fall back to defaultText.
type recordingExtractor struct {
	byPath      map[string]string
	defaultText string
	err         error
	calls       []string
}

func (r *recordingExtractor) ExtractFile(_ context.Context, path string) (string, error) {
	r.calls = append(r.calls, path)
	if r.err != nil {
		return "", r.err
	}
	if text, ok := r.byPath[path]; ok {
		return text, nil
	}
	return r.defaultText, nil
}

// tallyEmbedder is mockEmbedder plus a call counter, for asserting that a
// dry run spends nothing.
type tallyEmbedder struct {
	dim   int
	err   error
	calls int
}

func (c *tallyEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, c.dim)
		for j := range vec {
			vec[j] = float32(i) + 0.5
		}
		out[i] = vec
	}
	return out, nil
}

func (c *tallyEmbedder) Dimension() int { return c.dim }

// ── helpers ───────────────────────────────────────────────────────────────────

// seedDocument inserts a document row with the given path/sha and the chunk
// count, at the given embedding dimension, that a previous ingest would have
// left behind.
func seedDocument(t *testing.T, db *storage.DB, path, sha string, oldChunks, oldDim int) *storage.Document {
	t.Helper()
	docs := storage.NewDocumentRepo(db.DB())
	doc := &storage.Document{Path: path, SHA256: sha, Title: "seed", MimeType: "text/markdown"}
	if err := docs.Create(context.Background(), doc); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if oldChunks == 0 {
		return doc
	}
	chunks := make([]*storage.Chunk, oldChunks)
	for i := range chunks {
		vec := make([]float32, oldDim)
		chunks[i] = &storage.Chunk{DocumentID: doc.ID, ChunkIndex: i, Text: "old", TokenCount: 1, Embedding: vec}
	}
	if err := storage.NewChunkRepo(db.DB()).BulkInsert(context.Background(), chunks); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	return doc
}

// writeRawCopy writes the content-addressed archive copy reindex resolves by
// default: rawDir/<sha256><ext>.
func writeRawCopy(t *testing.T, rawDir, sha, ext, content string) string {
	t.Helper()
	if err := os.MkdirAll(rawDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(rawDir, sha+ext)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func storedChunks(t *testing.T, db *storage.DB, docID int64) []*storage.Chunk {
	t.Helper()
	chunks, err := storage.NewChunkRepo(db.DB()).ListByDocument(context.Background(), docID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	return chunks
}

// ── tests ────────────────────────────────────────────────────────────────────

// A document says where its archived copy is, so the copy is found however it
// is named — not only when the name matches sha256 + the path's extension.
func TestReindexDocument_readsTheRecordedRawPath(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	// Named for the original file, as a hand-moved or older archive would be.
	named := writeRawCopy(t, rawDir, "1-trawa-ledger", ".md", "archived body")

	ext := &recordingExtractor{byPath: map[string]string{named: "from the archive"}, defaultText: "WRONG SOURCE"}
	ing := newIngester(t, db, ext, &tallyEmbedder{dim: 4}, t.TempDir(), ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, "/gone/1-trawa-ledger.md", "534d3532", 0, 0)
	doc.RawPath = "1-trawa-ledger.md"
	if err := storage.NewDocumentRepo(db.DB()).Update(context.Background(), doc); err != nil {
		t.Fatal(err)
	}

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if res.Source != named || !res.FromRaw {
		t.Errorf("source = %q (fromRaw=%v), want the recorded copy %q", res.Source, res.FromRaw, named)
	}
}

// A knowledge base indexed before raw_path existed still resolves by the old
// derivation — and the location is recorded, so it is a plain lookup next time.
func TestReindexDocument_backfillsRawPathFromTheDerivedName(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	sha := "backfill1"
	writeRawCopy(t, rawDir, sha, ".md", "body")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4},
		t.TempDir(), ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, "/anywhere/doc.md", sha, 0, 0) // RawPath empty, as a v1 row is
	if res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{}); res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}

	stored, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), "/anywhere/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if stored.RawPath != sha+".md" {
		t.Errorf("raw_path = %q, want it recorded as %q", stored.RawPath, sha+".md")
	}
}

// --source-dir points at another copy of the archive, so what is found there
// says nothing about the configured one: it must not be recorded.
func TestReindexDocument_sourceDirDoesNotBackfill(t *testing.T) {
	db := openTestDB(t)
	configured := t.TempDir()
	backup := t.TempDir()
	sha := "nobackfill1"
	writeRawCopy(t, backup, sha, ".md", "backup body")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4},
		t.TempDir(), ingest.WithRawDir(configured))

	doc := seedDocument(t, db, "/anywhere/doc.md", sha, 0, 0)
	if res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{SourceDir: backup}); res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}

	stored, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), "/anywhere/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if stored.RawPath != "" {
		t.Errorf("raw_path = %q, want empty: the copy found lives in another archive", stored.RawPath)
	}
}

// A recorded path that no longer resolves still falls back, and the failure
// message names what was recorded rather than a derivation nobody used.
func TestReindexDocument_recordedRawPathMissingIsReported(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	ing := newIngester(t, db, &recordingExtractor{defaultText: "x"}, &tallyEmbedder{dim: 4},
		t.TempDir(), ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), "sha1", 0, 0)
	doc.RawPath = "moved-away.md"
	if err := storage.NewDocumentRepo(db.DB()).Update(context.Background(), doc); err != nil {
		t.Fatal(err)
	}

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if !res.Skipped {
		t.Fatalf("want skipped, got %+v", res)
	}
	if !strings.Contains(res.Reason, filepath.Join(rawDir, "moved-away.md")) {
		t.Errorf("reason %q should name the recorded copy", res.Reason)
	}
}

// The whole point of reindex: the document's live path is gone (imported from
// another machine), and the content comes from the raw archive instead.
func TestReindexDocument_readsFromRawArchive(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	sha := "abc123"
	rawPath := writeRawCopy(t, rawDir, sha, ".md", "raw archive body")

	ext := &recordingExtractor{byPath: map[string]string{rawPath: "from raw"}, defaultText: "WRONG SOURCE"}
	emb := &tallyEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, t.TempDir(), ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, "/gone/from/this/machine/doc.md", sha, 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if res.Skipped {
		t.Fatal("want re-embedded, got skipped")
	}
	if res.Source != rawPath {
		t.Errorf("source = %q, want raw copy %q", res.Source, rawPath)
	}
	if !res.FromRaw {
		t.Error("FromRaw = false, want true")
	}
	if res.Chunks == 0 {
		t.Fatal("no chunks stored")
	}
	if len(ext.calls) != 1 || ext.calls[0] != rawPath {
		t.Errorf("extractor calls = %v, want exactly [%s]", ext.calls, rawPath)
	}
	got := storedChunks(t, db, doc.ID)
	if len(got) != res.Chunks {
		t.Errorf("stored %d chunks, result says %d", len(got), res.Chunks)
	}
	if !strings.Contains(got[0].Text, "from raw") {
		t.Errorf("chunk text = %q, want the raw archive's content", got[0].Text)
	}
}

// The raw archive is optional (ingest.raw_dir empty, or --no-raw): those
// documents still reindex, from their stored live path.
func TestReindexDocument_fallsBackToLivePath(t *testing.T) {
	db := openTestDB(t)
	srcDir := t.TempDir()
	livePath := writeTempFile(t, srcDir, "live.md", "# live body")

	ext := &recordingExtractor{byPath: map[string]string{livePath: "from live"}, defaultText: "WRONG SOURCE"}
	ing := newIngester(t, db, ext, &tallyEmbedder{dim: 4}, t.TempDir()) // no raw dir

	doc := seedDocument(t, db, livePath, "nosuchsha", 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if res.FromRaw {
		t.Error("FromRaw = true, want false (no raw copy exists)")
	}
	if res.Source != livePath {
		t.Errorf("source = %q, want live path %q", res.Source, livePath)
	}
	if res.Chunks == 0 {
		t.Error("no chunks stored")
	}
}

// Neither source resolves: reported per-document, with a typed error so a
// programmatic caller can tell it apart, and never fatal to the run.
func TestReindexDocument_unresolvableSource(t *testing.T) {
	db := openTestDB(t)
	emb := &tallyEmbedder{dim: 4}
	ing := newIngester(t, db, &recordingExtractor{defaultText: "x"}, emb, t.TempDir(), ingest.WithRawDir(t.TempDir()))

	doc := seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), "missingsha", 2, 4)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if !res.Skipped {
		t.Error("Skipped = false, want true")
	}
	if !errors.Is(res.Err, ingest.ErrSourceUnavailable) {
		t.Errorf("err = %v, want ErrSourceUnavailable", res.Err)
	}
	if emb.calls != 0 {
		t.Errorf("embedder called %d times, want 0", emb.calls)
	}
	if n := len(storedChunks(t, db, doc.ID)); n != 2 {
		t.Errorf("existing chunks = %d, want the 2 seeded ones left intact", n)
	}
}

// A document that cannot be resolved has to say what was looked for. Without
// that, "no archived copy" is indistinguishable from a misconfigured raw dir,
// an unfinished ingest, or an archive named some other way.
func TestReindexDocument_unresolvableSaysWhatItLookedFor(t *testing.T) {
	rawDir := t.TempDir()
	missingPath := filepath.Join(t.TempDir(), "gone.md")

	cases := []struct {
		name     string
		rawDir   string
		sha      string
		contains []string
	}{
		{
			name:     "names the archived copy it expected",
			rawDir:   rawDir,
			sha:      "abc123",
			contains: []string{filepath.Join(rawDir, "abc123.md"), missingPath},
		},
		{
			name:     "says when raw archiving is switched off",
			rawDir:   "",
			sha:      "abc123",
			contains: []string{"raw_dir", missingPath},
		},
		{
			name:     "says when the document has no hash to look up",
			rawDir:   rawDir,
			sha:      "",
			contains: []string{"SHA256", missingPath},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			var opts []ingest.Option
			if tc.rawDir != "" {
				opts = append(opts, ingest.WithRawDir(tc.rawDir))
			}
			ing := newIngester(t, db, &recordingExtractor{defaultText: "x"}, &tallyEmbedder{dim: 4}, t.TempDir(), opts...)
			doc := seedDocument(t, db, missingPath, tc.sha, 0, 0)

			res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
			if !res.Skipped {
				t.Fatalf("want skipped, got %+v", res)
			}
			for _, want := range tc.contains {
				if !strings.Contains(res.Reason, want) {
					t.Errorf("reason %q should mention %q", res.Reason, want)
				}
				if !strings.Contains(res.Err.Error(), want) {
					t.Errorf("error %q should mention %q", res.Err, want)
				}
			}
		})
	}
}

// The motivating scenario: a knowledge base whose stored vectors are at the old
// dimension is re-embedded at the new one. Unlike ingest, reindex must NOT
// refuse on the dimension change — fixing it is the whole job.
func TestReindexDocument_replacesChunksAtNewDimension(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	sha := "dim1"
	writeRawCopy(t, rawDir, sha, ".md", "body")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "new text"}, &tallyEmbedder{dim: 8},
		t.TempDir(), ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, "/anywhere/doc.md", sha, 3, 4) // 3 chunks at 4 dims

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	got := storedChunks(t, db, doc.ID)
	if len(got) == 0 {
		t.Fatal("no chunks after reindex")
	}
	for _, c := range got {
		if len(c.Embedding) != 8 {
			t.Fatalf("chunk %d embedding dim = %d, want 8", c.ChunkIndex, len(c.Embedding))
		}
		if c.Text == "old" {
			t.Fatal("old chunk survived the replacement")
		}
	}
}

// --source-dir points the content-addressed lookup at another copy of raw/.
func TestReindexDocument_sourceDirOverridesRawDir(t *testing.T) {
	db := openTestDB(t)
	configuredRaw := t.TempDir()
	sha := "over1"
	writeRawCopy(t, configuredRaw, sha, ".md", "configured raw")
	backupRaw := t.TempDir()
	backupPath := writeRawCopy(t, backupRaw, sha, ".md", "backup raw")

	ext := &recordingExtractor{byPath: map[string]string{backupPath: "from backup"}, defaultText: "WRONG SOURCE"}
	ing := newIngester(t, db, ext, &tallyEmbedder{dim: 4}, t.TempDir(), ingest.WithRawDir(configuredRaw))

	doc := seedDocument(t, db, "/gone/doc.md", sha, 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{SourceDir: backupRaw})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if res.Source != backupPath {
		t.Errorf("source = %q, want --source-dir copy %q", res.Source, backupPath)
	}
}

// --dry-run reports the resolved source and spends nothing.
func TestReindexDocument_dryRunTouchesNothing(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	sha := "dry1"
	rawPath := writeRawCopy(t, rawDir, sha, ".md", "body")

	ext := &recordingExtractor{defaultText: "text"}
	emb := &tallyEmbedder{dim: 4}
	ing := newIngester(t, db, ext, emb, t.TempDir(), ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, "/anywhere/doc.md", sha, 2, 4)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{DryRun: true})
	if res.Err != nil {
		t.Fatalf("dry run: %v", res.Err)
	}
	if res.Source != rawPath || !res.FromRaw {
		t.Errorf("source = %q (fromRaw=%v), want %q from raw", res.Source, res.FromRaw, rawPath)
	}
	if res.Chunks != 0 {
		t.Errorf("chunks = %d, want 0 on a dry run", res.Chunks)
	}
	if emb.calls != 0 {
		t.Errorf("embedder called %d times, want 0 on a dry run", emb.calls)
	}
	if len(ext.calls) != 0 {
		t.Errorf("extractor called %v, want no extraction on a dry run", ext.calls)
	}
	if n := len(storedChunks(t, db, doc.ID)); n != 2 {
		t.Errorf("chunks = %d, want the 2 seeded ones untouched", n)
	}
}

// A dry run still classifies an unresolvable document, so the user learns about
// it before spending anything.
func TestReindexDocument_dryRunReportsUnresolvable(t *testing.T) {
	db := openTestDB(t)
	ing := newIngester(t, db, &recordingExtractor{defaultText: "x"}, &tallyEmbedder{dim: 4}, t.TempDir())

	doc := seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), "sha", 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{DryRun: true})
	if !res.Skipped || !errors.Is(res.Err, ingest.ErrSourceUnavailable) {
		t.Errorf("got skipped=%v err=%v, want skipped with ErrSourceUnavailable", res.Skipped, res.Err)
	}
}

// Text cached by an older extractor is not reused when the document can be
// read again: that is the whole point of versioning the cache.
func TestReindexDocument_reExtractsWhenTheCachedTextIsStale(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	extractedDir := t.TempDir()
	sha := "stale1"
	rawPath := writeRawCopy(t, rawDir, sha, ".md", "raw body")
	writeStaleExtractedFile(t, extractedDir, sha, "text from an older extractor")

	ext := &recordingExtractor{byPath: map[string]string{rawPath: "freshly extracted"}, defaultText: "WRONG"}
	ing := newIngester(t, db, ext, &tallyEmbedder{dim: 4}, extractedDir, ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, "/anywhere/doc.md", sha, 0, 0)
	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if len(ext.calls) != 1 {
		t.Errorf("extractor calls = %v, want one re-extraction", ext.calls)
	}
	if res.StaleText {
		t.Error("StaleText = true, but the document was re-extracted")
	}
	if got := storedChunks(t, db, doc.ID); len(got) == 0 || !strings.Contains(got[0].Text, "freshly extracted") {
		t.Errorf("chunks = %+v, want the fresh extraction", got)
	}
	// And the fresh text is cached under the current version.
	if _, err := os.Stat(filepath.Join(extractedDir, preprocess.CacheName(sha))); err != nil {
		t.Errorf("fresh text not cached under the current version: %v", err)
	}
}

// When there is nothing left to re-extract from, older text beats no document.
func TestReindexDocument_fallsBackToStaleTextWhenNothingCanBeReExtracted(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	sha := "stale2"
	writeStaleExtractedFile(t, extractedDir, sha, "text from an older extractor")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "WRONG"}, &tallyEmbedder{dim: 4},
		extractedDir, ingest.WithRawDir(t.TempDir()))

	doc := seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), sha, 0, 0)
	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Skipped {
		t.Fatalf("skipped despite older cached text: %s", res.Reason)
	}
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if !res.FromCache || !res.StaleText {
		t.Errorf("fromCache=%v staleText=%v, want both true", res.FromCache, res.StaleText)
	}
	if got := storedChunks(t, db, doc.ID); len(got) == 0 || !strings.Contains(got[0].Text, "older extractor") {
		t.Errorf("chunks = %+v, want the older cached text", got)
	}
}

// The extracted text is what reindex actually needs; a source file is only how
// it usually gets there. A document whose sources are all gone but whose text
// is still cached re-embeds from the cache rather than being skipped.
func TestReindexDocument_usesCachedTextWhenNoSourceExists(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	sha := "cachedonly"
	writeExtractedFile(t, extractedDir, sha, "text from the cache")

	ext := &recordingExtractor{defaultText: "SHOULD NOT EXTRACT"}
	ing := newIngester(t, db, ext, &tallyEmbedder{dim: 4}, extractedDir, ingest.WithRawDir(t.TempDir()))

	doc := seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), sha, 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Skipped {
		t.Fatalf("skipped despite cached text: %s", res.Reason)
	}
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if !res.FromCache {
		t.Error("FromCache = false, want true")
	}
	if res.Source != filepath.Join(extractedDir, preprocess.CacheName(sha)) {
		t.Errorf("source = %q, want the cached text", res.Source)
	}
	if len(ext.calls) != 0 {
		t.Errorf("extractor called %v, want the cache used", ext.calls)
	}
	got := storedChunks(t, db, doc.ID)
	if len(got) == 0 || !strings.Contains(got[0].Text, "text from the cache") {
		t.Errorf("chunks = %+v, want the cached text", got)
	}
}

// A dry run reports the cache as the source too, so the user can see that a
// document with no file left is still re-embeddable.
func TestReindexDocument_dryRunReportsTheCache(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	sha := "cacheddry"
	writeExtractedFile(t, extractedDir, sha, "cached")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "x"}, &tallyEmbedder{dim: 4}, extractedDir)
	doc := seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), sha, 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{DryRun: true})
	if res.Skipped || !res.FromCache {
		t.Errorf("got skipped=%v fromCache=%v, want a cache hit", res.Skipped, res.FromCache)
	}
}

// When the text came from the cache, say so: naming the archived copy would
// claim the bytes were read from a file that was never opened.
func TestReindexDocument_reportsTheCacheEvenWhenAnArchivedCopyExists(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	extractedDir := t.TempDir()
	sha := "both1"
	writeRawCopy(t, rawDir, sha, ".md", "raw body")
	writeExtractedFile(t, extractedDir, sha, "cached text")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "x"}, &tallyEmbedder{dim: 4},
		extractedDir, ingest.WithRawDir(rawDir))
	doc := seedDocument(t, db, "/anywhere/doc.md", sha, 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if !res.FromCache || res.FromRaw {
		t.Errorf("fromCache=%v fromRaw=%v, want the cache credited", res.FromCache, res.FromRaw)
	}
	// The archived copy is still recorded: the cache is disposable, that is not.
	stored, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), "/anywhere/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if stored.RawPath != sha+".md" {
		t.Errorf("raw_path = %q, want it recorded even when the cache supplied the text", stored.RawPath)
	}
}

// Nothing anywhere: the reason names the cache as well as the two file paths.
func TestReindexDocument_skipReasonNamesTheCache(t *testing.T) {
	db := openTestDB(t)
	extractedDir := t.TempDir()
	ing := newIngester(t, db, &recordingExtractor{defaultText: "x"}, &tallyEmbedder{dim: 4},
		extractedDir, ingest.WithRawDir(t.TempDir()))

	doc := seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), "nothingsha", 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if !res.Skipped {
		t.Fatalf("want skipped, got %+v", res)
	}
	if !strings.Contains(res.Reason, filepath.Join(extractedDir, preprocess.CacheName("nothingsha"))) {
		t.Errorf("reason %q should name the extracted text it looked for", res.Reason)
	}
}

// The extracted-text cache is content-addressed by the document's SHA256, so an
// imported extracted/ is reused rather than re-extracted (decision 12).
func TestReindexDocument_usesExtractedCache(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	extractedDir := t.TempDir()
	sha := "cached1"
	writeRawCopy(t, rawDir, sha, ".md", "raw body")
	writeExtractedFile(t, extractedDir, sha, "cached extraction")

	ext := &recordingExtractor{defaultText: "SHOULD NOT EXTRACT"}
	ing := newIngester(t, db, ext, &tallyEmbedder{dim: 4}, extractedDir, ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, "/anywhere/doc.md", sha, 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if len(ext.calls) != 0 {
		t.Errorf("extractor called %v, want the cache used instead", ext.calls)
	}
	got := storedChunks(t, db, doc.ID)
	if len(got) == 0 || !strings.Contains(got[0].Text, "cached extraction") {
		t.Errorf("chunks = %+v, want the cached extraction", got)
	}
}

// Automatic metadata is refreshed from the document's own stored path, never
// from the sha-named raw copy that supplied the bytes.
func TestReindexDocument_refreshesMetadataFromStoredPath(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	sha := "meta1"
	writeRawCopy(t, rawDir, sha, ".md", "body")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4},
		t.TempDir(), ingest.WithRawDir(rawDir))

	// filepath.Dir derives the "dir" key, so the stored path has to be spelled
	// the way this platform spells one: "\\notes\\design\\api.md" on Windows.
	docPath := filepath.FromSlash("/notes/design/api.md")
	doc := seedDocument(t, db, docPath, sha, 0, 0)
	meta := storage.NewMetadataRepo(db.DB())
	if err := meta.Set(context.Background(), doc.ID, "tag", "keep-me"); err != nil {
		t.Fatal(err)
	}

	if res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{}); res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}

	for key, want := range map[string]string{
		"filename":  "api.md",
		"extension": "md",
		"dir":       filepath.Dir(docPath),
		"tag":       "keep-me", // user-set keys survive
	} {
		got, err := meta.Get(context.Background(), doc.ID, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if got != want {
			t.Errorf("meta[%s] = %q, want %q", key, got, want)
		}
	}
}

// An embed failure leaves the document's existing chunks intact — same
// guarantee ingest/update already give.
func TestReindexDocument_embedErrorPreservesOldChunks(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	sha := "embedfail"
	writeRawCopy(t, rawDir, sha, ".md", "body")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"},
		&tallyEmbedder{dim: 4, err: errors.New("embedder down")}, t.TempDir(), ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, "/anywhere/doc.md", sha, 2, 4)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err == nil {
		t.Fatal("want embed error, got nil")
	}
	if res.Skipped {
		t.Error("Skipped = true, want a reported error instead")
	}
	if n := len(storedChunks(t, db, doc.ID)); n != 2 {
		t.Errorf("chunks = %d, want the 2 old ones preserved", n)
	}
}

// One unusable document must not abort the rest of the run (decision 4).
func TestReindexDocuments_continuesPastPerDocumentFailure(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	writeRawCopy(t, rawDir, "ok1", ".md", "body")

	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4},
		t.TempDir(), ingest.WithRawDir(rawDir))

	gone := seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), "missing1", 0, 0)
	good := seedDocument(t, db, "/anywhere/ok.md", "ok1", 0, 0)

	results := ing.ReindexDocuments(context.Background(), []*storage.Document{gone, good}, ingest.ReindexOptions{})
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if !results[0].Skipped {
		t.Errorf("first result = %+v, want skipped", results[0])
	}
	if results[1].Err != nil || results[1].Chunks == 0 {
		t.Errorf("second result = %+v, want a successful re-embed", results[1])
	}
	if n := len(storedChunks(t, db, good.ID)); n == 0 {
		t.Error("resolvable document was not re-embedded")
	}
}

// Cancellation (Ctrl-C) stops the run promptly rather than grinding through
// every remaining document.
func TestReindexDocuments_stopsOnCancel(t *testing.T) {
	db := openTestDB(t)
	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4}, t.TempDir())

	a := seedDocument(t, db, "/a.md", "a", 0, 0)
	b := seedDocument(t, db, "/b.md", "b", 0, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := ing.ReindexDocuments(ctx, []*storage.Document{a, b}, ingest.ReindexOptions{})
	if len(results) != 0 {
		t.Errorf("results = %d, want 0 after cancellation", len(results))
	}
}

// ReindexAll is the `tbuk reindex` default: every document in the KB.
func TestReindexAll_targetsEveryDocument(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	for _, sha := range []string{"all1", "all2", "all3"} {
		writeRawCopy(t, rawDir, sha, ".md", "body "+sha)
	}
	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4},
		t.TempDir(), ingest.WithRawDir(rawDir))

	for i, sha := range []string{"all1", "all2", "all3"} {
		seedDocument(t, db, filepath.Join("/kb", string(rune('a'+i))+".md"), sha, 0, 0)
	}

	results, err := ing.ReindexAll(context.Background(), ingest.ReindexOptions{})
	if err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	for _, r := range results {
		if r.Err != nil || r.Chunks == 0 {
			t.Errorf("result %+v, want a successful re-embed", r)
		}
	}
}

// An empty knowledge base is a no-op, not an error.
func TestReindexAll_emptyKnowledgeBase(t *testing.T) {
	db := openTestDB(t)
	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4}, t.TempDir())

	results, err := ing.ReindexAll(context.Background(), ingest.ReindexOptions{})
	if err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %d, want 0", len(results))
	}
}

// A failure listing the documents is fatal to the run — unlike a per-document
// failure, there is nothing to continue with.
func TestReindexAll_listErrorIsFatal(t *testing.T) {
	db := openTestDB(t)
	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4}, t.TempDir())
	_ = db.Close()

	if _, err := ing.ReindexAll(context.Background(), ingest.ReindexOptions{}); err == nil {
		t.Fatal("want error from closed DB, got nil")
	}
}

// A directory sitting where the raw copy would be is not a usable source: fall
// through to the live path rather than handing a directory to the extractor.
func TestReindexDocument_ignoresDirectoryAtRawPath(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	sha := "dir1"
	if err := os.MkdirAll(filepath.Join(rawDir, sha+".md"), 0o700); err != nil {
		t.Fatal(err)
	}
	srcDir := t.TempDir()
	livePath := writeTempFile(t, srcDir, "live.md", "# live")

	ext := &recordingExtractor{byPath: map[string]string{livePath: "from live"}, defaultText: "WRONG SOURCE"}
	ing := newIngester(t, db, ext, &tallyEmbedder{dim: 4}, t.TempDir(), ingest.WithRawDir(rawDir))

	doc := seedDocument(t, db, livePath, sha, 0, 0)

	res := ing.ReindexDocument(context.Background(), doc, ingest.ReindexOptions{})
	if res.Err != nil {
		t.Fatalf("reindex: %v", res.Err)
	}
	if res.FromRaw || res.Source != livePath {
		t.Errorf("source = %q (fromRaw=%v), want the live path %q", res.Source, res.FromRaw, livePath)
	}
}

// A large corpus re-embeds serially, so results are reported as they complete
// rather than in one batch at the end.
func TestReindexAll_reportsProgressAsItGoes(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	for _, sha := range []string{"p1", "p2"} {
		writeRawCopy(t, rawDir, sha, ".md", "body")
	}
	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4},
		t.TempDir(), ingest.WithRawDir(rawDir))

	seedDocument(t, db, "/kb/a.md", "p1", 0, 0)
	seedDocument(t, db, "/kb/b.md", "p2", 0, 0)
	seedDocument(t, db, filepath.Join(t.TempDir(), "gone.md"), "p3", 0, 0)

	type progress struct {
		index, total int
		path         string
		skipped      bool
	}
	var seen []progress
	opts := ingest.ReindexOptions{
		OnResult: func(index, total int, res ingest.ReindexResult) {
			seen = append(seen, progress{index, total, res.Path, res.Skipped})
		},
	}

	results, err := ing.ReindexAll(context.Background(), opts)
	if err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}
	if len(seen) != len(results) {
		t.Fatalf("callback fired %d times, want %d (one per result)", len(seen), len(results))
	}
	for i, p := range seen {
		if p.index != i+1 || p.total != 3 {
			t.Errorf("callback %d reported %d/%d, want %d/3", i, p.index, p.total, i+1)
		}
		if p.path != results[i].Path {
			t.Errorf("callback %d path = %q, want %q", i, p.path, results[i].Path)
		}
	}
	if !seen[2].skipped {
		t.Error("third document should be reported as skipped")
	}
}

// An ingest records where it put the archived copy, so re-reading it later is a
// lookup instead of a guess at the naming.
func TestIngester_recordsRawPathOnIngest(t *testing.T) {
	db := openTestDB(t)
	rawDir := t.TempDir()
	ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4},
		t.TempDir(), ingest.WithRawDir(rawDir))

	dir := t.TempDir()
	path := writeTempFile(t, dir, "doc.md", "# hello")

	if res := ing.IngestFile(context.Background(), path, ingest.Options{}); res.Err != nil {
		t.Fatalf("ingest: %v", res.Err)
	}

	doc, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.RawPath != doc.SHA256+".md" {
		t.Errorf("raw_path = %q, want %q", doc.RawPath, doc.SHA256+".md")
	}
	if !isFile(t, filepath.Join(rawDir, doc.RawPath)) {
		t.Errorf("no archived copy at the recorded location %q", doc.RawPath)
	}
}

// Nothing archived means nothing to record: --no-raw and a disabled archive
// both leave the location empty, which the resolver treats as "unknown".
func TestIngester_noRawPathWhenNothingIsArchived(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rawDir string
		opts   ingest.Options
	}{
		{name: "--no-raw", rawDir: t.TempDir(), opts: ingest.Options{NoRaw: true}},
		{name: "archive disabled", rawDir: "", opts: ingest.Options{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			var opts []ingest.Option
			if tc.rawDir != "" {
				opts = append(opts, ingest.WithRawDir(tc.rawDir))
			}
			ing := newIngester(t, db, &recordingExtractor{defaultText: "text"}, &tallyEmbedder{dim: 4}, t.TempDir(), opts...)

			path := writeTempFile(t, t.TempDir(), "doc.md", "# hello")
			if res := ing.IngestFile(context.Background(), path, tc.opts); res.Err != nil {
				t.Fatalf("ingest: %v", res.Err)
			}

			doc, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if doc.RawPath != "" {
				t.Errorf("raw_path = %q, want empty", doc.RawPath)
			}
		})
	}
}

func isFile(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
