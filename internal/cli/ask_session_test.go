package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/rewrite"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// recorder collects the turns RunAsk appends, standing in for SessionRepo.
type recorder struct {
	turns []conversation.Turn
	ids   []int64
	err   error
}

func (r *recorder) append(_ context.Context, sessionID int64, turn conversation.Turn) error {
	if r.err != nil {
		return r.err
	}
	r.ids = append(r.ids, sessionID)
	r.turns = append(r.turns, turn)
	return nil
}

// capturingRetrieve records the query retrieval was actually run on.
func capturingRetrieve(query *string, chunks []retrieval.RetrievedChunk) func(context.Context, string, int, map[string]string) ([]retrieval.RetrievedChunk, error) {
	return func(_ context.Context, q string, _ int, _ map[string]string) ([]retrieval.RetrievedChunk, error) {
		*query = q
		return chunks, nil
	}
}

// threadOf builds a thread of question/answer pairs.
func threadOf(pairs ...string) *conversation.Thread {
	th := &conversation.Thread{ID: 7, Name: "go", Template: "qa"}
	for i := 0; i+1 < len(pairs); i += 2 {
		th.Turns = append(th.Turns, conversation.Turn{Question: pairs[i], Answer: pairs[i+1]})
	}
	return th
}

// Prior turns go back to the model as role-tagged pairs ahead of the current
// turn's rendered user message — not as text inlined into the template (D3).
func TestRunAsk_sessionReplaysHistoryAsMessages(t *testing.T) {
	var out bytes.Buffer
	var msgs []llm.Message
	rec := &recorder{}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(nil, nil), recordingChat(&msgs),
		buildQATemplate(t), "and maps?", nil, 0, false,
		cli.WithSession(threadOf("how do slices grow?", "they double"), rec.append))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}

	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want system + one pair + the current question: %v", len(msgs), msgs)
	}
	want := []llm.Message{
		{Role: llm.RoleSystem, Content: msgs[0].Content},
		{Role: llm.RoleUser, Content: "how do slices grow?"},
		{Role: llm.RoleAssistant, Content: "they double"},
		{Role: llm.RoleUser, Content: msgs[3].Content},
	}
	for i := range want {
		if msgs[i].Role != want[i].Role || msgs[i].Content != want[i].Content {
			t.Errorf("message %d = %+v, want %+v", i, msgs[i], want[i])
		}
	}
	if !strings.Contains(msgs[3].Content, "and maps?") {
		t.Errorf("the current question should be the last user message, got %q", msgs[3].Content)
	}
	if strings.Contains(msgs[3].Content, "how do slices grow?") {
		t.Errorf("history must not be inlined into the rendered prompt, got %q", msgs[3].Content)
	}
}

// `and maps?` retrieved on its own reaches nothing: the planner folds the
// thread into the query so the second turn's evidence is about maps, not about
// whatever the first turn happened to return (D5).
func TestRunAsk_sessionRetrievesOnThePlannedQuery(t *testing.T) {
	var out bytes.Buffer
	var query string
	rec := &recorder{}

	err := cli.RunAsk(context.Background(), &out, capturingRetrieve(&query, nil), mockChat([]string{"ok"}, nil),
		buildQATemplate(t), "and maps?", nil, 0, false,
		cli.WithSession(threadOf("how do slices grow?", "they double"), rec.append),
		cli.WithPlanner(rewrite.Window{Turns: 2}))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if query != "how do slices grow? and maps?" {
		t.Errorf("retrieval query = %q, want the folded one", query)
	}
	if len(rec.turns) != 1 {
		t.Fatalf("want one appended turn, got %d", len(rec.turns))
	}
	if rec.turns[0].Query != query {
		t.Errorf("stored query = %q, want the query that ran (%q)", rec.turns[0].Query, query)
	}
	if rec.turns[0].Question != "and maps?" {
		t.Errorf("stored question = %q, want it as typed", rec.turns[0].Question)
	}
}

// A stored turn is question + query + answer + citations, against the thread it
// was asked in.
func TestRunAsk_sessionAppendsTheCompletedTurn(t *testing.T) {
	var out bytes.Buffer
	rec := &recorder{}
	chunks := []retrieval.RetrievedChunk{
		{Citation: "/a.md §0", Text: "slices grow by doubling"},
		{Citation: "/b.md §1", Text: "maps rehash"},
	}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(chunks, nil),
		mockChat([]string{"they ", "double"}, nil),
		buildQATemplate(t), "how do slices grow?", nil, 0, false,
		cli.WithSession(threadOf(), rec.append))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(rec.turns) != 1 {
		t.Fatalf("want one appended turn, got %d", len(rec.turns))
	}
	turn := rec.turns[0]
	if turn.Answer != "they double" {
		t.Errorf("stored answer = %q, want the whole streamed completion", turn.Answer)
	}
	if len(turn.Citations) != 2 || turn.Citations[0] != "/a.md §0" {
		t.Errorf("stored citations = %v, want both display strings", turn.Citations)
	}
	if len(rec.ids) != 1 || rec.ids[0] != 7 {
		t.Errorf("appended against session %v, want the thread's id 7", rec.ids)
	}
}

// A provider error mid-stream writes nothing: half an answer replayed as
// context is worse than a thread that lost a turn (D10).
func TestRunAsk_sessionAppendsNothingOnStreamError(t *testing.T) {
	var out bytes.Buffer
	rec := &recorder{}
	failing := func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		ch := make(chan llm.Token, 2)
		ch <- llm.Token{Text: "half an ans"}
		ch <- llm.Token{Error: errors.New("connection reset")}
		close(ch)
		return ch, nil
	}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(nil, nil), failing,
		buildQATemplate(t), "q", nil, 0, false,
		cli.WithSession(threadOf(), rec.append))
	if err == nil {
		t.Fatal("want the stream error surfaced, got nil")
	}
	if len(rec.turns) != 0 {
		t.Errorf("want nothing appended after a failed stream, got %v", rec.turns)
	}
}

// A completion with no text in it is not a turn worth replaying: an empty
// assistant message teaches the model nothing and costs the next prompt a pair.
func TestRunAsk_sessionAppendsNothingForAnEmptyAnswer(t *testing.T) {
	var out, errOut bytes.Buffer
	rec := &recorder{}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(nil, nil), mockChat(nil, nil),
		buildQATemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithSession(threadOf(), rec.append))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(rec.turns) != 0 {
		t.Errorf("want nothing appended for an empty answer, got %v", rec.turns)
	}
	if !strings.Contains(errOut.String(), "not recorded") {
		t.Errorf("want the warning to say the turn was not recorded, got: %q", errOut.String())
	}
}

// The stored answer is the normalized text — what the user actually saw.
func TestRunAsk_sessionStoresTheNormalizedAnswer(t *testing.T) {
	tmpl := buildTemplateWith(t,
		"name: qa\nnormalize:\n  filters: [strip_preamble]\noutput: text\n",
		"sys", "{{ .Question }}")
	var out bytes.Buffer
	rec := &recorder{}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(nil, nil),
		mockChat([]string{"Here is the answer:\n\nthey double"}, nil),
		tmpl, "q", nil, 0, false,
		cli.WithSession(threadOf(), rec.append))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(rec.turns) != 1 {
		t.Fatalf("want one appended turn, got %d", len(rec.turns))
	}
	if got := rec.turns[0].Answer; got != strings.TrimSpace(out.String()) {
		t.Errorf("stored answer %q, want what was printed (%q)", got, strings.TrimSpace(out.String()))
	}
}

// A thread that cannot be recorded is an error the user hears about: the
// answer is on screen, but the next --continue would silently miss this turn.
func TestRunAsk_sessionAppendError(t *testing.T) {
	var out bytes.Buffer
	rec := &recorder{err: errors.New("database is locked")}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(nil, nil), mockChat([]string{"ok"}, nil),
		buildQATemplate(t), "q", nil, 0, false,
		cli.WithSession(threadOf(), rec.append))
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Errorf("want the append failure surfaced, got %v", err)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("the answer should still have been printed, got %q", out.String())
	}
}

// Without --session nothing changes: the model gets exactly the two messages a
// single-shot ask has always sent, and nothing is recorded. That is the
// regression bar for the whole feature.
func TestRunAsk_withoutASessionIsUnchanged(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(10_000, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want exactly system + user: %v", len(msgs), msgs)
	}
	if msgs[0].Role != llm.RoleSystem || msgs[1].Role != llm.RoleUser {
		t.Errorf("roles = %s, %s; want system, user", msgs[0].Role, msgs[1].Role)
	}
	if errOut.Len() != 0 {
		t.Errorf("want no diagnostics for a plain ask, got: %q", errOut.String())
	}
}

// ── the budget ladder's new rung (D7) ────────────────────────────────────────

// historyThread returns a thread of n turns, each pair costing roughly 200
// tokens, so the budget arithmetic below has room to be approximate.
func historyThread(n int) *conversation.Thread {
	th := &conversation.Thread{ID: 1, Name: "go"}
	for i := 0; i < n; i++ {
		th.Turns = append(th.Turns, conversation.Turn{
			Question: strings.Repeat("an earlier question. ", 20),
			Answer:   strings.Repeat("an earlier answer. ", 21),
		})
	}
	return th
}

// Grounded evidence for the question in front of you beats a transcript of the
// ones behind it, so the oldest turns go before the retrieved text is touched.
func TestRunAsk_contextGuardDropsHistoryBeforeChunks(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message
	rec := &recorder{}

	// ~780 tokens with three pairs, ~580 with two: 700 fits after one pair goes.
	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithSession(historyThread(3), rec.append),
		cli.WithContextBudget(750, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(msgs) != 6 {
		t.Fatalf("got %d messages, want system + two pairs + question: %v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[len(msgs)-1].Content, "the cat sat on the mat") {
		t.Errorf("retrieved text should be untouched, got: %q", msgs[len(msgs)-1].Content)
	}
	if strings.Contains(errOut.String(), "compacted") {
		t.Errorf("history should go before compaction, got: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "1 oldest of 3") {
		t.Errorf("want a warning naming the dropped turns, got: %q", errOut.String())
	}
	for _, cit := range []string{"/a.md §0", "/b.md §1", "/c.md §2"} {
		if !strings.Contains(out.String(), cit) {
			t.Errorf("want %s still cited, got: %q", cit, out.String())
		}
	}
}

// The most recent pair is the floor: pronoun resolution dies without it, so
// chunks are compacted and then dropped around it rather than through it.
func TestRunAsk_contextGuardKeepsTheMostRecentPair(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message
	rec := &recorder{}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithSession(historyThread(3), rec.append),
		cli.WithContextBudget(280, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want system + the most recent pair + question: %v", len(msgs), msgs)
	}
	if !strings.Contains(errOut.String(), "2 oldest of 3") {
		t.Errorf("want a warning naming the two dropped turns, got: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "compacted") {
		t.Errorf("want the compaction warning once history hit its floor, got: %q", errOut.String())
	}
}

// A prompt that still does not fit with one pair and no chunks gives the floor
// up too — and says so, because an answer with no thread behind it reads very
// differently from one with it.
func TestRunAsk_contextGuardDropsTheThreadEntirely(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message
	rec := &recorder{}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithSession(historyThread(3), rec.append),
		cli.WithContextBudget(120, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want the thread gone: %v", len(msgs), msgs)
	}
	if !strings.Contains(errOut.String(), "thread") {
		t.Errorf("want a warning that the thread was dropped, got: %q", errOut.String())
	}
	// The turn is still recorded: it was asked and answered, thread or no thread.
	if len(rec.turns) != 1 {
		t.Errorf("want the turn recorded anyway, got %d", len(rec.turns))
	}
}

// Even with the thread gone there is a prompt that cannot fit, and it fails
// locally rather than at the provider.
func TestRunAsk_contextGuardErrorsWithAThreadAndNoRoom(t *testing.T) {
	var out, errOut bytes.Buffer
	rec := &recorder{}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), refusingChat(t),
		guardTemplate(t), strings.Repeat("a long question. ", 30), nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithSession(historyThread(2), rec.append),
		cli.WithContextBudget(60, 50))
	if err == nil {
		t.Fatal("want an error when nothing fits, got nil")
	}
	if !strings.Contains(err.Error(), "context_tokens") {
		t.Errorf("error should name the knob to raise, got: %v", err)
	}
	if len(rec.turns) != 0 {
		t.Errorf("nothing was answered, so nothing should be recorded, got %v", rec.turns)
	}
}

// ── the command boundary ─────────────────────────────────────────────────────

// fakeLLMServer stands in for a local llama.cpp server: the native /embedding
// endpoint the embedder calls, and the OpenAI-compatible chat stream the ask
// path reads. Both are faked; every other seam is production wiring.
func fakeLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/embedding", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]float32{"embedding": {1, 0, 0, 0}})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"they \"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"double\"}}]}\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, e)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// --continue names the thread you were just in, so with no threads at all it is
// an error: falling back to a single-shot ask would look identical on screen
// and answer from nothing (D11).
func TestAskCommand_continueWithoutAnySession(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")

	out, err := runRoot(t, "ask", "-c", "and maps?")
	if err == nil {
		t.Fatalf("want an error with no threads, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--session") {
		t.Errorf("error should point at --session, got: %v", err)
	}
}

// A name that does not exist yet is created: a thread is a shell history file,
// not a resource to provision (D11).
func TestAskCommand_sessionCreatesAndAppends(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	srv := fakeLLMServer(t)
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
	writeFile(t, fixture, "# Go\n\nSlices grow by doubling their capacity. Maps rehash when they fill.\n")
	mustRun(t, "ingest", fixture)

	mustRun(t, "ask", "--session", "Go", "how do slices grow?")
	mustRun(t, "ask", "--continue", "and maps?")

	db, err := storage.Open(filepath.Join(home, ".tbuk", "tbuk.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	repo := storage.NewSessionRepo(db.DB())
	sess, err := repo.GetByName(context.Background(), "go")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if sess.Template != "qa" {
		t.Errorf("thread template = %q, want the template it was opened with", sess.Template)
	}
	turns, err := repo.Turns(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("thread holds %d turns, want 2", len(turns))
	}
	if turns[1].Question != "and maps?" {
		t.Errorf("second question = %q, want the one --continue asked", turns[1].Question)
	}
	// The follow-up retrieved on the folded query, not on "and maps?" alone.
	if !strings.Contains(turns[1].Query, "how do slices grow?") {
		t.Errorf("second query = %q, want the thread folded in", turns[1].Query)
	}
	if turns[1].Answer == "" {
		t.Error("second turn recorded no answer")
	}
	// Citations are stored as display strings, so the thread survives a reindex
	// that renumbers every chunk id (D8).
	if len(turns[1].Citations) == 0 || !strings.Contains(turns[1].Citations[0], "go.md") {
		t.Errorf("second turn citations = %v, want the retrieved document", turns[1].Citations)
	}
}

// The two ways of naming a thread are alternatives, not a combination.
func TestAskCommand_sessionAndContinueAreExclusive(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")

	if _, err := runRoot(t, "ask", "--session", "go", "-c", "q"); err == nil {
		t.Fatal("want an error for --session with --continue, got nil")
	}
}

// A knowledge base built before sessions existed says what to run rather than
// failing with a raw "no such table" (D9).
func TestAskCommand_sessionWithoutTheTables(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")

	dbPath := filepath.Join(home, ".tbuk", "tbuk.sqlite")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, stmt := range []string{`DROP TABLE session_turns`, `DROP TABLE sessions`} {
		if _, err := db.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = db.Close()

	_, err = runRoot(t, "ask", "--session", "go", "q")
	if err == nil {
		t.Fatal("want an error against a knowledge base without the session tables")
	}
	if !strings.Contains(err.Error(), "add-sessions") {
		t.Errorf("error should name the script that adds them, got: %v", err)
	}
}
