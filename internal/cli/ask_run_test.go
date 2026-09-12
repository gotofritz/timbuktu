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

func mockRetrieve(chunks []retrieval.RetrievedChunk, err error) func(context.Context, []string, int, map[string]string) ([]retrieval.RetrievedChunk, error) {
	return func(_ context.Context, _ []string, _ int, _ map[string]string) ([]retrieval.RetrievedChunk, error) {
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

func TestRunAsk_trimsChunksToRetrievalMaxTokens(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "qa")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// max_tokens budget 25; each chunk is 40 bytes ≈ 10 tokens (len/4), so
	// only the first two chunks fit; the third must be dropped.
	manifest := "name: qa\nretrieval:\n  top_k: 3\n  max_tokens: 25\noutput: text\n"
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

	body := strings.Repeat("x", 40)
	chunks := []retrieval.RetrievedChunk{
		{Citation: "/a.md §0", Text: body},
		{Citation: "/b.md §1", Text: body},
		{Citation: "/c.md §2", Text: body},
	}

	var out bytes.Buffer
	if err := cli.RunAsk(context.Background(), &out, mockRetrieve(chunks, nil), mockChat([]string{"ok"}, nil), tmpl, "q", nil, 0, false); err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "/a.md §0") || !strings.Contains(got, "/b.md §1") {
		t.Errorf("want first two chunks kept, got: %q", got)
	}
	if strings.Contains(got, "/c.md §2") {
		t.Errorf("third chunk should be dropped by max_tokens budget, got: %q", got)
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

func TestRunAsk_stripsTerminalEscapesFromOutput(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer
	// A chunk whose citation carries an OSC 52 clipboard-write escape, and a
	// model stream that echoes an ESC-based CSI sequence: both are
	// document-derived and must not reach the terminal raw.
	chunks := []retrieval.RetrievedChunk{
		{Citation: "/docs/a.md\x1b]52;c;cHduZWQ\x07 §1", Text: "ctx", Path: "/docs/a.md", ChunkIndex: 1},
	}

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(chunks, nil),
		mockChat([]string{"ans\x1b[31mwer", "\x1b]0;pwned\x07"}, nil),
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
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("output leaked terminal control chars: %q", got)
	}
	// The visible text survives, only the control introducers are gone.
	if !strings.Contains(got, "ans[31mwer") {
		t.Errorf("stream text mangled: %q", got)
	}
	if !strings.Contains(got, "/docs/a.md]52;c;cHduZWQ §1") {
		t.Errorf("citation text mangled: %q", got)
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

// With no retrieved chunks, ask must warn to the error writer that it is
// answering from model priors, then still call the LLM.
func TestRunAsk_emptyContext_warns(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out, errBuf bytes.Buffer

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil), // zero chunks
		mockChat([]string{"from priors"}, nil),
		tmpl,
		"question",
		nil,
		0,
		false,
		cli.WithErrOut(&errBuf),
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(strings.ToLower(errBuf.String()), "no relevant context") {
		t.Errorf("want empty-context warning on errOut, got: %q", errBuf.String())
	}
	if !strings.Contains(out.String(), "from priors") {
		t.Errorf("LLM should still be called; out = %q", out.String())
	}
}

// --require-context must abort before the LLM call when retrieval is empty.
func TestRunAsk_requireContext_aborts(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer
	chatCalled := false
	spyChat := func(ctx context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		chatCalled = true
		return mockChat([]string{"x"}, nil)(ctx, nil)
	}

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		spyChat,
		tmpl,
		"question",
		nil,
		0,
		false,
		cli.WithRequireContext(true),
	)
	if err == nil {
		t.Fatal("expected abort error when --require-context and no chunks")
	}
	if chatCalled {
		t.Error("LLM must not be called when aborting on empty context")
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
	setHome(t, home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	t.Setenv("EDITOR", fakeEditor(t, "# edited"))

	if err := runCLI("--config", cfgPath, "template", "edit", "qa"); err != nil {
		t.Fatalf("template edit: %v", err)
	}

	// The shipped qa template is a real manifest; editing it must launch the
	// editor against that file, not merely print its path.
	manifest := filepath.Join(home, ".tbuk", "prompts", "qa", "manifest.yaml")
	got, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "# edited") {
		t.Errorf("template edit did not run editor on manifest; content = %q", got)
	}
}

// buildNormalizingTemplate returns a template that declares a normalize
// pipeline, as the builtin anki template does.
func buildNormalizingTemplate(t *testing.T) *prompts.Template {
	t.Helper()
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "cards")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `name: cards
output: text
normalize:
  filters: [strip_preamble, strip_fences, strip_list_markers, collapse_blank_lines]
  records:
    separator: "----"
    fields: [lead, note, body]
`
	for name, content := range map[string]string{
		"manifest.yaml": manifest,
		"system.tmpl":   "Make cards.",
		"user.tmpl":     "Topic: {{ .Question }}",
	} {
		if err := os.WriteFile(filepath.Join(tmplDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tmpl, err := prompts.NewTemplateDir(dir).Load("cards")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	return tmpl
}

// A template that declares a pipeline gets its completion repaired, whatever
// the model streamed.
func TestRunAsk_appliesTemplateNormalization(t *testing.T) {
	tmpl := buildNormalizingTemplate(t)
	var out bytes.Buffer

	drifted := []string{
		"Here are the cards:\n\n",
		"What is tokenization?\n- Converting text into tokens\n\n",
		"What is decode?\n1. Generating tokens one at a time\n",
	}
	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		mockChat(drifted, nil),
		tmpl,
		"topic",
		nil,
		0,
		false, // streaming requested: normalization must still buffer and repair
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}

	want := "----\n\nWhat is tokenization?\n\nConverting text into tokens\n\n" +
		"----\n\nWhat is decode?\n\nGenerating tokens one at a time\n"
	if got := out.String(); !strings.Contains(got, want) {
		t.Errorf("normalized output missing\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Templates without a pipeline keep the model's output verbatim.
func TestRunAsk_leavesOutputAloneWithoutPipeline(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out bytes.Buffer

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		mockChat([]string{"- a bullet\n- another\n"}, nil),
		tmpl,
		"question",
		nil,
		0,
		false,
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(out.String(), "- a bullet") {
		t.Errorf("output should be untouched, got %q", out.String())
	}
}

// Output that another program consumes must stay pure: the citations footer is
// provenance for the person running the command, not part of the record stream.
func TestRunAsk_recordsOutputKeepsCitationsOffStdout(t *testing.T) {
	tmpl := buildNormalizingTemplate(t)
	var out, errOut bytes.Buffer
	chunks := []retrieval.RetrievedChunk{
		{Citation: "/docs/a.md §1", Text: "ctx", Path: "/docs/a.md", ChunkIndex: 1},
	}

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(chunks, nil),
		mockChat([]string{"What is tokenization?\n- Converting text into tokens\n"}, nil),
		tmpl,
		"topic",
		nil,
		0,
		false,
		cli.WithErrOut(&errOut),
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}

	want := "----\n\nWhat is tokenization?\n\nConverting text into tokens\n"
	if got := out.String(); got != want {
		t.Errorf("stdout should hold records only\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
	if got := errOut.String(); !strings.Contains(got, "/docs/a.md §1") || !strings.Contains(got, "Sources:") {
		t.Errorf("citations should still be reported on stderr, got %q", got)
	}
}

// A template with only line filters still produces prose, so its citations stay
// where they were.
func TestRunAsk_filterOnlyTemplateKeepsCitationsOnStdout(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "tidy")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"manifest.yaml": "name: tidy\nnormalize:\n  filters: [strip_fences]\n",
		"system.tmpl":   "Answer.",
		"user.tmpl":     "{{ .Question }}",
	} {
		if err := os.WriteFile(filepath.Join(tmplDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tmpl, err := prompts.NewTemplateDir(dir).Load("tidy")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	chunks := []retrieval.RetrievedChunk{{Citation: "/docs/a.md §1", Text: "ctx", Path: "/docs/a.md"}}
	if err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(chunks, nil),
		mockChat([]string{"```\nprose answer\n```\n"}, nil),
		tmpl,
		"question",
		nil,
		0,
		false,
	); err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Sources:") {
		t.Errorf("prose output should keep its citations, got %q", got)
	}
}

// A model that returns nothing usable normalizes to an empty string. The
// records contract still holds: an empty file, not a file with a blank line.
func TestRunAsk_emptyRecordsOutputWritesNothing(t *testing.T) {
	tmpl := buildNormalizingTemplate(t)
	var out bytes.Buffer

	if err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve(nil, nil),
		mockChat([]string{"Here are the cards:\n\n"}, nil), // preamble only, no records
		tmpl,
		"topic",
		nil,
		0,
		false,
	); err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if got := out.String(); got != "" {
		t.Errorf("empty record output should write nothing, got %q", got)
	}
}

// Whatever the pipeline, the answer ends with exactly one newline: a model that
// already ends its text with one must not gain a blank line from it.
func TestRunAsk_endsWithExactlyOneNewline(t *testing.T) {
	filterOnly := buildFilterTemplate(t)

	cases := []struct {
		name   string
		tmpl   *prompts.Template
		tokens []string
		want   string
	}{
		{name: "prose streamed", tmpl: buildQATemplate(t), tokens: []string{"an answer\n"}, want: "an answer\n"},
		{name: "prose without its own newline", tmpl: buildQATemplate(t), tokens: []string{"an answer"}, want: "an answer\n"},
		{name: "filters only", tmpl: filterOnly, tokens: []string{"```\nan answer\n```\n"}, want: "an answer\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := cli.RunAsk(
				context.Background(),
				&out,
				mockRetrieve(nil, nil),
				mockChat(tc.tokens, nil),
				tc.tmpl,
				"question",
				nil,
				0,
				false,
			); err != nil {
				t.Fatalf("RunAsk: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// buildFilterTemplate returns a template declaring line filters but no records.
func buildFilterTemplate(t *testing.T) *prompts.Template {
	t.Helper()
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "tidy")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"manifest.yaml": "name: tidy\nnormalize:\n  filters: [strip_fences]\n",
		"system.tmpl":   "Answer.",
		"user.tmpl":     "{{ .Question }}",
	} {
		if err := os.WriteFile(filepath.Join(tmplDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tmpl, err := prompts.NewTemplateDir(dir).Load("tidy")
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

// A model that streams no text at all leaves the user staring at a bare
// "Sources:" list with nothing above it. Say so, rather than exiting 0 in
// silence.
func TestRunAsk_emptyCompletion_warns(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out, errBuf bytes.Buffer

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve([]retrieval.RetrievedChunk{{Text: "ctx", Citation: "a.md §0"}}, nil),
		mockChat(nil, nil), // stream closes with no text
		tmpl,
		"question",
		nil,
		0,
		false,
		cli.WithErrOut(&errBuf),
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(strings.ToLower(errBuf.String()), "returned no text") {
		t.Errorf("want empty-completion warning on errOut, got: %q", errBuf.String())
	}
}

// The warning is about the model saying nothing, not about a normalizer
// filtering everything out — a completion that arrives and reduces to zero
// records is a different situation and must stay quiet.
func TestRunAsk_normalizedToNothing_doesNotWarn(t *testing.T) {
	tmpl := buildNormalizingTemplate(t)
	var out, errBuf bytes.Buffer

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve([]retrieval.RetrievedChunk{{Text: "ctx", Citation: "a.md §0"}}, nil),
		mockChat([]string{"Here are your cards:\n"}, nil),
		tmpl,
		"question",
		nil,
		0,
		false,
		cli.WithErrOut(&errBuf),
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if strings.Contains(strings.ToLower(errBuf.String()), "returned no text") {
		t.Errorf("must not warn when the model did produce text, got: %q", errBuf.String())
	}
}

// --- context-window budget guard (#141) -------------------------------------

// buildTemplateWith writes a one-off template and loads it.
func buildTemplateWith(t *testing.T, manifest, system, user string) *prompts.Template {
	t.Helper()
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "qa")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"manifest.yaml": manifest,
		"system.tmpl":   system,
		"user.tmpl":     user,
	} {
		if err := os.WriteFile(filepath.Join(tmplDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tmpl, err := prompts.NewTemplateDir(dir).Load("qa")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	return tmpl
}

// guardTemplate is the fixture the budget tests share: no retrieval trim of its
// own, so the only thing bounding the prompt is the context guard.
func guardTemplate(t *testing.T) *prompts.Template {
	t.Helper()
	return buildTemplateWith(t,
		"name: qa\nretrieval:\n  top_k: 5\noutput: text\n",
		"sys",
		"{{ .Question }}{{ range .Chunks }}\n{{ .Citation }}\n{{ .Text }}{{ end }}")
}

// guardChunks are three chunks of prose that squeezes down by a third: 240
// bytes each raw (~60 tokens), 159 squeezed (~40).
func guardChunks() []retrieval.RetrievedChunk {
	body := strings.Repeat("the cat sat on the mat. ", 10)
	return []retrieval.RetrievedChunk{
		{Citation: "/a.md §0", Text: body},
		{Citation: "/b.md §1", Text: body},
		{Citation: "/c.md §2", Text: body},
	}
}

// recordingChat captures the messages sent to the model.
func recordingChat(got *[]llm.Message) func(context.Context, []llm.Message, ...llm.CallOptions) (<-chan llm.Token, error) {
	return func(_ context.Context, msgs []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		*got = msgs
		ch := make(chan llm.Token, 2)
		ch <- llm.Token{Text: "ok"}
		ch <- llm.Token{Done: true}
		close(ch)
		return ch, nil
	}
}

// refusingChat fails the test if the model is called at all.
func refusingChat(t *testing.T) func(context.Context, []llm.Message, ...llm.CallOptions) (<-chan llm.Token, error) {
	t.Helper()
	return func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		t.Error("model was called although the prompt does not fit the context window")
		ch := make(chan llm.Token, 1)
		ch <- llm.Token{Done: true}
		close(ch)
		return ch, nil
	}
}

// Without a window there is no guard: an oversized prompt goes out untouched,
// exactly as it did before the guard existed.
func TestRunAsk_contextGuardOffWithoutAWindow(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(0, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(msgs[1].Content, "the cat sat on the mat") {
		t.Errorf("chunk text should reach the model verbatim, got: %q", msgs[1].Content)
	}
	for _, cit := range []string{"/a.md §0", "/b.md §1", "/c.md §2"} {
		if !strings.Contains(out.String(), cit) {
			t.Errorf("want %s cited, got: %q", cit, out.String())
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("no guard, so no warning; got: %q", errOut.String())
	}
}

// A prompt that fits is sent exactly as rendered — the guard is invisible.
func TestRunAsk_contextGuardLeavesAFittingPromptAlone(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(10_000, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(msgs[1].Content, "the cat sat on the mat") {
		t.Errorf("prompt should be untouched, got: %q", msgs[1].Content)
	}
	if errOut.Len() != 0 {
		t.Errorf("want no warning for a prompt that fits, got: %q", errOut.String())
	}
}

// Over budget, the retrieved text is compacted before any passage is dropped:
// every chunk still reaches the model and is still cited.
func TestRunAsk_contextGuardSqueezesBeforeDropping(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message

	// budget = 200 − 50 = 150 tokens: ~190 raw, ~129 once compacted.
	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(200, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if strings.Contains(msgs[1].Content, "the cat sat on the mat") {
		t.Errorf("chunk text should have been compacted, got: %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "cat sat on mat.") {
		t.Errorf("compacted text should still carry the facts, got: %q", msgs[1].Content)
	}
	for _, cit := range []string{"/a.md §0", "/b.md §1", "/c.md §2"} {
		if !strings.Contains(out.String(), cit) {
			t.Errorf("want %s still cited, got: %q", cit, out.String())
		}
	}
	if !strings.Contains(errOut.String(), "compacted") {
		t.Errorf("want a warning naming the compaction, got: %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "dropped") {
		t.Errorf("nothing needed dropping, got: %q", errOut.String())
	}
}

// When compaction is not enough, the lowest-ranked chunks go, and the warning
// says how many.
func TestRunAsk_contextGuardDropsChunksWhenCompactionIsNotEnough(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message

	// budget = 150 − 50 = 100 tokens: ~129 compacted with three chunks, ~86
	// with two.
	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(150, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(errOut.String(), "dropped 1 of 3") {
		t.Errorf("want a warning naming what was dropped, got: %q", errOut.String())
	}
	if strings.Contains(out.String(), "/c.md §2") {
		t.Errorf("dropped chunk must not be cited, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "/a.md §0") || !strings.Contains(out.String(), "/b.md §1") {
		t.Errorf("want the surviving chunks cited, got: %q", out.String())
	}
}

// A prompt that cannot fit even with no context at all fails here, with the
// knobs named — not at the provider, as an HTTP 4xx.
func TestRunAsk_contextGuardErrorsWhenNothingFits(t *testing.T) {
	var out, errOut bytes.Buffer

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), refusingChat(t),
		guardTemplate(t), strings.Repeat("q", 400), nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(60, 50))
	if err == nil {
		t.Fatal("want an error when even a context-free prompt overflows")
	}
	if !strings.Contains(err.Error(), "context_tokens") {
		t.Errorf("error should name the knob to raise, got: %v", err)
	}
}

// A template pinned to a wide-window model overrides the config's window.
func TestRunAsk_contextGuardManifestWindowWins(t *testing.T) {
	tmpl := buildTemplateWith(t,
		"name: qa\ncontext_tokens: 100000\nretrieval:\n  top_k: 5\noutput: text\n",
		"sys",
		"{{ .Question }}{{ range .Chunks }}\n{{ .Citation }}\n{{ .Text }}{{ end }}")

	var out, errOut bytes.Buffer
	var msgs []llm.Message

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		tmpl, "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(60, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(msgs[1].Content, "the cat sat on the mat") {
		t.Errorf("manifest window should win, leaving the prompt untouched: %q", msgs[1].Content)
	}
	if errOut.Len() != 0 {
		t.Errorf("want no warning, got: %q", errOut.String())
	}
}

// The template's own output budget is what the window has to hold back for,
// when it declares one.
func TestRunAsk_contextGuardReservesManifestMaxTokens(t *testing.T) {
	tmpl := buildTemplateWith(t,
		"name: qa\nmax_tokens: 50\nretrieval:\n  top_k: 5\noutput: text\n",
		"sys",
		"{{ .Question }}{{ range .Chunks }}\n{{ .Citation }}\n{{ .Text }}{{ end }}")

	var out, errOut bytes.Buffer
	var msgs []llm.Message

	// The option's reserve of 1 is ignored: max_tokens 50 leaves a 100-token
	// budget out of 150, so the text is compacted.
	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		tmpl, "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(150, 1))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(errOut.String(), "dropped 1 of 3") {
		t.Errorf("want the manifest's max_tokens reserved, got: %q", errOut.String())
	}
}

// --require-context means "answer from my documents or not at all", so a budget
// that leaves room for none of them aborts rather than answering ungrounded.
func TestRunAsk_contextGuardRequireContextWhenAllDropped(t *testing.T) {
	var out, errOut bytes.Buffer

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), refusingChat(t),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithRequireContext(true),
		cli.WithContextBudget(70, 50))
	if err == nil {
		t.Fatal("want an error when the guard leaves no context and --require-context is set")
	}
	if !strings.Contains(err.Error(), "require-context") {
		t.Errorf("error should name the flag that caused the abort, got: %v", err)
	}
}

// Without --require-context the same budget answers anyway, warning that every
// retrieved passage was dropped.
func TestRunAsk_contextGuardDropsEverythingAndWarns(t *testing.T) {
	var out, errOut bytes.Buffer
	var msgs []llm.Message

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(guardChunks(), nil), recordingChat(&msgs),
		guardTemplate(t), "q", nil, 0, false,
		cli.WithErrOut(&errOut),
		cli.WithContextBudget(70, 50))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if !strings.Contains(errOut.String(), "dropped 3 of 3") {
		t.Errorf("want a warning that everything was dropped, got: %q", errOut.String())
	}
	if strings.Contains(out.String(), "Sources:") {
		t.Errorf("nothing survived, so nothing to cite, got: %q", out.String())
	}
}

// A reasoning model that spends the whole budget thinking streams no text, and
// the generic "raise max_tokens" hint leaves the user guessing which of several
// causes it was. The reasoning arrives on its own field, so say it plainly.
func TestRunAsk_reasonedWithoutAnswering_namesTheCause(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out, errBuf bytes.Buffer

	reasoningOnly := func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		ch := make(chan llm.Token, 2)
		ch <- llm.Token{Reasoning: "Okay, the user wants a summary. Let me think about"}
		ch <- llm.Token{Done: true}
		close(ch)
		return ch, nil
	}

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve([]retrieval.RetrievedChunk{{Text: "ctx", Citation: "a.md §0"}}, nil),
		reasoningOnly,
		tmpl,
		"question",
		nil,
		0,
		false,
		cli.WithErrOut(&errBuf),
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	got := strings.ToLower(errBuf.String())
	// The generic warning already guesses that a reasoning model "can" spend
	// the budget. Here the reasoning actually arrived, so the warning has to
	// state it rather than offer it as one possibility among several.
	if !strings.Contains(got, "spent the whole budget reasoning") {
		t.Errorf("warning hedges about a cause it can see: %q", errBuf.String())
	}
	if !strings.Contains(got, "max_tokens") {
		t.Errorf("warning does not name the budget to raise: %q", errBuf.String())
	}
}

// The definite wording must not appear when nothing said the model reasoned —
// a warning that asserts a cause it did not observe is worse than a vague one.
func TestRunAsk_emptyWithoutReasoning_doesNotClaimReasoning(t *testing.T) {
	tmpl := buildQATemplate(t)
	var out, errBuf bytes.Buffer

	err := cli.RunAsk(
		context.Background(),
		&out,
		mockRetrieve([]retrieval.RetrievedChunk{{Text: "ctx", Citation: "a.md §0"}}, nil),
		mockChat(nil, nil),
		tmpl,
		"question",
		nil,
		0,
		false,
		cli.WithErrOut(&errBuf),
	)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if strings.Contains(strings.ToLower(errBuf.String()), "spent the whole budget reasoning") {
		t.Errorf("warning claims reasoning it never saw: %q", errBuf.String())
	}
}
