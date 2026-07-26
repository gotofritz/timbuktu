package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	// RawDir is where ingest archives a copy of each source document
	// (content-addressed as <sha256><ext>). Empty disables archiving; the
	// per-ingest --no-raw flag suppresses it for a single run.
	RawDir string `yaml:"raw_dir"`
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

// DefaultRoot returns the default data-root directory (~/.tbuk) — the base
// under which the database, extracted-text store, raw archive, prompt templates
// and config file all live. It falls back to a relative ".tbuk" when the home
// directory cannot be resolved. The --root flag overrides it per invocation.
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tbuk"
	}
	return filepath.Join(home, ".tbuk")
}

// DefaultPath returns the default config file location under DefaultRoot().
func DefaultPath() string {
	return DefaultPathForRoot(DefaultRoot())
}

// DefaultPathForRoot returns the config file location under root.
func DefaultPathForRoot(root string) string {
	return filepath.Join(root, "config.yaml")
}

// Defaults returns a Config with paths rooted at DefaultRoot().
func Defaults() Config {
	return DefaultsForRoot(DefaultRoot())
}

// DefaultsForRoot returns a Config whose data paths (database, extracted-text
// store, raw archive, prompt templates) are all derived from root. The non-path
// defaults — providers, chunk sizes, dimension — are independent of root, so
// --root relocates the whole knowledge base without changing behaviour.
func DefaultsForRoot(root string) Config {
	return Config{
		Database: DatabaseConfig{
			Path: filepath.Join(root, "tbuk.sqlite"),
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
			// 400 keeps chunks safely below the llama.cpp default physical batch
			// size of 512 tokens, accounting for BPE counts exceeding the len/4
			// heuristic used by the chunker.
			Size:    400,
			Overlap: 50,
		},
		Preprocess: PreprocessConfig{
			OutputDir: filepath.Join(root, "extracted"),
		},
		Ingest: IngestConfig{
			EmbedConcurrency: 4,
			RawDir:           filepath.Join(root, "raw"),
		},
		Prompts: PromptsConfig{
			Dir: filepath.Join(root, "prompts"),
		},
	}
}

// Load reads config from path, falling back to DefaultRoot()-derived defaults
// for missing fields. If the file does not exist, defaults are returned without
// error.
func Load(path string) (Config, error) {
	return LoadForRoot(path, DefaultRoot())
}

// LoadForRoot reads config from path, seeding missing fields from
// DefaultsForRoot(root). This lets a --root invocation resolve every data path
// under root even when the config file is absent or only partially populated.
// If the file does not exist, the root-derived defaults are returned without
// error.
func LoadForRoot(path, root string) (Config, error) {
	cfg := DefaultsForRoot(root)

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

// DefaultYAML returns the default config (rooted at DefaultRoot()) serialised to
// commented YAML.
func DefaultYAML() (string, error) {
	return DefaultYAMLForRoot(DefaultRoot())
}

// DefaultYAMLForRoot returns the default config for root serialised to YAML with
// explanatory comments. Values are marshalled from DefaultsForRoot(root) — the
// single source of truth — so the two can never drift; a new default field
// appears here automatically. Comments are attached to the encoded node tree
// rather than typed into a hand-built string.
func DefaultYAMLForRoot(root string) (string, error) {
	var node yaml.Node
	if err := node.Encode(DefaultsForRoot(root)); err != nil {
		return "", fmt.Errorf("config: encode default yaml: %w", err)
	}

	mapKey(mapValue(&node, "llm"), "base_url").HeadComment = "base_url: leave empty to use the provider default\n" +
		"  llama/openai-compatible → http://localhost:8080\n" +
		"  ollama                  → http://localhost:11434\n" +
		"  claude                  → https://api.anthropic.com\n" +
		"  openai                  → https://api.openai.com"

	mapKey(mapValue(&node, "embedding"), "base_url").HeadComment =
		"base_url: leave empty to use the provider default (see llm above)"

	mapKey(mapValue(&node, "ingest"), "embed_concurrency").HeadComment =
		"max embed batches processed concurrently within one file (keep low to\n" +
			"respect provider rate limits; 1 = fully serial)"

	mapKey(mapValue(&node, "ingest"), "raw_dir").HeadComment =
		"raw_dir: archive a copy of each ingested source here (empty to disable;\n" +
			"ingest --no-raw skips it for one run)"

	mapKey(mapValue(&node, "prompts"), "dir").HeadComment =
		"dir: root directory holding prompt template folders"

	out, err := yaml.Marshal(&node)
	if err != nil {
		return "", fmt.Errorf("config: marshal default yaml: %w", err)
	}
	return string(out), nil
}

// exportPathKeys maps each config section to the single data-folder key that
// `tbuk export` comments out. Commenting these (rather than dropping them) keeps
// the original location visible for reference while letting an import re-home
// the component under its own root.
var exportPathKeys = map[string]string{
	"database":   "path",
	"preprocess": "output_dir",
	"ingest":     "raw_dir",
	"prompts":    "dir",
}

// ExportYAML serialises cfg to YAML with the data-folder path keys commented
// out (database.path, preprocess.output_dir, ingest.raw_dir, prompts.dir). An
// import then re-homes those components under the target root instead of the
// exporting machine's absolute paths, while portable settings (providers,
// models, chunk sizes) are left active. When commenting a key empties its
// section, the section header is commented too, so the key is absent on reload
// and the root-derived default survives rather than being overwritten by a
// zero value.
func ExportYAML(cfg Config) (string, error) {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("config: marshal export yaml: %w", err)
	}
	header := "# tbuk knowledge-base export.\n" +
		"# Data-folder paths below are commented out so `tbuk import` places each\n" +
		"# component under the target root (--root, else ~/.tbuk). Uncomment and\n" +
		"# edit a path to pin that component to a fixed location on import.\n"
	return header + commentPathKeys(string(out)), nil
}

// commentPathKeys comments out the data-folder path line in each section named
// in exportPathKeys, and the section header itself when no active key remains.
func commentPathKeys(yamlText string) string {
	lines := strings.Split(yamlText, "\n")

	type section struct {
		name          string
		header, start int
		end           int // exclusive
	}
	var sections []section
	for i, line := range lines {
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		if n := len(sections); n > 0 {
			sections[n-1].end = i
		}
		name := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		sections = append(sections, section{name: name, header: i, start: i + 1, end: len(lines)})
	}

	for _, s := range sections {
		key, ok := exportPathKeys[s.name]
		if !ok {
			continue
		}
		for i := s.start; i < s.end; i++ {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[0]) == key {
				lines[i] = commentLine(lines[i])
			}
		}
		if !hasActiveLine(lines[s.start:s.end]) {
			lines[s.header] = commentLine(lines[s.header])
		}
	}
	return strings.Join(lines, "\n")
}

// commentLine prefixes line's content with "# ", preserving its indentation.
func commentLine(line string) string {
	body := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(body)]
	return indent + "# " + body
}

// hasActiveLine reports whether any line is neither blank nor already a comment.
func hasActiveLine(lines []string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return true
		}
	}
	return false
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
