package cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/eval"
)

// fakeAnsweringServer answers questions and, when the judge's own prompt turns
// up, returns a verdict instead. One server stands in for both calls the
// generation stage makes, which is also how a real run spends one provider.
func fakeAnsweringServer(t *testing.T, answer, verdict string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/embedding", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]float32{"embedding": {1, 0, 0, 0}})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reply := answer
		if strings.Contains(string(body), "Grade two things separately") {
			reply = verdict
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]string{"content": reply}}},
		})
		_, _ = io.WriteString(w, "data: "+string(chunk)+"\n\ndata: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// evalHome sets up a knowledge base with one document and one label set that
// carries both halves of a case, and returns the home directory.
func evalHome(t *testing.T, srv *httptest.Server, labels string) string {
	t.Helper()
	home := t.TempDir()
	setHome(t, home)

	mustRun(t, "init")
	writeConfig(t, filepath.Join(home, ".tbuk", "config.yaml"), config.Config{
		Database:   config.DatabaseConfig{Path: filepath.Join(home, ".tbuk", "tbuk.sqlite")},
		LLM:        config.LLMConfig{Provider: "llama", MaxTokens: 256, BaseURL: srv.URL},
		Embedding:  config.EmbeddingConfig{Provider: "llama", Dimension: 4, BaseURL: srv.URL},
		Chunking:   config.ChunkingConfig{Size: 800, Overlap: 100},
		Preprocess: config.PreprocessConfig{OutputDir: filepath.Join(home, ".tbuk", "extracted")},
		Ingest:     config.IngestConfig{EmbedConcurrency: 1},
		Prompts:    config.PromptsConfig{Dir: filepath.Join(home, ".tbuk", "prompts")},
		Session:    config.SessionConfig{HistoryTurns: 6},
		Eval:       config.EvalConfig{Dir: filepath.Join(home, ".tbuk", "eval")},
	})

	fixture := filepath.Join(home, "go.md")
	writeFile(t, fixture, "# Go\n\nSlices grow by doubling their capacity when append finds len == cap.\n")
	mustRun(t, "ingest", fixture)

	writeFile(t, filepath.Join(home, ".tbuk", "eval", "go-docs.yaml"), labels)
	return home
}

const generationLabels = `version: 1
name: go-docs
cases:
  - id: slices
    query: how do slices grow?
    relevant:
      - path: go.md
    answer: append reallocates when len == cap, roughly doubling capacity.
    must_include: [doubling, capacity]
`

func TestEvalCommand_stageBothWithJudge(t *testing.T) {
	srv := fakeAnsweringServer(t,
		"Slices grow by doubling their capacity (go.md).",
		`{"correctness": 2, "correctness_reason": "right", "faithfulness": 2, "faithfulness_reason": "supported"}`)
	evalHome(t, srv, generationLabels)

	out := mustRun(t, "eval", "--stage", "both", "--judge", "--format", "json")
	var report eval.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("eval --format json: %v\n%s", err, out)
	}

	if report.Run.Stage != eval.StageBoth {
		t.Errorf("Run.Stage = %q, want both", report.Run.Stage)
	}
	if report.Overall.Cases != 1 {
		t.Errorf("retrieval cases = %d, want 1 — both halves are scored", report.Overall.Cases)
	}
	if report.Generation == nil {
		t.Fatalf("no generation half:\n%s", out)
	}
	g := report.Generation.Overall
	if g.Includes != 1 {
		t.Errorf("includes = %.2f, want 1.00 — the answer carries both required substrings", g.Includes)
	}
	if g.Citations != 1 {
		t.Errorf("citations = %.2f, want 1.00 — go.md is indexed", g.Citations)
	}
	if g.Groundedness <= 0 {
		t.Errorf("groundedness = %.2f, want more than 0 — the answer quotes the passage", g.Groundedness)
	}
	if g.Judged != 1 || g.Correctness != 1 || g.Faithfulness != 1 {
		t.Errorf("judge = %d judged, %.2f/%.2f, want 1 judged at 1.00/1.00",
			g.Judged, g.Correctness, g.Faithfulness)
	}
	if report.Run.Judge == "" {
		t.Error("Run.Judge is empty; a judged number has to name the judge that produced it")
	}

	// D7: eval searches and asks. It never writes to the knowledge base, so a
	// generation run leaves no thread behind to pollute the next one.
	if out := mustRun(t, "session", "list"); !strings.Contains(out, "No conversation threads") {
		t.Fatalf("the generation stage recorded a thread:\n%s", out)
	}
}

func TestEvalCommand_judgePromptIsPrintedUnderVerbose(t *testing.T) {
	srv := fakeAnsweringServer(t, "Slices double.",
		`{"correctness": 1, "correctness_reason": "partly", "faithfulness": 1, "faithfulness_reason": "partly"}`)
	evalHome(t, srv, generationLabels)

	// D6: the judge's prompt is versioned code, and printing it is what lets a
	// number be traced back to the question that produced it.
	out := mustRun(t, "eval", "--stage", "generation", "--judge", "--verbose")
	if !strings.Contains(out, "Grade two things separately") {
		t.Errorf("--judge --verbose did not print the judge's prompt:\n%s", out)
	}
	for _, want := range []string{"generation", "correctness", "faithfulness", "partly"} {
		if !strings.Contains(out, want) {
			t.Errorf("verbose report is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hit@") {
		t.Errorf("--stage generation printed retrieval metrics:\n%s", out)
	}
}

func TestEvalCommand_judgeNeedsTheGenerationStage(t *testing.T) {
	srv := fakeAnsweringServer(t, "anything", "{}")
	evalHome(t, srv, generationLabels)

	out, err := runRoot(t, "eval", "--judge")
	if err == nil {
		t.Fatalf("--judge under the retrieval stage succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--stage") {
		t.Errorf("error should point at --stage, got: %v", err)
	}
}

func TestEvalCommand_invalidStage(t *testing.T) {
	srv := fakeAnsweringServer(t, "anything", "{}")
	evalHome(t, srv, generationLabels)

	out, err := runRoot(t, "eval", "--stage", "answers")
	if err == nil {
		t.Fatalf("an unknown stage succeeded:\n%s", out)
	}
	if !strings.Contains(err.Error(), "retrieval") {
		t.Errorf("error should name the stages there are, got: %v", err)
	}
}

func TestEvalCommand_unjudgedCaseIsNotAZero(t *testing.T) {
	// The judge answers with prose rather than a verdict. The deterministic
	// scores still stand; the judged ones are absent rather than nil.
	srv := fakeAnsweringServer(t, "Slices grow by doubling their capacity.", "looks fine to me")
	evalHome(t, srv, generationLabels)

	out := mustRun(t, "eval", "--stage", "generation", "--judge", "--format", "json")
	var report eval.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("eval --format json: %v\n%s", err, out)
	}
	if report.Generation.Overall.Judged != 0 {
		t.Errorf("judged = %d, want 0", report.Generation.Overall.Judged)
	}
	if report.Generation.Overall.Includes != 1 {
		t.Errorf("includes = %.2f, want 1.00 — a failed judge does not touch the free scores",
			report.Generation.Overall.Includes)
	}
	if report.Cases[0].Unjudged == "" {
		t.Error("the row does not say why the case went unjudged")
	}
}
