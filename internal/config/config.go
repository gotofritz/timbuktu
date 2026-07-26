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

// relativeDefaults returns the default Config with every data path expressed
// relative to the data root (a leading "./"). It is the single source of truth
// for the defaults: DefaultsForRoot resolves these onto a concrete root, and
// DefaultYAMLForRoot serialises them verbatim so the config `tbuk init` writes
// is portable — relocating the root moves every component with it.
func relativeDefaults() Config {
	return Config{
		Database: DatabaseConfig{
			Path: "./tbuk.sqlite",
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
			OutputDir: "./extracted",
		},
		Ingest: IngestConfig{
			EmbedConcurrency: 4,
			RawDir:           "./raw",
		},
		Prompts: PromptsConfig{
			Dir: "./prompts",
		},
	}
}

// DefaultsForRoot returns a Config whose data paths (database, extracted-text
// store, raw archive, prompt templates) are all resolved under root. The
// non-path defaults — providers, chunk sizes, dimension — are independent of
// root, so --root relocates the whole knowledge base without changing behaviour.
func DefaultsForRoot(root string) Config {
	return relativeDefaults().ResolvePaths(root)
}

// ResolvePaths rebases each relative data path (database, extracted-text store,
// raw archive, prompt templates) onto root, leaving absolute paths and empty
// values untouched. Empty is preserved because it disables a component (an empty
// ingest.raw_dir turns off the source archive), and an absolute path pins that
// part to a fixed location regardless of root. This lets a single config keep
// its parts in different places — most relative to a portable root, some pinned
// absolutely (e.g. the raw archive on a larger disk).
func (c Config) ResolvePaths(root string) Config {
	c.Database.Path = resolveUnderRoot(c.Database.Path, root)
	c.Preprocess.OutputDir = resolveUnderRoot(c.Preprocess.OutputDir, root)
	c.Ingest.RawDir = resolveUnderRoot(c.Ingest.RawDir, root)
	c.Prompts.Dir = resolveUnderRoot(c.Prompts.Dir, root)
	return c
}

// resolveUnderRoot joins a relative path onto root; empty and absolute paths are
// returned unchanged.
func resolveUnderRoot(path, root string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
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

	// Resolve any relative data paths from the file against root, so a config can
	// hold portable relative paths (or absolute ones to pin a part elsewhere).
	// Seeded defaults are already absolute, so this is a no-op on them.
	return cfg.ResolvePaths(root), nil
}

// DefaultYAML returns the default config (rooted at DefaultRoot()) serialised to
// commented YAML.
func DefaultYAML() (string, error) {
	return DefaultYAMLForRoot(DefaultRoot())
}

// defaultConfigNode builds the YAML mapping node for the default config, with
// its explanatory field comments but no document header. It is the single
// source of truth shared by DefaultYAMLForRoot (which tops it with a header) and
// FillMissingDefaults (which copies missing keys — and their comments — from it).
// Values come from relativeDefaults(), so a new default field appears in both
// automatically. A fresh tree is returned each call, since callers mutate it.
func defaultConfigNode() (*yaml.Node, error) {
	var node yaml.Node
	if err := node.Encode(relativeDefaults()); err != nil {
		return nil, fmt.Errorf("config: encode default yaml: %w", err)
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

	return &node, nil
}

// DefaultYAMLForRoot returns the default config serialised to YAML with
// explanatory comments and a header naming root. Data paths are written relative
// to the root (./tbuk.sqlite, ./raw, …) so the file is portable — relocating the
// root moves every component with it. It round-trips back to DefaultsForRoot(root)
// via LoadForRoot, which resolves the relative paths under root.
func DefaultYAMLForRoot(root string) (string, error) {
	node, err := defaultConfigNode()
	if err != nil {
		return "", err
	}

	node.HeadComment = fmt.Sprintf("tbuk configuration — data root: %s\n"+
		"Relative paths below (./tbuk.sqlite, ./raw, …) resolve under this root;\n"+
		"give an absolute path to pin a component elsewhere.", root)

	out, err := yaml.Marshal(node)
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

// FillMissingDefaults returns existing config YAML with any default keys it
// lacks added in, and the list of keys added (dotted paths, e.g.
// "ingest.raw_dir"; a whole missing section is reported by its section name).
// The user's own keys, values, and comments are preserved — only absent
// defaults are appended, carrying their explanatory comments. An empty `added`
// means the config already carries every default, so callers can leave the file
// untouched. This lets `tbuk init` complete a partial config in place.
func FillMissingDefaults(existing []byte) ([]byte, []string, error) {
	defNode, err := defaultConfigNode()
	if err != nil {
		return nil, nil, err
	}

	var exDoc yaml.Node
	if err := yaml.Unmarshal(existing, &exDoc); err != nil {
		return nil, nil, fmt.Errorf("config: parse existing config: %w", err)
	}

	// A blank or comment-only file decodes to a zero node; treat it as an empty
	// mapping so every default is added.
	exMap := documentMapping(&exDoc)
	if exMap == nil {
		return nil, nil, fmt.Errorf("config: existing config is not a mapping")
	}

	var added []string
	mergeMissing(exMap, defNode, "", &added)

	out, err := yaml.Marshal(&exDoc)
	if err != nil {
		return nil, nil, fmt.Errorf("config: marshal merged config: %w", err)
	}
	return out, added, nil
}

// documentMapping returns the mapping node at the root of a decoded document,
// synthesising an empty one (rewiring doc) when the document is blank. It
// returns nil if the document's root is a non-mapping value.
func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 || len(doc.Content) == 0 {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		*doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{m}}
		return m
	}
	if doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0]
	}
	return nil
}

// mergeMissing appends every key present in src but absent from dst (both YAML
// mapping nodes), recording each addition's dotted path. When a key exists in
// both and both values are mappings, it recurses so a partially-populated
// section gains only its missing leaves; a scalar already present is left as the
// user set it.
func mergeMissing(dst, src *yaml.Node, prefix string, added *[]string) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, val := src.Content[i], src.Content[i+1]
		path := key.Value
		if prefix != "" {
			path = prefix + "." + key.Value
		}
		dstVal := mapValue(dst, key.Value)
		switch {
		case dstVal == nil:
			dst.Content = append(dst.Content, key, val)
			*added = append(*added, path)
		case dstVal.Kind == yaml.MappingNode && val.Kind == yaml.MappingNode:
			mergeMissing(dstVal, val, path, added)
		}
	}
}
