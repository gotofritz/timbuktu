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

// The two-phase vector search ranks on (id, embedding) then hydrates
// text/path/title for only the top-K ids. This pins that the hydrated fields
// are joined back to the correct chunk in the correct (descending-score) order
// — a mismatched id→row map would silently return the wrong text.
func TestVectorSearch_hydratesCorrectRowsInScoreOrder(t *testing.T) {
	db := openTestDB(t)
	docA := seedDoc(t, db, "/a.txt", "Doc A")
	docB := seedDoc(t, db, "/b.txt", "Doc B")

	// Query {1,0,0}. Cosine similarity descends: 1.0 > 0.8 > 0.6 > 0.
	seedChunk(t, db, docA, 0, "perfect", []float32{1, 0, 0})    // 1.00
	seedChunk(t, db, docB, 7, "strong", []float32{0.8, 0.6, 0}) // 0.80
	seedChunk(t, db, docA, 3, "weak", []float32{0.6, 0.8, 0})   // 0.60
	seedChunk(t, db, docB, 9, "orthogonal", []float32{0, 1, 0}) // 0.00

	emb := &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}
	s := search.New(db, emb)

	results, err := s.Vector(context.Background(), "query", search.Options{TopK: 3})
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}

	type want struct {
		text  string
		path  string
		title string
		docID int64
		idx   int
	}
	wants := []want{
		{"perfect", "/a.txt", "Doc A", docA, 0},
		{"strong", "/b.txt", "Doc B", docB, 7},
		{"weak", "/a.txt", "Doc A", docA, 3},
	}
	for i, w := range wants {
		r := results[i]
		if r.Text != w.text || r.Path != w.path || r.Title != w.title ||
			r.DocumentID != w.docID || r.ChunkIndex != w.idx {
			t.Errorf("result[%d] = {text:%q path:%q title:%q doc:%d idx:%d}, want %+v",
				i, r.Text, r.Path, r.Title, r.DocumentID, r.ChunkIndex, w)
		}
		if r.Source != "vector" {
			t.Errorf("result[%d].Source = %q, want vector", i, r.Source)
		}
		if i > 0 && results[i-1].Score < r.Score {
			t.Errorf("results not in descending score order at %d: %.4f < %.4f",
				i, results[i-1].Score, r.Score)
		}
	}
}

func BenchmarkVectorSearch(b *testing.B) {
	db := openBenchDB(b, 20000)
	s := search.New(db, &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3})
	b.ResetTimer()
	for range b.N {
		if _, err := s.Vector(context.Background(), "q", search.Options{TopK: 10}); err != nil {
			b.Fatal(err)
		}
	}
}

// openBenchDB builds an in-memory DB seeded with n embedded chunks.
func openBenchDB(b *testing.B, n int) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := storage.RunMigrations(db); err != nil {
		b.Fatalf("migrations: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })

	res, err := db.Exec(
		`INSERT INTO documents(path,sha256,title,mime_type,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		"/bench.txt", "sha-bench", "Bench", "text/plain", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		b.Fatalf("seed doc: %v", err)
	}
	docID, _ := res.LastInsertId()

	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO chunks(document_id,chunk_index,text,token_count,embedding) VALUES(?,?,?,?,?)`)
	if err != nil {
		b.Fatalf("prepare: %v", err)
	}
	for i := 0; i < n; i++ {
		blob := storage.Float32SliceToBlob([]float32{float32(i%7) / 7, float32(i%3) / 3, 0.1})
		if _, err := stmt.Exec(docID, i, fmt.Sprintf("chunk %d body text", i), 5, blob); err != nil {
			b.Fatalf("seed chunk: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
	return db
}
