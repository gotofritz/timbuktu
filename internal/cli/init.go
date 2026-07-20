package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialise the tbuk data directory and default config",
		RunE:  runInit,
	}
}

func runInit(_ *cobra.Command, _ []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	tbukDir := filepath.Join(home, ".tbuk")
	promptsDir := filepath.Join(tbukDir, "prompts", "qa")

	for _, dir := range []string{tbukDir, promptsDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	cfgPath := filepath.Join(tbukDir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		defaultYAML, err := config.DefaultYAML()
		if err != nil {
			return fmt.Errorf("render default config: %w", err)
		}
		if err := os.WriteFile(cfgPath, []byte(defaultYAML), 0o600); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Printf("Created config: %s\n", cfgPath)
	} else {
		fmt.Printf("Config already exists: %s\n", cfgPath)
	}

	if err := writeBuiltinQATemplate(promptsDir); err != nil {
		return err
	}

	fmt.Printf("Initialised tbuk at %s\n", tbukDir)
	return nil
}

func writeBuiltinQATemplate(dir string) error {
	manifest := `name: qa
description: "Question-answering over retrieved context."
model: ""
temperature: 0.2
max_tokens: 2048
retrieval:
  top_k: 5
output: text
`
	system := `You are a helpful assistant that answers questions using only the provided context.
If the context does not contain the answer, say so clearly.
`
	user := `Question:

{{ .Question }}

Context:

{{ range .Chunks }}
Source: {{ .Citation }}

{{ .Text }}

{{ end }}`

	files := map[string]string{
		"manifest.yaml": manifest,
		"system.tmpl":   system,
		"user.tmpl":     user,
	}

	for name, content := range files {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
		}
	}
	return nil
}
