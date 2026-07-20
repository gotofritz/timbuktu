package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/prompts"
)

func newTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage prompt templates",
	}
	cmd.AddCommand(newTemplateListCmd())
	cmd.AddCommand(newTemplateShowCmd())
	cmd.AddCommand(newTemplateEditCmd())
	return cmd
}

func newTemplateListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available prompt templates",
		RunE: func(_ *cobra.Command, _ []string) error {
			td := promptsDir()
			manifests, err := td.List()
			if err != nil {
				return fmt.Errorf("list templates: %w", err)
			}
			if len(manifests) == 0 {
				fmt.Println("No templates found.")
				return nil
			}
			for _, m := range manifests {
				fmt.Printf("%-20s %s\n", m.Name, m.Description)
			}
			return nil
		},
	}
}

func newTemplateShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print manifest and template files to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			td := promptsDir()
			tmpl, err := td.Load(name)
			if err != nil {
				return fmt.Errorf("load template %q: %w", name, err)
			}
			m := tmpl.Manifest()

			fmt.Printf("=== manifest ===\n")
			fmt.Printf("name:        %s\n", m.Name)
			fmt.Printf("description: %s\n", m.Description)
			fmt.Printf("model:       %s\n", m.Model)
			if m.Temperature != nil {
				fmt.Printf("temperature: %g\n", *m.Temperature)
			} else {
				fmt.Printf("temperature: (default)\n")
			}
			fmt.Printf("max_tokens:  %d\n", m.MaxTokens)
			fmt.Printf("output:      %s\n", m.Output)
			if m.Retrieval.TopK > 0 {
				fmt.Printf("retrieval.top_k:      %d\n", m.Retrieval.TopK)
			}
			if m.Retrieval.MaxTokens > 0 {
				fmt.Printf("retrieval.max_tokens: %d\n", m.Retrieval.MaxTokens)
			}
			if len(m.Variables) > 0 {
				fmt.Println("variables:")
				for k, v := range m.Variables {
					fmt.Printf("  %s: %s\n", k, v.Default)
				}
			}

			dir := filepath.Join(promptsRoot(), name)
			printFile("=== system.tmpl ===", filepath.Join(dir, "system.tmpl"))
			printFile("=== user.tmpl ===", filepath.Join(dir, "user.tmpl"))
			return nil
		},
	}
}

func newTemplateEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <name>",
		Short: "Open a template's manifest in $EDITOR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			manifestPath := filepath.Join(promptsRoot(), name, "manifest.yaml")
			if _, err := os.Stat(manifestPath); err != nil {
				return fmt.Errorf("template %q: %w", name, err)
			}
			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			return launchEditor(editor, manifestPath, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// launchEditor runs the configured editor against path with the given stdio
// attached, so an interactive terminal editor takes over the session. editor
// may include flags (e.g. "code --wait"); its fields are split and path is
// appended as the final argument.
func launchEditor(editor, path string, stdin io.Reader, stdout, stderr io.Writer) error {
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return fmt.Errorf("no editor configured (set $EDITOR)")
	}
	args := append(fields[1:], path)
	c := exec.Command(fields[0], args...)
	c.Stdin = stdin
	c.Stdout = stdout
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("run editor %q: %w", editor, err)
	}
	return nil
}

func promptsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tbuk", "prompts")
}

func promptsDir() *prompts.TemplateDir {
	return prompts.NewTemplateDir(promptsRoot())
}

func printFile(header, path string) {
	fmt.Println(header)
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("<error reading file: %v>\n", err)
		return
	}
	fmt.Println(string(data))
}
