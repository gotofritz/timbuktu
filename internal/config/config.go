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
	if c.Ingest.EmbedConcurrency < 1 {
		return fmt.Errorf("config: ingest embed_concurrency must be at least 1, got %d", c.Ingest.EmbedConcurrency)
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
	Ingest     IngestConfig     `yaml:"ingest"`
	Prompts    PromptsConfig    `yaml:"prompts"`
}

// PromptsConfig controls where prompt templates live.
type PromptsConfig struct {
	Dir string `yaml:"dir"`
}

// IngestConfig controls ingestion throughput.
type IngestConfig struct {
	// EmbedConcurrency bounds how many embed batches run concurrently within a
	// single file. 1 = fully serial. Kept low by default so it composes with
	// provider rate limits.
	EmbedConcurrency int `yaml:"embed_concurrency"`
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
		Ingest: IngestConfig{
			EmbedConcurrency: 4,
		},
		Prompts: PromptsConfig{
			Dir: filepath.Join(home, ".tbuk", "prompts"),
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

// DefaultYAML returns the default config serialised to YAML with explanatory
// comments. Values are marshalled from Defaults() — the single source of truth —
// so the two can never drift; a new default field appears here automatically.
// Comments are attached to the encoded node tree rather than typed into a
// hand-built string.
func DefaultYAML() (string, error) {
	var root yaml.Node
	if err := root.Encode(Defaults()); err != nil {
		return "", fmt.Errorf("config: encode default yaml: %w", err)
	}

	mapKey(mapValue(&root, "llm"), "base_url").HeadComment = "base_url: leave empty to use the provider default\n" +
		"  llama/openai-compatible → http://localhost:8080\n" +
		"  ollama                  → http://localhost:11434\n" +
		"  claude                  → https://api.anthropic.com\n" +
		"  openai                  → https://api.openai.com"

	mapKey(mapValue(&root, "embedding"), "base_url").HeadComment =
		"base_url: leave empty to use the provider default (see llm above)"

	mapKey(mapValue(&root, "ingest"), "embed_concurrency").HeadComment =
		"max embed batches processed concurrently within one file (keep low to\n" +
			"respect provider rate limits; 1 = fully serial)"

	mapKey(mapValue(&root, "prompts"), "dir").HeadComment =
		"dir: root directory holding prompt template folders"

	out, err := yaml.Marshal(&root)
	if err != nil {
		return "", fmt.Errorf("config: marshal default yaml: %w", err)
	}
	return string(out), nil
}

// mapValue returns the value node for key in a YAML mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mapKey returns the key node for key in a YAML mapping node, or nil. Comments
// attached to the key node render on the line above it.
func mapKey(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i]
		}
	}
	return nil
}
