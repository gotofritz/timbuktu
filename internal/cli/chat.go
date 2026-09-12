package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/search"
)

func newChatCmd() *cobra.Command {
	var (
		templateName   string
		vars           []string
		topK           int
		requireContext bool
		sessionName    string
		rewriteFlag    string
		expandFlag     int
	)

	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Talk to your knowledge base in a conversation",
		Long: "Runs a REPL over the same retrieval and prompt path as `tbuk ask`, in one process.\n\n" +
			"Without --session the thread lives in memory and nothing is recorded: most\n" +
			"conversations are not worth keeping, and a tool that hoards every idle question\n" +
			"makes `tbuk session list` useless within a week.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := configFrom(cmd)

			td := prompts.NewTemplateDir(cfg.Prompts.Dir)
			tmpl, err := td.Load(templateName)
			if err != nil {
				return fmt.Errorf("load template %q: %w", templateName, err)
			}
			mode, err := resolveRewriteMode(rewriteFlag, tmpl.Manifest())
			if err != nil {
				return err
			}
			expand, err := resolveExpand(expandFlag, cmd.Flags().Changed("expand"), tmpl.Manifest())
			if err != nil {
				return err
			}

			app, err := openApp(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = app.Close() }()

			// /new NAME needs the store whether or not the REPL started with one,
			// so the opener is built up front and the tables are checked once.
			var open func(context.Context, string) (*conversation.Thread, AppendTurnFn, error)
			if err := requireSessionTables(app.Sessions()); err == nil {
				open = func(ctx context.Context, name string) (*conversation.Thread, AppendTurnFn, error) {
					return openThread(ctx, app.Sessions(), cfg.Session, name, false, templateName)
				}
			} else if sessionName != "" {
				// Only a saved chat has to fail here: an unsaved one never touches
				// the tables, and /new NAME will say so if it is asked for.
				return err
			}

			thread, appendTurn := &conversation.Thread{}, AppendTurnFn(nil)
			if sessionName != "" {
				if thread, appendTurn, err = open(cmd.Context(), sessionName); err != nil {
					return err
				}
			}

			emb, err := app.Embedder()
			if err != nil {
				return err
			}
			l, err := app.LLM()
			if err != nil {
				return err
			}

			// Every REPL turn is a turn in a thread, so the planner is built the
			// way a threaded ask builds it — after the LLM, which condense and the
			// expansion spend.
			planner, err := plannerFor(mode, expand, tmpl.Manifest(), true, l.Chat, cmd.ErrOrStderr())
			if err != nil {
				return err
			}

			return RunChat(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), ChatDeps{
				Retrieve:     retrieval.New(search.New(app.DB(), emb)).RetrieveMany,
				Chat:         l.Chat,
				Template:     tmpl,
				Thread:       thread,
				Append:       appendTurn,
				Open:         open,
				HistoryTurns: cfg.Session.HistoryTurns,
				Vars:         vars,
				TopK:         topK,
				Ask: []AskOption{
					WithErrOut(cmd.ErrOrStderr()),
					WithRequireContext(requireContext),
					WithContextBudget(cfg.LLM.ContextTokens, cfg.LLM.MaxTokens),
					WithPlanner(planner),
				},
			})
		},
	}

	cmd.Flags().StringVarP(&templateName, "template", "t", "qa", "prompt template name")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "template variable override (key=value)")
	cmd.Flags().IntVar(&topK, "top", 0, "number of chunks to retrieve (overrides manifest)")
	cmd.Flags().BoolVar(&requireContext, "require-context", false, "abort a turn instead of answering when no relevant context is found")
	cmd.Flags().StringVar(&sessionName, "session", "", "record the conversation in a named thread (created if new); omit to keep it in memory")
	cmd.Flags().StringVar(&rewriteFlag, "rewrite", "", "how the retrieval query is planned: off | window | condense (overrides the template)")
	cmd.Flags().IntVar(&expandFlag, "expand", 0, "extra wordings of the query to retrieve on and fuse (overrides the template; 0 = off)")
	return cmd
}

// ChatDeps is everything the REPL talks to. The retrieval and chat seams, the
// template and the thread come in as values so the loop can be driven by a
// scripted stdin against fakes, with no database and no provider.
type ChatDeps struct {
	Retrieve retrieverFn
	Chat     chatFn
	Template *prompts.Template

	// Thread is the conversation the REPL opens in. Never nil: an unsaved chat
	// gets one that only ever lives in memory.
	Thread *conversation.Thread
	// Append records a completed turn. Nil is the unsaved chat — the turn is
	// still replayed for the rest of the session, it is just never written down.
	Append AppendTurnFn
	// Open switches to a named stored thread for /new NAME. Nil when there is no
	// store behind this REPL, which /new NAME then says rather than pretending.
	Open func(ctx context.Context, name string) (*conversation.Thread, AppendTurnFn, error)
	// HistoryTurns bounds what is replayed as the conversation grows, the same
	// session.history_turns a threaded `ask` is bounded by.
	HistoryTurns int

	Vars []string
	TopK int
	// Ask carries the options every turn is asked with — diagnostics writer,
	// context budget, planner. WithSession is added per turn by the loop.
	Ask []AskOption
}

// chatHelp is the whole command language. Anything larger is a shell, and this
// is a CLI.
const chatHelp = `  /sources        the citations behind the last answer
  /new [name]     start a fresh thread; with a name, a stored one
  /forget         drop the replayed history, keeping the thread
  /help           this list
  /exit           leave (so does Ctrl-D)`

// RunChat is the REPL: read a line, ask it as a turn in the running thread,
// print the answer, repeat. Every turn goes through RunAsk, so a chat and a
// `tbuk ask --session` differ in how they are typed and in nothing else.
//
// Exported for testing.
func RunChat(ctx context.Context, in io.Reader, out io.Writer, deps ChatDeps) error {
	thread, appendTurn := deps.Thread, deps.Append
	if thread == nil {
		thread = &conversation.Thread{}
	}

	// Document-derived text — the REPL's own lines as well as the answers, since
	// a citation printed by /sources came out of a document too (D14).
	w := newSanitizeWriter(out)
	printBanner(w, thread, appendTurn != nil)

	var last conversation.Turn
	scanner := bufio.NewScanner(in)
	for {
		if ctx.Err() != nil {
			return nil
		}
		_, _ = fmt.Fprint(w, "\n> ")
		if !scanner.Scan() {
			_, _ = fmt.Fprintln(w)
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "/") {
			cmd, arg, _ := strings.Cut(line, " ")
			if cmd == "/exit" {
				break
			}
			thread, appendTurn = runChatCommand(ctx, w, deps, cmd, strings.TrimSpace(arg),
				thread, appendTurn, &last)
			continue
		}

		// The turn is recorded through a wrapper rather than handed straight to
		// the store, so the thread in hand grows with the conversation: without
		// that the second question would be asked with the first one already
		// forgotten, which is the one thing a REPL is for.
		turn := conversation.Turn{}
		record := func(ctx context.Context, sessionID int64, t conversation.Turn) error {
			turn = t
			if appendTurn == nil {
				return nil
			}
			return appendTurn(ctx, sessionID, t)
		}

		opts := append(append([]AskOption{}, deps.Ask...), WithSession(thread, record))
		err := RunAsk(ctx, out, deps.Retrieve, deps.Chat, deps.Template, line,
			deps.Vars, deps.TopK, false, opts...)
		if err != nil {
			// One unreachable provider should not cost the whole conversation, so
			// the turn is lost and the loop is not.
			_, _ = fmt.Fprintf(w, "error: %v\n", err)
			continue
		}
		if turn.Question != "" {
			last = turn
			thread.Turns = conversation.Replay(append(thread.Turns, turn), deps.HistoryTurns)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	return nil
}

// printBanner says which thread the REPL opened in and, above all, whether
// anything is being written down: an unsaved chat mistaken for a saved one is
// how a conversation is lost.
func printBanner(w io.Writer, thread *conversation.Thread, saved bool) {
	switch {
	case saved && len(thread.Turns) > 0:
		_, _ = fmt.Fprintf(w, "tbuk chat — thread %q, replaying %d turns.\n", thread.Name, len(thread.Turns))
	case saved:
		_, _ = fmt.Fprintf(w, "tbuk chat — thread %q.\n", thread.Name)
	default:
		_, _ = fmt.Fprintln(w, "tbuk chat — unsaved thread; nothing is recorded. "+
			"Use --session NAME, or /new NAME, to keep it.")
	}
	_, _ = fmt.Fprintln(w, "/help for commands, /exit to leave.")
}

// runChatCommand handles one slash command and returns the thread and store the
// loop carries on with — unchanged for everything but /new.
func runChatCommand(
	ctx context.Context,
	w io.Writer,
	deps ChatDeps,
	cmd, arg string,
	thread *conversation.Thread,
	appendTurn AppendTurnFn,
	last *conversation.Turn,
) (*conversation.Thread, AppendTurnFn) {
	switch cmd {
	case "/help":
		_, _ = fmt.Fprintln(w, chatHelp)

	case "/sources":
		switch {
		case last.Question == "":
			_, _ = fmt.Fprintln(w, "no sources yet — ask something first.")
		case len(last.Citations) == 0:
			_, _ = fmt.Fprintln(w, "the last answer cited nothing.")
		default:
			_, _ = fmt.Fprintln(w, "Sources:")
			for i, c := range last.Citations {
				_, _ = fmt.Fprintf(w, "  [%d] %s\n", i+1, c)
			}
		}

	case "/forget":
		thread.Turns = nil
		_, _ = fmt.Fprintln(w, "forgot the replayed history; the thread itself is untouched.")

	case "/new":
		return startChatThread(ctx, w, deps, arg, thread, appendTurn, last)

	default:
		_, _ = fmt.Fprintf(w, "unknown command %q — /help for the list.\n", cmd)
	}
	return thread, appendTurn
}

// startChatThread implements /new: with no name an unsaved thread, with one the
// stored thread of that name, created if it is new — the same rule
// `ask --session` follows.
func startChatThread(
	ctx context.Context,
	w io.Writer,
	deps ChatDeps,
	name string,
	thread *conversation.Thread,
	appendTurn AppendTurnFn,
	last *conversation.Turn,
) (*conversation.Thread, AppendTurnFn) {
	if name == "" {
		*last = conversation.Turn{}
		_, _ = fmt.Fprintln(w, "started an unsaved thread; nothing from here is recorded.")
		return &conversation.Thread{}, nil
	}
	if deps.Open == nil {
		_, _ = fmt.Fprintf(w, "/new %s is not available here: this knowledge base has no "+
			"conversation threads. Add them with: go run ./scripts/add-sessions <db path>\n", name)
		return thread, appendTurn
	}
	opened, store, err := deps.Open(ctx, name)
	if err != nil {
		// The thread in hand is left alone: losing the conversation to a typo
		// would be a worse answer than refusing the switch.
		_, _ = fmt.Fprintf(w, "error: %v\n", err)
		return thread, appendTurn
	}
	*last = conversation.Turn{}
	_, _ = fmt.Fprintf(w, "switched to thread %q, replaying %d turns.\n", opened.Name, len(opened.Turns))
	return opened, store
}
