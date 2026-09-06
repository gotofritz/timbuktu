package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// sessionTimeLayout is RFC3339 with a fixed nine-digit fraction, so the stored
// text sorts the way the instants do. RFC3339Nano would not: it trims trailing
// zeros, and "…:05Z" sorts after "…:05.5Z" as text. --continue orders on this
// column, so two threads used in the same second still order correctly. It
// parses back with time.RFC3339, which accepts a fraction it did not ask for.
const sessionTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Session is a named conversation thread over this knowledge base.
type Session struct {
	ID   int64
	Name string
	// Template records which prompt template the thread was opened with. It is
	// recorded, not enforced: continuing a `qa` thread under `brief` is legal
	// and occasionally what you want.
	Template  string
	CreatedAt time.Time
	// UpdatedAt moves with every appended turn — it is what --continue orders by.
	UpdatedAt time.Time
	// TurnCount is how many turns the thread holds.
	TurnCount int
}

// SessionTurn is one exchange in a thread: what was asked, what retrieval was
// run on, what came back, and where it came from. Never the rendered prompt —
// replaying that would carry every chunk the thread ever saw into the next one.
type SessionTurn struct {
	ID        int64
	SessionID int64
	// Index is the turn's position in the thread, from zero, assigned by
	// AppendTurn.
	Index    int
	Question string
	// Query is what retrieval actually ran, which is not the question once a
	// planner has folded the thread into it.
	Query  string
	Answer string
	// Citations are display strings ("path §index"), not chunk ids: chunks.id
	// churns on every reindex and a thread has to survive a re-embed.
	Citations []string
	CreatedAt time.Time
}

// SessionRepo provides typed access to the sessions and session_turns tables.
type SessionRepo struct{ db *sql.DB }

// NewSessionRepo returns a SessionRepo backed by db.
func NewSessionRepo(db *sql.DB) *SessionRepo { return &SessionRepo{db: db} }

// HasTables reports whether this knowledge base carries the session tables, so
// a threaded ask can say what to run instead of failing with "no such table".
func (r *SessionRepo) HasTables() (bool, error) { return HasSessionTables(r.db) }

// NormalizeSessionName lowercases and trims a thread name, so `--session Work`
// and `--session work` name the same thread however the shell capitalised it.
// The same rule metadata topics already follow.
func NormalizeSessionName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// checkedName normalizes name and rejects an empty one.
func checkedName(method, name string) (string, error) {
	n := NormalizeSessionName(name)
	if n == "" {
		return "", fmt.Errorf("%s: session name must not be empty", method)
	}
	return n, nil
}

// Create inserts a new thread and sets s.ID, s.Name (normalized) and the
// timestamps. A name already in use is an error, not a silent merge of two
// conversations.
func (r *SessionRepo) Create(ctx context.Context, s *Session) error {
	name, err := checkedName("SessionRepo.Create", s.Name)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions(name,template,created_at,updated_at) VALUES(?,?,?,?)`,
		name, s.Template, now.Format(sessionTimeLayout), now.Format(sessionTimeLayout),
	)
	if err != nil {
		return fmt.Errorf("SessionRepo.Create: %w", err)
	}
	s.Name = name
	s.CreatedAt = now
	s.UpdatedAt = now
	s.ID, _ = res.LastInsertId()
	return nil
}

// sessionColumns is the select list every session lookup shares, turn count
// included so a listing never has to count in a second round trip.
const sessionColumns = `SELECT s.id, s.name, s.template, s.created_at, s.updated_at,
       (SELECT COUNT(*) FROM session_turns t WHERE t.session_id = s.id)
  FROM sessions s`

// GetByName returns the thread with the given name, or ErrNotFound.
func (r *SessionRepo) GetByName(ctx context.Context, name string) (*Session, error) {
	n, err := checkedName("SessionRepo.GetByName", name)
	if err != nil {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, sessionColumns+` WHERE s.name=?`, n)
	return scanSession(row, "SessionRepo.GetByName")
}

// MostRecent returns the thread whose last turn is newest — what --continue
// targets — or ErrNotFound when the knowledge base holds no threads at all.
func (r *SessionRepo) MostRecent(ctx context.Context) (*Session, error) {
	row := r.db.QueryRowContext(ctx, sessionColumns+` ORDER BY s.updated_at DESC, s.id DESC LIMIT 1`)
	return scanSession(row, "SessionRepo.MostRecent")
}

// List returns every thread, most recently used first, each with its turn count.
func (r *SessionRepo) List(ctx context.Context) ([]*Session, error) {
	rows, err := r.db.QueryContext(ctx, sessionColumns+` ORDER BY s.updated_at DESC, s.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("SessionRepo.List: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := []*Session{}
	for rows.Next() {
		var s Session
		var createdAt, updatedAt string
		if err := rows.Scan(&s.ID, &s.Name, &s.Template, &createdAt, &updatedAt, &s.TurnCount); err != nil {
			return nil, fmt.Errorf("SessionRepo.List scan: %w", err)
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		sessions = append(sessions, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("SessionRepo.List: %w", err)
	}
	return sessions, nil
}

// Rename moves a thread to a new name. An unknown thread is ErrNotFound; a name
// already in use is an error.
func (r *SessionRepo) Rename(ctx context.Context, oldName, newName string) error {
	from, err := checkedName("SessionRepo.Rename", oldName)
	if err != nil {
		return err
	}
	to, err := checkedName("SessionRepo.Rename", newName)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE sessions SET name=? WHERE name=?`, to, from)
	if err != nil {
		return fmt.Errorf("SessionRepo.Rename: %w", err)
	}
	return checkAffected(res, "SessionRepo.Rename", from)
}

// Delete removes a thread and, by the schema's cascade, its turns.
func (r *SessionRepo) Delete(ctx context.Context, name string) error {
	n, err := checkedName("SessionRepo.Delete", name)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE name=?`, n)
	if err != nil {
		return fmt.Errorf("SessionRepo.Delete: %w", err)
	}
	return checkAffected(res, "SessionRepo.Delete", n)
}

// AppendTurn stores a completed turn, assigning it the next index and touching
// the thread's updated_at, and fills in turn.ID, turn.Index and turn.CreatedAt.
//
// The index is computed inside the insert's transaction and the schema's
// UNIQUE(session_id, turn_index) backs it up, so two `tbuk ask --session work`
// running at once give the loser a constraint error rather than silently
// overwriting the winner's turn.
func (r *SessionRepo) AppendTurn(ctx context.Context, turn *SessionTurn) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("SessionRepo.AppendTurn begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var next int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(turn_index)+1, 0) FROM session_turns WHERE session_id=?`,
		turn.SessionID,
	).Scan(&next); err != nil {
		return fmt.Errorf("SessionRepo.AppendTurn index: %w", err)
	}

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO session_turns(session_id,turn_index,question,query,answer,citations,created_at)
         VALUES(?,?,?,?,?,?,?)`,
		turn.SessionID, next, turn.Question, turn.Query, turn.Answer,
		strings.Join(turn.Citations, "\n"), now.Format(sessionTimeLayout),
	)
	if err != nil {
		return fmt.Errorf("SessionRepo.AppendTurn: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET updated_at=? WHERE id=?`, now.Format(sessionTimeLayout), turn.SessionID,
	); err != nil {
		return fmt.Errorf("SessionRepo.AppendTurn touch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("SessionRepo.AppendTurn commit: %w", err)
	}

	turn.ID, _ = res.LastInsertId()
	turn.Index = next
	turn.CreatedAt = now
	return nil
}

// Turns returns a thread's turns in the order they were asked.
func (r *SessionRepo) Turns(ctx context.Context, sessionID int64) ([]SessionTurn, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, session_id, turn_index, question, query, answer, citations, created_at
           FROM session_turns WHERE session_id=? ORDER BY turn_index`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("SessionRepo.Turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	turns := []SessionTurn{}
	for rows.Next() {
		var t SessionTurn
		var citations, createdAt string
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Index, &t.Question, &t.Query,
			&t.Answer, &citations, &createdAt); err != nil {
			return nil, fmt.Errorf("SessionRepo.Turns scan: %w", err)
		}
		if citations != "" {
			t.Citations = strings.Split(citations, "\n")
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		turns = append(turns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("SessionRepo.Turns: %w", err)
	}
	return turns, nil
}

// Prune drops all but the keep most recent turns of a thread, returning how
// many it removed. A keep of zero or less keeps everything, which is what
// session.max_turns = 0 means. Turn indexes are not renumbered: they are the
// thread's history, and the next append continues past the highest one.
func (r *SessionRepo) Prune(ctx context.Context, sessionID int64, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM session_turns
          WHERE session_id=? AND turn_index <= (
              SELECT MAX(turn_index) FROM session_turns WHERE session_id=?
          ) - ?`, sessionID, sessionID, keep)
	if err != nil {
		return 0, fmt.Errorf("SessionRepo.Prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("SessionRepo.Prune: %w", err)
	}
	return int(n), nil
}

// scanSession reads one session row, reporting a miss as ErrNotFound so callers
// can tell "no such thread" from a database failure.
func scanSession(row *sql.Row, method string) (*Session, error) {
	var s Session
	var createdAt, updatedAt string
	err := row.Scan(&s.ID, &s.Name, &s.Template, &createdAt, &updatedAt, &s.TurnCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &s, nil
}

// checkAffected turns "the statement matched nothing" into ErrNotFound, so an
// update or delete against an unknown thread reads as a miss rather than as a
// success that did nothing.
func checkAffected(res sql.Result, method, name string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if n == 0 {
		return fmt.Errorf("%s %q: %w", method, name, ErrNotFound)
	}
	return nil
}
