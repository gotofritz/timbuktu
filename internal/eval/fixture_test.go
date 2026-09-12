package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/eval"
)

const fixtureModel = "lsa/fixture"

// writeFixtureFile writes raw JSON to a temporary fixture path.
func writeFixtureFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vectors.json")
	writeFile(t, path, body)
	return path
}

// twoVectorFixture is a fixture holding one vector per text, saved and reloaded
// so every test starts from the file rather than from the struct.
func twoVectorFixture(t *testing.T) *eval.Fixture {
	t.Helper()
	f := eval.NewFixture(fixtureModel, 2, time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC))
	f.Chunking = eval.ChunkSpec{Size: 60, Overlap: 10}
	if err := f.Put("slices grow", []float32{1, 0}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := f.Put("maps grow", []float32{0, 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return f
}

func TestFixture_saveRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.json")
	want := twoVectorFixture(t)
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := eval.LoadFixture(path)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if got.Model != want.Model || got.Dimension != want.Dimension {
		t.Fatalf("header = %q/%d, want %q/%d", got.Model, got.Dimension, want.Model, want.Dimension)
	}
	if !got.RecordedAt.Equal(want.RecordedAt) {
		t.Fatalf("RecordedAt = %v, want %v", got.RecordedAt, want.RecordedAt)
	}
	if len(got.Vectors) != 2 {
		t.Fatalf("reloaded %d vectors, want 2", len(got.Vectors))
	}

	embedder, err := got.Embedder(fixtureModel, 2)
	if err != nil {
		t.Fatalf("Embedder: %v", err)
	}
	vecs, err := embedder.Embed(context.Background(), []string{"slices grow"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs[0]) != 2 || vecs[0][0] != 1 {
		t.Fatalf("replayed vector = %v, want [1 0]", vecs[0])
	}
}

func TestFixture_keyedBySHA256OfText(t *testing.T) {
	f := twoVectorFixture(t)
	if _, ok := f.Vectors[eval.VectorKey("slices grow")]; !ok {
		t.Fatal("vector is not stored under VectorKey of its text")
	}
	if eval.VectorKey("slices grow") == eval.VectorKey("maps grow") {
		t.Fatal("two texts share a key")
	}
}

func TestFixture_putRejectsAVectorOfTheWrongLength(t *testing.T) {
	f := eval.NewFixture(fixtureModel, 2, time.Now())
	err := f.Put("slices grow", []float32{1, 0, 0})
	if err == nil {
		t.Fatal("Put accepted a 3-value vector into a 2-dimensional fixture")
	}
	if !strings.Contains(err.Error(), "slices grow") {
		t.Errorf("error does not name the text: %v", err)
	}
}

func TestLoadFixture_rejectsAMissingFile(t *testing.T) {
	if _, err := eval.LoadFixture(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("LoadFixture returned no error for a missing file")
	}
}

func TestLoadFixture_rejectsMalformedJSON(t *testing.T) {
	path := writeFixtureFile(t, "{not json")
	if _, err := eval.LoadFixture(path); err == nil {
		t.Fatal("LoadFixture returned no error for malformed JSON")
	}
}

func TestLoadFixture_rejectsAVectorDisagreeingWithItsOwnHeader(t *testing.T) {
	path := writeFixtureFile(t, `{
  "model": "lsa/fixture",
  "dimension": 2,
  "recorded_at": "2026-09-12T10:00:00Z",
  "chunking": {"size": 60, "overlap": 10},
  "vectors": {"aaa": [1, 0, 0]}
}`)
	_, err := eval.LoadFixture(path)
	if err == nil {
		t.Fatal("LoadFixture accepted a 3-value vector under a 2-dimensional header")
	}
	if !strings.Contains(err.Error(), "aaa") {
		t.Errorf("error does not name the offending key: %v", err)
	}
}

func TestFixture_embedderRejectsAHeaderFromAnotherInstrument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.json")
	if err := twoVectorFixture(t).Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	f, err := eval.LoadFixture(path)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}

	tests := []struct {
		name  string
		model string
		dim   int
		want  []string
	}{
		{"another model", "llama/nomic", 2, []string{fixtureModel, "llama/nomic"}},
		{"another dimension", fixtureModel, 768, []string{"2", "768"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.Embedder(tc.model, tc.dim)
			if err == nil {
				t.Fatal("Embedder accepted a mismatched header")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

func TestFixture_embedderMissNamesTheText(t *testing.T) {
	f := twoVectorFixture(t)
	embedder, err := f.Embedder(fixtureModel, 2)
	if err != nil {
		t.Fatalf("Embedder: %v", err)
	}

	_, err = embedder.Embed(context.Background(), []string{"channels block"})
	if err == nil {
		t.Fatal("a cache miss did not fail")
	}
	if !strings.Contains(err.Error(), "channels block") {
		t.Errorf("miss does not name the text: %v", err)
	}
}

func TestFixture_saveReportsAnUnwritablePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if err := twoVectorFixture(t).Save(filepath.Join(dir, "vectors.json")); err == nil {
		t.Fatal("Save returned no error writing into a missing directory")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("Save created the directory it was meant to fail in")
	}
}

// TestCommittedFixture_isRecorded guards the file itself. An empty or absent
// vectors.json is not a quiet loss of coverage — it is a suite that scores
// nothing and still reports, which is the failure the frozen vectors exist to
// prevent. Re-record with `make eval-record` (or `make eval-record-lsa`).
func TestCommittedFixture_isRecorded(t *testing.T) {
	f, err := eval.LoadFixture(filepath.Join("testdata", "corpus", "vectors.json"))
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if f.Model == "" {
		t.Error("fixture header names no model")
	}
	if f.Dimension < 1 {
		t.Errorf("fixture dimension = %d", f.Dimension)
	}
	if f.RecordedAt.IsZero() {
		t.Error("fixture header carries no recording time")
	}
	if len(f.Vectors) == 0 {
		t.Fatal("fixture holds no vectors — run `make eval-record-lsa`")
	}
	if _, err := f.Embedder(f.Model, f.Dimension); err != nil {
		t.Fatalf("fixture will not replay as an embedder: %v", err)
	}
}

func TestFixture_carriesTheChunkingItWasRecordedUnder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.json")
	f := eval.NewFixture(fixtureModel, 2, time.Now())
	f.Chunking = eval.ChunkSpec{Size: 60, Overlap: 10}
	if err := f.Put("slices grow", []float32{1, 0}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := f.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := eval.LoadFixture(path)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if got.Chunking != f.Chunking {
		t.Fatalf("Chunking = %+v, want %+v", got.Chunking, f.Chunking)
	}
}

func TestLoadFixture_rejectsAFixtureThatDoesNotSayHowItWasChunked(t *testing.T) {
	path := writeFixtureFile(t, `{
  "model": "lsa/fixture",
  "dimension": 2,
  "recorded_at": "2026-09-12T10:00:00Z",
  "chunking": {"size": 0, "overlap": 0},
  "vectors": {"aaa": [1, 0]}
}`)
	_, err := eval.LoadFixture(path)
	if err == nil {
		t.Fatal("LoadFixture accepted a fixture with no chunk size")
	}
	if !strings.Contains(err.Error(), "chunk") {
		t.Errorf("error does not mention chunking: %v", err)
	}
}

func TestCommittedFixture_recordsItsChunking(t *testing.T) {
	f, err := eval.LoadFixture(filepath.Join("testdata", "corpus", "vectors.json"))
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if f.Chunking.Size < 1 {
		t.Fatalf("committed fixture records chunk size %d", f.Chunking.Size)
	}
}
