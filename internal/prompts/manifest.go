package prompts

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/gotofritz/timbuktu/internal/normalize"
	"github.com/gotofritz/timbuktu/internal/rewrite"
)

// RetrievalConfig controls how many chunks to fetch for this template, and how
// the query they are fetched with is planned.
type RetrievalConfig struct {
	TopK      int `yaml:"top_k"`
	MaxTokens int `yaml:"max_tokens"`
	// Rewrite names the query planner used for a turn inside a thread:
	// "window" (the default) folds the last few questions into the query,
	// "off" retrieves on the question exactly as typed. Query planning spends
	// the template's model at the template's temperature, which is why it is
	// configured here rather than in config.yaml.
	Rewrite string `yaml:"rewrite"`
	// WindowTurns is how many prior questions "window" folds in. 0 inherits
	// rewrite.DefaultWindowTurns.
	WindowTurns int `yaml:"window_turns"`
}

// VariableDefault holds a default value for a template variable.
type VariableDefault struct {
	Default string `yaml:"default"`
}

// Manifest is the parsed manifest.yaml for a prompt template.
type Manifest struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Model       string   `yaml:"model"`
	Temperature *float64 `yaml:"temperature"` // nil = unset (provider default)
	MaxTokens   int      `yaml:"max_tokens"`
	// ContextTokens overrides llm.context_tokens for this template — the whole
	// window of the model it pins, prompt and reply together. 0 = inherit.
	ContextTokens int                        `yaml:"context_tokens"`
	Retrieval     RetrievalConfig            `yaml:"retrieval"`
	Variables     map[string]VariableDefault `yaml:"variables"`
	Output        string                     `yaml:"output"`
	// Normalize repairs model output that drifted from the shape system.tmpl
	// asked for. Empty means the completion is passed through untouched.
	Normalize normalize.Config `yaml:"normalize"`
}

func loadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if err := m.Normalize.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("manifest %s: %w", path, err)
	}
	if err := rewrite.ValidateMode(m.Retrieval.Rewrite); err != nil {
		return Manifest{}, fmt.Errorf("manifest %s: %w", path, err)
	}
	if m.Retrieval.WindowTurns < 0 {
		return Manifest{}, fmt.Errorf("manifest %s: retrieval window_turns must not be negative, got %d",
			path, m.Retrieval.WindowTurns)
	}
	return m, nil
}
