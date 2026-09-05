package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/normalize"
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/squeeze"
)

// retrieverFn is the signature used for testable dependency injection.
type retrieverFn func(ctx context.Context, query string, topK int, meta map[string]string) ([]retrieval.RetrievedChunk, error)

// chatFn is the signature used for testable dependency injection.
type chatFn func(ctx context.Context, messages []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error)

func newAskCmd() *cobra.Command {
	var (
		templateName   string
		vars           []string
		topK           int
		noStream       bool
		requireContext bool
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
				WithErrOut(cmd.ErrOrStderr()),
				WithRequireContext(requireContext),
				WithContextBudget(cfg.LLM.ContextTokens, cfg.LLM.MaxTokens),
			)
		},
	}

	cmd.Flags().StringVarP(&templateName, "template", "t", "qa", "prompt template name")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "template variable override (key=value)")
	cmd.Flags().IntVar(&topK, "top", 0, "number of chunks to retrieve (overrides manifest)")
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "buffer output instead of streaming")
	cmd.Flags().BoolVar(&requireContext, "require-context", false, "abort instead of answering when no relevant context is found")
	return cmd
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
}

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

// renderFn renders the prompt for a set of chunks.
type renderFn func([]retrieval.RetrievedChunk) (system, user string, err error)

// fittedPrompt is the prompt that survived the context budget, with whatever
// had to be done to it to make it fit.
type fittedPrompt struct {
	chunks   []retrieval.RetrievedChunk
	system   string
	user     string
	warnings []string
}

// promptTokens approximates what the rendered prompt costs, using the same
// estimator (chars/4) as the chunker and the retrieval.max_tokens trim.
func promptTokens(system, user string) int {
	return chunking.CountTokens(system) + chunking.CountTokens(user)
}

// fitToContext renders the prompt and, when the estimate exceeds budget,
// compacts the retrieved text and then drops the lowest-ranked chunks until it
// fits. Compaction comes first because text the model can still read beats a
// passage it can no longer cite. A prompt that overflows with no chunks left is
// an error: the provider would reject it, and it can say so less usefully.
func fitToContext(render renderFn, chunks []retrieval.RetrievedChunk, budget int) (fittedPrompt, error) {
	system, user, err := render(chunks)
	if err != nil {
		return fittedPrompt{}, err
	}
	if budget > 0 && promptTokens(system, user) <= budget {
		return fittedPrompt{chunks: chunks, system: system, user: user}, nil
	}

	if len(chunks) > 0 {
		squeezed := squeeze.Chunks(chunks)
		system, user, err = render(squeezed)
		if err != nil {
			return fittedPrompt{}, err
		}
		warnings := []string{
			"warning: the prompt exceeds the model's context budget — compacted the retrieved " +
				"text (whitespace and filler words) to make it fit",
		}
		if budget > 0 && promptTokens(system, user) <= budget {
			return fittedPrompt{chunks: squeezed, system: system, user: user, warnings: warnings}, nil
		}

		// Trailing chunks rank lowest, so they are the cheapest to lose.
		for kept := len(squeezed) - 1; kept >= 0; kept-- {
			system, user, err = render(squeezed[:kept])
			if err != nil {
				return fittedPrompt{}, err
			}
			if budget > 0 && promptTokens(system, user) <= budget {
				return fittedPrompt{
					chunks: squeezed[:kept],
					system: system,
					user:   user,
					warnings: append(warnings, fmt.Sprintf(
						"warning: still over the context budget after compacting — dropped %d of %d "+
							"retrieved chunks; raise llm.context_tokens if your model's window is larger",
						len(squeezed)-kept, len(squeezed))),
				}, nil
			}
		}
	}

	return fittedPrompt{}, fmt.Errorf(
		"prompt needs ~%d tokens but only %d are available for it, even with no retrieved context: "+
			"shorten the question, or raise llm.context_tokens (or lower the template's max_tokens)",
		promptTokens(system, user), budget)
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

	chunks, err := retrieve(ctx, question, k, nil)
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

	fitted := fittedPrompt{chunks: chunks}
	if window > 0 {
		fitted, err = fitToContext(render, chunks, window-reserve)
	} else {
		fitted.system, fitted.user, err = render(chunks)
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

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: fitted.system},
		{Role: llm.RoleUser, Content: fitted.user},
	}

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
		// Record output manages its own trailing newline, including when there are
		// no records and it is empty.
		endsWithNewline = manifest.Normalize.Records != nil || strings.HasSuffix(text, "\n")
	} else {
		for tok := range tokenCh {
			if tok.Error != nil {
				return fmt.Errorf("LLM stream: %w", tok.Error)
			}
			_, _ = fmt.Fprint(out, tok.Text)
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
		_, _ = fmt.Fprintln(cfg.errOut,
			"warning: the model returned no text — the answer is empty; if the template's "+
				"max_tokens is small, raise it (a reasoning model can spend the whole budget "+
				"before writing anything)")
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
	return nil
}
