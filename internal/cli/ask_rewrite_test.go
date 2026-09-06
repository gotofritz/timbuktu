package cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
)

// fakeRewriteServer answers embeddings, condense calls and answer calls from
// one endpoint.
func fakeRewriteServer(t *testing.T, condensed string) *httptest.Server {
	t.Helper()
	return fakePlannerServer(t, condensed, "")
}

// fakePlannerServer answers embeddings and every chat call the planner and the
// answer make, telling them apart by their system prompts. Doing it by content
// rather than by call order keeps the assertion honest however many times the
// command reaches for the model.
func fakePlannerServer(t *testing.T, condensed, expanded string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/embedding", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]float32{"embedding": {1, 0, 0, 0}})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := "they double"
		switch {
		case strings.Contains(string(body), "You rewrite the last question"):
			text = condensed
		case strings.Contains(string(body), "You write alternative search queries"):
			text = expanded
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]string{"content": text}}},
		})
		_, _ = io.WriteString(w, "data: "+string(payload)+"\n\ndata: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// rewriteHome sets up a knowledge base with one document, pointed at srv.
func rewriteHome(t *testing.T, srv *httptest.Server) string {
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
	})
	fixture := filepath.Join(home, "go.md")
	writeFile(t, fixture, "# Go\n\nSlices grow by doubling. Maps rehash when they fill.\n")
	mustRun(t, "ingest", fixture)
	return home
}

// setManifestRewrite adds a rewrite mode to a template's existing retrieval
// block. Appending a second `retrieval:` key would be a duplicate YAML key,
// which loadManifest rejects before the test gets to say anything.
func setManifestRewrite(t *testing.T, path, mode string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := strings.Replace(string(data), "retrieval:\n", "retrieval:\n  rewrite: "+mode+"\n", 1)
	if out == string(data) {
		t.Fatalf("%s has no retrieval block to add a rewrite mode to", path)
	}
	writeFile(t, path, out)
}

// A typo in --rewrite fails before anything is opened or any model is called —
// the same rule loadManifest follows for the manifest key.
func TestAskCommand_rewriteRejectsUnknownMode(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")

	out, err := runRoot(t, "ask", "--rewrite", "nonsense", "how do slices grow?")
	if err == nil {
		t.Fatalf("want an error for an unknown mode, got success:\n%s", out)
	}
	for _, want := range []string{"--rewrite", "nonsense"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The whole point of milestone 3: the follow-up reaches retrieval as a question
// that stands on its own, not as the words of the thread dragged along.
func TestAskCommand_condensePlansTheQuery(t *testing.T) {
	srv := fakeRewriteServer(t, "how do Go maps grow?")
	rewriteHome(t, srv)

	mustRun(t, "ask", "--session", "go", "how do slices grow?")
	mustRun(t, "ask", "-c", "--rewrite", "condense", "and maps?")

	out := mustRun(t, "session", "show", "go", "--verbose")
	if !strings.Contains(out, "[query] how do Go maps grow?") {
		t.Fatalf("the follow-up did not retrieve on the condensed question:\n%s", out)
	}
}

// The flag overrides the manifest for one run, in both directions: a template
// that asks to be condensed can be asked not to be.
func TestAskCommand_rewriteFlagOverridesTheManifest(t *testing.T) {
	srv := fakeRewriteServer(t, "how do Go maps grow?")
	home := rewriteHome(t, srv)
	setManifestRewrite(t, filepath.Join(home, ".tbuk", "prompts", "qa", "manifest.yaml"), "condense")

	mustRun(t, "ask", "--session", "go", "how do slices grow?")
	mustRun(t, "ask", "-c", "--rewrite", "window", "and maps?")

	out := mustRun(t, "session", "show", "go", "--verbose")
	if !strings.Contains(out, "[query] how do slices grow? and maps?") {
		t.Fatalf("--rewrite window did not override the manifest's condense:\n%s", out)
	}
}

// A manifest that names condense gets it with no flag at all.
func TestAskCommand_condenseFromTheManifest(t *testing.T) {
	srv := fakeRewriteServer(t, "how do Go maps grow?")
	home := rewriteHome(t, srv)
	setManifestRewrite(t, filepath.Join(home, ".tbuk", "prompts", "qa", "manifest.yaml"), "condense")

	mustRun(t, "ask", "--session", "go", "how do slices grow?")
	out := mustRun(t, "session", "show", "go", "--verbose")
	if !strings.Contains(out, "[query] how do Go maps grow?") {
		t.Fatalf("the manifest's condense did not plan the query:\n%s", out)
	}
}

// An LLM in the retrieval path may not cost the answer: a rewrite that comes
// back empty is a warning and the window's query, not a failed ask (D6).
func TestAskCommand_condenseFallsBackToTheWindow(t *testing.T) {
	srv := fakeRewriteServer(t, "")
	rewriteHome(t, srv)

	mustRun(t, "ask", "--session", "go", "how do slices grow?")
	out := mustRun(t, "ask", "-c", "--rewrite", "condense", "and maps?")
	if !strings.Contains(out, "could not condense") {
		t.Fatalf("a failed rewrite said nothing on the diagnostics stream:\n%s", out)
	}

	show := mustRun(t, "session", "show", "go", "--verbose")
	if !strings.Contains(show, "[query] how do slices grow? and maps?") {
		t.Fatalf("the failed rewrite did not fall back to the window:\n%s", show)
	}
}

// `off` inside a thread is the escape hatch for a corpus whose questions always
// stand on their own.
func TestAskCommand_rewriteOffRetrievesTheQuestionAsTyped(t *testing.T) {
	srv := fakeRewriteServer(t, "unused")
	rewriteHome(t, srv)

	mustRun(t, "ask", "--session", "go", "how do slices grow?")
	mustRun(t, "ask", "-c", "--rewrite", "off", "and maps?")

	out := mustRun(t, "session", "show", "go", "--verbose")
	if !strings.Contains(out, "[query] and maps?") {
		t.Fatalf("--rewrite off folded the thread in anyway:\n%s", out)
	}
}

// Condensing strips chit-chat and fixes typos, which is worth something on a
// question with no thread behind it — so a single-shot ask can ask for it.
func TestAskCommand_condenseWithoutASession(t *testing.T) {
	var condenseCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/embedding", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]float32{"embedding": {1, 0, 0, 0}})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := "they double"
		if strings.Contains(string(body), "You rewrite the last question") {
			condenseCalls++
			text = "how do slices grow?"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]string{"content": text}}},
		})
		_, _ = io.WriteString(w, "data: "+string(payload)+"\n\ndata: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	rewriteHome(t, srv)

	mustRun(t, "ask", "--rewrite", "condense", "hey so umm how do slices grow")
	if condenseCalls != 1 {
		t.Errorf("condense calls = %d, want exactly 1", condenseCalls)
	}
}

// The regression bar: with no --rewrite and a default manifest, a single-shot
// ask spends exactly one model call — the answer's. No planner, no second call.
func TestAskCommand_singleShotSpendsNoRewriteCall(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/embedding", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]float32{"embedding": {1, 0, 0, 0}})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"they double\"}}]}\n\ndata: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	rewriteHome(t, srv)

	mustRun(t, "ask", "how do slices grow?")
	if calls != 1 {
		t.Errorf("model calls = %d, want exactly 1 (the answer)", calls)
	}
}

// chat takes the same flag, because a REPL turn and a threaded ask differ in
// how they are typed and in nothing else.
func TestChatCommand_condensePlansTheQuery(t *testing.T) {
	srv := fakeRewriteServer(t, "how do Go maps grow?")
	rewriteHome(t, srv)

	mustRunStdin(t, "and maps?\n/exit\n", "chat", "--session", "go", "--rewrite", "condense")

	out := mustRun(t, "session", "show", "go", "--verbose")
	if !strings.Contains(out, "[query] how do Go maps grow?") {
		t.Fatalf("chat --rewrite condense did not plan the query:\n%s", out)
	}
}

func TestChatCommand_rewriteRejectsUnknownMode(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")

	out, err := runRoot(t, "chat", "--rewrite", "nonsense")
	if err == nil {
		t.Fatalf("want an error for an unknown mode, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--rewrite") {
		t.Errorf("error %q does not mention the flag", err)
	}
}
