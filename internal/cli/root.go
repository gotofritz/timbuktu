package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
)

// ctxKey namespaces values stored in the command context.
type ctxKey int

const (
	cfgKey ctxKey = iota
	cfgPathKey
)

// New returns the root cobra command.
func New() *cobra.Command {
	var cfgFile string

	root := &cobra.Command{
		Use:   "tbuk",
		Short: "Local-first RAG knowledge base",
		Long:  "tbuk indexes documents and lets you query them with your preferred LLM.",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			path := cfgFile
			if path == "" {
				path = config.DefaultPath()
			}
			cfg, err := config.Load(path)
			if err != nil {
				return fmt.Errorf("load config %s: %w", path, err)
			}
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config %s: %w", path, err)
			}
			ctx := context.WithValue(cmd.Context(), cfgKey, cfg)
			ctx = context.WithValue(ctx, cfgPathKey, path)
			cmd.SetContext(ctx)
			return nil
		},
	}

	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ~/.tbuk/config.yaml)")

	root.AddCommand(newInitCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newPreprocessCmd())
	root.AddCommand(newIngestCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newFindCmd())
	root.AddCommand(newMetaCmd())
	root.AddCommand(newAskCmd())
	root.AddCommand(newTemplateCmd())
	root.AddCommand(newDeleteCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newStatsCmd())

	return root
}

// configFrom returns the configuration loaded into cmd's context by the root
// PersistentPreRunE. It returns the zero Config if none was set.
func configFrom(cmd *cobra.Command) config.Config {
	cfg, _ := cmd.Context().Value(cfgKey).(config.Config)
	return cfg
}

// configPathFrom returns the resolved config file path from cmd's context.
func configPathFrom(cmd *cobra.Command) string {
	path, _ := cmd.Context().Value(cfgPathKey).(string)
	return path
}

// Execute runs the CLI and exits on error. It installs a signal-aware context
// so Ctrl-C (SIGINT) or SIGTERM cancels the root context, unwinding the
// ctx-plumbed pipeline cleanly — deferred cleanup runs, in-flight transactions
// roll back, and a partial directory-ingest summary still prints. A second
// signal restores the default handler and force-quits.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := New().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
