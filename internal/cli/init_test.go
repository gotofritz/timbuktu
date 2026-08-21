package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/prompts"
)

func runCLI(args ...string) error {
	cmd := cli.New()
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestInitCommand_createsDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	expected := []string{
		filepath.Join(home, ".tbuk"),
		filepath.Join(home, ".tbuk", "prompts", "qa"),
	}
	for _, dir := range expected {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("expected dir to exist: %s", dir)
		}
	}
}

// init --root <dir> must scaffold under that directory (config + templates with
// root-derived paths) and leave the default ~/.tbuk untouched.
func TestInitCommand_rootFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(t.TempDir(), "kb")
	if err := runCLI("--root", root, "init"); err != nil {
		t.Fatalf("init --root failed: %v", err)
	}

	for _, p := range []string{
		filepath.Join(root, "config.yaml"),
		filepath.Join(root, "prompts", "qa", "manifest.yaml"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s under custom root: %v", p, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The written config uses paths relative to the root (portable) and names
	// the root in its header comment, not an absolute database path.
	if !strings.Contains(string(data), "./tbuk.sqlite") {
		t.Errorf("written config does not use a relative database path:\n%s", data)
	}
	if !strings.Contains(string(data), root) {
		t.Errorf("written config header does not mention the data root %q:\n%s", root, data)
	}
	if abs := filepath.Join(root, "tbuk.sqlite"); strings.Contains(string(data), abs) {
		t.Errorf("written config should not hard-code the absolute path %q:\n%s", abs, data)
	}

	if _, err := os.Stat(filepath.Join(home, ".tbuk")); !os.IsNotExist(err) {
		t.Errorf("init --root must not create ~/.tbuk, stat err = %v", err)
	}
}

func TestInitCommand_dirsAndConfigPrivatePerms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	dir := filepath.Join(home, ".tbuk")
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf(".tbuk perms = %o, want 700", di.Mode().Perm())
	}

	fi, err := os.Stat(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config perms = %o, want 600", fi.Mode().Perm())
	}
}

func TestInitCommand_writesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if len(data) == 0 {
		t.Error("config file is empty")
	}
}

// init on an existing config must add default keys the file is missing while
// preserving the user's own values, rather than leaving it partial (#96).
func TestInitCommand_fillsMissingKeysInExistingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// A partial config: a custom chunk size, everything else absent.
	if err := os.WriteFile(cfgPath, []byte("chunking:\n  size: 999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "size: 999") {
		t.Errorf("init dropped the user's chunking.size:\n%s", got)
	}
	for _, want := range []string{"database:", "./tbuk.sqlite", "raw_dir", "provider: mlx"} {
		if !strings.Contains(got, want) {
			t.Errorf("init did not fill missing default %q:\n%s", want, got)
		}
	}
}

// init on a config that already carries every default must leave it byte-for-byte
// untouched (nothing to add → bail).
func TestInitCommand_completeConfigLeftUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("first init failed: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := runCLI("init"); err != nil {
		t.Fatalf("second init failed: %v", err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("second init rewrote a complete config:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestInitCommand_writesQATemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	for _, name := range []string{"manifest.yaml", "system.tmpl", "user.tmpl"} {
		path := filepath.Join(home, ".tbuk", "prompts", "qa", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected template file: %s", path)
		}
	}
}

func TestInitCommand_installsMissingTemplateOnRerun(t *testing.T) {
	// Simulate existing setup (config + brief) without anki, then re-run init.
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("first init failed: %v", err)
	}

	// Remove anki dir to simulate pre-anki installation.
	if err := os.RemoveAll(filepath.Join(home, ".tbuk", "prompts", "anki")); err != nil {
		t.Fatal(err)
	}

	if err := runCLI("init"); err != nil {
		t.Fatalf("second init failed: %v", err)
	}

	for _, name := range []string{"manifest.yaml", "system.tmpl", "user.tmpl"} {
		path := filepath.Join(home, ".tbuk", "prompts", "anki", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected anki template file after re-run: %s", path)
		}
	}
}

func TestInitCommand_writesAnkiTemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	for _, name := range []string{"manifest.yaml", "system.tmpl", "user.tmpl"} {
		path := filepath.Join(home, ".tbuk", "prompts", "anki", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected anki template file: %s", path)
		}
	}
}

func TestInitCommand_ankiTemplateIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatal(err)
	}

	systemPath := filepath.Join(home, ".tbuk", "prompts", "anki", "system.tmpl")
	sentinel := "# custom anki system prompt\n"
	if err := os.WriteFile(systemPath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCLI("init"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(systemPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sentinel {
		t.Error("second init overwrote existing anki template file")
	}
}

func TestVersionCommand(t *testing.T) {
	if err := runCLI("version"); err != nil {
		t.Fatalf("version command failed: %v", err)
	}
}

func TestRootCommand_badConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(cfgPath, []byte("not: valid: yaml: ["), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCLI("--config", cfgPath, "version")
	if err == nil {
		t.Fatal("expected error with malformed config file")
	}
}

func TestInitCommand_customConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "custom.yaml")
	if err := os.WriteFile(cfgPath, []byte("chunking:\n  size: 400\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCLI("--config", cfgPath, "init"); err != nil {
		t.Fatalf("init with custom config failed: %v", err)
	}
}

func TestInitCommand_writesBriefTemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	for _, name := range []string{"manifest.yaml", "system.tmpl", "user.tmpl"} {
		path := filepath.Join(home, ".tbuk", "prompts", "brief", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected brief template file: %s", path)
		}
	}
}

func TestInitCommand_briefTemplateIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatal(err)
	}

	systemPath := filepath.Join(home, ".tbuk", "prompts", "brief", "system.tmpl")
	sentinel := "# custom brief system prompt\n"
	if err := os.WriteFile(systemPath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCLI("init"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(systemPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sentinel {
		t.Error("second init overwrote existing brief template file")
	}
}

func TestInitCommand_templateIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatal(err)
	}

	systemPath := filepath.Join(home, ".tbuk", "prompts", "qa", "system.tmpl")
	sentinel := "# custom system prompt\n"
	if err := os.WriteFile(systemPath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCLI("init"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(systemPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != sentinel {
		t.Error("second init overwrote existing template file")
	}
}

// The builtin anki system prompt must model the card format it demands: the
// separator opening every card, and line 2 as a note field that stays present
// even when empty. Wording that called it "one blank line separating question
// from answer" invited models to drop it, and single-card examples gave them no
// separator to copy.
func TestInitCommand_ankiSystemPromptDemonstratesCardFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".tbuk", "prompts", "anki", "system.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(data)

	if n := strings.Count(prompt, "\n----\n"); n < 4 {
		t.Errorf("system prompt should open several example cards with the separator, found %d", n)
	}
	if strings.Contains(prompt, "unless source material requires") {
		t.Error("the bullet ban must be unconditional: models take any escape hatch")
	}
	if strings.Contains(prompt, "markdown document") {
		t.Error("asking for a markdown document invites the bullets and headings the format forbids")
	}
	// The note line is a field, not spacing: say so, and show a card that fills it.
	for _, want := range []string{"note field", "Never drop", "Do not start any line with", "no blank lines"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing rule %q", want)
		}
	}
	if !strings.Contains(prompt, "(LLM inference)\n") {
		t.Error("examples should include a card whose note line is filled in")
	}
}

// The anki template must declare its normalize pipeline: prompt wording alone
// does not survive a long generation.
func TestInitCommand_ankiManifestDeclaresNormalizePipeline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := runCLI("init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	tmpl, err := prompts.NewTemplateDir(filepath.Join(home, ".tbuk", "prompts")).Load("anki")
	if err != nil {
		t.Fatalf("load anki template: %v", err)
	}
	cfg := tmpl.Manifest().Normalize
	if !cfg.Declared() {
		t.Fatal("anki manifest should declare a normalize pipeline")
	}
	if cfg.Records == nil || cfg.Records.Separator != "----" {
		t.Errorf("records config = %+v, want separator ----", cfg.Records)
	}
	if want := []string{"lead", "note", "body"}; !slices.Equal(cfg.Records.Fields, want) {
		t.Errorf("record fields = %v, want %v", cfg.Records.Fields, want)
	}
	if !contains(cfg.Filters, "strip_list_markers") {
		t.Errorf("filters %v should strip list markers", cfg.Filters)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
