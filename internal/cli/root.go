package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
)

// ctxKey namespaces values stored in the command context.
type ctxKey int

const (
	cfgKey ctxKey = iota
	cfgPathKey
	rootKey
)

// New returns the root cobra command.
func New() *cobra.Command {
	var (
		cfgFile string
		rootDir string
	)

	root := &cobra.Command{
		Use:   "tbuk",
		Short: "Local-first RAG knowledge base",
		Long:  "tbuk indexes documents and lets you query them with your preferred LLM.",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Resolve the data root and config path. The config always lives
			// directly under the root, so the two flags relate as follows:
			//   --root DIR            → root=DIR, config=DIR/config.yaml
			//   --config FILE (alone) → root=dir(FILE), config=FILE  (so
			//                           `--config DIR/config.yaml` ≡ `--root DIR`)
			//   neither               → root=~/.tbuk, config=~/.tbuk/config.yaml
			// If both are given, --root wins for the root and --config just names
			// the file. Relative data paths in the config resolve under the root.
			var (
				dataRoot string
				path     string
			)
			switch {
			case rootDir != "":
				abs, err := filepath.Abs(rootDir)
				if err != nil {
					return fmt.Errorf("resolve --root %s: %w", rootDir, err)
				}
				dataRoot = abs
				path = cfgFile
				if path == "" {
					path = config.DefaultPathForRoot(dataRoot)
				}
			case cfgFile != "":
				abs, err := filepath.Abs(cfgFile)
				if err != nil {
					return fmt.Errorf("resolve --config %s: %w", cfgFile, err)
				}
				path = abs
				dataRoot = filepath.Dir(abs)
			default:
				dataRoot = config.DefaultRoot()
				path = config.DefaultPathForRoot(dataRoot)
			}
			cfg, err := config.LoadForRoot(path, dataRoot)
			if err != nil {
				return fmt.Errorf("load config %s: %w", path, err)
			}
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config %s: %w", path, err)
			}
			ctx := context.WithValue(cmd.Context(), cfgKey, cfg)
			ctx = context.WithValue(ctx, cfgPathKey, path)
			ctx = context.WithValue(ctx, rootKey, dataRoot)
			cmd.SetContext(ctx)
			return nil
		},
	}

	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: <root>/config.yaml)")
	root.PersistentFlags().StringVar(&rootDir, "root", "", "data root directory (default: ~/.tbuk)")

	root.AddCommand(newContextCmd())
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
	root.AddCommand(newListCmd())
	root.AddCommand(newExportCmd())
	root.AddCommand(newImportCmd())

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

// rootFrom returns the resolved data-root directory from cmd's context (the
// --root value or DefaultRoot()), as set by the root PersistentPreRunE.
func rootFrom(cmd *cobra.Command) string {
	r, _ := cmd.Context().Value(rootKey).(string)
	return r
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
