package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/storage"
)

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Inspect and manage conversation threads",
	}
	cmd.AddCommand(newSessionListCmd())
	cmd.AddCommand(newSessionShowCmd())
	cmd.AddCommand(newSessionRenameCmd())
	cmd.AddCommand(newSessionDeleteCmd())
	return cmd
}

func newSessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List conversation threads: name, turns, last used",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo, closeApp, err := openSessions(cmd)
			if err != nil {
				return err
			}
			defer closeApp()
			return RunSessionList(cmd.Context(), cmd.OutOrStdout(), repo)
		},
	}
}

func newSessionShowCmd() *cobra.Command {
	var verbose bool

	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Print a conversation thread, turn by turn",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, closeApp, err := openSessions(cmd)
			if err != nil {
				return err
			}
			defer closeApp()
			return RunSessionShow(cmd.Context(), cmd.OutOrStdout(), repo, args[0], verbose)
		},
	}

	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also print the query retrieval ran for each turn")
	return cmd
}

func newSessionRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a conversation thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, closeApp, err := openSessions(cmd)
			if err != nil {
				return err
			}
			defer closeApp()
			return RunSessionRename(cmd.Context(), cmd.OutOrStdout(), repo, args[0], args[1])
		},
	}
}

func newSessionDeleteCmd() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a conversation thread and its turns",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, closeApp, err := openSessions(cmd)
			if err != nil {
				return err
			}
			defer closeApp()
			return RunSessionDelete(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), repo, args[0], yes)
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation prompt")
	return cmd
}

// openSessions opens the configured knowledge base and returns its thread store
// plus a close function, refusing early on a database that predates the tables.
func openSessions(cmd *cobra.Command) (*storage.SessionRepo, func(), error) {
	app, err := openApp(configFrom(cmd))
	if err != nil {
		return nil, nil, err
	}
	repo := app.Sessions()
	if err := requireSessionTables(repo); err != nil {
		_ = app.Close()
		return nil, nil, err
	}
	return repo, func() { _ = app.Close() }, nil
}

// requireSessionTables refuses a knowledge base built before conversation
// threads existed, naming the script that adds them.
//
// Such a database reads, ingests and searches fine — nothing but the thread
// commands touches those tables — so without this the first one anybody runs
// fails with a raw "no such table" (issue #157).
func requireSessionTables(repo *storage.SessionRepo) error {
	ok, err := repo.HasTables()
	if err != nil {
		return fmt.Errorf("check for the session tables: %w", err)
	}
	if !ok {
		return errors.New("this knowledge base predates conversation threads; " +
			"add the tables with: go run ./scripts/add-sessions <db path>")
	}
	return nil
}

// sessionTimestamp renders a thread timestamp the way `tbuk list` renders a
// document's: RFC3339 in UTC, so two knowledge bases compared side by side read
// the same wherever they are.
func sessionTimestamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// RunSessionList prints every thread — name, turn count, last used — most
// recently used first, which is the order `--continue` picks from. Exported for
// testing.
func RunSessionList(ctx context.Context, out io.Writer, repo *storage.SessionRepo) error {
	sessions, err := repo.List(ctx)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if len(sessions) == 0 {
		_, _ = fmt.Fprintln(out, "No conversation threads. Start one with: tbuk ask --session NAME \"…\"")
		return nil
	}

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tTURNS\tTEMPLATE\tLAST USED")
	for _, s := range sessions {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n",
			stripControl(s.Name), s.TurnCount, stripControl(s.Template), sessionTimestamp(s.UpdatedAt))
	}
	return tw.Flush() //nolint:wrapcheck // a write failure to the terminal speaks for itself
}

// RunSessionShow prints a thread turn by turn. With verbose it also prints the
// query retrieval actually ran, so a planned query that fetched the wrong thing
// is visible rather than mysterious.
//
// Everything printed is document-derived — an answer echoes the chunks it was
// given — so it goes out through the control-character filter that `search` and
// `ask` already use (D14). Exported for testing.
func RunSessionShow(ctx context.Context, out io.Writer, repo *storage.SessionRepo, name string, verbose bool) error {
	sess, err := lookupSession(ctx, repo, name)
	if err != nil {
		return err
	}
	turns, err := repo.Turns(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("read session %q: %w", sess.Name, err)
	}

	w := newSanitizeWriter(out)
	_, _ = fmt.Fprintf(w, "thread:    %s\ntemplate:  %s\nturns:     %d\nlast used: %s\n",
		sess.Name, sess.Template, len(turns), sessionTimestamp(sess.UpdatedAt))
	if len(turns) == 0 {
		_, _ = fmt.Fprintf(w, "\nThis thread has no turns yet. Add one with: tbuk ask --session %s \"…\"\n", sess.Name)
		return nil
	}

	for _, t := range turns {
		_, _ = fmt.Fprintf(w, "\n── turn %d ── %s\n> %s\n", t.Index+1, sessionTimestamp(t.CreatedAt), t.Question)
		if verbose {
			_, _ = fmt.Fprintf(w, "[query] %s\n", t.Query)
		}
		_, _ = fmt.Fprintf(w, "\n%s\n", strings.TrimRight(t.Answer, "\n"))
		if len(t.Citations) > 0 {
			_, _ = fmt.Fprintln(w, "\nSources:")
			for i, c := range t.Citations {
				_, _ = fmt.Fprintf(w, "  [%d] %s\n", i+1, c)
			}
		}
	}
	return nil
}

// RunSessionRename moves a thread to a new name, keeping its turns. Exported
// for testing.
func RunSessionRename(ctx context.Context, out io.Writer, repo *storage.SessionRepo, oldName, newName string) error {
	from, err := lookupSession(ctx, repo, oldName)
	if err != nil {
		return err
	}
	to := storage.NormalizeSessionName(newName)
	if to == "" {
		return errors.New("the new session name must not be empty")
	}
	// Checked before the statement so the collision reads as one, rather than as
	// a UNIQUE constraint from the driver. Two threads are never merged.
	if to != from.Name {
		if _, err := repo.GetByName(ctx, to); err == nil {
			return fmt.Errorf("a conversation thread named %q already exists", to)
		} else if !errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("look up %q: %w", to, err)
		}
	}
	if err := repo.Rename(ctx, from.Name, to); err != nil {
		return fmt.Errorf("rename session: %w", err)
	}
	_, _ = fmt.Fprintf(newSanitizeWriter(out), "Renamed %q to %q (%d turns).\n", from.Name, to, from.TurnCount)
	return nil
}

// RunSessionDelete removes a thread and, by the schema's cascade, its turns.
// Without yes it confirms first, the way `tbuk delete` does. Exported for
// testing.
func RunSessionDelete(ctx context.Context, in io.Reader, out io.Writer, repo *storage.SessionRepo, name string, yes bool) error {
	sess, err := lookupSession(ctx, repo, name)
	if err != nil {
		return err
	}
	if !yes {
		prompt := fmt.Sprintf("Delete conversation thread %q (%d turns)? [y/N] ", sess.Name, sess.TurnCount)
		if !ConfirmYes(in, newSanitizeWriter(out), prompt) {
			_, _ = fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}
	if err := repo.Delete(ctx, sess.Name); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	_, _ = fmt.Fprintf(newSanitizeWriter(out), "Deleted conversation thread %q (%d turns removed).\n",
		sess.Name, sess.TurnCount)
	return nil
}

// lookupSession resolves a name to a thread, reporting a miss as an error that
// names the threads that do exist: the usual cause is a typo, and the answer is
// then already on screen (D11). Unlike `ask --session`, these commands never
// create the thread — nothing would be in it.
func lookupSession(ctx context.Context, repo *storage.SessionRepo, name string) (*storage.Session, error) {
	sess, err := repo.GetByName(ctx, name)
	if err == nil {
		return sess, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("look up session %q: %w", name, err)
	}

	wanted := storage.NormalizeSessionName(name)
	known, listErr := repo.List(ctx)
	if listErr != nil || len(known) == 0 {
		return nil, fmt.Errorf("no conversation thread named %q; "+
			"this knowledge base has none yet — start one with: tbuk ask --session %s \"…\"",
			wanted, wanted)
	}
	names := make([]string, len(known))
	for i, s := range known {
		names[i] = stripControl(s.Name)
	}
	return nil, fmt.Errorf("no conversation thread named %q; known threads: %s",
		wanted, strings.Join(names, ", "))
}
