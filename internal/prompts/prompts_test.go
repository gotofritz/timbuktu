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
	if m.Temperature != 0 {
		t.Errorf("temperature default: want 0, got %f", m.Temperature)
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
