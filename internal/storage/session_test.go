package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotofritz/timbuktu/internal/storage"
)

// A knowledge base built before sessions existed opens, ingests and searches
// fine — only `tbuk ask --session` fails on it, and with a raw "no such table"
// unless something can ask first (issue #157).
func TestHasSessionTables(t *testing.T) {
	db := openTestDB(t)
	ok, err := storage.HasSessionTables(db.DB())
	if err != nil {
		t.Fatalf("HasSessionTables: %v", err)
	}
	if !ok {
		t.Error("current schema reported as missing the session tables")
	}

	for _, stmt := range []string{`DROP TABLE session_turns`, `DROP TABLE sessions`} {
		if _, err := db.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	ok, err = storage.HasSessionTables(db.DB())
	if err != nil {
		t.Fatalf("HasSessionTables after drop: %v", err)
	}
	if ok {
		t.Error("dropped tables still reported as present")
	}
}

// Half the pair is not the pair: a knowledge base carrying sessions but not
// session_turns still cannot record a thread.
func TestHasSessionTables_partial(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.DB().Exec(`DROP TABLE session_turns`); err != nil {
		t.Fatalf("drop session_turns: %v", err)
	}
	ok, err := storage.HasSessionTables(db.DB())
	if err != nil {
		t.Fatalf("HasSessionTables: %v", err)
	}
	if ok {
		t.Error("half the schema reported as present")
	}
}

func TestHasSessionTables_closedDB(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sqlDB := db.DB()
	_ = db.Close()

	if _, err := storage.HasSessionTables(sqlDB); err == nil {
		t.Error("want an error from a closed database, got nil")
	}
}

// ── SessionRepo ───────────────────────────────────────────────────────────────

// newSession creates a session and fails the test if it cannot.
func newSession(t *testing.T, r *storage.SessionRepo, name string) *storage.Session {
	t.Helper()
	s := &storage.Session{Name: name, Template: "qa"}
	if err := r.Create(context.Background(), s); err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	return s
}

// appendTurn appends one turn and fails the test if it cannot.
func appendTurn(t *testing.T, r *storage.SessionRepo, sessionID int64, question, answer string) *storage.SessionTurn {
	t.Helper()
	turn := &storage.SessionTurn{
		SessionID: sessionID,
		Question:  question,
		Query:     question,
		Answer:    answer,
		Citations: []string{"a.md §0"},
	}
	if err := r.AppendTurn(context.Background(), turn); err != nil {
		t.Fatalf("AppendTurn(%q): %v", question, err)
	}
	return turn
}

// A name is stored normalized, so `--session Work` and `--session work` are the
// same thread however the shell capitalised it (D11).
func TestSessionRepo_createNormalizesName(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	ctx := context.Background()

	s := newSession(t, repo, "  Work Notes  ")
	if s.Name != "work notes" {
		t.Errorf("stored name = %q, want %q", s.Name, "work notes")
	}
	if s.ID == 0 {
		t.Error("Create left ID unset")
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		t.Error("Create left timestamps unset")
	}

	got, err := repo.GetByName(ctx, "WORK NOTES")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.ID != s.ID {
		t.Errorf("GetByName returned id %d, want %d", got.ID, s.ID)
	}
	if got.Template != "qa" {
		t.Errorf("template = %q, want %q", got.Template, "qa")
	}
}

func TestSessionRepo_emptyName(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	ctx := context.Background()

	if err := repo.Create(ctx, &storage.Session{Name: "   "}); err == nil {
		t.Error("Create with a blank name: want an error, got nil")
	}
	if _, err := repo.GetByName(ctx, ""); err == nil {
		t.Error("GetByName with a blank name: want an error, got nil")
	}
}

// Two threads cannot share a name: the second Create is the caller's bug, not a
// silent merge of two conversations.
func TestSessionRepo_createDuplicate(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	newSession(t, repo, "go")
	if err := repo.Create(context.Background(), &storage.Session{Name: "Go"}); err == nil {
		t.Error("duplicate Create: want an error, got nil")
	}
}

func TestSessionRepo_getByNameMiss(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	_, err := repo.GetByName(context.Background(), "nope")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// --continue targets the thread whose last turn is most recent, which is what a
// person means by "the one I was just in" (D11).
func TestSessionRepo_mostRecent(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	ctx := context.Background()

	if _, err := repo.MostRecent(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MostRecent with no sessions: want ErrNotFound, got %v", err)
	}

	first := newSession(t, repo, "first")
	second := newSession(t, repo, "second")

	got, err := repo.MostRecent(ctx)
	if err != nil {
		t.Fatalf("MostRecent: %v", err)
	}
	if got.ID != second.ID {
		t.Errorf("MostRecent = %q, want %q", got.Name, second.Name)
	}

	// A turn on the older thread makes it the most recent one.
	appendTurn(t, repo, first.ID, "q", "a")
	got, err = repo.MostRecent(ctx)
	if err != nil {
		t.Fatalf("MostRecent after append: %v", err)
	}
	if got.ID != first.ID {
		t.Errorf("MostRecent after append = %q, want %q", got.Name, first.Name)
	}
}

// Turn indexes are consecutive from zero, and a turn round-trips with its
// citations split back out of the stored newline-joined text (D8).
func TestSessionRepo_appendAndReadTurns(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	ctx := context.Background()
	s := newSession(t, repo, "go")

	first := appendTurn(t, repo, s.ID, "how do slices grow?", "they double")
	if first.Index != 0 {
		t.Errorf("first turn index = %d, want 0", first.Index)
	}
	second := &storage.SessionTurn{
		SessionID: s.ID,
		Question:  "and maps?",
		Query:     "how do slices grow? and maps?",
		Answer:    "they rehash",
		Citations: []string{"map.md §1", "map.md §2"},
	}
	if err := repo.AppendTurn(ctx, second); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if second.Index != 1 {
		t.Errorf("second turn index = %d, want 1", second.Index)
	}
	if second.CreatedAt.IsZero() {
		t.Error("AppendTurn left CreatedAt unset")
	}

	turns, err := repo.Turns(ctx, s.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("Turns returned %d rows, want 2", len(turns))
	}
	if turns[0].Question != "how do slices grow?" || turns[1].Question != "and maps?" {
		t.Errorf("turns out of order: %q then %q", turns[0].Question, turns[1].Question)
	}
	if turns[1].Query != "how do slices grow? and maps?" {
		t.Errorf("planned query = %q, want the folded one", turns[1].Query)
	}
	if len(turns[1].Citations) != 2 || turns[1].Citations[0] != "map.md §1" {
		t.Errorf("citations = %v, want the two stored strings", turns[1].Citations)
	}
}

// A turn with no citations reads back as no citations, not as one empty string.
func TestSessionRepo_turnWithoutCitations(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	ctx := context.Background()
	s := newSession(t, repo, "go")

	if err := repo.AppendTurn(ctx, &storage.SessionTurn{
		SessionID: s.ID, Question: "q", Answer: "a",
	}); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	turns, err := repo.Turns(ctx, s.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns[0].Citations) != 0 {
		t.Errorf("citations = %v, want none", turns[0].Citations)
	}
}

// Appending bumps the thread's updated_at, which is what --continue orders by.
func TestSessionRepo_appendTouchesSession(t *testing.T) {
	db := openTestDB(t)
	repo := storage.NewSessionRepo(db.DB())
	ctx := context.Background()
	s := newSession(t, repo, "go")

	// Backdate the thread so the bump is visible whatever the clock's
	// resolution: a real thread's turns are minutes apart, a test's are not.
	if _, err := db.DB().Exec(
		`UPDATE sessions SET created_at=?, updated_at=? WHERE id=?`,
		"2020-01-01T00:00:00.000000000Z", "2020-01-01T00:00:00.000000000Z", s.ID,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	appendTurn(t, repo, s.ID, "q", "a")

	got, err := repo.GetByName(ctx, "go")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("updated_at %v not after created_at %v", got.UpdatedAt, got.CreatedAt)
	}
}

// Two `tbuk ask --session work` at once get a constraint error on the loser
// rather than a silently overwritten turn (D13).
func TestSessionRepo_duplicateTurnIndexRejected(t *testing.T) {
	db := openTestDB(t)
	repo := storage.NewSessionRepo(db.DB())
	s := newSession(t, repo, "go")
	appendTurn(t, repo, s.ID, "q", "a")

	_, err := db.DB().Exec(
		`INSERT INTO session_turns(session_id,turn_index,question,query,answer,citations,created_at)
		 VALUES(?,?,?,?,?,?,?)`,
		s.ID, 0, "q", "", "a", "", time.Now().UTC().Format(time.RFC3339))
	if err == nil {
		t.Error("second writer at the same turn index: want a constraint error, got nil")
	}
}

func TestSessionRepo_listOrdersByRecency(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	ctx := context.Background()

	sessions, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List on an empty KB: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("List on an empty KB returned %d rows", len(sessions))
	}

	older := newSession(t, repo, "older")
	newSession(t, repo, "newer")
	appendTurn(t, repo, older.ID, "q1", "a1")
	appendTurn(t, repo, older.ID, "q2", "a2")

	sessions, err = repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(sessions))
	}
	if sessions[0].Name != "older" || sessions[1].Name != "newer" {
		t.Errorf("List order = %q, %q; want the most recently used first", sessions[0].Name, sessions[1].Name)
	}
	if sessions[0].TurnCount != 2 || sessions[1].TurnCount != 0 {
		t.Errorf("turn counts = %d, %d; want 2, 0", sessions[0].TurnCount, sessions[1].TurnCount)
	}
}

func TestSessionRepo_rename(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	ctx := context.Background()
	newSession(t, repo, "go")
	newSession(t, repo, "rust")

	if err := repo.Rename(ctx, "GO", " golang "); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := repo.GetByName(ctx, "golang"); err != nil {
		t.Fatalf("GetByName after rename: %v", err)
	}
	if _, err := repo.GetByName(ctx, "go"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("old name still resolves: %v", err)
	}
	if err := repo.Rename(ctx, "nope", "other"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("renaming an unknown thread: want ErrNotFound, got %v", err)
	}
	if err := repo.Rename(ctx, "golang", "rust"); err == nil {
		t.Error("renaming onto an existing name: want an error, got nil")
	}
	if err := repo.Rename(ctx, "golang", "  "); err == nil {
		t.Error("renaming to a blank name: want an error, got nil")
	}
}

// Deleting a thread takes its turns with it, and leaves other threads alone.
func TestSessionRepo_deleteCascades(t *testing.T) {
	db := openTestDB(t)
	repo := storage.NewSessionRepo(db.DB())
	ctx := context.Background()

	doomed := newSession(t, repo, "doomed")
	keeper := newSession(t, repo, "keeper")
	appendTurn(t, repo, doomed.ID, "q", "a")
	appendTurn(t, repo, keeper.ID, "q", "a")

	if err := repo.Delete(ctx, "DOOMED"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByName(ctx, "doomed"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("deleted thread still resolves: %v", err)
	}
	var n int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM session_turns`).Scan(&n); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if n != 1 {
		t.Errorf("%d turns left after the cascade, want 1 (the keeper's)", n)
	}
	if err := repo.Delete(ctx, "doomed"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("deleting an unknown thread: want ErrNotFound, got %v", err)
	}
}

// session.max_turns caps a thread; pruning drops the oldest turns and keeps the
// most recent ones, which are the ones that get replayed.
func TestSessionRepo_prune(t *testing.T) {
	repo := storage.NewSessionRepo(openTestDB(t).DB())
	ctx := context.Background()
	s := newSession(t, repo, "go")
	for _, q := range []string{"q0", "q1", "q2", "q3"} {
		appendTurn(t, repo, s.ID, q, "a")
	}

	// 0 means keep everything, so it must not touch a thing.
	if n, err := repo.Prune(ctx, s.ID, 0); err != nil || n != 0 {
		t.Fatalf("Prune(0) = %d, %v; want 0, nil", n, err)
	}

	n, err := repo.Prune(ctx, s.ID, 2)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Errorf("Prune dropped %d turns, want 2", n)
	}
	turns, err := repo.Turns(ctx, s.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 2 || turns[0].Question != "q2" || turns[1].Question != "q3" {
		t.Errorf("kept %v, want the last two turns", turns)
	}
	// The next append continues the numbering rather than colliding with a
	// pruned index.
	next := appendTurn(t, repo, s.ID, "q4", "a")
	if next.Index != 4 {
		t.Errorf("index after pruning = %d, want 4", next.Index)
	}
}

// Threads are about the corpus, not about any one document: deleting a document
// must not disturb them (they hold citation strings, not chunk ids).
func TestSessionRepo_documentDeleteLeavesSessions(t *testing.T) {
	db := openTestDB(t)
	repo := storage.NewSessionRepo(db.DB())
	ctx := context.Background()
	s := newSession(t, repo, "go")
	appendTurn(t, repo, s.ID, "q", "a")

	docs := storage.NewDocumentRepo(db.DB())
	doc := &storage.Document{Path: "a.md", SHA256: "abc"}
	if err := docs.Create(ctx, doc); err != nil {
		t.Fatalf("Create document: %v", err)
	}
	if err := docs.Delete(ctx, doc.ID); err != nil {
		t.Fatalf("Delete document: %v", err)
	}

	turns, err := repo.Turns(ctx, s.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("%d turns survived the document delete, want 1", len(turns))
	}
}

// Every method surfaces a real database failure rather than reporting it as a
// miss or swallowing it.
func TestSessionRepo_closedDB(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := storage.NewSessionRepo(db.DB())
	ctx := context.Background()
	s := &storage.Session{Name: "go"}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = db.Close()

	if err := repo.Create(ctx, &storage.Session{Name: "other"}); err == nil {
		t.Error("Create on a closed DB: want an error")
	}
	if _, err := repo.GetByName(ctx, "go"); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetByName on a closed DB: want a real error, got %v", err)
	}
	if _, err := repo.MostRecent(ctx); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Errorf("MostRecent on a closed DB: want a real error, got %v", err)
	}
	if _, err := repo.List(ctx); err == nil {
		t.Error("List on a closed DB: want an error")
	}
	if _, err := repo.Turns(ctx, s.ID); err == nil {
		t.Error("Turns on a closed DB: want an error")
	}
	if err := repo.AppendTurn(ctx, &storage.SessionTurn{SessionID: s.ID}); err == nil {
		t.Error("AppendTurn on a closed DB: want an error")
	}
	if err := repo.Rename(ctx, "go", "golang"); err == nil {
		t.Error("Rename on a closed DB: want an error")
	}
	if err := repo.Delete(ctx, "go"); err == nil {
		t.Error("Delete on a closed DB: want an error")
	}
	if _, err := repo.Prune(ctx, s.ID, 1); err == nil {
		t.Error("Prune on a closed DB: want an error")
	}
}
