package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/storage"
)

func openTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ── migrations ────────────────────────────────────────────────────────────────

func TestMigrations_idempotent(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Run migrations a second time via a fresh Open on the same *sql.DB handle.
	if err := storage.RunMigrations(db.DB()); err != nil {
		t.Fatalf("second RunMigrations: %v", err)
	}
}

// A database whose recorded schema version is newer than this binary knows
// about must be rejected, not silently read/written with a misunderstood
// schema (P1-22).
func TestMigrations_newerDBRejected(t *testing.T) {
	db := openTestDB(t)

	// Simulate a DB written by a future tbuk: record a version far ahead.
	if _, err := db.DB().Exec(
		`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		9999, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed future version: %v", err)
	}

	err := storage.RunMigrations(db.DB())
	if err == nil {
		t.Fatal("expected error opening a newer-than-supported DB, got nil")
	}
	if !errors.Is(err, storage.ErrSchemaTooNew) {
		t.Errorf("want ErrSchemaTooNew, got %v", err)
	}
}

// ── foreign-key cascade across a connection pool (P0-1) ───────────────────────

// TestForeignKeyCascade_AcrossPooledConnections proves the PRAGMA foreign_keys
// setting reaches every pooled connection, not just the one that ran it during
// Open. Deleting a document must cascade to its chunks (and the FTS delete
// trigger must fire) even when the DELETE lands on a fresh pooled connection.
func TestForeignKeyCascade_AcrossPooledConnections(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storage.Open(dir + "/fk.sqlite")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	sqldb := db.DB()
	sqldb.SetMaxOpenConns(4)

	docRepo := storage.NewDocumentRepo(sqldb)
	chunkRepo := storage.NewChunkRepo(sqldb)

	doc := makeDoc(dir + "/a.txt")
	if err := docRepo.Create(ctx, doc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	chunks := []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "orphanme alpha", TokenCount: 2},
		{DocumentID: doc.ID, ChunkIndex: 1, Text: "orphanme beta", TokenCount: 2},
	}
	if err := chunkRepo.BulkInsert(ctx, chunks); err != nil {
		t.Fatalf("BulkInsert: %v", err)
	}

	// Occupy other pooled connections so the DELETE is served by one that never
	// ran the pragma via db.Exec during Open.
	held := make([]*sql.Conn, 0, 3)
	for i := 0; i < 3; i++ {
		c, err := sqldb.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn: %v", err)
		}
		if err := c.PingContext(ctx); err != nil {
			t.Fatalf("Ping: %v", err)
		}
		held = append(held, c)
	}

	if err := docRepo.Delete(ctx, doc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for _, c := range held {
		_ = c.Close()
	}

	var orphans int
	if err := sqldb.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE document_id NOT IN (SELECT id FROM documents)`).
		Scan(&orphans); err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("expected 0 orphan chunks after cascade delete, got %d", orphans)
	}

	var stale int
	if err := sqldb.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH ?`, "orphanme").
		Scan(&stale); err != nil {
		t.Fatalf("count stale fts: %v", err)
	}
	if stale != 0 {
		t.Fatalf("expected 0 stale FTS rows after cascade delete, got %d", stale)
	}
}

// ── embed helpers ─────────────────────────────────────────────────────────────

func TestEmbedRoundtrip(t *testing.T) {
	orig := []float32{0, 1, -1, math.MaxFloat32, math.SmallestNonzeroFloat32}
	blob := storage.Float32SliceToBlob(orig)
	got, err := storage.BlobToFloat32Slice(blob)
	if err != nil {
		t.Fatalf("BlobToFloat32Slice: %v", err)
	}
	if len(got) != len(orig) {
		t.Fatalf("length mismatch: want %d got %d", len(orig), len(got))
	}
	for i := range orig {
		if got[i] != orig[i] {
			t.Errorf("[%d] want %v got %v", i, orig[i], got[i])
		}
	}
}

func TestBlobToFloat32Slice_emptyNil(t *testing.T) {
	got, err := storage.BlobToFloat32Slice(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %v", got)
	}
}

func TestBlobToFloat32Slice_badLength(t *testing.T) {
	_, err := storage.BlobToFloat32Slice([]byte{0x01, 0x02, 0x03}) // not multiple of 4
	if err == nil {
		t.Fatal("expected error for bad-length blob")
	}
}

// ── DocumentRepo ──────────────────────────────────────────────────────────────

func makeDoc(path string) *storage.Document {
	return &storage.Document{
		Path:     path,
		SHA256:   "abc123",
		Title:    "Test Doc",
		MimeType: "text/plain",
	}
}

func TestDocumentRepo_CRUD(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := storage.NewDocumentRepo(db.DB())

	doc := makeDoc("/tmp/a.txt")
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if doc.ID == 0 {
		t.Fatal("Create did not set ID")
	}

	got, err := repo.GetByPath(ctx, doc.Path)
	if err != nil {
		t.Fatalf("GetByPath: %v", err)
	}
	if got.Title != doc.Title {
		t.Errorf("Title mismatch: want %q got %q", doc.Title, got.Title)
	}

	got.Title = "Updated"
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got2, _ := repo.GetByPath(ctx, doc.Path)
	if got2.Title != "Updated" {
		t.Errorf("Update not persisted: got %q", got2.Title)
	}

	if err := repo.Delete(ctx, doc.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByPath(ctx, doc.Path); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDocumentRepo_GetBySHA256(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := storage.NewDocumentRepo(db.DB())

	doc := makeDoc("/tmp/b.txt")
	doc.SHA256 = "deadbeef"
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetBySHA256(ctx, "deadbeef")
	if err != nil {
		t.Fatalf("GetBySHA256: %v", err)
	}
	if got.Path != doc.Path {
		t.Errorf("wrong doc: want %q got %q", doc.Path, got.Path)
	}
}

func TestDocumentRepo_List(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := storage.NewDocumentRepo(db.DB())

	for i, path := range []string{"/a", "/b", "/c"} {
		d := makeDoc(path)
		d.SHA256 = string(rune('a' + i))
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("Create %s: %v", path, err)
		}
	}

	docs, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(docs) != 3 {
		t.Errorf("want 3 docs, got %d", len(docs))
	}
}

// ── ChunkRepo ─────────────────────────────────────────────────────────────────

func seedDocument(t *testing.T, ctx context.Context, repo *storage.DocumentRepo) *storage.Document {
	t.Helper()
	return seedDocumentAt(t, ctx, repo, "/seed.txt")
}

func seedDocumentAt(t *testing.T, ctx context.Context, repo *storage.DocumentRepo, path string) *storage.Document {
	t.Helper()
	doc := makeDoc(path)
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatalf("seed document %s: %v", path, err)
	}
	return doc
}

func TestChunkRepo_BulkInsert(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	chunkRepo := storage.NewChunkRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)

	chunks := []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "hello world", TokenCount: 2},
		{DocumentID: doc.ID, ChunkIndex: 1, Text: "foo bar baz", TokenCount: 3},
	}
	if err := chunkRepo.BulkInsert(ctx, chunks); err != nil {
		t.Fatalf("BulkInsert: %v", err)
	}
	for _, c := range chunks {
		if c.ID == 0 {
			t.Errorf("chunk index %d has no ID after insert", c.ChunkIndex)
		}
	}

	got, err := chunkRepo.ListByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("want 2 chunks, got %d", len(got))
	}
}

func TestChunkRepo_DeleteByDocument(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	chunkRepo := storage.NewChunkRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	chunks := []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "alpha", TokenCount: 1},
	}
	if err := chunkRepo.BulkInsert(ctx, chunks); err != nil {
		t.Fatalf("BulkInsert: %v", err)
	}

	if err := chunkRepo.DeleteByDocument(ctx, doc.ID); err != nil {
		t.Fatalf("DeleteByDocument: %v", err)
	}

	got, err := chunkRepo.ListByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(got))
	}
}

func TestChunkRepo_ReplaceForDocument(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	chunkRepo := storage.NewChunkRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	old := []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "old one", TokenCount: 2},
		{DocumentID: doc.ID, ChunkIndex: 1, Text: "old two", TokenCount: 2},
	}
	if err := chunkRepo.BulkInsert(ctx, old); err != nil {
		t.Fatalf("BulkInsert: %v", err)
	}

	fresh := []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "new one", TokenCount: 2},
	}
	if err := chunkRepo.ReplaceForDocument(ctx, doc.ID, fresh); err != nil {
		t.Fatalf("ReplaceForDocument: %v", err)
	}

	got, err := chunkRepo.ListByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument: %v", err)
	}
	if len(got) != 1 || got[0].Text != "new one" {
		t.Fatalf("want [new one], got %+v", got)
	}
	if got[0].ID == 0 {
		t.Error("replaced chunk missing ID")
	}
}

func TestChunkRepo_ReplaceForDocument_Empty(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	chunkRepo := storage.NewChunkRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	if err := chunkRepo.BulkInsert(ctx, []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "gone", TokenCount: 1},
	}); err != nil {
		t.Fatalf("BulkInsert: %v", err)
	}

	// Replacing with an empty set clears existing chunks.
	if err := chunkRepo.ReplaceForDocument(ctx, doc.ID, nil); err != nil {
		t.Fatalf("ReplaceForDocument nil: %v", err)
	}
	got, err := chunkRepo.ListByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 chunks, got %d", len(got))
	}
}

func TestChunkRepo_ReplaceForDocument_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewChunkRepo(closedDB(t).DB())
	chunks := []*storage.Chunk{{DocumentID: 1, ChunkIndex: 0, Text: "x"}}
	if err := repo.ReplaceForDocument(ctx, 1, chunks); err == nil {
		t.Error("expected error on closed DB")
	}
}

// ── MetadataRepo ──────────────────────────────────────────────────────────────

func TestMetadataRepo_SetGet(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	metaRepo := storage.NewMetadataRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)

	if err := metaRepo.Set(ctx, doc.ID, "author", "Alice"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := metaRepo.Get(ctx, doc.ID, "author")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "Alice" {
		t.Errorf("want %q got %q", "Alice", val)
	}

	// Upsert
	if err := metaRepo.Set(ctx, doc.ID, "author", "Bob"); err != nil {
		t.Fatalf("Set upsert: %v", err)
	}
	val2, _ := metaRepo.Get(ctx, doc.ID, "author")
	if val2 != "Bob" {
		t.Errorf("upsert: want %q got %q", "Bob", val2)
	}
}

func TestMetadataRepo_Delete(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	metaRepo := storage.NewMetadataRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	_ = metaRepo.Set(ctx, doc.ID, "tag", "go")

	if err := metaRepo.Delete(ctx, doc.ID, "tag"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := metaRepo.Get(ctx, doc.ID, "tag")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestMetadataRepo_CascadeOnDocumentDelete(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	metaRepo := storage.NewMetadataRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	_ = metaRepo.Set(ctx, doc.ID, "key", "val")

	if err := docRepo.Delete(ctx, doc.ID); err != nil {
		t.Fatalf("Delete doc: %v", err)
	}

	_, err := metaRepo.Get(ctx, doc.ID, "key")
	if err == nil {
		t.Fatal("expected metadata cascade-deleted")
	}
}

// ── ChunkRepo with embeddings ─────────────────────────────────────────────────

func TestChunkRepo_WithEmbedding(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	chunkRepo := storage.NewChunkRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	emb := []float32{0.1, 0.2, 0.3}
	chunks := []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "embedded", TokenCount: 1, Embedding: emb},
	}
	if err := chunkRepo.BulkInsert(ctx, chunks); err != nil {
		t.Fatalf("BulkInsert with embedding: %v", err)
	}

	got, err := chunkRepo.ListByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListByDocument: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(got))
	}
	if len(got[0].Embedding) != 3 {
		t.Errorf("embedding not persisted: got %v", got[0].Embedding)
	}
}

func TestChunkRepo_EmbeddingDimension(t *testing.T) {
	ctx := context.Background()

	t.Run("empty knowledge base returns not found", func(t *testing.T) {
		db := openTestDB(t)
		chunkRepo := storage.NewChunkRepo(db.DB())
		_, found, err := chunkRepo.EmbeddingDimension(ctx, 0)
		if err != nil {
			t.Fatalf("EmbeddingDimension: %v", err)
		}
		if found {
			t.Errorf("found = true, want false for empty KB")
		}
	})

	t.Run("consistent dimension reported", func(t *testing.T) {
		db := openTestDB(t)
		docRepo := storage.NewDocumentRepo(db.DB())
		chunkRepo := storage.NewChunkRepo(db.DB())
		doc := seedDocument(t, ctx, docRepo)
		chunks := []*storage.Chunk{
			{DocumentID: doc.ID, ChunkIndex: 0, Text: "a", Embedding: []float32{1, 2, 3, 4}},
			{DocumentID: doc.ID, ChunkIndex: 1, Text: "b", Embedding: []float32{5, 6, 7, 8}},
		}
		if err := chunkRepo.BulkInsert(ctx, chunks); err != nil {
			t.Fatalf("BulkInsert: %v", err)
		}
		dim, found, err := chunkRepo.EmbeddingDimension(ctx, 0)
		if err != nil {
			t.Fatalf("EmbeddingDimension: %v", err)
		}
		if !found || dim != 4 {
			t.Errorf("dim=%d found=%v, want dim=4 found=true", dim, found)
		}
	})

	t.Run("inconsistent dimensions error", func(t *testing.T) {
		db := openTestDB(t)
		docRepo := storage.NewDocumentRepo(db.DB())
		chunkRepo := storage.NewChunkRepo(db.DB())
		d1 := seedDocumentAt(t, ctx, docRepo, "/one.txt")
		d2 := seedDocumentAt(t, ctx, docRepo, "/two.txt")
		if err := chunkRepo.BulkInsert(ctx, []*storage.Chunk{
			{DocumentID: d1.ID, ChunkIndex: 0, Text: "a", Embedding: []float32{1, 2, 3}},
		}); err != nil {
			t.Fatalf("BulkInsert d1: %v", err)
		}
		if err := chunkRepo.BulkInsert(ctx, []*storage.Chunk{
			{DocumentID: d2.ID, ChunkIndex: 0, Text: "b", Embedding: []float32{1, 2, 3, 4}},
		}); err != nil {
			t.Fatalf("BulkInsert d2: %v", err)
		}
		if _, _, err := chunkRepo.EmbeddingDimension(ctx, 0); err == nil {
			t.Error("expected error for inconsistent stored dimensions")
		}
	})

	t.Run("excludes given document", func(t *testing.T) {
		db := openTestDB(t)
		docRepo := storage.NewDocumentRepo(db.DB())
		chunkRepo := storage.NewChunkRepo(db.DB())
		d1 := seedDocumentAt(t, ctx, docRepo, "/keep.txt")
		d2 := seedDocumentAt(t, ctx, docRepo, "/exclude.txt")
		if err := chunkRepo.BulkInsert(ctx, []*storage.Chunk{
			{DocumentID: d1.ID, ChunkIndex: 0, Text: "a", Embedding: []float32{1, 2, 3}},
		}); err != nil {
			t.Fatalf("BulkInsert d1: %v", err)
		}
		if err := chunkRepo.BulkInsert(ctx, []*storage.Chunk{
			{DocumentID: d2.ID, ChunkIndex: 0, Text: "b", Embedding: []float32{1, 2, 3, 4, 5}},
		}); err != nil {
			t.Fatalf("BulkInsert d2: %v", err)
		}
		// Excluding d2 leaves only the 3-dim chunk → no mismatch, dim=3.
		dim, found, err := chunkRepo.EmbeddingDimension(ctx, d2.ID)
		if err != nil {
			t.Fatalf("EmbeddingDimension: %v", err)
		}
		if !found || dim != 3 {
			t.Errorf("dim=%d found=%v, want dim=3 found=true", dim, found)
		}
	})
}

// ── error / constraint paths ──────────────────────────────────────────────────

func TestDocumentRepo_DuplicatePath(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := storage.NewDocumentRepo(db.DB())

	doc := makeDoc("/dup.txt")
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	doc2 := makeDoc("/dup.txt")
	doc2.SHA256 = "other"
	if err := repo.Create(ctx, doc2); err == nil {
		t.Fatal("expected unique-constraint error on duplicate path")
	}
}

func TestDocumentRepo_GetByPath_NotFound(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := storage.NewDocumentRepo(db.DB())

	_, err := repo.GetByPath(ctx, "/no/such/path")
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("want errors.Is(err, ErrNotFound), got %v", err)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ErrNotFound must wrap sql.ErrNoRows, got %v", err)
	}
}

func TestDocumentRepo_GetBySHA256_NotFound(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := storage.NewDocumentRepo(db.DB())

	_, err := repo.GetBySHA256(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing sha256")
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("want errors.Is(err, ErrNotFound), got %v", err)
	}
}

func TestMetadataRepo_Get_NotFound(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	metaRepo := storage.NewMetadataRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	if _, err := metaRepo.Get(ctx, doc.ID, "missing-key"); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestOpen_FilePath(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.sqlite"
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open file: %v", err)
	}
	_ = db.Close()

	// Reopen to verify migrations are idempotent on real file.
	db2, err := storage.Open(path)
	if err != nil {
		t.Fatalf("reopen file: %v", err)
	}
	_ = db2.Close()
}

func TestOpen_FilePrivatePerms(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/perms.sqlite"
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("Open file: %v", err)
	}
	defer func() { _ = db.Close() }()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("db perms = %o, want 600", fi.Mode().Perm())
	}
}

func TestChunkRepo_BulkInsert_Empty(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	chunkRepo := storage.NewChunkRepo(db.DB())

	if err := chunkRepo.BulkInsert(ctx, nil); err != nil {
		t.Fatalf("BulkInsert nil: %v", err)
	}
	if err := chunkRepo.BulkInsert(ctx, []*storage.Chunk{}); err != nil {
		t.Fatalf("BulkInsert empty: %v", err)
	}
}

func TestMetadataRepo_AllKeys(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	metaRepo := storage.NewMetadataRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		if err := metaRepo.Set(ctx, doc.ID, k, "v"); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	for _, k := range keys {
		val, err := metaRepo.Get(ctx, doc.ID, k)
		if err != nil {
			t.Fatalf("Get %s: %v", k, err)
		}
		if val != "v" {
			t.Errorf("key %s: want %q got %q", k, "v", val)
		}
	}
}

// ── error paths via closed DB ─────────────────────────────────────────────────

func closedDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = db.Close()
	return db
}

func TestDocumentRepo_Create_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewDocumentRepo(closedDB(t).DB())
	if err := repo.Create(ctx, makeDoc("/x")); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestDocumentRepo_Update_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewDocumentRepo(closedDB(t).DB())
	doc := &storage.Document{ID: 1, Path: "/x", SHA256: "s"}
	if err := repo.Update(ctx, doc); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestDocumentRepo_Delete_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewDocumentRepo(closedDB(t).DB())
	if err := repo.Delete(ctx, 1); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestDocumentRepo_List_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewDocumentRepo(closedDB(t).DB())
	if _, err := repo.List(ctx); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestChunkRepo_BulkInsert_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewChunkRepo(closedDB(t).DB())
	chunks := []*storage.Chunk{{DocumentID: 1, ChunkIndex: 0, Text: "x", TokenCount: 1}}
	if err := repo.BulkInsert(ctx, chunks); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestChunkRepo_DeleteByDocument_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewChunkRepo(closedDB(t).DB())
	if err := repo.DeleteByDocument(ctx, 1); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestChunkRepo_ListByDocument_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewChunkRepo(closedDB(t).DB())
	if _, err := repo.ListByDocument(ctx, 1); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestMetadataRepo_Set_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewMetadataRepo(closedDB(t).DB())
	if err := repo.Set(ctx, 1, "k", "v"); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestMetadataRepo_Delete_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewMetadataRepo(closedDB(t).DB())
	if err := repo.Delete(ctx, 1, "k"); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestMetadataRepo_List(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	metaRepo := storage.NewMetadataRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	want := map[string]string{"author": "Alice", "tag": "go", "year": "2026"}
	for k, v := range want {
		if err := metaRepo.Set(ctx, doc.ID, k, v); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}

	got, err := metaRepo.List(ctx, doc.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("want %d entries, got %d", len(want), len(got))
	}
	// entries ordered by key
	if got[0].Key != "author" || got[1].Key != "tag" || got[2].Key != "year" {
		t.Errorf("expected key order [author tag year], got %v", []string{got[0].Key, got[1].Key, got[2].Key})
	}
	for _, m := range got {
		if want[m.Key] != m.Value {
			t.Errorf("key %s: want %q got %q", m.Key, want[m.Key], m.Value)
		}
	}
}

func TestMetadataRepo_List_Empty(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	metaRepo := storage.NewMetadataRepo(db.DB())

	doc := seedDocument(t, ctx, docRepo)
	got, err := metaRepo.List(ctx, doc.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no entries, got %d", len(got))
	}
}

func TestMetadataRepo_List_ClosedDB(t *testing.T) {
	ctx := context.Background()
	repo := storage.NewMetadataRepo(closedDB(t).DB())
	if _, err := repo.List(ctx, 1); err == nil {
		t.Fatal("expected error on closed DB")
	}
}

// ── time helpers ──────────────────────────────────────────────────────────────

func TestDocumentRepo_TimestampsSet(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := storage.NewDocumentRepo(db.DB())

	before := time.Now().UTC().Truncate(time.Second)
	doc := makeDoc("/ts.txt")
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	if doc.CreatedAt.Before(before) || doc.CreatedAt.After(after) {
		t.Errorf("CreatedAt out of range: %v", doc.CreatedAt)
	}
	if doc.UpdatedAt.Before(before) || doc.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt out of range: %v", doc.UpdatedAt)
	}
}

func TestDocumentRepo_Count(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	repo := storage.NewDocumentRepo(db.DB())

	if n, err := repo.Count(ctx); err != nil || n != 0 {
		t.Fatalf("empty Count: got (%d, %v), want (0, nil)", n, err)
	}

	for _, p := range []string{"/a.txt", "/b.txt", "/c.txt"} {
		if err := repo.Create(ctx, makeDoc(p)); err != nil {
			t.Fatalf("Create %s: %v", p, err)
		}
	}

	n, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("want 3 documents, got %d", n)
	}
}

func TestChunkRepo_Count(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	docRepo := storage.NewDocumentRepo(db.DB())
	chunkRepo := storage.NewChunkRepo(db.DB())

	if n, err := chunkRepo.Count(ctx); err != nil || n != 0 {
		t.Fatalf("empty Count: got (%d, %v), want (0, nil)", n, err)
	}

	doc := seedDocument(t, ctx, docRepo)
	chunks := []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "one", TokenCount: 1},
		{DocumentID: doc.ID, ChunkIndex: 1, Text: "two", TokenCount: 1},
	}
	if err := chunkRepo.BulkInsert(ctx, chunks); err != nil {
		t.Fatalf("BulkInsert: %v", err)
	}

	n, err := chunkRepo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 chunks, got %d", n)
	}
}
