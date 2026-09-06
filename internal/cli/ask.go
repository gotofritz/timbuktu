package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/normalize"
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/rewrite"
	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/squeeze"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// retrieverFn is the signature used for testable dependency injection.
type retrieverFn func(ctx context.Context, query string, topK int, meta map[string]string) ([]retrieval.RetrievedChunk, error)

// chatFn is the signature used for testable dependency injection.
type chatFn func(ctx context.Context, messages []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error)

func newAskCmd() *cobra.Command {
	var (
		templateName    string
		vars            []string
		topK            int
		noStream        bool
		requireContext  bool
		sessionName     string
		continueSession bool
	)

	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask a question using RAG over your knowledge base",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			question := strings.Join(args, " ")
			cfg := configFrom(cmd)

			td := prompts.NewTemplateDir(cfg.Prompts.Dir)

			tmpl, err := td.Load(templateName)
			if err != nil {
				return fmt.Errorf("load template %q: %w", templateName, err)
			}

			app, err := openApp(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = app.Close() }()

			opts := []AskOption{
				WithErrOut(cmd.ErrOrStderr()),
				WithRequireContext(requireContext),
				WithContextBudget(cfg.LLM.ContextTokens, cfg.LLM.MaxTokens),
			}

			// Resolved before the embedder and the LLM are built, so an unknown
			// thread or a knowledge base without the tables fails on its own
			// terms rather than behind a connection error.
			if sessionName != "" || continueSession {
				thread, appendTurn, err := openThread(
					cmd.Context(), app.Sessions(), cfg.Session, sessionName, continueSession, templateName)
				if err != nil {
					return err
				}
				planner, err := rewrite.New(
					tmpl.Manifest().Retrieval.Rewrite, tmpl.Manifest().Retrieval.WindowTurns)
				if err != nil {
					return fmt.Errorf("template %q: %w", templateName, err)
				}
				opts = append(opts, WithSession(thread, appendTurn), WithPlanner(planner))
			}

			emb, err := app.Embedder()
			if err != nil {
				return err
			}

			ret := retrieval.New(search.New(app.DB(), emb))

			l, err := app.LLM()
			if err != nil {
				return err
			}

			return RunAsk(
				cmd.Context(),
				os.Stdout,
				ret.Retrieve,
				l.Chat,
				tmpl,
				question,
				vars,
				topK,
				noStream,
				opts...,
			)
		},
	}

	cmd.Flags().StringVarP(&templateName, "template", "t", "qa", "prompt template name")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "template variable override (key=value)")
	cmd.Flags().IntVar(&topK, "top", 0, "number of chunks to retrieve (overrides manifest)")
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "buffer output instead of streaming")
	cmd.Flags().BoolVar(&requireContext, "require-context", false, "abort instead of answering when no relevant context is found")
	cmd.Flags().StringVar(&sessionName, "session", "", "record this turn in a named conversation thread (created if new)")
	cmd.Flags().BoolVarP(&continueSession, "continue", "c", false, "continue the most recently used conversation thread")
	cmd.MarkFlagsMutuallyExclusive("session", "continue")
	return cmd
}

// openThread resolves the thread this ask belongs to — named, or the most
// recently used one — and returns it alongside the function that records the
// completed turn.
//
// An unknown name creates the thread: a thread is a shell history file, not a
// resource to provision. --continue is an error when there is nothing to
// continue, never a silent fall back to a single-shot ask, which would look
// identical on screen and answer from nothing.
func openThread(
	ctx context.Context,
	repo *storage.SessionRepo,
	cfg config.SessionConfig,
	name string,
	continueSession bool,
	template string,
) (*conversation.Thread, AppendTurnFn, error) {
	// A knowledge base built before sessions existed reads and ingests fine, so
	// the first threaded ask is where it surfaces — as "no such table" unless
	// something asks first.
	if err := requireSessionTables(repo); err != nil {
		return nil, nil, err
	}

	var (
		sess *storage.Session
		err  error
	)
	if continueSession {
		sess, err = repo.MostRecent(ctx)
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, fmt.Errorf("--continue: this knowledge base has no conversation " +
				"threads yet; start one with --session NAME")
		}
	} else {
		sess, err = repo.GetByName(ctx, name)
		if errors.Is(err, storage.ErrNotFound) {
			sess = &storage.Session{Name: name, Template: template}
			err = repo.Create(ctx, sess)
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open session: %w", err)
	}

	stored, err := repo.Turns(ctx, sess.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("read session %q: %w", sess.Name, err)
	}
	turns := make([]conversation.Turn, len(stored))
	for i, t := range stored {
		turns[i] = conversation.Turn{
			Question:  t.Question,
			Query:     t.Query,
			Answer:    t.Answer,
			Citations: t.Citations,
		}
	}

	thread := &conversation.Thread{
		ID:       sess.ID,
		Name:     sess.Name,
		Template: sess.Template,
		// Bounded here rather than in the ask path: how much of a thread comes
		// back is the user's setting, not the prompt's business.
		Turns: conversation.Replay(turns, cfg.HistoryTurns),
	}

	appendTurn := func(ctx context.Context, sessionID int64, turn conversation.Turn) error {
		if err := repo.AppendTurn(ctx, &storage.SessionTurn{
			SessionID: sessionID,
			Question:  turn.Question,
			Query:     turn.Query,
			Answer:    turn.Answer,
			Citations: turn.Citations,
		}); err != nil {
			return err //nolint:wrapcheck // already "SessionRepo.AppendTurn: …"
		}
		if _, err := repo.Prune(ctx, sessionID, cfg.MaxTurns); err != nil {
			return err //nolint:wrapcheck // already "SessionRepo.Prune: …"
		}
		return nil
	}
	return thread, appendTurn, nil
}

// trimToTokenBudget drops trailing chunks once the cumulative approximate
// token count would exceed budget. A non-positive budget disables trimming.
// At least one chunk is always kept when any are present.
func trimToTokenBudget(chunks []retrieval.RetrievedChunk, budget int) []retrieval.RetrievedChunk {
	if budget <= 0 || len(chunks) == 0 {
		return chunks
	}
	total := 0
	for i, ch := range chunks {
		total += chunking.CountTokens(ch.Text)
		if total > budget && i > 0 {
			return chunks[:i]
		}
	}
	return chunks
}

// AskOption configures optional RunAsk behaviour.
type AskOption func(*askConfig)

type askConfig struct {
	errOut         io.Writer
	requireContext bool
	contextWindow  int // model context window (prompt + reply); 0 = guard off
	outputReserve  int // tokens held back for the reply when the template sets none
	thread         *conversation.Thread
	appendTurn     AppendTurnFn
	planner        rewrite.Planner
}

// AppendTurnFn records a completed turn against a thread. It is a function
// rather than a repository so the ask path does not reach into storage, and so
// a test can assert what was recorded without a database.
type AppendTurnFn func(ctx context.Context, sessionID int64, turn conversation.Turn) error

// WithErrOut sets the writer for diagnostics such as the empty-context
// warning. Defaults to io.Discard when unset.
func WithErrOut(w io.Writer) AskOption { return func(c *askConfig) { c.errOut = w } }

// WithRequireContext makes RunAsk abort (instead of calling the LLM) when
// retrieval returns no chunks.
func WithRequireContext(b bool) AskOption { return func(c *askConfig) { c.requireContext = b } }

// WithContextBudget bounds the rendered prompt against the model's context
// window: window is the whole window (prompt + reply), outputReserve the tokens
// held back for the reply when the template declares no max_tokens of its own.
// A window of 0 disables the guard.
func WithContextBudget(window, outputReserve int) AskOption {
	return func(c *askConfig) {
		c.contextWindow = window
		c.outputReserve = outputReserve
	}
}

// WithSession makes the ask a turn in a conversation: thread's turns are
// replayed into the prompt (they are already bounded by the caller's
// session.history_turns) and the completed turn is handed to appendTurn.
func WithSession(thread *conversation.Thread, appendTurn AppendTurnFn) AskOption {
	return func(c *askConfig) {
		c.thread = thread
		c.appendTurn = appendTurn
	}
}

// WithPlanner sets what turns the thread and the question into the query
// retrieval runs. Unset means retrieving on the question exactly as typed,
// which is what a single-shot ask has always done.
func WithPlanner(p rewrite.Planner) AskOption { return func(c *askConfig) { c.planner = p } }

// buildFn assembles the whole message slice — the system message, the replayed
// history, and the current turn's rendered user message — for one candidate
// combination of history and chunks.
type buildFn func([]conversation.Turn, []retrieval.RetrievedChunk) ([]llm.Message, error)

// fittedPrompt is the prompt that survived the context budget, with whatever
// had to be done to it to make it fit.
type fittedPrompt struct {
	chunks   []retrieval.RetrievedChunk
	messages []llm.Message
	warnings []string
}

// promptTokens approximates what the rendered prompt costs, using the same
// script-aware estimator as the chunker and the retrieval.max_tokens trim.
func promptTokens(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += chunking.CountTokens(m.Content)
	}
	return total
}

const compactedWarning = "warning: the prompt exceeds the model's context budget — compacted the retrieved " +
	"text (whitespace and filler words) to make it fit"

// droppedHistoryWarning names how much of the thread went, since an answer that
// has forgotten the turn before it reads very differently from one that has not.
func droppedHistoryWarning(dropped, total int) string {
	return fmt.Sprintf(
		"warning: the prompt exceeds the model's context budget — dropped the %d oldest of %d "+
			"replayed turns; lower session.history_turns, or raise llm.context_tokens",
		dropped, total)
}

// fitToContext renders the prompt and, when the estimate exceeds budget, climbs
// a ladder: drop the oldest replayed turns, compact the retrieved text, drop the
// lowest-ranked chunks, and only then give up the thread entirely.
//
// History goes first because grounded evidence for the question in front of you
// beats a transcript of the questions behind it, and because the user can
// restate what the thread forgot but cannot restate what the corpus was never
// asked for. The most recent pair is the floor — pronoun resolution dies
// without it — so it is surrendered only when nothing else is left, and loudly.
// A prompt that overflows with no chunks and no thread is an error: the
// provider would reject it, and it can say so less usefully.
func fitToContext(build buildFn, history []conversation.Turn, chunks []retrieval.RetrievedChunk, budget int) (fittedPrompt, error) {
	fits := func(messages []llm.Message) bool {
		return budget > 0 && promptTokens(messages) <= budget
	}

	messages, err := build(history, chunks)
	if err != nil {
		return fittedPrompt{}, err
	}
	if fits(messages) {
		return fittedPrompt{chunks: chunks, messages: messages}, nil
	}

	var warnings []string

	// Rung 1: the oldest pairs, one at a time, down to the most recent one.
	for dropped := 1; dropped < len(history); dropped++ {
		messages, err = build(history[dropped:], chunks)
		if err != nil {
			return fittedPrompt{}, err
		}
		if fits(messages) {
			return fittedPrompt{
				chunks:   chunks,
				messages: messages,
				warnings: []string{droppedHistoryWarning(dropped, len(history))},
			}, nil
		}
	}
	if len(history) > 1 {
		warnings = append(warnings, droppedHistoryWarning(len(history)-1, len(history)))
		history = history[len(history)-1:]
	}

	// Rungs 2 and 3: compact the retrieved text, then drop the lowest-ranked
	// chunks, with the floor of the thread still in place.
	fitted, ok, err := fitChunks(build, fits, history, chunks)
	if err != nil {
		return fittedPrompt{}, err
	}
	if ok {
		fitted.warnings = append(warnings, fitted.warnings...)
		return fitted, nil
	}

	// Rung 4: the floor goes too. Try the chunk ladder again without it, since
	// the room it frees may let some evidence back in.
	if len(history) > 0 {
		warnings = append(warnings,
			"warning: still over the context budget — dropped the conversation thread entirely; "+
				"this answer does not see the earlier turns")
		fitted, ok, err = fitChunks(build, fits, nil, chunks)
		if err != nil {
			return fittedPrompt{}, err
		}
		if ok {
			fitted.warnings = append(warnings, fitted.warnings...)
			return fitted, nil
		}
	}

	if messages, err = build(nil, nil); err != nil {
		return fittedPrompt{}, err
	}
	return fittedPrompt{}, fmt.Errorf(
		"prompt needs ~%d tokens but only %d are available for it, even with no retrieved context: "+
			"shorten the question, or raise llm.context_tokens (or lower the template's max_tokens)",
		promptTokens(messages), budget)
}

// fitChunks compacts the retrieved text and then drops the lowest-ranked
// passages, under a fixed amount of history. It reports whether anything fit.
//
// Compaction comes before dropping because text the model can still read beats
// a passage it can no longer cite; trailing chunks rank lowest, so they are the
// cheapest to lose.
func fitChunks(
	build buildFn,
	fits func([]llm.Message) bool,
	history []conversation.Turn,
	chunks []retrieval.RetrievedChunk,
) (fittedPrompt, bool, error) {
	if len(chunks) == 0 {
		return fittedPrompt{}, false, nil
	}
	squeezed := squeeze.Chunks(chunks)
	messages, err := build(history, squeezed)
	if err != nil {
		return fittedPrompt{}, false, err
	}
	warnings := []string{compactedWarning}
	if fits(messages) {
		return fittedPrompt{chunks: squeezed, messages: messages, warnings: warnings}, true, nil
	}

	for kept := len(squeezed) - 1; kept >= 0; kept-- {
		messages, err = build(history, squeezed[:kept])
		if err != nil {
			return fittedPrompt{}, false, err
		}
		if fits(messages) {
			return fittedPrompt{
				chunks:   squeezed[:kept],
				messages: messages,
				warnings: append(warnings, fmt.Sprintf(
					"warning: still over the context budget after compacting — dropped %d of %d "+
						"retrieved chunks; raise llm.context_tokens if your model's window is larger",
					len(squeezed)-kept, len(squeezed))),
			}, true, nil
		}
	}
	return fittedPrompt{}, false, nil
}

// RunAsk is the testable core of the ask command. It runs retrieval and the
// LLM call under a cancellable context derived from ctx, cancelled on return so
// an abandoned stream goroutine is released (and Ctrl-C interrupts the call).
func RunAsk(
	ctx context.Context,
	out io.Writer,
	retrieve retrieverFn,
	chat chatFn,
	tmpl *prompts.Template,
	question string,
	varOverrides []string,
	topK int,
	noStream bool,
	opts ...AskOption,
) error {
	cfg := askConfig{errOut: io.Discard}
	for _, o := range opts {
		o(&cfg)
	}

	// Retrieved document text is echoed by the model and printed in citations;
	// route everything written to the terminal through a control-char filter so
	// ingested ANSI/OSC escapes can't reach it raw.
	out = newSanitizeWriter(out)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	manifest := tmpl.Manifest()

	k := topK
	if k <= 0 {
		k = manifest.Retrieval.TopK
	}
	if k <= 0 {
		k = 5
	}

	// fail fast on malformed --var flags
	variables := make(map[string]string)
	for k2, v := range manifest.Variables {
		variables[k2] = v.Default
	}
	for _, kv := range varOverrides {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid --var %q: expected key=value", kv)
		}
		variables[parts[0]] = parts[1]
	}

	// A follow-up question is not a query: `and maps?` retrieved on its own
	// reaches nothing, so inside a thread the planner folds the turns behind it
	// back in. Without a planner the query is the question, exactly as typed.
	var history []conversation.Turn
	if cfg.thread != nil {
		history = cfg.thread.Turns
	}
	query := question
	if cfg.planner != nil {
		queries, err := cfg.planner.Queries(ctx, history, question)
		if err != nil {
			return fmt.Errorf("plan query: %w", err)
		}
		if len(queries) > 0 && strings.TrimSpace(queries[0]) != "" {
			query = queries[0]
		}
	}

	chunks, err := retrieve(ctx, query, k, nil)
	if err != nil {
		return fmt.Errorf("retrieve: %w", err)
	}
	chunks = trimToTokenBudget(chunks, manifest.Retrieval.MaxTokens)

	// Empty retrieval means the answer comes purely from model priors, not the
	// user's documents. Warn loudly (or abort under --require-context) so this
	// isn't mistaken for a grounded answer.
	if len(chunks) == 0 {
		if cfg.requireContext {
			return fmt.Errorf("no relevant context found in the knowledge base; " +
				"aborting because --require-context is set")
		}
		_, _ = fmt.Fprintln(cfg.errOut,
			"warning: no relevant context found — answering from the model's general "+
				"knowledge; the response may not reflect your documents")
	}

	render := func(cs []retrieval.RetrievedChunk) (string, string, error) {
		system, user, err := tmpl.Render(prompts.TemplateData{
			Question:  question,
			Chunks:    cs,
			Variables: variables,
		})
		if err != nil {
			return "", "", fmt.Errorf("render template: %w", err)
		}
		return system, user, nil
	}

	// The whole prompt — system + template + chunks + question — has to fit the
	// model's window alongside the reply it has to leave room for. The template
	// speaks for the model it pins; the config for everything else.
	window := manifest.ContextTokens
	if window <= 0 {
		window = cfg.contextWindow
	}
	reserve := manifest.MaxTokens
	if reserve <= 0 {
		reserve = cfg.outputReserve
	}

	build := func(hs []conversation.Turn, cs []retrieval.RetrievedChunk) ([]llm.Message, error) {
		system, user, err := render(cs)
		if err != nil {
			return nil, err
		}
		return conversation.Messages(system, user, hs), nil
	}

	fitted := fittedPrompt{chunks: chunks}
	if window > 0 {
		fitted, err = fitToContext(build, history, chunks, window-reserve)
	} else {
		fitted.messages, err = build(history, chunks)
	}
	if err != nil {
		return err
	}
	for _, w := range fitted.warnings {
		_, _ = fmt.Fprintln(cfg.errOut, w)
	}
	// Everything retrieved was dropped to make the prompt fit, so the answer
	// would come from the model's priors after all.
	if cfg.requireContext && len(fitted.chunks) == 0 && len(chunks) > 0 {
		return fmt.Errorf("the retrieved context does not fit the model's context window; " +
			"aborting because --require-context is set")
	}
	chunks = fitted.chunks
	messages := fitted.messages

	callOpts := llm.CallOptions{
		Model:       manifest.Model,
		Temperature: manifest.Temperature,
		MaxTokens:   manifest.MaxTokens,
	}
	tokenCh, err := chat(ctx, messages, callOpts) //nolint:wrapcheck
	if err != nil {
		return fmt.Errorf("LLM chat: %w", err)
	}

	// The answer ends with exactly one newline, whichever path produced it: the
	// model's own trailing newline counts, so prose does not gain a blank line
	// and a record stream stays byte-exact.
	endsWithNewline := false

	// A completion with no text in it leaves nothing above the Sources list.
	// Tracked on the raw stream, not the printed output, so a normalizer that
	// legitimately reduces a real completion to nothing is not mistaken for it.
	gotText := false

	// Under a session the answer has to be kept as well as printed, since a turn
	// stores what the user saw. The tee exists only then, so the single-shot
	// path allocates exactly what it always did (D10).
	var answer *strings.Builder
	if cfg.thread != nil {
		answer = &strings.Builder{}
	}

	// A declared normalize pipeline rewrites whole cards, so the completion has
	// to be in hand before anything is printed — streaming is not available for
	// those templates.
	if noStream || manifest.Normalize.Declared() {
		var sb strings.Builder
		for tok := range tokenCh {
			if tok.Error != nil {
				return fmt.Errorf("LLM stream: %w", tok.Error)
			}
			sb.WriteString(tok.Text)
			if tok.Done {
				break
			}
		}
		gotText = sb.Len() > 0
		text, err := normalize.Apply(sb.String(), manifest.Normalize)
		if err != nil {
			return fmt.Errorf("normalize output: %w", err)
		}
		_, _ = fmt.Fprint(out, text)
		if answer != nil {
			// The normalized text, not the raw completion: a turn records what
			// the user saw.
			answer.WriteString(text)
		}
		// Record output manages its own trailing newline, including when there are
		// no records and it is empty.
		endsWithNewline = manifest.Normalize.Records != nil || strings.HasSuffix(text, "\n")
	} else {
		for tok := range tokenCh {
			if tok.Error != nil {
				return fmt.Errorf("LLM stream: %w", tok.Error)
			}
			_, _ = fmt.Fprint(out, tok.Text)
			if answer != nil {
				answer.WriteString(tok.Text)
			}
			if tok.Text != "" {
				gotText = true
				endsWithNewline = strings.HasSuffix(tok.Text, "\n")
			}
			if tok.Done {
				break
			}
		}
	}
	if !endsWithNewline {
		_, _ = fmt.Fprintln(out)
	}

	// Silence is indistinguishable from a model that had nothing to say, so name
	// the usual cause: a max_tokens sized for the answer's length leaves nothing
	// for a model that reasons before it writes, and the whole budget goes to
	// text the API never returns.
	if !gotText {
		empty := "warning: the model returned no text — the answer is empty; if the template's " +
			"max_tokens is small, raise it (a reasoning model can spend the whole budget " +
			"before writing anything)"
		if cfg.thread != nil {
			// An empty assistant message replayed as history teaches the model
			// nothing and costs the next prompt a pair, so the turn is not
			// recorded — which the user has to hear, or `--continue` will look
			// as though it forgot the question.
			empty += "; the turn was not recorded in the thread"
		}
		_, _ = fmt.Fprintln(cfg.errOut, empty)
	}

	if len(chunks) > 0 {
		// Citations are provenance for whoever ran the command. When the template
		// declares a record layout the output is meant for another program — an
		// Anki import, a script — so the footer would corrupt it; report it on the
		// diagnostics stream instead of dropping it.
		sink := out
		if manifest.Normalize.Records != nil {
			sink = newSanitizeWriter(cfg.errOut)
		}
		_, _ = fmt.Fprintln(sink, "\nSources:")
		for i, ch := range chunks {
			_, _ = fmt.Fprintf(sink, "  [%d] %s\n", i+1, ch.Citation)
		}
	}

	// The turn is appended last and only here: a Ctrl-C, a provider error or a
	// normalize failure has already returned, so a thread never holds half an
	// answer — half an answer replayed as context is worse than a thread that
	// lost a turn, because the model reads a truncated message as one it
	// finished making.
	if cfg.thread != nil && cfg.appendTurn != nil && gotText {
		citations := make([]string, len(chunks))
		for i, ch := range chunks {
			citations[i] = ch.Citation
		}
		if err := cfg.appendTurn(ctx, cfg.thread.ID, conversation.Turn{
			Question:  question,
			Query:     query,
			Answer:    strings.TrimRight(answer.String(), "\n"),
			Citations: citations,
		}); err != nil {
			return fmt.Errorf("record turn in session %q: %w", cfg.thread.Name, err)
		}
	}
	return nil
}
