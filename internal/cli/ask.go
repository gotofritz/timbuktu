package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/embeddings"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// retrieverFn is the signature used for testable dependency injection.
type retrieverFn func(ctx context.Context, query string, topK int, meta map[string]string) ([]retrieval.RetrievedChunk, error)

// chatFn is the signature used for testable dependency injection.
type chatFn func(ctx context.Context, messages []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error)

func newAskCmd() *cobra.Command {
	var (
		templateName string
		vars         []string
		topK         int
		noStream     bool
	)

	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask a question using RAG over your knowledge base",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			question := strings.Join(args, " ")
			cfg := Config()

			home, _ := os.UserHomeDir()
			promptsRoot := filepath.Join(home, ".tbuk", "prompts")
			td := prompts.NewTemplateDir(promptsRoot)

			tmpl, err := td.Load(templateName)
			if err != nil {
				return fmt.Errorf("load template %q: %w", templateName, err)
			}

			db, err := storage.Open(cfg.Database.Path)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = db.Close() }()

			emb, err := embeddings.NewEmbedder(cfg.Embedding)
			if err != nil {
				return fmt.Errorf("create embedder: %w", err)
			}

			searcher := search.New(db.DB(), emb)
			ret := retrieval.New(searcher)

			l, err := llm.NewLLM(&cfg.LLM)
			if err != nil {
				return fmt.Errorf("create LLM: %w", err)
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
			)
		},
	}

	cmd.Flags().StringVarP(&templateName, "template", "t", "qa", "prompt template name")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "template variable override (key=value)")
	cmd.Flags().IntVar(&topK, "top", 0, "number of chunks to retrieve (overrides manifest)")
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "buffer output instead of streaming")
	return cmd
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
) error {
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

	opts := llm.CallOptions{
		Model:       manifest.Model,
		Temperature: manifest.Temperature,
		MaxTokens:   manifest.MaxTokens,
	}
	tokenCh, err := chat(ctx, messages, opts) //nolint:wrapcheck
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
