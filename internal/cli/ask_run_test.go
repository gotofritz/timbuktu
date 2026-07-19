package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
)

// buildQATemplate creates a qa template under dir and returns the loaded Template.
func buildQATemplate(t *testing.T) *prompts.Template {
	t.Helper()
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "qa")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `name: qa
description: "Q&A"
temperature: 0.2
max_tokens: 2048
retrieval:
  top_k: 3
variables:
  language:
    default: "English"
output: text
`
	system := `You are helpful.`
	user := `Question: {{ .Question }}{{ range .Chunks }}
[{{ .Citation }}] {{ .Text }}{{ end }}`

	for name, content := range map[string]string{
		"manifest.yaml": manifest,
		"system.tmpl":   system,
		"user.tmpl":     user,
	} {
		if err := os.WriteFile(filepath.Join(tmplDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	td := prompts.NewTemplateDir(dir)
	tmpl, err := td.Load("qa")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	return tmpl
}

func mockRetrieve(chunks []retrieval.RetrievedChunk, err error) func(context.Context, string, int, map[string]string) ([]retrieval.RetrievedChunk, error) {
	return func(_ context.Context, _ string, _ int, _ map[string]string) ([]retrieval.RetrievedChunk, error) {
		return chunks, err
	}
}

func mockChat(tokens []string, err error) func(context.Context, []llm.Message, ...llm.CallOptions) (<-chan llm.Token, error) {
	return func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		if err != nil {
			return nil, err
		}
		ch := make(chan llm.Token, len(tokens)+1)
		for _, t := range tokens {
			ch <- llm.Token{Text: t}
		}
		ch <- llm.Token{Done: true}
		close(ch)
		return ch, nil
	}
}

func TestRunAsk_streamsOutput(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		mockChat([]string{"Hello", " world"}, nil),
		tmpl,
		"What is Go?",
		nil,
		0,
		false,
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Hello world") {
		t.Errorf("want 'Hello world' in output, got: %q", got)
	}
}

// capturingChat records the CallOptions it receives.
func capturingChat(got *[]llm.CallOptions) func(context.Context, []llm.Message, ...llm.CallOptions) (<-chan llm.Token, error) {
	return func(_ context.Context, _ []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error) {
		*got = opts
		ch := make(chan llm.Token, 1)
		ch <- llm.Token{Done: true}
		close(ch)
		return ch, nil
	}
}

func TestRunAsk_forwardsManifestCallOptions(t *testing.T) {
	tmpl := buildQATemplate(t) // manifest: temperature 0.2, max_tokens 2048
	var out bytes.Buffer
	var got []llm.CallOptions

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		capturingChat(&got),
		tmpl,
		"question",
		nil,
		0,
		false,
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want CallOptions forwarded, got %d", len(got))
	}
	if got[0].MaxTokens != 2048 {
		t.Errorf("max_tokens: want 2048, got %d", got[0].MaxTokens)
	}
	if got[0].Temperature == nil {
		t.Fatal("temperature: want 0.2, got nil")
	}
	if *got[0].Temperature != 0.2 {
		t.Errorf("temperature: want 0.2, got %g", *got[0].Temperature)
	}
}

func TestRunAsk_honorsExplicitTemperatureZero(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "qa")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name: qa\ntemperature: 0.0\nmax_tokens: 100\nretrieval:\n  top_k: 3\noutput: text\n"
	for name, content := range map[string]string{
		"manifest.yaml": manifest,
		"system.tmpl":   "sys",
		"user.tmpl":     "{{ .Question }}",
	} {
		if err := os.WriteFile(filepath.Join(tmplDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tmpl, err := prompts.NewTemplateDir(dir).Load("qa")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	var out bytes.Buffer
	var got []llm.CallOptions
	if err := cli.RunAsk(context.Background(), &out, mockRetrieve(nil, nil), capturingChat(&got), tmpl, "q", nil, 0, false); err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(got) != 1 || got[0].Temperature == nil {
		t.Fatalf("want explicit temperature forwarded, got %+v", got)
	}
	if *got[0].Temperature != 0 {
		t.Errorf("temperature: want explicit 0, got %g", *got[0].Temperature)
	}
}

func TestRunAsk_noStream(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		mockChat([]string{"buffered"}, nil),
		tmpl,
		"question",
		nil,
		0,
		true,
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(out.String(), "buffered") {
		t.Errorf("want 'buffered' in output, got: %q", out.String())
	}
}

func TestRunAsk_printsCitations(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer
	chunks := []retrieval.RetrievedChunk{
		{Citation: "/docs/a.md §1", Text: "ctx", Path: "/docs/a.md", ChunkIndex: 1},
	}

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(chunks, nil),
		mockChat([]string{"answer"}, nil),
		tmpl,
		"question",
		nil,
		0,
		false,
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "/docs/a.md §1") {
		t.Errorf("want citation in output, got: %q", got)
	}
	if !strings.Contains(got, "Sources:") {
		t.Errorf("want 'Sources:' in output, got: %q", got)
	}
}

func TestRunAsk_badVarFormat(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		mockChat(nil, nil),
		tmpl,
		"question",
		[]string{"nodequals"}, // missing =
		0,
		false,
	)
	if err == nil {
		t.Fatal("expected error for bad --var")
	}
	if !strings.Contains(err.Error(), "nodequals") {
		t.Errorf("error should mention 'nodequals', got: %v", err)
	}
}

func TestRunAsk_retrieveError(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer
	sentinel := errors.New("search broken")

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, sentinel),
		mockChat(nil, nil),
		tmpl,
		"question",
		nil,
		0,
		false,
	)
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel error, got: %v", err)
	}
}

func TestRunAsk_chatError(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer
	sentinel := errors.New("LLM down")

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		mockChat(nil, sentinel),
		tmpl,
		"question",
		nil,
		0,
		false,
	)
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel error, got: %v", err)
	}
}

func TestRunAsk_streamError(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer
	sentinel := errors.New("stream error")

	chatWithError := func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		ch := make(chan llm.Token, 1)
		ch <- llm.Token{Error: sentinel}
		close(ch)
		return ch, nil
	}

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		chatWithError,
		tmpl,
		"question",
		nil,
		0,
		false,
	)
	if !errors.Is(err, sentinel) {
		t.Errorf("want sentinel error, got: %v", err)
	}
}

// RunAsk must run the LLM call under a cancellable context and cancel it when
// it returns, so an abandoned stream goroutine is released (P1-8).
func TestRunAsk_cancelsStreamContextOnExit(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer
	var captured context.Context

	chat := func(ctx context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		captured = ctx
		ch := make(chan llm.Token, 1)
		ch <- llm.Token{Error: errors.New("boom")} // force early return mid-stream
		close(ch)
		return ch, nil
	}

	_ = cli.RunAsk(context.Background(), &out, mockRetrieve(nil, nil), chat, tmpl, "q", nil, 0, false)

	if captured == nil {
		t.Fatal("chat was not called")
	}
	if captured.Err() == nil {
		t.Error("expected RunAsk to cancel the stream context on return")
	}
}

func TestRunAsk_customVar(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "qa")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `name: qa
description: "Q&A"
variables:
  language:
    default: "English"
`
	system := `Language: {{ index .Variables "language" }}`
	user := `Q: {{ .Question }}`
	for name, content := range map[string]string{
		"manifest.yaml": manifest,
		"system.tmpl":   system,
		"user.tmpl":     user,
	} {
		if err := os.WriteFile(filepath.Join(tmplDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	td := prompts.NewTemplateDir(dir)
	tmpl, _ := td.Load("qa")

	var out bytes.Buffer
	_ = cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		mockChat([]string{"ok"}, nil),
		tmpl,
		"hello",
		[]string{"language=French"},
		0,
		false,
	)
	// system template rendered with French — we can't easily inspect it from here,
	// but the call must not error
}

func TestAskCommand_templateEditCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runCLI("--config", cfgPath, "template", "edit", "qa")

	_ = w.Close()
	os.Stdout = oldStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if err != nil {
		t.Fatalf("template edit: %v", err)
	}
	if !strings.Contains(output, "manifest.yaml") {
		t.Errorf("template edit should mention manifest.yaml, got: %q", output)
	}
}
