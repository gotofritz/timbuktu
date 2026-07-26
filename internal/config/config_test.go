package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/config"
)

func TestDefaults(t *testing.T) {
	cfg := config.Defaults()

	if cfg.Chunking.Size != 400 {
		t.Errorf("chunking size: want 400, got %d", cfg.Chunking.Size)
	}
	if cfg.Chunking.Overlap != 50 {
		t.Errorf("chunking overlap: want 50, got %d", cfg.Chunking.Overlap)
	}
	if cfg.LLM.Provider != "llama" {
		t.Errorf("llm.provider: want llama, got %s", cfg.LLM.Provider)
	}
	if cfg.Embedding.Provider != "llama" {
		t.Errorf("embedding.provider: want llama, got %s", cfg.Embedding.Provider)
	}
	if cfg.Embedding.Dimension != 768 {
		t.Errorf("embedding.dimension: want 768, got %d", cfg.Embedding.Dimension)
	}
	// base_url must be empty in defaults so each provider factory resolves its
	// own default — otherwise switching provider silently keeps localhost:8080.
	if cfg.LLM.BaseURL != "" {
		t.Errorf("llm.base_url: want empty (provider resolves), got %q", cfg.LLM.BaseURL)
	}
	if cfg.Embedding.BaseURL != "" {
		t.Errorf("embedding.base_url: want empty (provider resolves), got %q", cfg.Embedding.BaseURL)
	}
	// prompts.dir must default under ~/.tbuk like the db and extracted dirs, so
	// the prompt root is configurable rather than hardcoded in the ask/template
	// commands.
	if !strings.HasSuffix(cfg.Prompts.Dir, filepath.Join(".tbuk", "prompts")) {
		t.Errorf("prompts.dir: want a path ending in .tbuk/prompts, got %q", cfg.Prompts.Dir)
	}
}

func TestDefaults_rawDir(t *testing.T) {
	cfg := config.Defaults()
	// ingest.raw_dir must default under ~/.tbuk like the db, extracted and
	// prompt dirs, so the on-ingest source archive is configurable rather than
	// hardcoded in the ingester.
	if !strings.HasSuffix(cfg.Ingest.RawDir, filepath.Join(".tbuk", "raw")) {
		t.Errorf("ingest.raw_dir: want a path ending in .tbuk/raw, got %q", cfg.Ingest.RawDir)
	}
}

func TestDefaultRoot_endsInTbuk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := config.DefaultRoot(), filepath.Join(home, ".tbuk"); got != want {
		t.Errorf("DefaultRoot() = %q, want %q", got, want)
	}
}

// DefaultsForRoot must derive every data path from the given root while leaving
// the non-path defaults (providers, sizes, dimension) untouched, so --root
// relocates the whole knowledge base in one move.
func TestDefaultsForRoot_derivesPathsFromRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	cfg := config.DefaultsForRoot(root)

	paths := map[string]string{
		"database.path":         cfg.Database.Path,
		"preprocess.output_dir": cfg.Preprocess.OutputDir,
		"ingest.raw_dir":        cfg.Ingest.RawDir,
		"prompts.dir":           cfg.Prompts.Dir,
	}
	for name, got := range paths {
		if !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a path under root %q", name, got, root)
		}
	}
	if cfg.Database.Path != filepath.Join(root, "tbuk.sqlite") {
		t.Errorf("database.path = %q, want %q", cfg.Database.Path, filepath.Join(root, "tbuk.sqlite"))
	}
	// Non-path defaults must match Defaults() regardless of root.
	if cfg.Chunking.Size != 400 || cfg.Embedding.Dimension != 768 || cfg.LLM.Provider != "llama" {
		t.Errorf("non-path defaults drifted under a custom root: %+v", cfg)
	}
}

// ResolvePaths rebases relative data paths onto root while leaving absolute
// paths and empty values (which disable a component) untouched — this is what
// lets a config hold paths relative to its root, or pin a single part with an
// absolute path, so the pipeline's parts can live in different places (#96).
func TestResolvePaths_rebasesRelativeLeavesAbsoluteAndEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	abs := filepath.Join(t.TempDir(), "elsewhere", "big.sqlite")
	cfg := config.Config{
		Database:   config.DatabaseConfig{Path: "db/kb.sqlite"}, // relative → under root
		Preprocess: config.PreprocessConfig{OutputDir: abs},     // absolute → untouched
		Ingest:     config.IngestConfig{RawDir: ""},             // empty → disabled, untouched
		Prompts:    config.PromptsConfig{Dir: "prompts"},        // relative → under root
	}

	got := cfg.ResolvePaths(root)

	if want := filepath.Join(root, "db", "kb.sqlite"); got.Database.Path != want {
		t.Errorf("database.path = %q, want %q", got.Database.Path, want)
	}
	if got.Preprocess.OutputDir != abs {
		t.Errorf("preprocess.output_dir = %q, want unchanged absolute %q", got.Preprocess.OutputDir, abs)
	}
	if got.Ingest.RawDir != "" {
		t.Errorf("ingest.raw_dir = %q, want empty (disabled) preserved", got.Ingest.RawDir)
	}
	if want := filepath.Join(root, "prompts"); got.Prompts.Dir != want {
		t.Errorf("prompts.dir = %q, want %q", got.Prompts.Dir, want)
	}
}

// A config file may hold paths relative to the data root; LoadForRoot resolves
// them under root so each component lands beside the config rather than beside
// the current working directory (#96).
func TestLoadForRoot_relativeFilePathsResolveUnderRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "database:\n  path: data/kb.sqlite\ningest:\n  raw_dir: archive\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadForRoot(path, root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if want := filepath.Join(root, "data", "kb.sqlite"); cfg.Database.Path != want {
		t.Errorf("database.path = %q, want %q", cfg.Database.Path, want)
	}
	if want := filepath.Join(root, "archive"); cfg.Ingest.RawDir != want {
		t.Errorf("ingest.raw_dir = %q, want %q", cfg.Ingest.RawDir, want)
	}
}

func TestDefaultPathForRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	if got, want := config.DefaultPathForRoot(root), filepath.Join(root, "config.yaml"); got != want {
		t.Errorf("DefaultPathForRoot(%q) = %q, want %q", root, got, want)
	}
}

// A --root run with no config file present must still resolve every path under
// root, not under ~/.tbuk.
func TestLoadForRoot_missingFileUsesRootDefaults(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	cfg, err := config.LoadForRoot(config.DefaultPathForRoot(root), root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.Database.Path != filepath.Join(root, "tbuk.sqlite") {
		t.Errorf("database.path = %q, want under root %q", cfg.Database.Path, root)
	}
	if cfg.Prompts.Dir != filepath.Join(root, "prompts") {
		t.Errorf("prompts.dir = %q, want under root %q", cfg.Prompts.Dir, root)
	}
}

// Fields present in the config file win; absent ones fall back to root-derived
// defaults rather than ~/.tbuk.
func TestLoadForRoot_fileOverridesRootDefaults(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("database:\n  path: /explicit/db.sqlite\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForRoot(path, root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.Database.Path != "/explicit/db.sqlite" {
		t.Errorf("database.path = %q, want the file's explicit value", cfg.Database.Path)
	}
	if cfg.Ingest.RawDir != filepath.Join(root, "raw") {
		t.Errorf("ingest.raw_dir = %q, want root-derived default under %q", cfg.Ingest.RawDir, root)
	}
}

// DefaultYAMLForRoot must emit root-derived paths and round-trip back to
// DefaultsForRoot exactly — the same anti-drift guard as the no-root variant.
func TestDefaultYAMLForRoot_roundTrips(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	yamlStr, err := config.DefaultYAMLForRoot(root)
	if err != nil {
		t.Fatalf("DefaultYAMLForRoot: %v", err)
	}
	if !strings.Contains(yamlStr, root) {
		t.Errorf("DefaultYAMLForRoot output does not mention root %q:\n%s", root, yamlStr)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yamlStr), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadForRoot(path, root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if want := config.DefaultsForRoot(root); !reflect.DeepEqual(loaded, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", loaded, want)
	}
}

// init must ship a config whose data paths are relative to the root (./raw,
// ./prompts, ./tbuk.sqlite, ./extracted) so the file is portable — relocating
// the root moves every component with it (#96).
func TestDefaultYAMLForRoot_emitsRelativePaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "kb")
	yamlStr, err := config.DefaultYAMLForRoot(root)
	if err != nil {
		t.Fatalf("DefaultYAMLForRoot: %v", err)
	}
	for _, want := range []string{"./tbuk.sqlite", "./extracted", "./raw", "./prompts"} {
		if !strings.Contains(yamlStr, want) {
			t.Errorf("default config missing relative path %q:\n%s", want, yamlStr)
		}
	}
	if abs := filepath.Join(root, "tbuk.sqlite"); strings.Contains(yamlStr, abs) {
		t.Errorf("default config should not hard-code the absolute path %q", abs)
	}
}

// FillMissingDefaults adds default keys absent from an existing config while
// preserving the user's own values, so `tbuk init` can complete a partial
// config in place (#96).
func TestFillMissingDefaults_addsMissingPreservesExisting(t *testing.T) {
	existing := []byte("chunking:\n  size: 999\ningest:\n  embed_concurrency: 2\n")

	merged, added, err := config.FillMissingDefaults(existing)
	if err != nil {
		t.Fatalf("FillMissingDefaults: %v", err)
	}

	// A previously-missing whole section and a missing leaf are both reported.
	have := map[string]bool{}
	for _, a := range added {
		have[a] = true
	}
	if !have["database"] || !have["ingest.raw_dir"] {
		t.Errorf("added = %v, want it to include \"database\" and \"ingest.raw_dir\"", added)
	}

	// The merged config must load with user values preserved and defaults filled.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "kb")
	cfg, err := config.LoadForRoot(path, root)
	if err != nil {
		t.Fatalf("LoadForRoot(merged): %v", err)
	}
	if cfg.Chunking.Size != 999 {
		t.Errorf("chunking.size = %d, want preserved 999", cfg.Chunking.Size)
	}
	if cfg.Ingest.EmbedConcurrency != 2 {
		t.Errorf("ingest.embed_concurrency = %d, want preserved 2", cfg.Ingest.EmbedConcurrency)
	}
	if want := filepath.Join(root, "raw"); cfg.Ingest.RawDir != want {
		t.Errorf("ingest.raw_dir = %q, want filled default %q", cfg.Ingest.RawDir, want)
	}
	if cfg.LLM.Provider != "llama" {
		t.Errorf("llm.provider = %q, want filled default llama", cfg.LLM.Provider)
	}
}

// A config already carrying every default key is reported as complete (no keys
// added) so init can leave it untouched.
func TestFillMissingDefaults_completeConfigAddsNothing(t *testing.T) {
	full, err := config.DefaultYAML()
	if err != nil {
		t.Fatalf("DefaultYAML: %v", err)
	}
	_, added, err := config.FillMissingDefaults([]byte(full))
	if err != nil {
		t.Fatalf("FillMissingDefaults: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("added = %v, want none for a complete config", added)
	}
}

func TestLoad_missingFile(t *testing.T) {
	cfg, err := config.Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg.Chunking.Size != 400 {
		t.Errorf("want default size 400, got %d", cfg.Chunking.Size)
	}
}

func TestLoad_partialYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	err := os.WriteFile(path, []byte("chunking:\n  size: 512\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Chunking.Size != 512 {
		t.Errorf("want 512, got %d", cfg.Chunking.Size)
	}
	// unset fields should keep defaults
	if cfg.Chunking.Overlap != 50 {
		t.Errorf("overlap should keep default 50, got %d", cfg.Chunking.Overlap)
	}
	if cfg.LLM.Provider != "llama" {
		t.Errorf("llm.provider should keep default, got %s", cfg.LLM.Provider)
	}
}

// A typo'd or unknown config key must fail loudly rather than being silently
// dropped so the default quietly wins (P1-18).
func TestLoad_unknownKeyRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// chunk_size is a typo for chunking.size — must not be silently ignored.
	if err := os.WriteFile(path, []byte("chunking:\n  chunk_size: 512\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown config key, got nil")
	}
	if !strings.Contains(err.Error(), "chunk_size") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestLoad_directoryPath(t *testing.T) {
	dir := t.TempDir()
	_, err := config.Load(dir) // passing a directory, not a file
	if err == nil {
		t.Fatal("expected error when path is a directory")
	}
}

func TestLoad_badYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	err := os.WriteFile(path, []byte("not: valid: yaml: ["), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	_, err = config.Load(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestDefaultPath_containsTbuk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := config.DefaultPath()
	if filepath.Base(path) != "config.yaml" {
		t.Errorf("want config.yaml basename, got %s", filepath.Base(path))
	}
	if filepath.Dir(filepath.Dir(path)) != home {
		t.Errorf("want path inside HOME/.tbuk/, got %s", path)
	}
}

func TestDefaultYAML_isValidYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	yamlStr, err := config.DefaultYAML()
	if err != nil {
		t.Fatalf("DefaultYAML: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yamlStr), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("DefaultYAML not valid YAML: %v", err)
	}
	if cfg.Chunking.Size != 400 {
		t.Errorf("want default size 400, got %d", cfg.Chunking.Size)
	}
}

// TestDefaultYAML_roundTripsDefaults is the anti-drift guard: the emitted YAML,
// loaded back, must reproduce Defaults() exactly. Because DefaultYAML now
// marshals Defaults() instead of hand-building the string, the two can no
// longer diverge — a new default field is covered automatically.
func TestDefaultYAML_roundTripsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	yamlStr, err := config.DefaultYAML()
	if err != nil {
		t.Fatalf("DefaultYAML: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yamlStr), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := config.Defaults(); !reflect.DeepEqual(loaded, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", loaded, want)
	}
}

// TestDefaultYAML_keepsComments guards the explanatory comments so switching to
// a marshalled node tree does not silently drop the base_url / concurrency help.
func TestDefaultYAML_keepsComments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	yamlStr, err := config.DefaultYAML()
	if err != nil {
		t.Fatalf("DefaultYAML: %v", err)
	}
	for _, want := range []string{"provider default", "embed_concurrency", "1 = fully serial"} {
		if !strings.Contains(yamlStr, want) {
			t.Errorf("DefaultYAML missing comment %q", want)
		}
	}
}

func TestLoad_fullYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `database:
  path: /tmp/test.sqlite
llm:
  provider: claude
  model: claude-haiku-4-5
  max_tokens: 2048
embedding:
  provider: openai
  model: text-embedding-3-small
  dimension: 1536
chunking:
  size: 600
  overlap: 80
`
	err := os.WriteFile(path, []byte(content), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Database.Path != "/tmp/test.sqlite" {
		t.Errorf("db path: want /tmp/test.sqlite, got %s", cfg.Database.Path)
	}
	if cfg.LLM.Provider != "claude" {
		t.Errorf("llm.provider: want claude, got %s", cfg.LLM.Provider)
	}
	if cfg.LLM.MaxTokens != 2048 {
		t.Errorf("llm.max_tokens: want 2048, got %d", cfg.LLM.MaxTokens)
	}
	if cfg.Embedding.Dimension != 1536 {
		t.Errorf("embedding.dimension: want 1536, got %d", cfg.Embedding.Dimension)
	}
	if cfg.Chunking.Size != 600 {
		t.Errorf("chunking.size: want 600, got %d", cfg.Chunking.Size)
	}
}

func TestConfig_Validate(t *testing.T) {
	base := config.Defaults()

	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string // substring; "" means expect no error
	}{
		{"defaults valid", func(*config.Config) {}, ""},
		{"zero chunk size", func(c *config.Config) { c.Chunking.Size = 0 }, "size"},
		{"negative chunk size", func(c *config.Config) { c.Chunking.Size = -1 }, "size"},
		{"negative overlap", func(c *config.Config) { c.Chunking.Overlap = -5 }, "overlap"},
		{"overlap equals size", func(c *config.Config) { c.Chunking.Size = 200; c.Chunking.Overlap = 200 }, "overlap"},
		{"overlap exceeds size", func(c *config.Config) { c.Chunking.Size = 100; c.Chunking.Overlap = 150 }, "overlap"},
		{"zero max_tokens", func(c *config.Config) { c.LLM.MaxTokens = 0 }, "max_tokens"},
		{"zero dimension", func(c *config.Config) { c.Embedding.Dimension = 0 }, "dimension"},
		{"unknown llm provider", func(c *config.Config) { c.LLM.Provider = "gpt5" }, "llm provider"},
		{"unknown embedding provider", func(c *config.Config) { c.Embedding.Provider = "word2vec" }, "embedding provider"},
		{"claude not valid embedder", func(c *config.Config) { c.Embedding.Provider = "claude" }, "embedding provider"},
		{"empty db path", func(c *config.Config) { c.Database.Path = "" }, "database"},
		{"zero embed concurrency", func(c *config.Config) { c.Ingest.EmbedConcurrency = 0 }, "embed_concurrency"},
		{"negative embed concurrency", func(c *config.Config) { c.Ingest.EmbedConcurrency = -2 }, "embed_concurrency"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("Validate() = nil, want error mentioning %q", tc.wantErr)
			case tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("Validate() = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateKeyedBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https remote ok", "https://api.openai.com", false},
		{"https with port ok", "https://example.com:8443", false},
		{"http loopback localhost ok", "http://localhost:8080", false},
		{"http loopback 127.0.0.1 ok", "http://127.0.0.1:1234", false},
		{"http loopback ipv6 ok", "http://[::1]:8080", false},
		{"http remote host rejected", "http://api.example.com", true},
		{"http remote ip rejected", "http://10.0.0.5:8080", true},
		{"empty rejected", "", true},
		{"malformed rejected", "://nope", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateKeyedBaseURL(tc.url)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateKeyedBaseURL(%q) = nil, want error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateKeyedBaseURL(%q) = %v, want nil", tc.url, err)
			}
		})
	}
}

func TestExportYAML_commentsOutDataFolderPaths(t *testing.T) {
	cfg := config.DefaultsForRoot("/home/alice/.tbuk")
	out, err := config.ExportYAML(cfg)
	if err != nil {
		t.Fatalf("ExportYAML: %v", err)
	}

	// The machine-specific path keys must be commented out so `tbuk import`
	// re-homes each component under the target root instead of pinning it to
	// the exporting machine's absolute paths.
	for _, frag := range []string{
		"# path: /home/alice/.tbuk/tbuk.sqlite",
		"# output_dir: /home/alice/.tbuk/extracted",
		"# raw_dir: /home/alice/.tbuk/raw",
		"# dir: /home/alice/.tbuk/prompts",
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("exported config missing commented line %q\n---\n%s", frag, out)
		}
	}
}

func TestExportYAML_keepsPortableKeysActive(t *testing.T) {
	cfg := config.DefaultsForRoot("/root")
	out, err := config.ExportYAML(cfg)
	if err != nil {
		t.Fatalf("ExportYAML: %v", err)
	}

	// Provider/model/chunking settings are portable and must stay active
	// (uncommented) so they survive an import.
	for _, frag := range []string{"provider: llama", "size: 400", "overlap: 50", "dimension: 768"} {
		if !strings.Contains(out, frag) {
			t.Errorf("exported config should keep %q active, got:\n%s", frag, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "provider: llama") && strings.Contains(line, "#") {
			t.Errorf("portable line unexpectedly commented: %q", line)
		}
		if strings.Contains(line, "embed_concurrency:") && strings.Contains(line, "#") {
			t.Errorf("embed_concurrency should stay active, got: %q", line)
		}
	}
}

func TestExportYAML_roundTripReHomesUnderTargetRoot(t *testing.T) {
	src := config.DefaultsForRoot("/home/alice/.tbuk")
	src.LLM.Provider = "openai" // a non-default portable setting
	src.Chunking.Size = 512

	out, err := config.ExportYAML(src)
	if err != nil {
		t.Fatalf("ExportYAML: %v", err)
	}

	// Simulate importing into a different root: write the exported config and
	// load it against the target root. Commented paths must fall back to the
	// target root's defaults; portable settings must survive.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "bob-kb")
	loaded, err := config.LoadForRoot(path, target)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}

	checks := []struct {
		name, got, want string
	}{
		{"db path", loaded.Database.Path, filepath.Join(target, "tbuk.sqlite")},
		{"extracted dir", loaded.Preprocess.OutputDir, filepath.Join(target, "extracted")},
		{"raw dir", loaded.Ingest.RawDir, filepath.Join(target, "raw")},
		{"prompts dir", loaded.Prompts.Dir, filepath.Join(target, "prompts")},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: want re-homed %q, got %q", c.name, c.want, c.got)
		}
	}
	if loaded.LLM.Provider != "openai" {
		t.Errorf("llm.provider: want openai (portable), got %q", loaded.LLM.Provider)
	}
	if loaded.Chunking.Size != 512 {
		t.Errorf("chunking.size: want 512 (portable), got %d", loaded.Chunking.Size)
	}
}
