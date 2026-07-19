package prompts

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// RetrievalConfig controls how many chunks to fetch for this template.
type RetrievalConfig struct {
	TopK      int `yaml:"top_k"`
	MaxTokens int `yaml:"max_tokens"`
}

// VariableDefault holds a default value for a template variable.
type VariableDefault struct {
	Default string `yaml:"default"`
}

// Manifest is the parsed manifest.yaml for a prompt template.
type Manifest struct {
	Name        string                     `yaml:"name"`
	Description string                     `yaml:"description"`
	Model       string                     `yaml:"model"`
	Temperature *float64                   `yaml:"temperature"` // nil = unset (provider default)
	MaxTokens   int                        `yaml:"max_tokens"`
	Retrieval   RetrievalConfig            `yaml:"retrieval"`
	Variables   map[string]VariableDefault `yaml:"variables"`
	Output      string                     `yaml:"output"`
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
	return m, nil
}
