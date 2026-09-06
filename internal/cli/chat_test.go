package cli_test

import (
	"bytes"
	"context"
	"errors"
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

// chatDeps wires a REPL over the fakes the ask tests already use: the answer is
// always "ok", retrieval always returns nothing, and turns land in rec.
func chatDeps(t *testing.T, rec *recorder, thread *conversation.Thread) cli.ChatDeps {
	t.Helper()
	return cli.ChatDeps{
		Retrieve:     mockRetrieve(nil, nil),
		Chat:         mockChat([]string{"ok"}, nil),
		Template:     buildQATemplate(t),
		Thread:       thread,
		Append:       rec.append,
		HistoryTurns: 6,
	}
}

// scripted runs the REPL over lines typed one per line, returning everything it
// printed.
func scripted(t *testing.T, deps cli.ChatDeps, lines ...string) string {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	if err := cli.RunChat(context.Background(), in, &out, deps); err != nil {
		t.Fatalf("RunChat: %v", err)
	}
	return out.String()
}

// The loop answers each line and stops on /exit.
func TestRunChat_answersAndExits(t *testing.T) {
	rec := &recorder{}
	out := scripted(t, chatDeps(t, rec, &conversation.Thread{}),
		"how do slices grow?", "and maps?", "/exit")

	if strings.Count(out, "ok") != 2 {
		t.Errorf("want one answer per question:\n%s", out)
	}
	if len(rec.turns) != 2 {
		t.Fatalf("want two turns kept, got %d", len(rec.turns))
	}
	if rec.turns[1].Question != "and maps?" {
		t.Errorf("second turn question = %q", rec.turns[1].Question)
	}
}

// End of input is a way out too: a piped script has no /exit to type.
func TestRunChat_exitsOnEOF(t *testing.T) {
	rec := &recorder{}
	out := scripted(t, chatDeps(t, rec, &conversation.Thread{}), "a question")
	if !strings.Contains(out, "ok") {
		t.Errorf("want the answer before EOF ended the loop:\n%s", out)
	}
}

// A blank line is not a question — the model is not called for it.
func TestRunChat_ignoresBlankLines(t *testing.T) {
	rec := &recorder{}
	scripted(t, chatDeps(t, rec, &conversation.Thread{}), "", "   ", "/exit")
	if len(rec.turns) != 0 {
		t.Errorf("blank lines produced %d turns, want none", len(rec.turns))
	}
}

// The thread grows as the conversation does, so the second question is asked
// with the first one behind it — the whole point of the REPL.
func TestRunChat_replaysTheGrowingThread(t *testing.T) {
	var msgs []llm.Message
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Chat = recordingChat(&msgs)

	scripted(t, deps, "how do slices grow?", "and maps?", "/exit")

	// system + the first pair + the current question.
	if len(msgs) != 4 {
		t.Fatalf("second turn sent %d messages, want the first pair replayed: %v", len(msgs), msgs)
	}
	if msgs[1].Content != "how do slices grow?" {
		t.Errorf("first replayed message = %q, want the first question", msgs[1].Content)
	}
}

// Retrieval sees the thread too: a follow-up is planned against it, not sent
// bare (D5).
func TestRunChat_retrievesOnThePlannedQuery(t *testing.T) {
	var queries []string
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Retrieve = capturingRetrieve(&queries, nil)
	deps.Ask = []cli.AskOption{cli.WithPlanner(rewrite.Window{Turns: 2})}

	scripted(t, deps, "how do slices grow?", "and maps?", "/exit")

	if len(queries) != 1 || queries[0] != "how do slices grow? and maps?" {
		t.Errorf("second retrieval queries = %q, want the thread folded in", queries)
	}
}

// ── REPL commands ────────────────────────────────────────────────────────────

func TestRunChat_help(t *testing.T) {
	out := scripted(t, chatDeps(t, &recorder{}, &conversation.Thread{}), "/help", "/exit")
	for _, want := range []string{"/exit", "/sources", "/new", "/forget", "/help"} {
		if !strings.Contains(out, want) {
			t.Errorf("/help does not mention %q:\n%s", want, out)
		}
	}
}

func TestRunChat_sources(t *testing.T) {
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Retrieve = mockRetrieve([]retrieval.RetrievedChunk{
		{Citation: "/go.md §0", Text: "slices grow by doubling"},
	}, nil)

	out := scripted(t, deps, "how do slices grow?", "/sources", "/exit")
	if strings.Count(out, "/go.md §0") != 2 {
		t.Errorf("want the citation from the answer and from /sources:\n%s", out)
	}
}

func TestRunChat_sourcesBeforeAnyTurn(t *testing.T) {
	out := scripted(t, chatDeps(t, &recorder{}, &conversation.Thread{}), "/sources", "/exit")
	if !strings.Contains(out, "no sources yet") {
		t.Errorf("/sources with nothing asked = %q", out)
	}
}

// /forget drops what is replayed without deleting the thread: the turns already
// recorded stay recorded.
func TestRunChat_forget(t *testing.T) {
	var msgs []llm.Message
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Chat = recordingChat(&msgs)

	out := scripted(t, deps, "how do slices grow?", "/forget", "and maps?", "/exit")

	if len(msgs) != 2 {
		t.Errorf("after /forget the prompt is system + question, got %d messages: %v", len(msgs), msgs)
	}
	if len(rec.turns) != 2 {
		t.Errorf("/forget must not un-record turns, got %d", len(rec.turns))
	}
	if !strings.Contains(out, "forgot") {
		t.Errorf("/forget said nothing:\n%s", out)
	}
}

// /new with no name starts an unsaved thread — history and store both dropped.
func TestRunChat_newUnsaved(t *testing.T) {
	var msgs []llm.Message
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Chat = recordingChat(&msgs)

	scripted(t, deps, "how do slices grow?", "/new", "and maps?", "/exit")

	if len(msgs) != 2 {
		t.Errorf("/new should start from an empty thread, got %d messages: %v", len(msgs), msgs)
	}
	if len(rec.turns) != 1 {
		t.Errorf("the unsaved thread recorded %d turns through the store, want only the first", len(rec.turns))
	}
}

// /new NAME switches to a stored thread and replays what is already in it.
func TestRunChat_newNamed(t *testing.T) {
	var msgs []llm.Message
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Chat = recordingChat(&msgs)
	opened := &recorder{}
	deps.Open = func(_ context.Context, name string) (*conversation.Thread, cli.AppendTurnFn, error) {
		if name != "go" {
			t.Errorf("/new was handed %q, want the name as typed", name)
		}
		return threadOf("how do slices grow?", "they double"), opened.append, nil
	}

	out := scripted(t, deps, "/new go", "and maps?", "/exit")

	if len(msgs) != 4 {
		t.Errorf("the named thread's history was not replayed, got %d messages: %v", len(msgs), msgs)
	}
	if len(opened.turns) != 1 || len(rec.turns) != 0 {
		t.Errorf("the turn went to the wrong store: opened=%d original=%d", len(opened.turns), len(rec.turns))
	}
	if !strings.Contains(out, "go") {
		t.Errorf("/new go said nothing about the thread it opened:\n%s", out)
	}
}

// /new NAME against a REPL with no store behind it says so rather than
// pretending to have saved anything.
func TestRunChat_newNamedWithoutAStore(t *testing.T) {
	out := scripted(t, chatDeps(t, &recorder{}, &conversation.Thread{}), "/new go", "/exit")
	if !strings.Contains(out, "/new go") && !strings.Contains(out, "not available") {
		t.Errorf("/new NAME with no store = %q, want it to explain", out)
	}
}

// A thread that cannot be opened leaves the current one alone: the REPL says so
// and carries on.
func TestRunChat_newNamedOpenFails(t *testing.T) {
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Open = func(context.Context, string) (*conversation.Thread, cli.AppendTurnFn, error) {
		return nil, nil, errors.New("no such knowledge base")
	}

	out := scripted(t, deps, "/new go", "a question", "/exit")
	if !strings.Contains(out, "no such knowledge base") {
		t.Errorf("the failure was swallowed:\n%s", out)
	}
	if len(rec.turns) != 1 {
		t.Errorf("the REPL should keep answering into the old thread, got %d turns", len(rec.turns))
	}
}

func TestRunChat_unknownCommand(t *testing.T) {
	rec := &recorder{}
	out := scripted(t, chatDeps(t, rec, &conversation.Thread{}), "/nope", "/exit")
	if !strings.Contains(out, "/help") {
		t.Errorf("an unknown command should point at /help:\n%s", out)
	}
	if len(rec.turns) != 0 {
		t.Errorf("an unknown command was asked as a question: %v", rec.turns)
	}
}

// A failed turn — the provider is down, say — is reported and the loop lives:
// losing the whole thread to one timeout is worse than the timeout.
func TestRunChat_answerErrorDoesNotEndTheLoop(t *testing.T) {
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Chat = mockChat(nil, errors.New("connection refused"))

	out := scripted(t, deps, "a question", "/exit")
	if !strings.Contains(out, "connection refused") {
		t.Errorf("the provider error was swallowed:\n%s", out)
	}
}

// A cancelled context ends the REPL rather than spinning on a dead provider.
func TestRunChat_stopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	in := strings.NewReader("a question\nanother\n")
	if err := cli.RunChat(ctx, in, &out, chatDeps(t, &recorder{}, &conversation.Thread{})); err != nil {
		t.Fatalf("RunChat: %v", err)
	}
}

// The banner says whether anything is being written down, because an unsaved
// chat that looked saved is how a thread is lost.
func TestRunChat_bannerNamesTheThread(t *testing.T) {
	saved := scripted(t, chatDeps(t, &recorder{}, &conversation.Thread{ID: 1, Name: "go"}), "/exit")
	if !strings.Contains(saved, "go") {
		t.Errorf("banner does not name the thread:\n%s", saved)
	}
	// No store behind it is what makes a chat unsaved, and the banner has to say
	// so: an unsaved chat mistaken for a saved one is how a conversation is lost.
	deps := chatDeps(t, &recorder{}, &conversation.Thread{})
	deps.Append = nil
	unsaved := scripted(t, deps, "/exit")
	if !strings.Contains(unsaved, "unsaved") {
		t.Errorf("banner does not say the chat is unsaved:\n%s", unsaved)
	}
}

// Answers echo document text, so the REPL filters control characters like the
// rest of the document-derived output does (D14).
func TestRunChat_stripsControlCharacters(t *testing.T) {
	rec := &recorder{}
	deps := chatDeps(t, rec, &conversation.Thread{})
	deps.Chat = mockChat([]string{"harmless \x1b]0;pwned\x07 text"}, nil)

	out := scripted(t, deps, "a question", "/exit")
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("control characters reached the terminal: %q", out)
	}
}

// ── the command boundary ─────────────────────────────────────────────────────

// Without --session a chat is in memory and saves nothing: most conversations
// are not worth keeping, and a tool that hoards every idle question makes
// `session list` useless within a week.
func TestChatCommand_unsavedByDefault(t *testing.T) {
	home := chatHome(t)

	out := mustRunStdin(t, "how do slices grow?\n/exit\n", "chat")
	if !strings.Contains(out, "double") {
		t.Fatalf("chat output = %q, want the faked answer", out)
	}

	db, err := storage.Open(filepath.Join(home, ".tbuk", "tbuk.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	sessions, err := storage.NewSessionRepo(db.DB()).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("an unsaved chat recorded %d threads, want none", len(sessions))
	}
}

// With --session the REPL writes into the same store `ask --session` uses.
func TestChatCommand_sessionRecordsTheThread(t *testing.T) {
	home := chatHome(t)

	mustRunStdin(t, "how do slices grow?\nand maps?\n/exit\n", "chat", "--session", "Go")

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
	turns, err := repo.Turns(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("thread holds %d turns, want both questions", len(turns))
	}
	if !strings.Contains(turns[1].Query, "how do slices grow?") {
		t.Errorf("second query = %q, want the thread folded in", turns[1].Query)
	}
}

func TestChatCommand_sessionWithoutTheTables(t *testing.T) {
	home := chatHome(t)
	dropSessionTables(t, filepath.Join(home, ".tbuk", "tbuk.sqlite"))

	_, err := runRoot(t, "chat", "--session", "go")
	if err == nil {
		t.Fatal("want an error against a knowledge base without the session tables")
	}
	if !strings.Contains(err.Error(), "add-sessions") {
		t.Errorf("error should name the script, got: %v", err)
	}
}

// chatHome builds an initialised knowledge base with one document, pointed at a
// faked LLM server, and returns its home directory.
func chatHome(t *testing.T) string {
	t.Helper()
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
	return home
}
