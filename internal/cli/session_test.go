package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// sessionRepo opens an in-memory knowledge base and returns its thread store.
func sessionRepo(t *testing.T) *storage.SessionRepo {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return storage.NewSessionRepo(db.DB())
}

// seedThread creates a thread and appends one turn per question/answer pair.
func seedThread(t *testing.T, repo *storage.SessionRepo, name string, turns ...storage.SessionTurn) *storage.Session {
	t.Helper()
	sess := &storage.Session{Name: name, Template: "qa"}
	if err := repo.Create(context.Background(), sess); err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	for i := range turns {
		turns[i].SessionID = sess.ID
		if err := repo.AppendTurn(context.Background(), &turns[i]); err != nil {
			t.Fatalf("append to %q: %v", name, err)
		}
	}
	return sess
}

// ── list ─────────────────────────────────────────────────────────────────────

func TestRunSessionList(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "rust")
	seedThread(t, repo, "go", storage.SessionTurn{Question: "how do slices grow?", Answer: "they double"})

	var out bytes.Buffer
	if err := cli.RunSessionList(context.Background(), &out, repo); err != nil {
		t.Fatalf("RunSessionList: %v", err)
	}
	got := out.String()
	for _, want := range []string{"NAME", "TURNS", "go", "rust"} {
		if !strings.Contains(got, want) {
			t.Errorf("listing is missing %q:\n%s", want, got)
		}
	}
	// The most recently used thread comes first, because `--continue` and this
	// listing answer the same question.
	if strings.Index(got, "go") > strings.Index(got, "rust") {
		t.Errorf("most recently used thread should come first:\n%s", got)
	}
}

func TestRunSessionList_empty(t *testing.T) {
	var out bytes.Buffer
	if err := cli.RunSessionList(context.Background(), &out, sessionRepo(t)); err != nil {
		t.Fatalf("RunSessionList: %v", err)
	}
	if !strings.Contains(out.String(), "No conversation threads") {
		t.Errorf("empty listing = %q, want the empty-store message", out.String())
	}
}

// A thread name is user input and reaches the terminal like any other stored
// string, so an escape sequence in one is stripped on the way out (D14).
func TestRunSessionList_stripsControlCharacters(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go\x1b]0;pwned\x07")

	var out bytes.Buffer
	if err := cli.RunSessionList(context.Background(), &out, repo); err != nil {
		t.Fatalf("RunSessionList: %v", err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("control characters reached the terminal: %q", out.String())
	}
}

// ── show ─────────────────────────────────────────────────────────────────────

func TestRunSessionShow(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go",
		storage.SessionTurn{
			Question:  "how do slices grow?",
			Query:     "how do slices grow?",
			Answer:    "they double their capacity",
			Citations: []string{"/go.md §0"},
		},
		storage.SessionTurn{
			Question: "and maps?",
			Query:    "how do slices grow? and maps?",
			Answer:   "they rehash",
		},
	)

	var out bytes.Buffer
	if err := cli.RunSessionShow(context.Background(), &out, repo, "GO", false); err != nil {
		t.Fatalf("RunSessionShow: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"go", "qa", "how do slices grow?", "they double their capacity",
		"and maps?", "they rehash", "/go.md §0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("show is missing %q:\n%s", want, got)
		}
	}
	// Without --verbose the planned query stays out of the way: it is only
	// interesting when the answer is not.
	if strings.Contains(got, "how do slices grow? and maps?") {
		t.Errorf("planned query printed without --verbose:\n%s", got)
	}
}

// --verbose prints the query retrieval actually ran, so a rewrite that fetched
// the wrong thing is visible rather than mysterious.
func TestRunSessionShow_verbosePrintsThePlannedQuery(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go", storage.SessionTurn{
		Question: "and maps?",
		Query:    "how do slices grow? and maps?",
		Answer:   "they rehash",
	})

	var out bytes.Buffer
	if err := cli.RunSessionShow(context.Background(), &out, repo, "go", true); err != nil {
		t.Fatalf("RunSessionShow: %v", err)
	}
	if !strings.Contains(out.String(), "how do slices grow? and maps?") {
		t.Errorf("--verbose should print the planned query:\n%s", out.String())
	}
}

// An answer echoes document text, which is untrusted: an OSC sequence stored in
// a turn must not reach the terminal when the thread is read back (D14).
func TestRunSessionShow_stripsControlCharacters(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go", storage.SessionTurn{
		Question:  "what?",
		Answer:    "harmless \x1b]0;pwned\x07 text",
		Citations: []string{"/a\x1b[31m.md §0"},
	})

	var out bytes.Buffer
	if err := cli.RunSessionShow(context.Background(), &out, repo, "go", false); err != nil {
		t.Fatalf("RunSessionShow: %v", err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("control characters reached the terminal: %q", out.String())
	}
	if !strings.Contains(out.String(), "harmless") {
		t.Errorf("stripping ate the text around the escape:\n%s", out.String())
	}
}

func TestRunSessionShow_emptyThread(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go")

	var out bytes.Buffer
	if err := cli.RunSessionShow(context.Background(), &out, repo, "go", false); err != nil {
		t.Fatalf("RunSessionShow: %v", err)
	}
	if !strings.Contains(out.String(), "no turns") {
		t.Errorf("show of an empty thread = %q, want it to say so", out.String())
	}
}

// An unknown name is an error naming the threads that do exist — the usual
// cause is a typo, and the answer is on screen (D11).
func TestRunSessionShow_unknownNameListsTheKnownOnes(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go")
	seedThread(t, repo, "rust")

	var out bytes.Buffer
	err := cli.RunSessionShow(context.Background(), &out, repo, "gol", false)
	if err == nil {
		t.Fatal("want an error for an unknown thread, got nil")
	}
	for _, want := range []string{"gol", "go", "rust"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q", err, want)
		}
	}
}

func TestRunSessionShow_unknownNameWithNoThreadsAtAll(t *testing.T) {
	var out bytes.Buffer
	err := cli.RunSessionShow(context.Background(), &out, sessionRepo(t), "go", false)
	if err == nil {
		t.Fatal("want an error against an empty store, got nil")
	}
	if !strings.Contains(err.Error(), "--session") {
		t.Errorf("error should say how to start a thread, got: %v", err)
	}
}

// ── rename ───────────────────────────────────────────────────────────────────

func TestRunSessionRename(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go", storage.SessionTurn{Question: "q", Answer: "a"})

	var out bytes.Buffer
	if err := cli.RunSessionRename(context.Background(), &out, repo, "go", " Golang "); err != nil {
		t.Fatalf("RunSessionRename: %v", err)
	}
	sess, err := repo.GetByName(context.Background(), "golang")
	if err != nil {
		t.Fatalf("GetByName after rename: %v", err)
	}
	if sess.TurnCount != 1 {
		t.Errorf("renamed thread holds %d turns, want its turns kept", sess.TurnCount)
	}
	if !strings.Contains(out.String(), "golang") {
		t.Errorf("rename output = %q, want the new name", out.String())
	}
}

func TestRunSessionRename_unknownName(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go")

	var out bytes.Buffer
	err := cli.RunSessionRename(context.Background(), &out, repo, "rust", "systems")
	if err == nil {
		t.Fatal("want an error renaming an unknown thread, got nil")
	}
	if !strings.Contains(err.Error(), "go") {
		t.Errorf("error should name the known threads, got: %v", err)
	}
}

// Renaming onto a name in use would merge two conversations, or fail with a raw
// constraint error. It does neither.
func TestRunSessionRename_ontoAnExistingName(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go")
	seedThread(t, repo, "rust")

	var out bytes.Buffer
	err := cli.RunSessionRename(context.Background(), &out, repo, "go", "rust")
	if err == nil {
		t.Fatal("want an error renaming onto an existing thread, got nil")
	}
	if !strings.Contains(err.Error(), "already") {
		t.Errorf("error should say the name is taken, got: %v", err)
	}
}

// ── delete ───────────────────────────────────────────────────────────────────

func TestRunSessionDelete_confirmed(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go", storage.SessionTurn{Question: "q", Answer: "a"})

	var out bytes.Buffer
	if err := cli.RunSessionDelete(context.Background(), strings.NewReader("y\n"), &out, repo, "go", false); err != nil {
		t.Fatalf("RunSessionDelete: %v", err)
	}
	if _, err := repo.GetByName(context.Background(), "go"); err == nil {
		t.Error("thread survived a confirmed delete")
	}
	if !strings.Contains(out.String(), "Deleted") {
		t.Errorf("delete output = %q, want confirmation", out.String())
	}
}

func TestRunSessionDelete_aborted(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go")

	var out bytes.Buffer
	if err := cli.RunSessionDelete(context.Background(), strings.NewReader("\n"), &out, repo, "go", false); err != nil {
		t.Fatalf("RunSessionDelete: %v", err)
	}
	if _, err := repo.GetByName(context.Background(), "go"); err != nil {
		t.Errorf("plain Enter deleted the thread anyway: %v", err)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("delete output = %q, want the abort message", out.String())
	}
}

func TestRunSessionDelete_yesSkipsThePrompt(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go")

	var out bytes.Buffer
	if err := cli.RunSessionDelete(context.Background(), strings.NewReader(""), &out, repo, "go", true); err != nil {
		t.Fatalf("RunSessionDelete: %v", err)
	}
	if _, err := repo.GetByName(context.Background(), "go"); err == nil {
		t.Error("--yes did not delete the thread")
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("--yes still prompted: %q", out.String())
	}
}

func TestRunSessionDelete_unknownName(t *testing.T) {
	repo := sessionRepo(t)
	seedThread(t, repo, "go")

	var out bytes.Buffer
	err := cli.RunSessionDelete(context.Background(), strings.NewReader("y\n"), &out, repo, "rust", true)
	if err == nil {
		t.Fatal("want an error deleting an unknown thread, got nil")
	}
	if !strings.Contains(err.Error(), "go") {
		t.Errorf("error should name the known threads, got: %v", err)
	}
}

// ── the command boundary ─────────────────────────────────────────────────────

func TestSessionCommands_endToEnd(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")

	if out := mustRun(t, "session", "list"); !strings.Contains(out, "No conversation threads") {
		t.Fatalf("session list on a fresh KB = %q", out)
	}

	db, err := storage.Open(filepath.Join(home, ".tbuk", "tbuk.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	repo := storage.NewSessionRepo(db.DB())
	seedThread(t, repo, "go", storage.SessionTurn{
		Question:  "how do slices grow?",
		Query:     "how do slices grow?",
		Answer:    "they double",
		Citations: []string{"/go.md §0"},
	})
	_ = db.Close()

	if out := mustRun(t, "session", "list"); !strings.Contains(out, "go") {
		t.Fatalf("session list = %q, want the seeded thread", out)
	}
	if out := mustRun(t, "session", "show", "go"); !strings.Contains(out, "they double") {
		t.Fatalf("session show = %q, want the answer", out)
	}
	mustRun(t, "session", "rename", "go", "golang")
	if out := mustRun(t, "session", "show", "golang", "--verbose"); !strings.Contains(out, "how do slices grow?") {
		t.Fatalf("session show after rename = %q", out)
	}
	if out := mustRun(t, "session", "delete", "golang", "--yes"); !strings.Contains(out, "Deleted") {
		t.Fatalf("session delete = %q, want confirmation", out)
	}
	if out := mustRun(t, "session", "list"); !strings.Contains(out, "No conversation threads") {
		t.Fatalf("session list after delete = %q", out)
	}
}

// A knowledge base built before the tables existed says what to run, rather
// than failing with a raw "no such table" (D9).
func TestSessionCommands_withoutTheTables(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")
	dropSessionTables(t, filepath.Join(home, ".tbuk", "tbuk.sqlite"))

	for _, args := range [][]string{
		{"session", "list"},
		{"session", "show", "go"},
		{"session", "rename", "go", "rust"},
		{"session", "delete", "go", "--yes"},
	} {
		_, err := runRoot(t, args...)
		if err == nil {
			t.Errorf("%v: want an error against a knowledge base without the tables", args)
			continue
		}
		if !strings.Contains(err.Error(), "add-sessions") {
			t.Errorf("%v: error should name the script, got: %v", args, err)
		}
	}
}
