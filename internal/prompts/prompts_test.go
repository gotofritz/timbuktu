package prompts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
)

// helpers

func writeTemplate(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	tmplDir := filepath.Join(dir, name)
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for fname, content := range files {
		if err := os.WriteFile(filepath.Join(tmplDir, fname), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const qaManifest = `name: qa
description: "Q&A template"
model: ""
temperature: 0.2
max_tokens: 2048
retrieval:
  top_k: 5
  max_tokens: 8000
variables:
  language:
    default: "English"
output: text
`

const systemTmpl = `You are helpful. Language: {{ index .Variables "language" }}.`
const userTmpl = `Question: {{ .Question }}{{ range .Chunks }}
Source: {{ .Citation }}
{{ .Text }}{{ end }}`

// TestManifest_defaults — missing optional fields get default values.
func TestManifest_defaults(t *testing.T) {
	dir := t.TempDir()
	minimal := `name: minimal
description: "minimal"
`
	writeTemplate(t, dir, "minimal", map[string]string{
		"manifest.yaml": minimal,
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})

	td := prompts.NewTemplateDir(dir)
	tmpl, err := td.Load("minimal")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := tmpl.Manifest()
	if m.Temperature != nil {
		t.Errorf("temperature default: want nil (unset), got %v", *m.Temperature)
	}
	if m.MaxTokens != 0 {
		t.Errorf("max_tokens default: want 0, got %d", m.MaxTokens)
	}
	if m.Output != "" {
		t.Errorf("output default: want empty, got %q", m.Output)
	}
}

// TestManifest_badYAML — error on malformed manifest.
func TestManifest_badYAML(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "bad", map[string]string{
		"manifest.yaml": ":\tinvalid: yaml: [\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})

	td := prompts.NewTemplateDir(dir)
	_, err := td.Load("bad")
	if err == nil {
		t.Fatal("expected error for bad YAML, got nil")
	}
}

// TestManifest_missingTemplate — error when system.tmpl or user.tmpl absent.
func TestManifest_missingTemplate(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "notmpl", map[string]string{
		"manifest.yaml": qaManifest,
		// no system.tmpl, no user.tmpl
	})

	td := prompts.NewTemplateDir(dir)
	_, err := td.Load("notmpl")
	if err == nil {
		t.Fatal("expected error for missing templates, got nil")
	}
}

// TestTemplateRender_qa — built-in qa template renders expected output.
func TestTemplateRender_qa(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "qa", map[string]string{
		"manifest.yaml": qaManifest,
		"system.tmpl":   systemTmpl,
		"user.tmpl":     userTmpl,
	})

	td := prompts.NewTemplateDir(dir)
	tmpl, err := td.Load("qa")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	data := prompts.TemplateData{
		Question: "What is Go?",
		Chunks: []retrieval.RetrievedChunk{
			{Citation: "/docs/go.md §1", Text: "Go is a language."},
		},
		Variables: map[string]string{"language": "English"},
	}

	system, user, err := tmpl.Render(data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(system, "helpful") {
		t.Errorf("system prompt missing 'helpful': %q", system)
	}
	if !strings.Contains(user, "What is Go?") {
		t.Errorf("user prompt missing question: %q", user)
	}
	if !strings.Contains(user, "/docs/go.md §1") {
		t.Errorf("user prompt missing citation: %q", user)
	}
	if !strings.Contains(user, "Go is a language.") {
		t.Errorf("user prompt missing chunk text: %q", user)
	}
}

// TestTemplateRender_customVar — --var language=French appears in output.
func TestTemplateRender_customVar(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "qa", map[string]string{
		"manifest.yaml": qaManifest,
		"system.tmpl":   systemTmpl,
		"user.tmpl":     userTmpl,
	})

	td := prompts.NewTemplateDir(dir)
	tmpl, err := td.Load("qa")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	data := prompts.TemplateData{
		Question:  "Bonjour?",
		Chunks:    nil,
		Variables: map[string]string{"language": "French"},
	}

	system, _, err := tmpl.Render(data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(system, "French") {
		t.Errorf("system prompt missing 'French': %q", system)
	}
}

// TestTemplateList — returns all template names with valid manifest.yaml.
func TestTemplateList(t *testing.T) {
	dir := t.TempDir()

	// valid template
	writeTemplate(t, dir, "qa", map[string]string{
		"manifest.yaml": qaManifest,
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})
	// another valid template
	writeTemplate(t, dir, "anki", map[string]string{
		"manifest.yaml": "name: anki\ndescription: anki cards\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})
	// directory without manifest (should be ignored)
	if err := os.MkdirAll(filepath.Join(dir, "nomanifest"), 0o755); err != nil {
		t.Fatal(err)
	}

	td := prompts.NewTemplateDir(dir)
	manifests, err := td.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(manifests) != 2 {
		t.Errorf("want 2 manifests, got %d", len(manifests))
	}
	names := make(map[string]bool)
	for _, m := range manifests {
		names[m.Name] = true
	}
	if !names["qa"] {
		t.Error("expected 'qa' in list")
	}
	if !names["anki"] {
		t.Error("expected 'anki' in list")
	}
}

// TestTemplateList_emptyDir — empty dir returns empty list without error.
func TestTemplateList_emptyDir(t *testing.T) {
	dir := t.TempDir()
	td := prompts.NewTemplateDir(dir)
	manifests, err := td.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(manifests) != 0 {
		t.Errorf("want 0 manifests, got %d", len(manifests))
	}
}

// TestTemplateList_nonexistentDir — non-existent dir returns empty list without error.
func TestTemplateList_nonexistentDir(t *testing.T) {
	td := prompts.NewTemplateDir("/no/such/dir/ever")
	manifests, err := td.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(manifests) != 0 {
		t.Errorf("want 0 manifests, got %d", len(manifests))
	}
}

// TestTemplateRender_systemError — system template execution error propagates.
func TestTemplateRender_systemError(t *testing.T) {
	dir := t.TempDir()
	// Template references an undefined sub-template, causing execution error.
	writeTemplate(t, dir, "bad", map[string]string{
		"manifest.yaml": qaManifest,
		"system.tmpl":   `{{ template "undefined_subtmpl" . }}`,
		"user.tmpl":     `Q: {{ .Question }}`,
	})

	td := prompts.NewTemplateDir(dir)
	tmpl, err := td.Load("bad")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	data := prompts.TemplateData{Question: "hi", Variables: map[string]string{}}
	_, _, err = tmpl.Render(data)
	if err == nil {
		t.Fatal("expected error from system template execution, got nil")
	}
}

// TestTemplateRender_userError — user template execution error propagates.
func TestTemplateRender_userError(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "bad2", map[string]string{
		"manifest.yaml": qaManifest,
		"system.tmpl":   `ok`,
		"user.tmpl":     `{{ template "undefined_subtmpl" . }}`,
	})

	td := prompts.NewTemplateDir(dir)
	tmpl, err := td.Load("bad2")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	data := prompts.TemplateData{Question: "hi", Variables: map[string]string{}}
	_, _, err = tmpl.Render(data)
	if err == nil {
		t.Fatal("expected error from user template execution, got nil")
	}
}

func TestLoad_readsNormalizePipeline(t *testing.T) {
	dir := t.TempDir()
	tdir := filepath.Join(dir, "cards")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(tdir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.yaml", "name: cards\nnormalize:\n  filters: [strip_fences]\n  records:\n    separator: \"----\"\n    fields: [lead, note, body]\n")
	write("system.tmpl", "sys")
	write("user.tmpl", "usr")

	tmpl, err := prompts.NewTemplateDir(dir).Load("cards")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := tmpl.Manifest().Normalize
	if !got.Declared() {
		t.Fatal("manifest should carry the declared pipeline")
	}
	if len(got.Filters) != 1 || got.Filters[0] != "strip_fences" {
		t.Errorf("filters = %v", got.Filters)
	}
	if got.Records == nil || got.Records.Separator != "----" {
		t.Errorf("records = %+v", got.Records)
	}
}

func TestLoad_rejectsUnknownNormalizeFilter(t *testing.T) {
	dir := t.TempDir()
	tdir := filepath.Join(dir, "cards")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"manifest.yaml": "name: cards\nnormalize:\n  filters: [make_it_nice]\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	} {
		if err := os.WriteFile(filepath.Join(tdir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := prompts.NewTemplateDir(dir).Load("cards")
	if err == nil {
		t.Fatal("expected a load error for an unknown filter")
	}
	if !strings.Contains(err.Error(), "make_it_nice") {
		t.Errorf("error should name the unknown filter, got %q", err)
	}
}

// A template pinned to its own model needs its own context window: manifest
// context_tokens overrides llm.context_tokens for that template (#141).
// Absent means "inherit the config", which is the zero value.
func TestManifest_contextTokens(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "wide", map[string]string{
		"manifest.yaml": "name: wide\ndescription: \"wide window\"\nmax_tokens: 4096\ncontext_tokens: 131072\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})
	writeTemplate(t, dir, "inherits", map[string]string{
		"manifest.yaml": "name: inherits\ndescription: \"no window of its own\"\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})

	td := prompts.NewTemplateDir(dir)

	wide, err := td.Load("wide")
	if err != nil {
		t.Fatalf("Load(wide): %v", err)
	}
	if got := wide.Manifest().ContextTokens; got != 131072 {
		t.Errorf("context_tokens: want 131072, got %d", got)
	}

	inherits, err := td.Load("inherits")
	if err != nil {
		t.Fatalf("Load(inherits): %v", err)
	}
	if got := inherits.Manifest().ContextTokens; got != 0 {
		t.Errorf("context_tokens default: want 0 (inherit config), got %d", got)
	}
}

// Query planning spends the template's model at the template's temperature, so
// it is configured next to top_k rather than in config.yaml (#157). Absent
// means "inherit the default", which is the zero value.
func TestManifest_rewrite(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "threaded", map[string]string{
		"manifest.yaml": "name: threaded\nretrieval:\n  top_k: 5\n  rewrite: window\n  window_turns: 3\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})
	writeTemplate(t, dir, "inherits", map[string]string{
		"manifest.yaml": "name: inherits\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})

	td := prompts.NewTemplateDir(dir)

	threaded, err := td.Load("threaded")
	if err != nil {
		t.Fatalf("Load(threaded): %v", err)
	}
	if got := threaded.Manifest().Retrieval.Rewrite; got != "window" {
		t.Errorf("retrieval.rewrite: want window, got %q", got)
	}
	if got := threaded.Manifest().Retrieval.WindowTurns; got != 3 {
		t.Errorf("retrieval.window_turns: want 3, got %d", got)
	}

	inherits, err := td.Load("inherits")
	if err != nil {
		t.Fatalf("Load(inherits): %v", err)
	}
	if got := inherits.Manifest().Retrieval.Rewrite; got != "" {
		t.Errorf("retrieval.rewrite default: want empty (inherit), got %q", got)
	}
	if got := inherits.Manifest().Retrieval.WindowTurns; got != 0 {
		t.Errorf("retrieval.window_turns default: want 0 (inherit), got %d", got)
	}
}

// Expansion is a count of extra wordings, and it is a template key for the same
// reason the mode is: it spends the template's model.
func TestManifest_expand(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "wide", map[string]string{
		"manifest.yaml": "name: wide\nretrieval:\n  top_k: 5\n  expand: 3\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})
	writeTemplate(t, dir, "plain", map[string]string{
		"manifest.yaml": "name: plain\n",
		"system.tmpl":   "sys",
		"user.tmpl":     "usr",
	})

	td := prompts.NewTemplateDir(dir)

	wide, err := td.Load("wide")
	if err != nil {
		t.Fatalf("Load(wide): %v", err)
	}
	if got := wide.Manifest().Retrieval.Expand; got != 3 {
		t.Errorf("retrieval.expand: want 3, got %d", got)
	}

	plain, err := td.Load("plain")
	if err != nil {
		t.Fatalf("Load(plain): %v", err)
	}
	if got := plain.Manifest().Retrieval.Expand; got != 0 {
		t.Errorf("retrieval.expand default: want 0 (off), got %d", got)
	}
}

// Every mode this build ships loads; the command decides whether it has a model
// to spend on the ones that need one.
func TestLoad_acceptsEveryRewriteMode(t *testing.T) {
	for _, mode := range []string{"off", "window", "condense"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeTemplate(t, dir, "t", map[string]string{
				"manifest.yaml": "name: t\nretrieval:\n  rewrite: " + mode + "\n",
				"system.tmpl":   "sys",
				"user.tmpl":     "usr",
			})
			tmpl, err := prompts.NewTemplateDir(dir).Load("t")
			if err != nil {
				t.Fatalf("Load(%s): %v", mode, err)
			}
			if got := tmpl.Manifest().Retrieval.Rewrite; got != mode {
				t.Errorf("retrieval.rewrite = %q, want %q", got, mode)
			}
		})
	}
}

// A typo in the planner mode fails at template load rather than after a model
// call, the rule normalize's filters already follow.
func TestLoad_rejectsBadRewrite(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{"unknown mode", "name: t\nretrieval:\n  rewrite: windwo\n", "windwo"},
		{"negative window", "name: t\nretrieval:\n  rewrite: window\n  window_turns: -1\n", "window_turns"},
		{"negative expansion", "name: t\nretrieval:\n  expand: -2\n", "expand"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTemplate(t, dir, "t", map[string]string{
				"manifest.yaml": tc.manifest,
				"system.tmpl":   "sys",
				"user.tmpl":     "usr",
			})
			_, err := prompts.NewTemplateDir(dir).Load("t")
			if err == nil {
				t.Fatal("expected a load error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should name %q, got %q", tc.want, err)
			}
		})
	}
}
