package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// validLLMProviders and validEmbeddingProviders mirror the factory switches in
// internal/llm and internal/embeddings. Kept here so a bad provider fails fast
// at config load with a clear message, rather than deep inside a factory.
var (
	validLLMProviders       = map[string]bool{"claude": true, "llama": true, "openai": true, "ollama": true}
	validEmbeddingProviders = map[string]bool{"llama": true, "openai": true, "ollama": true}
)

// Validate reports the first configuration error, or nil if the config is
// internally consistent. It checks value sanity (positive sizes, overlap
// smaller than chunk size) and known provider names, so every command can fail
// fast with a clear message instead of crashing deep inside a factory.
func (c Config) Validate() error {
	if c.Database.Path == "" {
		return fmt.Errorf("config: database path must not be empty")
	}
	if c.Chunking.Size <= 0 {
		return fmt.Errorf("config: chunking size must be positive, got %d", c.Chunking.Size)
	}
	if c.Chunking.Overlap < 0 {
		return fmt.Errorf("config: chunking overlap must not be negative, got %d", c.Chunking.Overlap)
	}
	if c.Chunking.Overlap >= c.Chunking.Size {
		return fmt.Errorf(
			"config: chunking overlap (%d) must be smaller than size (%d), otherwise chunks never advance",
			c.Chunking.Overlap, c.Chunking.Size)
	}
	if c.LLM.MaxTokens <= 0 {
		return fmt.Errorf("config: llm max_tokens must be positive, got %d", c.LLM.MaxTokens)
	}
	if c.Embedding.Dimension <= 0 {
		return fmt.Errorf("config: embedding dimension must be positive, got %d", c.Embedding.Dimension)
	}
	if !validLLMProviders[c.LLM.Provider] {
		return fmt.Errorf("config: unknown llm provider %q (want claude, llama, openai, or ollama)", c.LLM.Provider)
	}
	if !validEmbeddingProviders[c.Embedding.Provider] {
		return fmt.Errorf("config: unknown embedding provider %q (want llama, openai, or ollama)", c.Embedding.Provider)
	}
	return nil
}

// Config holds all application settings.
type Config struct {
	Database   DatabaseConfig   `yaml:"database"`
	LLM        LLMConfig        `yaml:"llm"`
	Embedding  EmbeddingConfig  `yaml:"embedding"`
	Chunking   ChunkingConfig   `yaml:"chunking"`
	Preprocess PreprocessConfig `yaml:"preprocess"`
}

// PreprocessConfig controls where extracted text files are stored.
type PreprocessConfig struct {
	OutputDir string `yaml:"output_dir"`
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
			Provider:  "llama",
			Model:     "",
			MaxTokens: 4096,
			// BaseURL intentionally empty: each provider factory resolves its
			// own default (llama/openai-compatible → :8080, ollama → :11434,
			// claude → api.anthropic.com, openai → api.openai.com), so
			// switching provider doesn't silently target a stale localhost URL.
			BaseURL: "",
		},
		Embedding: EmbeddingConfig{
			Provider:  "llama",
			Model:     "",
			Dimension: 768,
			BaseURL:   "",
		},
		Chunking: ChunkingConfig{
			Size:    800,
			Overlap: 100,
		},
		Preprocess: PreprocessConfig{
			OutputDir: filepath.Join(home, ".tbuk", "extracted"),
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

	// Decode with KnownFields(true) so a typo'd or unknown key (e.g. chunk_size
	// for size, baseurl for base_url) fails loudly instead of being silently
	// dropped while the default quietly wins.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
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
  provider: llama
  model: ""
  max_tokens: 4096
  # base_url: leave empty to use the provider default
  #   llama/openai-compatible → http://localhost:8080
  #   ollama                  → http://localhost:11434
  #   claude                  → https://api.anthropic.com
  #   openai                  → https://api.openai.com
  base_url: ""

embedding:
  provider: llama
  model: ""
  dimension: 768
  # base_url: leave empty to use the provider default (see llm above)
  base_url: ""

chunking:
  size: 800
  overlap: 100

preprocess:
  output_dir: ` + filepath.Join(home, ".tbuk", "extracted") + `
`
}
