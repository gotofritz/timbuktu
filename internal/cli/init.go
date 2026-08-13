package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// The data root comes from --root (resolved in the root PersistentPreRunE),
	// defaulting to ~/.tbuk. All scaffolding — config, templates, and the config
	// file's own data paths — is derived from it.
	tbukDir := rootFrom(cmd)
	if tbukDir == "" {
		tbukDir = config.DefaultRoot()
	}
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
	switch existing, err := os.ReadFile(cfgPath); {
	case errors.Is(err, os.ErrNotExist):
		defaultYAML, err := config.DefaultYAMLForRoot(tbukDir)
		if err != nil {
			return fmt.Errorf("render default config: %w", err)
		}
		if err := os.WriteFile(cfgPath, []byte(defaultYAML), 0o600); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Printf("Created config: %s\n", cfgPath)
	case err != nil:
		return fmt.Errorf("read config %s: %w", cfgPath, err)
	default:
		// Config exists: add any default keys it is missing, preserving the
		// user's own values, then rewrite it. If nothing is missing, leave it
		// untouched.
		merged, added, err := config.FillMissingDefaults(existing)
		if err != nil {
			return fmt.Errorf("update config %s: %w", cfgPath, err)
		}
		if len(added) == 0 {
			fmt.Printf("Config already complete: %s\n", cfgPath)
		} else {
			if err := os.WriteFile(cfgPath, merged, 0o600); err != nil {
				return fmt.Errorf("write config: %w", err)
			}
			fmt.Printf("Updated config: %s (added %s)\n", cfgPath, strings.Join(added, ", "))
		}
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
# Long generations drift back into markdown lists and stop emitting separators,
# so the card shape is repaired after the call instead of only being asked for.
normalize:
  filters: [strip_preamble, strip_fences, strip_headings, strip_list_markers, collapse_blank_lines]
  records:
    separator: "----"
    fields: [lead, note, body]
`
	system := `Generate Anki flashcards from the provided context.

Emit cards and nothing else: no preamble, no closing remark, no headings, no
code fences, no card numbers.

Every card is the same block, and the separator is part of it — every card
starts with one:

  ----          separator line: four hyphens, nothing else
  (empty line)
  line A        question
  line B        note: extra context for the question. Most cards have none, so
                line B is usually empty — emit the empty line anyway. It is the
                note field, not spacing. Never drop it.
  line C on     answer, one item per line
  (empty line)  ends the card

Fields are positional: the consuming script reads line A as the question, line B
as the note, and every line after that as the answer. Drop line B and the first
answer item silently becomes the note.

The only empty lines in a card are the one after ----, line B when the card has
no note, and the one that ends the card. The answer itself has no blank lines.

Answer items are bare lines. Do not start any line with -, *, + or a bullet
character, and do not number them. Inline emphasis or backticks inside a line
are fine; line-leading list markup is not.

Reproduce this shape exactly — four cards, the third one carrying a note:

===BEGIN EXAMPLE===
----

What is tokenization?

Converting text into tokens a model can process

----

What are the two phases of LLM inference?

Prefill
Decode

----

What is prefill?
(LLM inference)
Stage where the model processes the whole prompt and builds the KV cache

----

Why does decode slow down with long context?

More KV cache data must be read per generated token
===END EXAMPLE===

Check every card before emitting it: it opens with ----, its question is
followed by exactly one note line (empty unless the card needs a note), and its
answer carries no blank lines and no list markup.

Rules:
- 1 question -> 1 fact
- ---- before every card, first card included
- No bullets, no numbering, no headings
- A card may have many answer lines; question, note and answer are logical
  blocks, not fixed line counts
- Split aggressively: split when an answer holds more than one independent idea,
  more than 4 list items, or tests multiple relationships

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
