//go:build record

package eval_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/embeddings"
	"github.com/gotofritz/timbuktu/internal/eval"
	"github.com/gotofritz/timbuktu/internal/preprocess"
	"github.com/gotofritz/timbuktu/internal/rewrite"
	"github.com/gotofritz/timbuktu/internal/searchtext"
)

// corpusDir holds the fixture documents, their labels, and the recorded vectors.
var corpusDir = filepath.Join("testdata", "corpus")

// lsaDimension is what the stopgap embedder is asked for. The corpus rank caps
// it well below this, and the fixture header records what it actually got.
const lsaDimension = 64

// recordBatch is how many texts go to the embedder at once, matching ingest.
const recordBatch = 16

// TestRecordFixtureVectors embeds the fixture corpus and its label set's
// queries, and writes testdata/corpus/vectors.json.
//
//	make eval-record       # the embedder the config names — needs a server
//	make eval-record-lsa   # the offline LSA stopgap, no server, no weights
//
// It is a test behind a build tag rather than a CLI flag because the
// production surface owes nothing to a test concern, and it runs on a machine
// that has a model, which CI does not.
func TestRecordFixtureVectors(t *testing.T) {
	ctx := context.Background()

	texts := append(corpusTexts(t, ctx), queryTexts(t)...)
	embedder, model := recordEmbedder(t, corpusTexts(t, ctx))

	fixture := eval.NewFixture(model, embedder.Dimension(), time.Now().UTC().Truncate(time.Second))
	for start := 0; start < len(texts); start += recordBatch {
		end := start + recordBatch
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		vecs, err := embedder.Embed(ctx, batch)
		if err != nil {
			t.Fatalf("embed batch %d: %v", start/recordBatch, err)
		}
		if len(vecs) != len(batch) {
			t.Fatalf("embed batch %d: sent %d texts, got %d vectors", start/recordBatch, len(batch), len(vecs))
		}
		for i, text := range batch {
			if err := fixture.Put(text, vecs[i]); err != nil {
				t.Fatalf("record: %v", err)
			}
		}
	}

	path := filepath.Join(corpusDir, "vectors.json")
	if err := fixture.Save(path); err != nil {
		t.Fatalf("save %s: %v", path, err)
	}
	t.Logf("recorded %d vectors of dimension %d from %s into %s",
		len(fixture.Vectors), fixture.Dimension, fixture.Model, path)
}

// corpusTexts returns what the embedder is shown for every chunk of the
// fixture corpus: the reduced search encoding, in document then chunk order.
//
// It mirrors the ingest pipeline — preprocess, chunk, reduce — rather than
// reading the knowledge base, so recording needs no database. A drift from
// ingest is not silent: the recorded keys stop matching and every lookup fails
// by name.
func corpusTexts(t *testing.T, ctx context.Context) []string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(corpusDir, "*.md"))
	if err != nil {
		t.Fatalf("glob corpus: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("no documents in %s", corpusDir)
	}
	sort.Strings(paths)

	chunker := &chunking.Chunker{
		Size:    config.Defaults().Chunking.Size,
		Overlap: config.Defaults().Chunking.Overlap,
	}
	var texts []string
	for _, path := range paths {
		text, _, _, err := preprocess.Extract(ctx, path)
		if err != nil {
			t.Fatalf("extract %s: %v", path, err)
		}
		for _, chunk := range chunker.Split(text) {
			texts = append(texts, reduceForSearch(chunk.Text))
		}
	}
	return dedupe(texts)
}

// queryTexts returns every query string the deterministic sweeps embed: the
// question as typed (--rewrite off), the window fold (the default), and the
// gold ceiling. The planners that need a model are not recorded — they are not
// reproducible without one, which is why they are not in check-ci.
func queryTexts(t *testing.T) []string {
	t.Helper()

	set, err := eval.LoadSet(filepath.Join(corpusDir, "labels.yaml"))
	if err != nil {
		t.Fatalf("load labels: %v", err)
	}
	window := rewrite.Window{Turns: rewrite.DefaultWindowTurns}

	var texts []string
	for _, c := range set.Cases {
		texts = append(texts, c.Query)
		if c.GoldQuery != "" {
			texts = append(texts, c.GoldQuery)
		}
		turns := make([]conversation.Turn, len(c.Thread))
		for i, turn := range c.Thread {
			turns[i] = conversation.Turn{Question: turn.Question, Answer: turn.Answer}
		}
		planned, err := window.Queries(context.Background(), turns, c.Query)
		if err != nil {
			t.Fatalf("plan case %q: %v", c.ID, err)
		}
		texts = append(texts, planned...)
	}
	return dedupe(texts)
}

// recordEmbedder returns the instrument to record with, and the name that goes
// into the fixture header. There is no silent fallback between the two: a
// missing config is an error naming the stopgap, because a fixture recorded by
// something other than what the header claims is the one failure the frozen
// vectors exist to prevent.
func recordEmbedder(t *testing.T, corpus []string) (embeddings.Embedder, string) {
	t.Helper()

	if os.Getenv("EVAL_EMBEDDER") == "lsa" {
		e, err := eval.FitLSA(corpus, lsaDimension)
		if err != nil {
			t.Fatalf("fit lsa: %v", err)
		}
		return e, fmt.Sprintf("lsa/tfidf-svd-%d", e.Dimension())
	}

	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatalf("load config: %v\nrun `make eval-record-lsa` to record with the offline stopgap instead", err)
	}
	e, err := embeddings.NewEmbedder(cfg.Embedding)
	if err != nil {
		t.Fatalf("build embedder: %v", err)
	}
	name := cfg.Embedding.Provider
	if cfg.Embedding.Model != "" {
		name += "/" + cfg.Embedding.Model
	}
	return e, name
}

// reduceForSearch is ingest's encoding of a chunk for the embedder, repeated
// here because it is unexported there: a chunk of pure syntax reduces to
// nothing and keeps its text as written.
func reduceForSearch(text string) string {
	reduced := searchtext.Reduce(text)
	if strings.TrimSpace(reduced) == "" {
		return text
	}
	return reduced
}

// dedupe drops repeats while keeping first-seen order, so one text is embedded
// once however many cases ask for it.
func dedupe(texts []string) []string {
	seen := make(map[string]struct{}, len(texts))
	out := make([]string, 0, len(texts))
	for _, text := range texts {
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}
