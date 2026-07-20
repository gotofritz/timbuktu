package ingest_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// countingEmbedder is concurrency-safe and records the peak number of Embed
// calls running simultaneously. When barrier > 0 it blocks each call until
// `barrier` calls have arrived, forcing that many to overlap (proving the
// batches really run in parallel rather than merely being counted mid-flight).
type countingEmbedder struct {
	dim      int
	inFlight int64
	peak     int64

	barrier int
	mu      sync.Mutex
	arrived int
	closed  bool
	release chan struct{}
}

func (c *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	n := atomic.AddInt64(&c.inFlight, 1)
	for {
		p := atomic.LoadInt64(&c.peak)
		if n <= p || atomic.CompareAndSwapInt64(&c.peak, p, n) {
			break
		}
	}
	if c.barrier > 0 {
		c.mu.Lock()
		if c.release == nil {
			c.release = make(chan struct{})
		}
		c.arrived++
		rel := c.release
		if c.arrived >= c.barrier && !c.closed {
			c.closed = true
			close(rel) // enough calls overlap now; let them all through
		}
		c.mu.Unlock()
		<-rel
	}
	atomic.AddInt64(&c.inFlight, -1)
	out := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, c.dim)
		for j := range vec {
			vec[j] = float32(i) + 0.1
		}
		out[i] = vec
	}
	return out, nil
}

func (c *countingEmbedder) Dimension() int { return c.dim }

func ingesterWithConcurrency(t *testing.T, db *storage.DB, emb ingest.Embedder, dir, content string, conc int) *ingest.Ingester {
	t.Helper()
	sqlDB := db.DB()
	return ingest.NewIngester(
		storage.NewDocumentRepo(sqlDB),
		storage.NewChunkRepo(sqlDB),
		storage.NewMetadataRepo(sqlDB),
		&mockExtractor{text: content},
		&chunking.Chunker{Size: 20, Overlap: 0},
		emb,
		dir,
		ingest.WithEmbedConcurrency(conc),
	)
}

// With concurrency > 1 and enough chunks to form several embed batches, more
// than one Embed call must run at once.
func TestIngestFile_embedsBatchesConcurrently(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()

	// ~4600 chars / 20-char chunks = ~230 chunks = ~15 batches of 16, so a
	// barrier of 2 is comfortably reachable.
	content := strings.Repeat("alpha beta gamma delta ", 200)
	path := writeTempFile(t, dir, "big.txt", content)

	emb := &countingEmbedder{dim: 4, barrier: 2}
	ing := ingesterWithConcurrency(t, db, emb, t.TempDir(), content, 4)

	res := ing.IngestFile(context.Background(), path, ingest.Options{})
	if res.Err != nil {
		t.Fatalf("IngestFile: %v", res.Err)
	}
	if peak := atomic.LoadInt64(&emb.peak); peak < 2 {
		t.Fatalf("peak concurrent Embed calls = %d, want >= 2 (batches ran serially)", peak)
	}
}

// Concurrent embedding must not corrupt chunk order: each stored chunk's
// embedding must correspond to its own position within its batch.
func TestIngestFile_concurrentPreservesChunkOrder(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	content := strings.Repeat("one two three four five ", 100)
	path := writeTempFile(t, dir, "ordered.txt", content)

	emb := &countingEmbedder{dim: 4} // no gate: run free
	ing := ingesterWithConcurrency(t, db, emb, t.TempDir(), content, 4)

	res := ing.IngestFile(context.Background(), path, ingest.Options{})
	if res.Err != nil {
		t.Fatalf("IngestFile: %v", res.Err)
	}

	rows, err := db.DB().Query(`SELECT chunk_index, embedding FROM chunks ORDER BY chunk_index`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	batchSize := 16
	for rows.Next() {
		var idx int
		var blob []byte
		if err := rows.Scan(&idx, &blob); err != nil {
			t.Fatalf("scan: %v", err)
		}
		vec, err := storage.BlobToFloat32Slice(blob)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		// mockEmbedder sets each component to float32(posInBatch)+0.1.
		want := float32(idx%batchSize) + 0.1
		if vec[0] != want {
			t.Fatalf("chunk %d: embedding[0] = %v, want %v (batch position corrupted)", idx, vec[0], want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
}
