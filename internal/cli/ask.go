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
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/search"
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
}

// WithErrOut sets the writer for diagnostics such as the empty-context
// warning. Defaults to io.Discard when unset.
func WithErrOut(w io.Writer) AskOption { return func(c *askConfig) { c.errOut = w } }

// WithRequireContext makes RunAsk abort (instead of calling the LLM) when
// retrieval returns no chunks.
func WithRequireContext(b bool) AskOption { return func(c *askConfig) { c.requireContext = b } }

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

	data := prompts.TemplateData{
		Question:  question,
		Chunks:    chunks,
		Variables: variables,
	}

	systemPrompt, userPrompt, err := tmpl.Render(data)
	if err != nil {
		return fmt.Errorf("render template: %w", err)
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
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

	if noStream {
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
		_, _ = fmt.Fprint(out, sb.String())
	} else {
		for tok := range tokenCh {
			if tok.Error != nil {
				return fmt.Errorf("LLM stream: %w", tok.Error)
			}
			_, _ = fmt.Fprint(out, tok.Text)
			if tok.Done {
				break
			}
		}
	}
	_, _ = fmt.Fprintln(out)

	if len(chunks) > 0 {
		_, _ = fmt.Fprintln(out, "\nSources:")
		for i, ch := range chunks {
			_, _ = fmt.Fprintf(out, "  [%d] %s\n", i+1, ch.Citation)
		}
	}
	return nil
}
