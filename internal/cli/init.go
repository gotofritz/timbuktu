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

func runInit(cmd *cobra.Command, _ []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	tbukDir := filepath.Join(home, ".tbuk")
	// Seed built-in templates under the configured prompts root so init and
	// the ask/template commands agree on where templates live.
	promptsRoot := configFrom(cmd).Prompts.Dir
	qaDir := filepath.Join(promptsRoot, "qa")
	briefDir := filepath.Join(promptsRoot, "brief")
	ankiDir := filepath.Join(promptsRoot, "anki")

	for _, dir := range []string{tbukDir, qaDir, briefDir, ankiDir} {
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

	if err := writeBuiltinQATemplate(qaDir); err != nil {
		return err
	}
	if err := writeBuiltinBriefTemplate(briefDir); err != nil {
		return err
	}
	if err := writeBuiltinAnkiTemplate(ankiDir); err != nil {
		return err
	}

	fmt.Printf("Initialised tbuk at %s\n", tbukDir)
	return nil
}

func writeBuiltinBriefTemplate(dir string) error {
	manifest := `name: brief
description: "Telegraphic, tweet-like answers from retrieved context."
model: ""
temperature: 0.3
max_tokens: 280
retrieval:
  top_k: 5
output: text
`
	system := `You are a telegraphic assistant. Answer using only the provided context.
Rules:
- Max 280 characters per answer
- Drop articles, filler words, hedging
- Fragments OK
- Facts only, no padding
- If context lacks the answer, say: "Not in notes."
`
	user := `Question: {{ .Question }}

Context:
{{ range .Chunks }}
Source: {{ .Citation }}
{{ .Text }}
{{ end }}

Answer in ≤280 chars, telegraphic style:
`

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

func writeBuiltinAnkiTemplate(dir string) error {
	manifest := `name: anki
description: "Generate Anki flashcards from retrieved context."
model: ""
temperature: 0.3
max_tokens: 4096
retrieval:
  top_k: 10
output: text
`
	system := `Generate Anki flashcards from the provided context. Output a single markdown document containing all cards.

Fields are positional (the consuming script assigns meaning by line position):
  line 1       question
  line 2       clarification note in parentheses (optional; same field as question)
  blank line   ONE blank line separating question block from answer block — nowhere else
  line 3+      answer lines, one per item; NO blank lines between answer lines

` + "`----`" + ` separates cards.

Simple card:

` + "```" + `
What is tokenization?

Converting text into tokens that a model can process
` + "```" + `

Question with clarification:

` + "```" + `
What is prefill?
(LLM inference)

Stage where model processes entire prompt and builds KV cache
` + "```" + `

List answer — answer lines are consecutive, no blank lines between them:

` + "```" + `
What are the two phases of LLM inference?

Prefill
Decode
` + "```" + `

Rules:
- 1 question → 1 fact
- Exactly one blank line per card: between question block and answer block
- No blank lines within the answer block
- ` + "`----`" + ` = card separator
- No card numbers
- No bullets unless source material requires them
- Card may have more than two fields; question and answer are logical, not fixed line counts
- Split aggressively: answer has >1 independent idea, >4 list items, or tests multiple relationships

Priority order for card content:
1. Core mental models
2. Cause-and-effect relationships
3. System behaviour
4. Definitions
5. Implementation details

Do not restate source wording. Test understanding, not recall of phrasing.

Bad: "What is decode? The decode phase is when the model generates tokens."
Good: "Why does decode slow down with long context? More KV cache data must be read per token."

Generate the minimum cards needed to cover all important concepts. Never merge concepts to reduce card count.
`
	user := `Topic: {{ .Question }}

Context:

{{ range .Chunks }}
Source: {{ .Citation }}

{{ .Text }}

{{ end }}

Flashcards:
`

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
