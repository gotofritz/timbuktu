package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
)

var (
	cfgFile string
	cfg     config.Config
)

// New returns the root cobra command.
func New() *cobra.Command {
	root := &cobra.Command{
		Use:   "tbuk",
		Short: "Local-first RAG knowledge base",
		Long:  "tbuk indexes documents and lets you query them with your preferred LLM.",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			path := cfgFile
			if path == "" {
				path = config.DefaultPath()
			}
			var err error
			cfg, err = config.Load(path)
			if err != nil {
				return fmt.Errorf("load config %s: %w", path, err)
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ~/.tbuk/config.yaml)")

	root.AddCommand(newInitCmd())
	root.AddCommand(newVersionCmd())

	return root
}

// Config returns the loaded configuration after PersistentPreRunE has run.
func Config() config.Config { return cfg }

// Execute runs the CLI and exits on error.
func Execute() {
	if err := New().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
