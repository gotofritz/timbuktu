package search_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// openTestDB opens an in-memory SQLite DB with migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatalf("foreign keys: %v", err)
	}
	if err := storage.RunMigrations(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedDoc inserts a document and returns its ID.
func seedDoc(t *testing.T, db *sql.DB, path, title string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO documents(path,sha256,title,mime_type,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		path, fmt.Sprintf("sha-%s", path), title, "text/plain", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("seedDoc: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedChunk inserts a chunk with optional embedding.
func seedChunk(t *testing.T, db *sql.DB, docID int64, idx int, text string, emb []float32) int64 {
	t.Helper()
	var blob []byte
	if emb != nil {
		blob = storage.Float32SliceToBlob(emb)
	}
	res, err := db.Exec(
		`INSERT INTO chunks(document_id,chunk_index,text,token_count,embedding) VALUES(?,?,?,?,?)`,
		docID, idx, text, len(text)/4, blob,
	)
	if err != nil {
		t.Fatalf("seedChunk: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedMeta inserts a metadata key/value for a document.
func seedMeta(t *testing.T, db *sql.DB, docID int64, key, value string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO metadata(document_id,key,value) VALUES(?,?,?)`,
		docID, key, value,
	); err != nil {
		t.Fatalf("seedMeta: %v", err)
	}
}

// stubEmbedder returns a fixed vector for every call.
type stubEmbedder struct {
	vec []float32
	dim int
}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = s.vec
	}
	return out, nil
}
func (s *stubEmbedder) Dimension() int { return s.dim }

// ── Vector Search ──────────────────────────────────────────────────────────────

func TestVectorSearch_returnsTopK(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/a.txt", "Doc A")

	// Chunk 0: perfectly aligned with query → highest score
	seedChunk(t, db, docID, 0, "best match", []float32{1, 0, 0})
	// Chunks 1-9: orthogonal or partial overlap
	for i := 1; i < 10; i++ {
		seedChunk(t, db, docID, i, fmt.Sprintf("other chunk %d", i), []float32{0, 1, 0})
	}

	emb := &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}
	s := search.New(db, emb)

	results, err := s.Vector(context.Background(), "query", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("want 5 results, got %d", len(results))
	}
	if results[0].ChunkIndex != 0 {
		t.Errorf("want top result to be chunk 0, got chunk %d (score %.4f)", results[0].ChunkIndex, results[0].Score)
	}
	if results[0].Source != "vector" {
		t.Errorf("want source=vector, got %q", results[0].Source)
	}
}

func TestVectorSearch_emptyDB(t *testing.T) {
	db := openTestDB(t)
	emb := &stubEmbedder{vec: []float32{1, 0}, dim: 2}
	s := search.New(db, emb)

	results, err := s.Vector(context.Background(), "anything", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results for empty DB, got %d", len(results))
	}
}

func TestVectorSearch_noEmbeddings(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/b.txt", "Doc B")
	seedChunk(t, db, docID, 0, "no embedding here", nil)

	emb := &stubEmbedder{vec: []float32{1, 0}, dim: 2}
	s := search.New(db, emb)

	results, err := s.Vector(context.Background(), "query", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results (no embeddings), got %d", len(results))
	}
}

func TestVectorSearch_minScore(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/c.txt", "Doc C")
	seedChunk(t, db, docID, 0, "aligned", []float32{1, 0, 0})
	seedChunk(t, db, docID, 1, "orthogonal", []float32{0, 1, 0})

	emb := &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}
	s := search.New(db, emb)

	results, err := s.Vector(context.Background(), "q", search.Options{TopK: 10, MinScore: 0.5})
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("want 1 result above threshold, got %d", len(results))
	}
}

// ── Keyword Search ─────────────────────────────────────────────────────────────

func TestKeywordSearch_match(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/d.txt", "Doc D")
	seedChunk(t, db, docID, 0, "authentication uses JWT tokens signed with RS256", nil)
	seedChunk(t, db, docID, 1, "unrelated content about databases", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "JWT", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("want at least one keyword result, got none")
	}
	if results[0].Score <= 0 {
		t.Errorf("want positive score, got %f", results[0].Score)
	}
	if results[0].Source != "keyword" {
		t.Errorf("want source=keyword, got %q", results[0].Source)
	}
}

func TestKeywordSearch_noMatch(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/e.txt", "Doc E")
	seedChunk(t, db, docID, 0, "some text here", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "xyzzy_nonexistent", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results for non-matching query, got %d", len(results))
	}
}

func TestKeywordSearch_emptyDB(t *testing.T) {
	db := openTestDB(t)
	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "anything", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results for empty DB, got %d", len(results))
	}
}

func TestKeywordSearch_sanitizesSpecialChars(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/l.txt", "Doc L")
	seedChunk(t, db, docID, 0, "uses JWT tokens", nil)

	s := search.New(db, nil)

	// Raw FTS5 syntax operators/unbalanced parens/quotes previously errored and
	// were swallowed to nil. After sanitizing, terms become quoted phrases so
	// the query is always valid FTS5.
	for _, q := range []string{`JWT`, `JWT!`, `foo AND (`, `"unterminated`, `NOT OR`, ``} {
		if _, err := s.Keyword(context.Background(), q, search.Options{TopK: 5}); err != nil {
			t.Errorf("query %q: want no error, got %v", q, err)
		}
	}

	// A special-char query still matches the underlying term after sanitizing.
	results, err := s.Keyword(context.Background(), `JWT!`, search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) == 0 {
		t.Error(`want "JWT!" to match "JWT" after sanitizing`)
	}
}

func TestKeywordSearch_dbErrorPropagates(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/m.txt", "Doc M")
	seedChunk(t, db, docID, 0, "content", nil)
	_ = db.Close() // force a real query error

	s := search.New(db, nil)
	if _, err := s.Keyword(context.Background(), "content", search.Options{TopK: 5}); err == nil {
		t.Fatal("want error from closed DB, got nil")
	}
}

// ── Metadata Search ────────────────────────────────────────────────────────────

func TestMetadataSearch_singleFilter(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/f.txt", "Doc F")
	seedMeta(t, db, docID, "lang", "go")
	seedChunk(t, db, docID, 0, "some go code", nil)

	docID2 := seedDoc(t, db, "/g.txt", "Doc G")
	seedMeta(t, db, docID2, "lang", "python")
	seedChunk(t, db, docID2, 0, "python code", nil)

	s := search.New(db, nil)
	results, err := s.Metadata(context.Background(), map[string]string{"lang": "go"})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("want at least one result, got none")
	}
	for _, r := range results {
		if r.DocumentID != docID {
			t.Errorf("want docID=%d, got %d", docID, r.DocumentID)
		}
	}
}

func TestMetadataSearch_multiFilter(t *testing.T) {
	db := openTestDB(t)

	// Doc A: lang=go AND topic=auth → should match
	docA := seedDoc(t, db, "/h.txt", "Doc H")
	seedMeta(t, db, docA, "lang", "go")
	seedMeta(t, db, docA, "topic", "auth")
	seedChunk(t, db, docA, 0, "go auth code", nil)

	// Doc B: lang=go only → should NOT match
	docB := seedDoc(t, db, "/i.txt", "Doc I")
	seedMeta(t, db, docB, "lang", "go")
	seedChunk(t, db, docB, 0, "other go code", nil)

	s := search.New(db, nil)
	results, err := s.Metadata(context.Background(), map[string]string{"lang": "go", "topic": "auth"})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	for _, r := range results {
		if r.DocumentID != docA {
			t.Errorf("got unexpected docID %d (want only %d)", r.DocumentID, docA)
		}
	}
	found := false
	for _, r := range results {
		if r.DocumentID == docA {
			found = true
		}
	}
	if !found {
		t.Error("want Doc H in results")
	}
}

func TestMetadataSearch_manyFilters(t *testing.T) {
	// More than 10 filters previously produced non-alphanumeric JOIN aliases
	// (rune('0'+10) == ':'), yielding a SQL syntax error.
	db := openTestDB(t)

	doc := seedDoc(t, db, "/many.txt", "Doc Many")
	filters := make(map[string]string, 12)
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("k%d", i)
		seedMeta(t, db, doc, key, "v")
		filters[key] = "v"
	}
	seedChunk(t, db, doc, 0, "body", nil)

	s := search.New(db, nil)
	results, err := s.Metadata(context.Background(), filters)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(results) == 0 || results[0].DocumentID != doc {
		t.Errorf("want Doc Many matched by all 12 filters, got %+v", results)
	}
}

func TestMetadataSearch_noFilters(t *testing.T) {
	db := openTestDB(t)
	s := search.New(db, nil)
	results, err := s.Metadata(context.Background(), map[string]string{})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	_ = results // empty filter returns empty results
}

// ── Hybrid Search (RRF) ────────────────────────────────────────────────────────

func TestHybridSearch_combinesResults(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/j.txt", "Doc J")

	// Chunk 0: high cosine AND good FTS match → should rank 1st in hybrid
	seedChunk(t, db, docID, 0, "authentication JWT token RS256 security", []float32{1, 0, 0})
	// Chunk 1: only FTS match
	seedChunk(t, db, docID, 1, "JWT configuration setting", nil)
	// Chunk 2: only vector match
	seedChunk(t, db, docID, 2, "unrelated text about cars", []float32{1, 0, 0})

	emb := &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}
	s := search.New(db, emb)

	results, err := s.Hybrid(context.Background(), "JWT authentication", search.Options{TopK: 3})
	if err != nil {
		t.Fatalf("Hybrid: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("want at least one hybrid result")
	}
	if results[0].Source != "hybrid" {
		t.Errorf("want source=hybrid, got %q", results[0].Source)
	}
	// chunk 0 ranked highest in both — should be first
	if results[0].ChunkIndex != 0 {
		t.Errorf("want chunk 0 first (highest combined rank), got chunk %d", results[0].ChunkIndex)
	}
}

func TestHybridSearch_respectsMinScore(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/n.txt", "Doc N")

	// chunk 0: matches both legs (vector + keyword) → highest RRF (~2/61)
	seedChunk(t, db, docID, 0, "authentication JWT token RS256 security", []float32{1, 0, 0})
	// chunk 1: keyword-only match → single-leg RRF (≤ 1/61)
	seedChunk(t, db, docID, 1, "JWT configuration setting", nil)
	// chunk 2: vector-only match → single-leg RRF (≤ 1/61)
	seedChunk(t, db, docID, 2, "unrelated text about cars", []float32{1, 0, 0})

	emb := &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}
	s := search.New(db, emb)

	// MinScore between a two-leg RRF (~0.0325) and a one-leg RRF (~0.0164).
	results, err := s.Hybrid(context.Background(), "JWT authentication", search.Options{TopK: 5, MinScore: 0.02})
	if err != nil {
		t.Fatalf("Hybrid: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result above MinScore, got %d", len(results))
	}
	if results[0].ChunkIndex != 0 {
		t.Errorf("want chunk 0 (two-leg match), got chunk %d", results[0].ChunkIndex)
	}
}

func TestHybridSearch_emptyDB(t *testing.T) {
	db := openTestDB(t)
	emb := &stubEmbedder{vec: []float32{1, 0}, dim: 2}
	s := search.New(db, emb)

	results, err := s.Hybrid(context.Background(), "query", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Hybrid: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results, got %d", len(results))
	}
}

// ── FTS5 health check ──────────────────────────────────────────────────────────

func TestCheckFTS5_healthy(t *testing.T) {
	db := openTestDB(t)
	if err := search.CheckFTS5(db); err != nil {
		t.Fatalf("CheckFTS5: %v", err)
	}
}
