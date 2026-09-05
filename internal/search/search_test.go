package search_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/searchtext"
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

// seedChunk inserts a chunk with optional embedding. search_text carries the
// reduced encoding the FTS5 index is built from; for the prose these tests use
// it is the text itself, which is what ingest stores too.
func seedChunk(t *testing.T, db *sql.DB, docID int64, idx int, text string, emb []float32) int64 {
	t.Helper()
	var blob []byte
	if emb != nil {
		blob = storage.Float32SliceToBlob(emb)
	}
	res, err := db.Exec(
		`INSERT INTO chunks(document_id,chunk_index,text,search_text,token_count,embedding) VALUES(?,?,?,?,?,?)`,
		docID, idx, text, searchtext.Reduce(text), len(text)/4, blob,
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

// A query embedding whose dimension differs from the stored vectors must
// return a loud error instead of silently scoring everything 0 (cosine
// similarity returns 0 for mismatched lengths).
func TestVectorSearch_dimensionMismatchErrors(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/a.txt", "Doc A")
	seedChunk(t, db, docID, 0, "stored 3-dim", []float32{1, 0, 0})

	// Query embedder now produces 4-dim vectors (model/config changed).
	emb := &stubEmbedder{vec: []float32{1, 0, 0, 0}, dim: 4}
	s := search.New(db, emb)

	_, err := s.Vector(context.Background(), "query", search.Options{TopK: 5})
	if err == nil {
		t.Fatal("expected dimension-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Errorf("error = %v, want it to mention dimension", err)
	}
}

// Options.Metadata must restrict vector results to documents matching the
// AND-combined filters, not be silently ignored.
func TestVectorSearch_metadataPrefilter(t *testing.T) {
	db := openTestDB(t)
	docGo := seedDoc(t, db, "/go.txt", "Go Doc")
	seedMeta(t, db, docGo, "topic", "go")
	seedChunk(t, db, docGo, 0, "go chunk", []float32{1, 0, 0})

	docRust := seedDoc(t, db, "/rust.txt", "Rust Doc")
	seedChunk(t, db, docRust, 0, "rust chunk", []float32{1, 0, 0})

	emb := &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}
	s := search.New(db, emb)

	results, err := s.Vector(context.Background(), "query",
		search.Options{TopK: 5, Metadata: map[string]string{"topic": "go"}})
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result (topic=go only), got %d", len(results))
	}
	if results[0].DocumentID != docGo {
		t.Errorf("want doc %d, got %d", docGo, results[0].DocumentID)
	}
}

func TestKeywordSearch_metadataPrefilter(t *testing.T) {
	db := openTestDB(t)
	docGo := seedDoc(t, db, "/go.txt", "Go Doc")
	seedMeta(t, db, docGo, "topic", "go")
	seedChunk(t, db, docGo, 0, "shared keyword alpha", nil)

	docRust := seedDoc(t, db, "/rust.txt", "Rust Doc")
	seedChunk(t, db, docRust, 0, "shared keyword alpha", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "alpha",
		search.Options{TopK: 5, Metadata: map[string]string{"topic": "go"}})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result (topic=go only), got %d", len(results))
	}
	if results[0].DocumentID != docGo {
		t.Errorf("want doc %d, got %d", docGo, results[0].DocumentID)
	}
}

func TestHybridSearch_metadataPrefilter(t *testing.T) {
	db := openTestDB(t)
	docGo := seedDoc(t, db, "/go.txt", "Go Doc")
	seedMeta(t, db, docGo, "topic", "go")
	seedChunk(t, db, docGo, 0, "shared keyword alpha", []float32{1, 0, 0})

	docRust := seedDoc(t, db, "/rust.txt", "Rust Doc")
	seedChunk(t, db, docRust, 0, "shared keyword alpha", []float32{1, 0, 0})

	emb := &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}
	s := search.New(db, emb)

	results, err := s.Hybrid(context.Background(), "alpha",
		search.Options{TopK: 5, Metadata: map[string]string{"topic": "go"}})
	if err != nil {
		t.Fatalf("Hybrid: %v", err)
	}
	for _, r := range results {
		if r.DocumentID != docGo {
			t.Errorf("result from doc %d leaked past metadata filter (want only %d)",
				r.DocumentID, docGo)
		}
	}
	if len(results) == 0 {
		t.Fatal("want ≥1 result for topic=go, got 0")
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

// TestKeywordSearch_naturalLanguageQuestion pins the behaviour that made
// `tbuk ask` blind to its most on-topic document: a question carries common
// words no single chunk has to contain, so requiring every term matched
// nothing at all and the hybrid keyword leg contributed zero results.
func TestKeywordSearch_naturalLanguageQuestion(t *testing.T) {
	db := openTestDB(t)
	onTopic := seedDoc(t, db, "/ppas.md", "PPAs")
	seedChunk(t, db, onTopic, 0,
		"A PPA is a power purchase agreement. PPAs fix a price for energy over a term.", nil)
	inPassing := seedDoc(t, db, "/market-guide.md", "Market guide")
	seedChunk(t, db, inPassing, 0,
		"What do you know about this market? You should know about tariffs, about grid access, "+
			"and about what you do with surplus power.", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "what do you know about ppas", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("want the on-topic document for a natural-language question, got no results")
	}
	if results[0].Path != "/ppas.md" {
		t.Errorf("want /ppas.md ranked first, got %q", results[0].Path)
	}
}

// TestKeywordSearch_ranksByTermOverlap guards the other half of the trade: now
// that a query no longer demands every term, BM25 ranking is what keeps
// precision. The chunk carrying both terms must beat the one carrying only the
// common one.
func TestKeywordSearch_ranksByTermOverlap(t *testing.T) {
	db := openTestDB(t)
	both := seedDoc(t, db, "/both.md", "Both")
	seedChunk(t, db, both, 0, "power purchase agreements are signed yearly", nil)
	oneTerm := seedDoc(t, db, "/one.md", "One")
	seedChunk(t, db, oneTerm, 0, "the power supply hums", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "power purchase", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want both chunks matched, got %d", len(results))
	}
	if results[0].Path != "/both.md" {
		t.Errorf("want /both.md ranked first, got %q", results[0].Path)
	}
}

// TestKeywordSearch_dropsStopWords checks that a common word in the question
// does not drag in documents that match nothing else.
func TestKeywordSearch_dropsStopWords(t *testing.T) {
	db := openTestDB(t)
	onTopic := seedDoc(t, db, "/ppas.md", "PPAs")
	seedChunk(t, db, onTopic, 0, "PPAs fix a price for energy over a term", nil)
	noise := seedDoc(t, db, "/noise.md", "Noise")
	seedChunk(t, db, noise, 0, "a note about the weather", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "about ppas", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if got := paths(results); len(got) != 1 || got[0] != "/ppas.md" {
		t.Errorf("want only /ppas.md, got %v", got)
	}
}

// TestKeywordSearch_allStopWordsStillMatches keeps a query made entirely of
// stop words usable: dropping every term would silently match nothing.
func TestKeywordSearch_allStopWordsStillMatches(t *testing.T) {
	db := openTestDB(t)
	docID := seedDoc(t, db, "/q.md", "Q")
	seedChunk(t, db, docID, 0, "what is it that we do", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "what is it", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) == 0 {
		t.Error("want a match for an all-stop-word query, got none")
	}
}

// TestHybridSearch_keywordLegSurvivesAQuestion checks the fusion end of the
// same bug: with the keyword leg returning nothing, Hybrid silently degraded to
// vector-only, so a document the embedder ranked poorly could not be rescued by
// an exact term match.
func TestHybridSearch_keywordLegSurvivesAQuestion(t *testing.T) {
	db := openTestDB(t)
	onTopic := seedDoc(t, db, "/ppas.md", "PPAs")
	seedChunk(t, db, onTopic, 0,
		"A PPA is a power purchase agreement. PPAs fix a price for energy over a term.",
		[]float32{0, 1, 0})
	other := seedDoc(t, db, "/notes.md", "Notes")
	for i := range 5 {
		seedChunk(t, db, other, i, "meeting notes about the quarter", []float32{1, 0, 0})
	}

	// The query embedding points away from the on-topic chunk, so only the
	// keyword leg can surface it.
	s := search.New(db, &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3})
	results, err := s.Hybrid(context.Background(), "what do you know about ppas", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Hybrid: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Path == "/ppas.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("want /ppas.md fused in from the keyword leg, got %+v", paths(results))
	}
}

// ── Query semantics (issue #136) ───────────────────────────────────────────────

// seedForms indexes the two chunks the query-semantics rules are stated
// against: the identifier written with an underscore, and the same words
// written apart. Both are plain prose, so searchtext.Reduce passes them
// through and what separates them in the index is the tokenizer alone.
func seedForms(t *testing.T, db *sql.DB, emb []float32) {
	t.Helper()
	under := seedDoc(t, db, "/under.md", "Underscore")
	seedChunk(t, db, under, 0, "the main_consumption register is read hourly", emb)
	spaced := seedDoc(t, db, "/spaced.md", "Spaced")
	seedChunk(t, db, spaced, 0, "the main consumption register is read hourly", emb)
}

// The rules of issue #136 on the keyword leg. Typing the punctuation is a
// signal of intent: someone who goes to the trouble of typing
// `main_consumption` wants that string, and someone who types the words apart
// is happy with either form.
func TestKeywordSearch_operatorSemantics(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"exact term matches the underscore form only", "main_consumption", []string{"/under.md"}},
		{"negation excludes the underscore form", "main consumption -main_consumption", []string{"/spaced.md"}},
		{"a quoted phrase matches the words as written", `"main consumption"`, []string{"/spaced.md"}},
		{"stop words survive inside a phrase", `"the main consumption"`, []string{"/spaced.md"}},
		{"an inner dash is part of the term, not an exclusion", "read-hourly", nil},
		{"an exclusion with nothing to exclude from matches nothing", "-main_consumption", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			seedForms(t, db, nil)

			s := search.New(db, nil)
			results, err := s.Keyword(context.Background(), tt.query, search.Options{TopK: 5, Operators: true})
			if err != nil {
				t.Fatalf("Keyword: %v", err)
			}
			assertPaths(t, results, tt.want)
		})
	}
}

// The loose query reaches an identifier because searchtext.Reduce emitted its
// split form into the indexed text (issue #138) — not because the tokenizer
// took the identifier apart, which is exactly what it no longer does. So the
// reach depends on the encoding: an identifier inside a fence or an inline
// span carries its split form, one written bare in prose does not.
func TestKeywordSearch_looseQueryReachesSplitForms(t *testing.T) {
	db := openTestDB(t)
	code := seedDoc(t, db, "/code.md", "Code")
	seedChunk(t, db, code, 0, "```go\nfunc readMeter() { main_consumption++ }\n```", nil)
	prose := seedDoc(t, db, "/prose.md", "Prose")
	seedChunk(t, db, prose, 0, "the main consumption register is read hourly", nil)
	span := seedDoc(t, db, "/span.md", "Span")
	seedChunk(t, db, span, 0, "the `main_consumption` register is read hourly", nil)
	bare := seedDoc(t, db, "/bare.md", "Bare prose")
	seedChunk(t, db, bare, 0, "the main_consumption register is read hourly", nil)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"exact term reaches every encoding of it", "main_consumption",
			[]string{"/bare.md", "/code.md", "/span.md"}},
		{"loose words reach the split forms, but not a bare prose identifier", "main consumption",
			[]string{"/code.md", "/prose.md", "/span.md"}},
		{"negation leaves the prose behind", "main consumption -main_consumption",
			[]string{"/prose.md"}},
		{"camelCase identifier", "readMeter", []string{"/code.md"}},
		{"a word only the split form carries", "meter", []string{"/code.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := search.New(db, nil)
			results, err := s.Keyword(context.Background(), tt.query, search.Options{TopK: 10, Operators: true})
			if err != nil {
				t.Fatalf("Keyword: %v", err)
			}
			assertPaths(t, results, tt.want)
		})
	}
}

// The goal of the encoding working end to end: a prose query finds the code
// that defines the thing. Every chunk here contains "read", so an OR-combined
// query reaches them all — what matters is that the split form of readMeter
// puts the code chunk on top.
func TestKeywordSearch_proseQueryRanksTheCodeFirst(t *testing.T) {
	db := openTestDB(t)
	code := seedDoc(t, db, "/code.md", "Code")
	seedChunk(t, db, code, 0, "```go\nfunc readMeter() { main_consumption++ }\n```", nil)
	prose := seedDoc(t, db, "/prose.md", "Prose")
	seedChunk(t, db, prose, 0, "the main consumption register is read hourly", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "read meter", search.Options{TopK: 5, Operators: true})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	if len(results) == 0 || results[0].Path != "/code.md" {
		t.Errorf("want /code.md ranked first, got %v", paths(results))
	}
}

// A phrase matches the words adjacent in the *indexed* text, and the split
// form searchtext.Reduce emits sits right beside the identifier it came from —
// so a phrase alone cannot separate the two forms once an identifier is in a
// code span. Combining it with an exclusion can, which is the composition the
// user guide points at.
func TestKeywordSearch_phraseAgainstASplitForm(t *testing.T) {
	db := openTestDB(t)
	span := seedDoc(t, db, "/span.md", "Span")
	seedChunk(t, db, span, 0, "the `main_consumption` register is read hourly", nil)
	prose := seedDoc(t, db, "/prose.md", "Prose")
	seedChunk(t, db, prose, 0, "the main consumption register is read hourly", nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), `"main consumption"`, search.Options{TopK: 5, Operators: true})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	assertPaths(t, results, []string{"/prose.md", "/span.md"})

	results, err = s.Keyword(context.Background(), `"main consumption" -main_consumption`,
		search.Options{TopK: 5, Operators: true})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	assertPaths(t, results, []string{"/prose.md"})
}

// Without Operators the same input is a bag of words, which is what `tbuk ask`
// sends: a question is not an expression, and reading one as an expression is
// how its keyword leg comes back empty.
func TestKeywordSearch_lenientPathIgnoresOperators(t *testing.T) {
	db := openTestDB(t)
	seedForms(t, db, nil)

	s := search.New(db, nil)
	results, err := s.Keyword(context.Background(), "consumption -main_consumption", search.Options{TopK: 5})
	if err != nil {
		t.Fatalf("Keyword: %v", err)
	}
	assertPaths(t, results, []string{"/spaced.md"})
}

// An exclusion has to survive fusion. The keyword leg drops the excluded chunk,
// but the vector leg knows nothing about NOT and would hand it straight back.
func TestHybridSearch_honoursExclusions(t *testing.T) {
	db := openTestDB(t)
	seedForms(t, db, []float32{1, 0, 0})

	s := search.New(db, &stubEmbedder{vec: []float32{1, 0, 0}, dim: 3})
	results, err := s.Hybrid(context.Background(), "main consumption -main_consumption",
		search.Options{TopK: 5, Operators: true})
	if err != nil {
		t.Fatalf("Hybrid: %v", err)
	}
	assertPaths(t, results, []string{"/spaced.md"})
}

// The vector leg embeds the positive half of the query. Embedding the operator
// syntax verbatim would pull the excluded chunks towards the query.
func TestVectorSearch_embedsPositiveTermsOnly(t *testing.T) {
	db := openTestDB(t)
	seedForms(t, db, []float32{1, 0, 0})

	emb := &recordingEmbedder{stubEmbedder: stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}}
	s := search.New(db, emb)
	if _, err := s.Vector(context.Background(), `"main consumption" register -main_consumption`,
		search.Options{TopK: 5, Operators: true}); err != nil {
		t.Fatalf("Vector: %v", err)
	}
	if want := "main consumption register"; emb.last != want {
		t.Errorf("want embedded text %q, got %q", want, emb.last)
	}
}

// A query with nothing left to embed never reaches the embedder.
func TestVectorSearch_exclusionOnlyQueryReturnsNothing(t *testing.T) {
	db := openTestDB(t)
	seedForms(t, db, []float32{1, 0, 0})

	emb := &recordingEmbedder{stubEmbedder: stubEmbedder{vec: []float32{1, 0, 0}, dim: 3}}
	s := search.New(db, emb)
	results, err := s.Vector(context.Background(), "-main_consumption", search.Options{TopK: 5, Operators: true})
	if err != nil {
		t.Fatalf("Vector: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want no results, got %v", paths(results))
	}
	if emb.last != "" {
		t.Errorf("want the embedder left alone, got %q", emb.last)
	}
}

// recordingEmbedder remembers the last text it was asked to embed.
type recordingEmbedder struct {
	stubEmbedder
	last string
}

func (r *recordingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) > 0 {
		r.last = texts[0]
	}
	return r.stubEmbedder.Embed(ctx, texts)
}

// assertPaths compares the paths of results with want, order-insensitively.
func assertPaths(t *testing.T, results []search.SearchResult, want []string) {
	t.Helper()
	got := paths(results)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func paths(results []search.SearchResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Path)
	}
	return out
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

// TestCheckFTS5_releasesConnection pins the pool to a single connection and
// calls CheckFTS5 repeatedly. The old implementation discarded the *sql.Rows
// without closing it, so the one connection stayed checked out and the second
// call blocked forever waiting for the pool. Each call must return promptly.
func TestCheckFTS5_releasesConnection(t *testing.T) {
	db := openTestDB(t)
	db.SetMaxOpenConns(1)

	for i := 0; i < 3; i++ {
		done := make(chan error, 1)
		go func() { done <- search.CheckFTS5(db) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("CheckFTS5 call %d: %v", i, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("CheckFTS5 call %d blocked — connection leaked", i)
		}
	}
}
