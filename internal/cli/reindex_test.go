package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// countingStubEmbedder is stubEmbedder with a call counter and a settable
// dimension, so a test can prove a dry run spends nothing and that a re-embed
// lands at the local config's dimension.
type countingStubEmbedder struct {
	dim   int
	calls int
	err   error
}

func (c *countingStubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, c.dim)
	}
	return out, nil
}

func (c *countingStubEmbedder) Dimension() int { return c.dim }

// reindexFixture is a knowledge base seeded straight into the DB, as an import
// or a provider switch would leave it: document rows plus chunks at the old
// embedding dimension.
type reindexFixture struct {
	db     *storage.DB
	docs   *storage.DocumentRepo
	chunks *storage.ChunkRepo
	ing    *ingest.Ingester
	emb    *countingStubEmbedder
	rawDir string
}

func newReindexFixture(t *testing.T, newDim int) *reindexFixture {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sqlDB := db.DB()
	rawDir := t.TempDir()
	emb := &countingStubEmbedder{dim: newDim}
	return &reindexFixture{
		db:     db,
		docs:   storage.NewDocumentRepo(sqlDB),
		chunks: storage.NewChunkRepo(sqlDB),
		emb:    emb,
		rawDir: rawDir,
		ing: ingest.NewIngester(
			storage.NewDocumentRepo(sqlDB),
			storage.NewChunkRepo(sqlDB),
			storage.NewMetadataRepo(sqlDB),
			&stubExtractor{text: "extracted body"},
			&chunking.Chunker{Size: 100, Overlap: 0},
			emb,
			t.TempDir(),
			ingest.WithRawDir(rawDir),
		),
	}
}

// seedDoc inserts a document with oldDim-dimensional chunks. When rawCopy is
// true a matching raw archive entry is written, so the document is resolvable.
func (f *reindexFixture) seedDoc(t *testing.T, path, sha string, oldDim int, rawCopy bool) *storage.Document {
	t.Helper()
	doc := &storage.Document{Path: path, SHA256: sha, Title: "t", MimeType: "text/markdown"}
	if err := f.docs.Create(context.Background(), doc); err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	if err := f.chunks.BulkInsert(context.Background(), []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "old", TokenCount: 1, Embedding: make([]float32, oldDim)},
	}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	if rawCopy {
		p := filepath.Join(f.rawDir, sha+filepath.Ext(path))
		if err := os.WriteFile(p, []byte("raw body"), 0o600); err != nil {
			t.Fatalf("write raw copy: %v", err)
		}
	}
	return doc
}

func (f *reindexFixture) chunkDim(t *testing.T, docID int64) int {
	t.Helper()
	got, err := f.chunks.ListByDocument(context.Background(), docID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("document %d has no chunks", docID)
	}
	return len(got[0].Embedding)
}

func TestReindexCommand_rejectsArgs(t *testing.T) {
	if err := runCLI("reindex", "some-path"); err == nil {
		t.Fatal("expected error: reindex takes no arguments")
	}
}

func TestReindexCommand_exposesFlags(t *testing.T) {
	cmd, _, err := cli.New().Find([]string{"reindex"})
	if err != nil {
		t.Fatalf("find reindex command: %v", err)
	}
	for _, name := range []string{"source-dir", "dry-run"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("reindex is missing the --%s flag", name)
		}
	}
	// Topics are not implemented yet, so --topic must not be advertised.
	if cmd.Flags().Lookup("topic") != nil {
		t.Error("reindex advertises --topic, but topics do not exist yet")
	}
}

// The motivating scenario: every document is re-embedded at the local config's
// dimension, reading from raw/.
func TestRunReindex_reEmbedsEveryDocument(t *testing.T) {
	f := newReindexFixture(t, 8)
	a := f.seedDoc(t, "/gone/a.md", "sha-a", 4, true)
	b := f.seedDoc(t, "/gone/b.md", "sha-b", 4, true)

	var out, errOut bytes.Buffer
	if err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{}, true); err != nil {
		t.Fatalf("RunReindex: %v", err)
	}

	for _, doc := range []*storage.Document{a, b} {
		if dim := f.chunkDim(t, doc.ID); dim != 8 {
			t.Errorf("%s chunk dim = %d, want the local config's 8", doc.Path, dim)
		}
	}
	got := out.String()
	if !strings.Contains(got, "[1/2]") || !strings.Contains(got, "[2/2]") {
		t.Errorf("output should show per-document progress, got:\n%s", got)
	}
	if !strings.Contains(got, "2 re-embedded") {
		t.Errorf("output should summarise 2 re-embedded, got:\n%s", got)
	}
}

// One unresolvable document is reported and the rest still run (decision 4).
func TestRunReindex_perDocumentFailureDoesNotStopTheRun(t *testing.T) {
	f := newReindexFixture(t, 8)
	gone := f.seedDoc(t, filepath.Join(t.TempDir(), "gone.md"), "sha-gone", 4, false)
	good := f.seedDoc(t, "/gone/good.md", "sha-good", 4, true)

	var out, errOut bytes.Buffer
	if err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{}, false); err != nil {
		t.Fatalf("RunReindex: %v", err)
	}

	if dim := f.chunkDim(t, good.ID); dim != 8 {
		t.Errorf("resolvable document was not re-embedded (dim %d)", dim)
	}
	if dim := f.chunkDim(t, gone.ID); dim != 4 {
		t.Errorf("unresolvable document should keep its old chunks, dim = %d", dim)
	}
	got := out.String()
	if !strings.Contains(got, "skipped") {
		t.Errorf("output should report the skipped document, got:\n%s", got)
	}
	// The line has to name the archived copy it looked for, or the user cannot
	// tell a missing copy from a misconfigured raw directory.
	if !strings.Contains(got, filepath.Join(f.rawDir, "sha-gone.md")) {
		t.Errorf("skip line should name the path it tried, got:\n%s", got)
	}
	if !strings.Contains(got, "1 re-embedded") || !strings.Contains(got, "1 skipped") {
		t.Errorf("summary should count 1 re-embedded and 1 skipped, got:\n%s", got)
	}
}

// --dry-run reports the resolved source for each document and spends nothing.
func TestRunReindex_dryRunWritesNothing(t *testing.T) {
	f := newReindexFixture(t, 8)
	doc := f.seedDoc(t, "/gone/a.md", "sha-a", 4, true)

	var out, errOut bytes.Buffer
	err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{DryRun: true}, false)
	if err != nil {
		t.Fatalf("RunReindex: %v", err)
	}

	if f.emb.calls != 0 {
		t.Errorf("embedder called %d times, want 0 on a dry run", f.emb.calls)
	}
	if dim := f.chunkDim(t, doc.ID); dim != 4 {
		t.Errorf("chunks changed on a dry run (dim %d)", dim)
	}
	got := out.String()
	if !strings.Contains(got, "would") {
		t.Errorf("dry-run output should say what would happen, got:\n%s", got)
	}
	if !strings.Contains(got, filepath.Join(f.rawDir, "sha-a.md")) {
		t.Errorf("dry-run output should name the resolved source, got:\n%s", got)
	}
}

// --source-dir resolves the content-addressed copies from another raw archive.
func TestRunReindex_sourceDirOverride(t *testing.T) {
	f := newReindexFixture(t, 8)
	doc := f.seedDoc(t, "/gone/a.md", "sha-a", 4, false) // nothing in the configured raw dir

	backup := t.TempDir()
	if err := os.WriteFile(filepath.Join(backup, "sha-a.md"), []byte("backup body"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{SourceDir: backup}, false)
	if err != nil {
		t.Fatalf("RunReindex: %v", err)
	}
	if dim := f.chunkDim(t, doc.ID); dim != 8 {
		t.Errorf("chunk dim = %d, want 8 re-embedded from --source-dir", dim)
	}
}

// An empty knowledge base says so rather than printing an empty summary.
func TestRunReindex_emptyKnowledgeBase(t *testing.T) {
	f := newReindexFixture(t, 8)

	var out, errOut bytes.Buffer
	if err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{}, false); err != nil {
		t.Fatalf("RunReindex: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "No documents") {
		t.Errorf("output = %q, want a no-documents message", got)
	}
}

// A run with real per-document errors exits non-zero, like a directory ingest.
func TestRunReindex_errorsFailTheRun(t *testing.T) {
	f := newReindexFixture(t, 8)
	doc := f.seedDoc(t, "/gone/a.md", "sha-a", 4, true)
	// A resolvable document whose embedding fails is an error, not a skip.
	f.emb.err = errors.New("embedding provider unreachable")

	var out, errOut bytes.Buffer
	err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{}, false)
	if err == nil {
		t.Fatal("expected a non-nil error when a document fails to re-embed")
	}
	if !strings.Contains(errOut.String(), "error:") {
		t.Errorf("stderr should carry the failure, got:\n%s", errOut.String())
	}
	if !strings.Contains(out.String(), "1 errors") {
		t.Errorf("summary should count the failure, got:\n%s", out.String())
	}
	if dim := f.chunkDim(t, doc.ID); dim != 4 {
		t.Errorf("failed document should keep its old chunks, dim = %d", dim)
	}
}

// Listing failures abort: there is nothing to iterate over.
func TestRunReindex_listFailureIsFatal(t *testing.T) {
	f := newReindexFixture(t, 8)
	_ = f.db.Close()

	var out, errOut bytes.Buffer
	if err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{}, false); err == nil {
		t.Fatal("expected an error listing documents from a closed DB")
	}
}

// End to end through cobra: a real config, a real raw archive, and a knowledge
// base whose stored vectors are at the wrong dimension.
func TestReindexCommand_endToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	// A document indexed at the old dimension, with its raw copy present and its
	// original path long gone.
	root := filepath.Join(home, ".tbuk")
	db, err := storage.Open(filepath.Join(root, "tbuk.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	docs := storage.NewDocumentRepo(db.DB())
	doc := &storage.Document{Path: "/vanished/notes.md", SHA256: "e2esha", Title: "notes", MimeType: "text/markdown"}
	if err := docs.Create(context.Background(), doc); err != nil {
		t.Fatalf("create doc: %v", err)
	}
	_ = db.Close()

	rawDir := filepath.Join(root, "raw")
	if err := os.MkdirAll(rawDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rawDir, "e2esha.md"), []byte("# archived"), 0o600); err != nil {
		t.Fatal(err)
	}

	// --dry-run needs no embedding provider to be reachable.
	if err := runCLI("--config", cfgPath, "reindex", "--dry-run"); err != nil {
		t.Fatalf("reindex --dry-run: %v", err)
	}
}

// The point of a run is the summary and anything that went wrong. A line per
// document buries a single missing one under everything that worked.
func TestRunReindex_quietByDefault(t *testing.T) {
	f := newReindexFixture(t, 8)
	f.seedDoc(t, "/gone/a.md", "sha-a", 4, true)
	f.seedDoc(t, "/gone/b.md", "sha-b", 4, true)
	gone := f.seedDoc(t, filepath.Join(t.TempDir(), "gone.md"), "sha-gone", 4, false)

	var out, errOut bytes.Buffer
	if err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{}, false); err != nil {
		t.Fatalf("RunReindex: %v", err)
	}

	got := out.String()
	if strings.Contains(got, "/gone/a.md") || strings.Contains(got, "/gone/b.md") {
		t.Errorf("successful documents should not be listed by default, got:\n%s", got)
	}
	if !strings.Contains(got, gone.Path) {
		t.Errorf("the skipped document must still be reported, got:\n%s", got)
	}
	if !strings.Contains(got, "2 re-embedded, 1 skipped") {
		t.Errorf("summary missing, got:\n%s", got)
	}
}

func TestRunReindex_verboseListsEveryDocument(t *testing.T) {
	f := newReindexFixture(t, 8)
	f.seedDoc(t, "/gone/a.md", "sha-a", 4, true)

	var out, errOut bytes.Buffer
	if err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{}, true); err != nil {
		t.Fatalf("RunReindex: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "[1/1]") || !strings.Contains(got, "/gone/a.md") {
		t.Errorf("verbose should list each document, got:\n%s", got)
	}
}

// A dry run exists to show what would happen, so it lists regardless.
func TestRunReindex_dryRunListsWithoutVerbose(t *testing.T) {
	f := newReindexFixture(t, 8)
	f.seedDoc(t, "/gone/a.md", "sha-a", 4, true)

	var out, errOut bytes.Buffer
	err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{DryRun: true}, false)
	if err != nil {
		t.Fatalf("RunReindex: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "/gone/a.md") {
		t.Errorf("a dry run should list documents without --verbose, got:\n%s", got)
	}
}

// Failures go to stderr whatever the verbosity: they are the reason to look.
func TestRunReindex_errorsAlwaysReported(t *testing.T) {
	f := newReindexFixture(t, 8)
	f.seedDoc(t, "/gone/a.md", "sha-a", 4, true)
	f.emb.err = errors.New("embedding provider unreachable")

	var out, errOut bytes.Buffer
	if err := cli.RunReindex(context.Background(), &out, &errOut, f.ing, ingest.ReindexOptions{}, false); err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(errOut.String(), "/gone/a.md") {
		t.Errorf("failures must be reported when quiet, got:\n%s", errOut.String())
	}
}

func TestReindexCommand_exposesVerbose(t *testing.T) {
	cmd, _, err := cli.New().Find([]string{"reindex"})
	if err != nil {
		t.Fatalf("find reindex: %v", err)
	}
	if cmd.LocalFlags().Lookup("verbose") == nil {
		t.Error("reindex should expose --verbose")
	}
}
