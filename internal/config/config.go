package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds all application settings.
type Config struct {
	Database  DatabaseConfig  `yaml:"database"`
	LLM       LLMConfig       `yaml:"llm"`
	Embedding EmbeddingConfig `yaml:"embedding"`
	Chunking  ChunkingConfig  `yaml:"chunking"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type LLMConfig struct {
	Provider  string `yaml:"provider"`
	Model     string `yaml:"model"`
	MaxTokens int    `yaml:"max_tokens"`
	BaseURL   string `yaml:"base_url"`
}

type EmbeddingConfig struct {
	Provider  string `yaml:"provider"`
	Model     string `yaml:"model"`
	Dimension int    `yaml:"dimension"`
	BaseURL   string `yaml:"base_url"`
}

type ChunkingConfig struct {
	Size    int `yaml:"size"`
	Overlap int `yaml:"overlap"`
}

// DefaultPath returns the default config file location.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tbuk/config.yaml"
	}
	return filepath.Join(home, ".tbuk", "config.yaml")
}

// Defaults returns a Config with sensible default values.
func Defaults() Config {
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".tbuk", "tbuk.sqlite")
	return Config{
		Database: DatabaseConfig{
			Path: dbPath,
		},
		LLM: LLMConfig{
			Provider:  "ollama",
			Model:     "",
			MaxTokens: 4096,
			BaseURL:   "http://localhost:11434",
		},
		Embedding: EmbeddingConfig{
			Provider:  "ollama",
			Model:     "nomic-embed-text",
			Dimension: 768,
			BaseURL:   "http://localhost:11434",
		},
		Chunking: ChunkingConfig{
			Size:    800,
			Overlap: 100,
		},
	}
}

// Load reads config from path, falling back to defaults for missing fields.
// If the file does not exist, defaults are returned without error.
func Load(path string) (Config, error) {
	cfg := Defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, err
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// DefaultYAML returns a YAML representation of the default config.
func DefaultYAML() string {
	home, _ := os.UserHomeDir()
	return `database:
  path: ` + filepath.Join(home, ".tbuk", "tbuk.sqlite") + `

llm:
  provider: ollama
  model: ""
  max_tokens: 4096
  base_url: http://localhost:11434

embedding:
  provider: ollama
  model: nomic-embed-text
  dimension: 768
  base_url: http://localhost:11434

chunking:
  size: 800
  overlap: 100
`
}
