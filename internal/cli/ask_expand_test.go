package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/conversation"
)

// plannedQueries is a planner that hands RunAsk a fixed plan, which is what an
// expansion produces: the query, plus wordings of it.
type plannedQueries []string

func (p plannedQueries) Queries(_ context.Context, _ []conversation.Turn, _ string) ([]string, error) {
	return p, nil
}

// Every planned query is retrieved on, not just the first: expansion is only
// worth a model call if the paraphrases reach the index.
func TestRunAsk_retrievesOnEveryPlannedQuery(t *testing.T) {
	var out bytes.Buffer
	var queries []string

	err := cli.RunAsk(context.Background(), &out, capturingRetrieve(&queries, nil),
		mockChat([]string{"ok"}, nil), buildQATemplate(t), "how do slices grow?", nil, 0, false,
		cli.WithPlanner(plannedQueries{"how do slices grow?", "slice growth", "append reallocation"}))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	want := []string{"how do slices grow?", "slice growth", "append reallocation"}
	if !reflect.DeepEqual(queries, want) {
		t.Errorf("retrieved on %q, want %q", queries, want)
	}
}

// The regression bar: with no planner the retriever is handed the question and
// nothing else, which is what `tbuk ask` has always sent.
func TestRunAsk_withoutAPlannerRetrievesOnTheQuestion(t *testing.T) {
	var out bytes.Buffer
	var queries []string

	err := cli.RunAsk(context.Background(), &out, capturingRetrieve(&queries, nil),
		mockChat([]string{"ok"}, nil), buildQATemplate(t), "how do slices grow?", nil, 0, false)
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if want := []string{"how do slices grow?"}; !reflect.DeepEqual(queries, want) {
		t.Errorf("retrieved on %q, want %q", queries, want)
	}
}

// A turn records what retrieval ran, so `session show --verbose` can explain a
// surprising answer. Under expansion that is every query, not the first of
// them.
func TestRunAsk_recordsEveryQueryInTheTurn(t *testing.T) {
	var out bytes.Buffer
	rec := &recorder{}

	err := cli.RunAsk(context.Background(), &out, mockRetrieve(nil, nil), mockChat([]string{"ok"}, nil),
		buildQATemplate(t), "how do slices grow?", nil, 0, false,
		cli.WithSession(threadOf(), rec.append),
		cli.WithPlanner(plannedQueries{"how do slices grow?", "slice growth"}))
	if err != nil {
		t.Fatalf("RunAsk: %v", err)
	}
	if len(rec.turns) != 1 {
		t.Fatalf("want one appended turn, got %d", len(rec.turns))
	}
	if want := "how do slices grow? | slice growth"; rec.turns[0].Query != want {
		t.Errorf("stored query = %q, want %q", rec.turns[0].Query, want)
	}
}

// A planner that comes back with nothing usable leaves the question standing:
// an empty query would match the whole corpus in rank order.
func TestRunAsk_emptyPlanFallsBackToTheQuestion(t *testing.T) {
	for _, plan := range []plannedQueries{{}, {"  "}} {
		var out bytes.Buffer
		var queries []string

		err := cli.RunAsk(context.Background(), &out, capturingRetrieve(&queries, nil),
			mockChat([]string{"ok"}, nil), buildQATemplate(t), "how do slices grow?", nil, 0, false,
			cli.WithPlanner(plan))
		if err != nil {
			t.Fatalf("RunAsk: %v", err)
		}
		if want := []string{"how do slices grow?"}; !reflect.DeepEqual(queries, want) {
			t.Errorf("plan %q: retrieved on %q, want %q", plan, queries, want)
		}
	}
}

// A typo in --expand fails before anything is opened or any model is called —
// the rule --rewrite and the manifest keys already follow.
func TestAskCommand_expandRejectsANegativeCount(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")

	out, err := runRoot(t, "ask", "--expand", "-2", "how do slices grow?")
	if err == nil {
		t.Fatalf("want an error for a negative expansion, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "expand") {
		t.Errorf("error %q does not mention --expand", err)
	}
}

// End to end: the model writes the alternative wordings, retrieval runs all of
// them, and the turn records what ran.
func TestAskCommand_expandPlansSeveralQueries(t *testing.T) {
	srv := fakePlannerServer(t, "", "slice growth\nappend reallocation")
	rewriteHome(t, srv)

	mustRun(t, "ask", "--session", "go", "--expand", "2", "how do slices grow?")

	out := mustRun(t, "session", "show", "go", "--verbose")
	want := "[query] how do slices grow? | slice growth | append reallocation"
	if !strings.Contains(out, want) {
		t.Fatalf("session show does not report the expanded plan:\nwant %q\ngot\n%s", want, out)
	}
}

// Expansion composes with the mode: condense plans the standalone question and
// the paraphrases are of that, not of the words that were typed.
func TestAskCommand_expandComposesWithCondense(t *testing.T) {
	srv := fakePlannerServer(t, "how do Go maps grow?", "go map growth")
	rewriteHome(t, srv)

	mustRun(t, "ask", "--session", "go", "how do slices grow?")
	mustRun(t, "ask", "-c", "--rewrite", "condense", "--expand", "1", "and maps?")

	out := mustRun(t, "session", "show", "go", "--verbose")
	if want := "[query] how do Go maps grow? | go map growth"; !strings.Contains(out, want) {
		t.Fatalf("the follow-up did not condense and then expand:\nwant %q\ngot\n%s", want, out)
	}
}

// The manifest key does the same thing without a flag, since expansion spends
// the template's model.
func TestAskCommand_expandFromTheManifest(t *testing.T) {
	srv := fakePlannerServer(t, "", "slice growth")
	home := rewriteHome(t, srv)
	setManifestRetrieval(t, filepath.Join(home, ".tbuk", "prompts", "qa", "manifest.yaml"), "expand: 1")

	mustRun(t, "ask", "--session", "go", "how do slices grow?")

	out := mustRun(t, "session", "show", "go", "--verbose")
	if want := "[query] how do slices grow? | slice growth"; !strings.Contains(out, want) {
		t.Fatalf("the manifest's expansion did not run:\nwant %q\ngot\n%s", want, out)
	}
}

// The flag overrides the manifest for one run, in both directions: a template
// that expands can be asked not to.
func TestAskCommand_expandFlagOverridesTheManifest(t *testing.T) {
	srv := fakePlannerServer(t, "", "slice growth")
	home := rewriteHome(t, srv)
	setManifestRetrieval(t, filepath.Join(home, ".tbuk", "prompts", "qa", "manifest.yaml"), "expand: 2")

	mustRun(t, "ask", "--session", "go", "--expand", "0", "how do slices grow?")

	out := mustRun(t, "session", "show", "go", "--verbose")
	if want := "[query] how do slices grow?\n"; !strings.Contains(out, want) {
		t.Fatalf("--expand 0 did not switch the manifest's expansion off:\nwant %q\ngot\n%s", want, out)
	}
}

// A single-shot ask expands too: there is no thread to fold in, but the extra
// wordings still reach passages the question's own vocabulary missed.
func TestAskCommand_expandWithoutASession(t *testing.T) {
	srv := fakePlannerServer(t, "", "slice growth")
	rewriteHome(t, srv)

	out := mustRun(t, "ask", "--expand", "1", "how do slices grow?")
	if !strings.Contains(out, "Sources:") {
		t.Fatalf("a single-shot expanded ask did not answer from the corpus:\n%s", out)
	}
}

// setManifestRetrieval adds a key to a template's existing retrieval block.
func setManifestRetrieval(t *testing.T, path, line string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := strings.Replace(string(data), "retrieval:\n", "retrieval:\n  "+line+"\n", 1)
	if out == string(data) {
		t.Fatalf("%s has no retrieval block to add %q to", path, line)
	}
	writeFile(t, path, out)
}

// The REPL takes the same knob, and rejects the same typo at the same edge.
func TestChatCommand_expandRejectsANegativeCount(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mustRun(t, "init")

	out, err := runRoot(t, "chat", "--expand", "-1")
	if err == nil {
		t.Fatalf("want an error for a negative expansion, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "expand") {
		t.Errorf("error %q does not mention --expand", err)
	}
}
