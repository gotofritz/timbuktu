package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/eval"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/rewrite"
)

// evalSet parses a label set from YAML, failing the test if it will not load.
func evalSet(t *testing.T, y string) eval.Set {
	t.Helper()
	set, err := eval.ParseSet(strings.NewReader(y))
	if err != nil {
		t.Fatalf("ParseSet: %v", err)
	}
	return set
}

// recordingRetriever returns a retriever that answers with chunks and records
// every query plan it was handed.
func recordingRetriever(chunks []retrieval.RetrievedChunk, got *[][]string) func(context.Context, []string, int, map[string]string) ([]retrieval.RetrievedChunk, error) {
	return func(_ context.Context, queries []string, _ int, _ map[string]string) ([]retrieval.RetrievedChunk, error) {
		*got = append(*got, append([]string(nil), queries...))
		return chunks, nil
	}
}

func chunkAt(path string) retrieval.RetrievedChunk {
	return retrieval.RetrievedChunk{Path: path, Text: "the capacity is doubled", Citation: path + " §0"}
}

const twoCaseSet = `
version: 1
name: go-docs
cases:
  - id: slices
    query: how do slices grow?
    relevant:
      - path: go/slices.md
  - id: maps
    query: and maps?
    relevant:
      - path: go/maps.md
`

func TestRunEval_scoresEveryCase(t *testing.T) {
	var seen [][]string
	set := evalSet(t, twoCaseSet)

	report, err := cli.RunEval(context.Background(), set,
		recordingRetriever([]retrieval.RetrievedChunk{chunkAt("/n/go/slices.md")}, &seen),
		cli.EvalOptions{Mode: "hybrid", TopK: 5, Embedding: "llama/nomic"})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}

	if len(report.Cases) != 2 {
		t.Fatalf("scored %d cases, want 2", len(report.Cases))
	}
	// The first case's labelled document came back; the second's did not.
	if report.Cases[0].Metrics.Hit != 1 || report.Cases[1].Metrics.Hit != 0 {
		t.Errorf("hits = %v / %v, want 1 / 0", report.Cases[0].Metrics.Hit, report.Cases[1].Metrics.Hit)
	}
	if report.Overall.Cases != 2 {
		t.Errorf("Overall.Cases = %d, want 2", report.Overall.Cases)
	}
	// Without a planner, retrieval runs on the question exactly as written.
	if len(seen) != 2 || len(seen[0]) != 1 || seen[0][0] != "how do slices grow?" {
		t.Errorf("queries run = %v, want the questions as typed", seen)
	}
	// The instrument travels with the numbers.
	if report.Run.Mode != "hybrid" || report.Run.TopK != 5 || report.Run.Embedding != "llama/nomic" {
		t.Errorf("run = %+v", report.Run)
	}
	if report.Run.At.IsZero() {
		t.Error("run should be timestamped")
	}
	if report.Set != "go-docs" {
		t.Errorf("Set = %q", report.Set)
	}
}

func TestRunEval_skipsCasesWithNothingToScore(t *testing.T) {
	// A generation-only case has no labels: scoring its retrieval is a question
	// with no answer, so it is skipped rather than counted as a zero that would
	// drag the average and read like a retrieval failure.
	set := evalSet(t, `
version: 1
name: mixed
cases:
  - id: scored
    query: q
    relevant:
      - path: a.md
  - id: generation-only
    query: q
    answer: a reference answer
`)
	var seen [][]string
	report, err := cli.RunEval(context.Background(), set,
		recordingRetriever([]retrieval.RetrievedChunk{chunkAt("/n/a.md")}, &seen),
		cli.EvalOptions{Mode: "hybrid", TopK: 5})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if len(report.Cases) != 1 || report.Cases[0].ID != "scored" {
		t.Fatalf("cases = %+v, want only the labelled one", report.Cases)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].ID != "generation-only" {
		t.Fatalf("skipped = %+v, want the unlabelled case named", report.Skipped)
	}
	if !strings.Contains(report.Skipped[0].Reason, "label") {
		t.Errorf("reason = %q, want it to say there are no labels", report.Skipped[0].Reason)
	}
	// A skipped case costs no search.
	if len(seen) != 1 {
		t.Errorf("ran %d searches, want 1", len(seen))
	}
}

func TestRunEval_goldQueryIsTheCeiling(t *testing.T) {
	set := evalSet(t, `
version: 1
name: followups
cases:
  - id: has-gold
    query: and maps?
    gold_query: how do Go maps grow as they fill up?
    relevant:
      - path: go/maps.md
  - id: no-gold
    query: and channels?
    relevant:
      - path: go/channels.md
`)
	var seen [][]string
	report, err := cli.RunEval(context.Background(), set,
		recordingRetriever([]retrieval.RetrievedChunk{chunkAt("/n/go/maps.md")}, &seen),
		cli.EvalOptions{Mode: "keyword", TopK: 5, Gold: true})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if len(seen) != 1 || seen[0][0] != "how do Go maps grow as they fill up?" {
		t.Errorf("queries = %v, want the gold query alone", seen)
	}
	// A case with no ceiling written down cannot be scored against one.
	if len(report.Skipped) != 1 || report.Skipped[0].ID != "no-gold" {
		t.Fatalf("skipped = %+v, want the case with no gold_query", report.Skipped)
	}
	if !strings.Contains(report.Skipped[0].Reason, "gold") {
		t.Errorf("reason = %q, want it to name gold_query", report.Skipped[0].Reason)
	}
	if report.Run.Rewrite != "gold" {
		t.Errorf("Run.Rewrite = %q, want the report to record that this is the ceiling", report.Run.Rewrite)
	}
}

func TestRunEval_plannerFoldsTheThreadIn(t *testing.T) {
	// The whole reason a case may carry a thread: "and maps?" retrieved alone
	// reaches nothing, so window puts the topic back into the query.
	set := evalSet(t, `
version: 1
name: followups
cases:
  - id: maps
    thread:
      - question: how do slices grow?
        answer: append reallocates when len == cap
    query: and maps?
    relevant:
      - path: go/maps.md
`)
	var seen [][]string
	_, err := cli.RunEval(context.Background(), set,
		recordingRetriever(nil, &seen),
		cli.EvalOptions{Mode: "hybrid", TopK: 5, Rewrite: "window", Planner: rewrite.Window{Turns: 2}})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("searches = %v", seen)
	}
	if !strings.Contains(seen[0][0], "how do slices grow?") || !strings.Contains(seen[0][0], "and maps?") {
		t.Errorf("query = %q, want the thread folded into it", seen[0][0])
	}
}

func TestRunEval_caseFilter(t *testing.T) {
	set := evalSet(t, twoCaseSet)
	var seen [][]string

	report, err := cli.RunEval(context.Background(), set,
		recordingRetriever(nil, &seen), cli.EvalOptions{Mode: "hybrid", TopK: 5, CaseID: "maps"})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if len(report.Cases) != 1 || report.Cases[0].ID != "maps" {
		t.Errorf("cases = %+v, want only maps", report.Cases)
	}
	// A filtered-out case is not a skipped case: nothing was asked of it.
	if len(report.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none", report.Skipped)
	}

	_, err = cli.RunEval(context.Background(), set,
		recordingRetriever(nil, &seen), cli.EvalOptions{Mode: "hybrid", TopK: 5, CaseID: "nope"})
	if err == nil {
		t.Fatal("RunEval with an unknown --case = nil error, want one")
	}
	if !strings.Contains(err.Error(), "slices") {
		t.Errorf("error = %v, want it to name the ids that do exist", err)
	}
}

func TestRunEval_retrieveErrorNamesTheCase(t *testing.T) {
	set := evalSet(t, twoCaseSet)
	failing := func(context.Context, []string, int, map[string]string) ([]retrieval.RetrievedChunk, error) {
		return nil, errors.New("embedding server refused")
	}
	_, err := cli.RunEval(context.Background(), set, failing, cli.EvalOptions{Mode: "hybrid", TopK: 5})
	if err == nil {
		t.Fatal("RunEval = nil error, want the retrieval failure")
	}
	if !strings.Contains(err.Error(), "slices") || !strings.Contains(err.Error(), "embedding server refused") {
		t.Errorf("error = %v, want it to name the case and the cause", err)
	}
}

func TestRunEval_recordsLatency(t *testing.T) {
	set := evalSet(t, twoCaseSet)
	var seen [][]string
	report, err := cli.RunEval(context.Background(), set,
		recordingRetriever(nil, &seen), cli.EvalOptions{Mode: "hybrid", TopK: 5})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	// Timings are real wall clock, so the only safe assertion is that they were
	// taken at all; #161's kill criterion is stated in latency, so a report
	// without it cannot decide anything.
	for _, c := range report.Cases {
		if c.LatencyMS < 0 {
			t.Errorf("case %s latency = %v", c.ID, c.LatencyMS)
		}
	}
	if report.Latency.MedianMS < 0 || report.Latency.P95MS < 0 {
		t.Errorf("latency = %+v", report.Latency)
	}
}

func TestResolveSetPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go-docs.yaml"), "version: 1\n")
	writeFile(t, filepath.Join(dir, "ops.yml"), "version: 1\n")
	writeFile(t, filepath.Join(dir, "notes.txt"), "not a label set\n")

	t.Run("no argument runs every set in the directory", func(t *testing.T) {
		got, err := cli.ResolveSetPaths("", dir)
		if err != nil {
			t.Fatalf("ResolveSetPaths: %v", err)
		}
		// Sorted, so a run over several sets reports them in a stable order.
		want := []string{filepath.Join(dir, "go-docs.yaml"), filepath.Join(dir, "ops.yml")}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("a name resolves under the directory", func(t *testing.T) {
		got, err := cli.ResolveSetPaths("go-docs", dir)
		if err != nil {
			t.Fatalf("ResolveSetPaths: %v", err)
		}
		if len(got) != 1 || got[0] != filepath.Join(dir, "go-docs.yaml") {
			t.Errorf("got %v", got)
		}
	})

	t.Run("a name finds a .yml set too", func(t *testing.T) {
		got, err := cli.ResolveSetPaths("ops", dir)
		if err != nil {
			t.Fatalf("ResolveSetPaths: %v", err)
		}
		if len(got) != 1 || got[0] != filepath.Join(dir, "ops.yml") {
			t.Errorf("got %v", got)
		}
	})

	t.Run("an existing path is used as given", func(t *testing.T) {
		elsewhere := filepath.Join(t.TempDir(), "elsewhere.yaml")
		writeFile(t, elsewhere, "version: 1\n")
		got, err := cli.ResolveSetPaths(elsewhere, dir)
		if err != nil {
			t.Fatalf("ResolveSetPaths: %v", err)
		}
		if len(got) != 1 || got[0] != elsewhere {
			t.Errorf("got %v, want the path as given", got)
		}
	})

	t.Run("an unknown name names the sets that exist", func(t *testing.T) {
		_, err := cli.ResolveSetPaths("absent", dir)
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "go-docs") || !strings.Contains(err.Error(), "ops") {
			t.Errorf("error = %v, want it to list the known sets", err)
		}
	})

	t.Run("an empty directory says where to put a set", func(t *testing.T) {
		_, err := cli.ResolveSetPaths("", t.TempDir())
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "label set") {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("no configured directory and no argument", func(t *testing.T) {
		_, err := cli.ResolveSetPaths("", "")
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "eval.dir") {
			t.Errorf("error = %v, want it to name the config key", err)
		}
	})
}

func TestEvalOutput(t *testing.T) {
	report := eval.NewReport("go-docs", eval.Run{Mode: "hybrid", TopK: 5},
		[]eval.CaseResult{{ID: "a", Query: "q", Metrics: eval.Score(
			[]eval.Result{{Path: "/n/a.md"}}, []eval.Label{{Path: "a.md", Grade: 1}}, 5)}})
	baseline := eval.NewReport("go-docs", eval.Run{Mode: "hybrid", TopK: 5},
		[]eval.CaseResult{{ID: "a", Query: "q"}})

	t.Run("text", func(t *testing.T) {
		var sb strings.Builder
		if err := cli.EvalOutput(&sb, report, nil, "text", false); err != nil {
			t.Fatalf("EvalOutput: %v", err)
		}
		if !strings.Contains(sb.String(), "go-docs") || !strings.Contains(sb.String(), "hit@5") {
			t.Errorf("output = %s", sb.String())
		}
	})

	t.Run("json round-trips, so it can be the next baseline", func(t *testing.T) {
		var sb strings.Builder
		if err := cli.EvalOutput(&sb, report, nil, "json", false); err != nil {
			t.Fatalf("EvalOutput: %v", err)
		}
		var back eval.Report
		if err := json.Unmarshal([]byte(sb.String()), &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if back.Set != "go-docs" {
			t.Errorf("round-tripped set = %q", back.Set)
		}
	})

	t.Run("text with a baseline prints the run and then the deltas", func(t *testing.T) {
		var sb strings.Builder
		if err := cli.EvalOutput(&sb, report, &baseline, "text", false); err != nil {
			t.Fatalf("EvalOutput: %v", err)
		}
		if !strings.Contains(sb.String(), "hit@5") {
			t.Errorf("want the absolute numbers:\n%s", sb.String())
		}
		if !strings.Contains(sb.String(), "+1.00") {
			t.Errorf("want the delta:\n%s", sb.String())
		}
	})

	t.Run("json with a baseline emits the diff, which carries both sides", func(t *testing.T) {
		var sb strings.Builder
		if err := cli.EvalOutput(&sb, report, &baseline, "json", false); err != nil {
			t.Fatalf("EvalOutput: %v", err)
		}
		var d eval.ReportDiff
		if err := json.Unmarshal([]byte(sb.String()), &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(d.Metrics) == 0 || d.Set != "go-docs" {
			t.Errorf("diff = %+v", d)
		}
	})

	t.Run("a baseline for another set is an error", func(t *testing.T) {
		other := eval.NewReport("ops", eval.Run{Mode: "hybrid", TopK: 5}, nil)
		if err := cli.EvalOutput(&strings.Builder{}, report, &other, "text", false); err == nil {
			t.Fatal("want an error comparing two label sets")
		}
	})

	t.Run("unknown format", func(t *testing.T) {
		if err := cli.EvalOutput(&strings.Builder{}, report, nil, "yaml", false); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestCheckEvalSets(t *testing.T) {
	t.Run("no directory configured", func(t *testing.T) {
		msg, status, sets := cli.CheckEvalSets("")
		if status != "" || len(sets) != 0 {
			t.Errorf("msg=%q status=%q sets=%d", msg, status, len(sets))
		}
		if !strings.Contains(msg, "eval.dir") {
			t.Errorf("msg = %q, want it to name the config key", msg)
		}
	})

	t.Run("directory with no sets is not a fault", func(t *testing.T) {
		// Every knowledge base is in this state until someone writes a set.
		msg, status, sets := cli.CheckEvalSets(t.TempDir())
		if status == "✗" {
			t.Errorf("an empty eval dir should not read as broken: %q", msg)
		}
		if len(sets) != 0 {
			t.Errorf("sets = %d, want 0", len(sets))
		}
	})

	t.Run("counts sets and cases", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "go-docs.yaml"),
			"version: 1\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: p.md\n"+
				"  - id: b\n    query: q\n    relevant:\n      - path: p.md\n")
		writeFile(t, filepath.Join(dir, "ops.yaml"),
			"version: 1\ncases:\n  - id: c\n    query: q\n    relevant:\n      - path: p.md\n")

		msg, status, sets := cli.CheckEvalSets(dir)
		if status != "✓" {
			t.Errorf("status = %q, msg = %q", status, msg)
		}
		if len(sets) != 2 {
			t.Fatalf("sets = %d, want 2", len(sets))
		}
		for _, want := range []string{"go-docs", "ops", "3"} {
			if !strings.Contains(msg, want) {
				t.Errorf("msg = %q, want it to mention %q", msg, want)
			}
		}
	})

	t.Run("a set that will not parse is named", func(t *testing.T) {
		// Otherwise the first anyone hears of it is tbuk eval refusing to run.
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "broken.yaml"), "version: 1\ncases:\n  - id: a\n")
		msg, status, _ := cli.CheckEvalSets(dir)
		if status != "✗" {
			t.Errorf("status = %q, want ✗ for an unloadable set", status)
		}
		if !strings.Contains(msg, "broken") {
			t.Errorf("msg = %q, want it to name the file", msg)
		}
	})
}

func TestCheckEvalLabels(t *testing.T) {
	set := evalSet(t, `
version: 1
name: paths
cases:
  - id: ok
    query: q
    relevant:
      - path: go/slices.md
  - id: absent
    query: q
    relevant:
      - path: go/generics.md
`)

	t.Run("a label naming nothing indexed is the check that earns the section", func(t *testing.T) {
		// It scores zero forever and reads on the report exactly like a
		// retrieval failure, so it has to be said somewhere.
		msg, status := cli.CheckEvalLabels([]eval.Set{set}, []string{"/home/u/go/slices.md"})
		if status != "✗" {
			t.Errorf("status = %q, msg = %q", status, msg)
		}
		if !strings.Contains(msg, "go/generics.md") {
			t.Errorf("msg = %q, want it to name the unindexed path", msg)
		}
	})

	t.Run("all resolved", func(t *testing.T) {
		msg, status := cli.CheckEvalLabels([]eval.Set{set},
			[]string{"/home/u/go/slices.md", "/home/u/go/generics.md"})
		if status != "✓" {
			t.Errorf("status = %q, msg = %q", status, msg)
		}
	})

	t.Run("an ambiguous label is reported too", func(t *testing.T) {
		ambiguous := evalSet(t,
			"version: 1\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: notes.md\n")
		msg, status := cli.CheckEvalLabels([]eval.Set{ambiguous},
			[]string{"/home/u/work/notes.md", "/home/u/personal/notes.md"})
		if status != "✗" {
			t.Errorf("status = %q, msg = %q", status, msg)
		}
		if !strings.Contains(msg, "notes.md") {
			t.Errorf("msg = %q", msg)
		}
	})

	t.Run("no sets, nothing to say", func(t *testing.T) {
		_, status := cli.CheckEvalLabels(nil, []string{"/home/u/a.md"})
		if status == "✗" {
			t.Error("no label sets is not a fault")
		}
	})
}

const genSet = `
version: 1
name: go-docs
cases:
  - id: slices
    query: how do slices grow?
    relevant:
      - path: go/slices.md
    answer: append reallocates when len == cap, roughly doubling capacity.
    must_include: [append, cap]
  - id: retrieval-only
    query: and maps?
    relevant:
      - path: go/maps.md
`

// fixedAnswer answers every case with text, echoing back the chunks it was
// given as the ones that reached the prompt.
func fixedAnswer(text string) cli.AnswerFn {
	return func(_ context.Context, _ string, _ []conversation.Turn, chunks []retrieval.RetrievedChunk) (string, []retrieval.RetrievedChunk, error) {
		return text, chunks, nil
	}
}

func TestRunEval_generationStageScoresAnswers(t *testing.T) {
	var seen [][]string
	report, err := cli.RunEval(context.Background(), evalSet(t, genSet),
		recordingRetriever([]retrieval.RetrievedChunk{chunkAt("/n/go/slices.md")}, &seen),
		cli.EvalOptions{
			Mode: "hybrid", TopK: 5, Stage: eval.StageGeneration,
			Answer:  fixedAnswer("append reallocates when len == cap; the capacity is doubled."),
			Indexed: []string{"/n/go/slices.md"},
		})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}

	if report.Generation == nil {
		t.Fatal("no generation half in the report")
	}
	// Only the case carrying a reference answer is scored; the retrieval-only
	// one has nothing this stage can mark.
	if report.Generation.Overall.Cases != 1 {
		t.Errorf("generation cases = %d, want 1", report.Generation.Overall.Cases)
	}
	if got := report.Generation.Overall.Includes; got != 1 {
		t.Errorf("includes = %.2f, want 1.00 — the answer carries both required substrings", got)
	}
	// The retrieval half was not asked for, so it is not reported.
	if report.Overall.Cases != 0 {
		t.Errorf("Overall.Cases = %d, want 0 under --stage generation", report.Overall.Cases)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].ID != "retrieval-only" {
		t.Errorf("skipped = %+v, want the retrieval-only case", report.Skipped)
	}
	if report.Run.Stage != eval.StageGeneration {
		t.Errorf("Run.Stage = %q, want %q", report.Run.Stage, eval.StageGeneration)
	}
}

func TestRunEval_bothStagesScoreBothHalves(t *testing.T) {
	var seen [][]string
	report, err := cli.RunEval(context.Background(), evalSet(t, genSet),
		recordingRetriever([]retrieval.RetrievedChunk{chunkAt("/n/go/slices.md")}, &seen),
		cli.EvalOptions{
			Mode: "hybrid", TopK: 5, Stage: eval.StageBoth,
			Answer: fixedAnswer("append reallocates when len == cap."),
		})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	// Both cases scored on retrieval; only the one with a reference answer on
	// generation. Attribution is the point: the denominators differ and say so.
	if report.Overall.Cases != 2 {
		t.Errorf("Overall.Cases = %d, want 2", report.Overall.Cases)
	}
	if report.Generation == nil || report.Generation.Overall.Cases != 1 {
		t.Fatalf("Generation = %+v, want one answer scored", report.Generation)
	}
	if len(report.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none — every case was scored by one stage or the other", report.Skipped)
	}
}

func TestRunEval_generationNeedsAnAnswerFunction(t *testing.T) {
	var seen [][]string
	_, err := cli.RunEval(context.Background(), evalSet(t, genSet),
		recordingRetriever(nil, &seen),
		cli.EvalOptions{Mode: "hybrid", TopK: 5, Stage: eval.StageGeneration})
	if err == nil {
		t.Fatal("RunEval scored the generation stage with nothing to answer with")
	}
}

func TestRunEval_generationGroundsOnWhatReachedThePrompt(t *testing.T) {
	// The context ladder can drop passages between retrieval and the prompt.
	// Groundedness asks what the model could have read, so it has to score
	// against what was actually sent.
	var seen [][]string
	report, err := cli.RunEval(context.Background(), evalSet(t, genSet),
		recordingRetriever([]retrieval.RetrievedChunk{chunkAt("/n/go/slices.md")}, &seen),
		cli.EvalOptions{
			Mode: "hybrid", TopK: 5, Stage: eval.StageGeneration,
			Answer: func(_ context.Context, _ string, _ []conversation.Turn, _ []retrieval.RetrievedChunk) (string, []retrieval.RetrievedChunk, error) {
				return "capacity doubled", nil, nil // everything was dropped
			},
		})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if got := report.Generation.Overall.Groundedness; got != 0 {
		t.Errorf("groundedness = %.2f, want 0 — no passage reached the prompt", got)
	}
}

func TestRunEval_judgeScoresAndFailsSoftly(t *testing.T) {
	set := evalSet(t, `
version: 1
name: go-docs
cases:
  - id: a
    query: q1
    answer: a reference
  - id: b
    query: q2
    answer: another reference
`)
	var calls int
	judge := &eval.Judge{Chat: func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		calls++
		ch := make(chan llm.Token, 1)
		if calls == 1 {
			ch <- llm.Token{Text: `{"correctness": 2, "correctness_reason": "right",
				"faithfulness": 0, "faithfulness_reason": "unsupported"}`, Done: true}
		} else {
			ch <- llm.Token{Text: "I think it's fine", Done: true} // not a verdict
		}
		close(ch)
		return ch, nil
	}}

	var seen [][]string
	report, err := cli.RunEval(context.Background(), set,
		recordingRetriever(nil, &seen),
		cli.EvalOptions{
			Mode: "hybrid", TopK: 5, Stage: eval.StageGeneration,
			Answer: fixedAnswer("an answer"), Judge: judge, JudgeModel: "llama/llama3",
		})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	g := report.Generation.Overall
	if g.Judged != 1 {
		t.Fatalf("judged = %d of 2, want 1 — the malformed verdict is unjudged, not zero", g.Judged)
	}
	// Averaged over the one case actually judged, not over both.
	if g.Correctness != 1 || g.Faithfulness != 0 {
		t.Errorf("correctness/faithfulness = %.2f/%.2f, want 1.00/0.00", g.Correctness, g.Faithfulness)
	}
	if report.Cases[0].Judge == nil || report.Cases[0].Judge.Correctness.Reason != "right" {
		t.Errorf("case a judge = %+v, want the verdict and its reason", report.Cases[0].Judge)
	}
	if report.Cases[1].Unjudged == "" {
		t.Error("case b has no reason for being unjudged")
	}
	if report.Run.Judge != "llama/llama3" {
		t.Errorf("Run.Judge = %q, want llama/llama3", report.Run.Judge)
	}
}

func TestRunEval_unresolvedCitationsAreNamed(t *testing.T) {
	set := evalSet(t, `
version: 1
name: go-docs
cases:
  - id: a
    query: q
    answer: a reference
`)
	var seen [][]string
	report, err := cli.RunEval(context.Background(), set,
		recordingRetriever(nil, &seen),
		cli.EvalOptions{
			Mode: "hybrid", TopK: 5, Stage: eval.StageGeneration,
			Answer:  fixedAnswer("See go/slices.md and go/generics.md."),
			Indexed: []string{"/n/go/slices.md"},
		})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if got := report.Cases[0].UnresolvedCitations; len(got) != 1 || got[0] != "go/generics.md" {
		t.Errorf("unresolved citations = %v, want [go/generics.md]", got)
	}
	if got := report.Generation.Overall.Citations; got != 0.5 {
		t.Errorf("citations = %.2f, want 0.50", got)
	}
}

func TestRunEval_generationCaseCarriesItsThread(t *testing.T) {
	set := evalSet(t, `
version: 1
name: go-docs
cases:
  - id: followup
    thread:
      - question: how do slices grow?
        answer: they double
    query: and maps?
    answer: maps rehash their buckets
`)
	var gotThread []conversation.Turn
	var seen [][]string
	if _, err := cli.RunEval(context.Background(), set,
		recordingRetriever(nil, &seen),
		cli.EvalOptions{
			Mode: "hybrid", TopK: 5, Stage: eval.StageGeneration,
			Answer: func(_ context.Context, _ string, thread []conversation.Turn, chunks []retrieval.RetrievedChunk) (string, []retrieval.RetrievedChunk, error) {
				gotThread = thread
				return "maps rehash", chunks, nil
			},
		}); err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if len(gotThread) != 1 || gotThread[0].Question != "how do slices grow?" {
		t.Errorf("thread handed to the answer = %+v, want the one turn behind the question", gotThread)
	}
}

func TestEvalAnswerFn_rendersThePromptAskWouldAndReturnsTheAnswer(t *testing.T) {
	tmpl := buildQATemplate(t)
	var seen []llm.Message
	answer := cli.EvalAnswerFn(tmpl, func(_ context.Context, msgs []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		seen = msgs
		ch := make(chan llm.Token, 2)
		ch <- llm.Token{Text: "a slice "}
		ch <- llm.Token{Text: "doubles", Done: true}
		close(ch)
		return ch, nil
	}, 0, 0)

	chunks := []retrieval.RetrievedChunk{chunkAt("/n/go/slices.md")}
	text, used, err := answer(context.Background(), "how do slices grow?",
		[]conversation.Turn{{Question: "what is a slice?", Answer: "a view over an array"}}, chunks)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if text != "a slice doubles" {
		t.Errorf("answer = %q, want the whole stream joined", text)
	}
	if len(used) != 1 {
		t.Errorf("passages that reached the prompt = %d, want 1", len(used))
	}
	// The question, the thread and the retrieved text all reach the model, the
	// way `ask` sends them — the generation stage has to measure the prompt
	// people actually run.
	joined := ""
	for _, m := range seen {
		joined += m.Content + "\n"
	}
	for _, want := range []string{"how do slices grow?", "what is a slice?", "the capacity is doubled"} {
		if !strings.Contains(joined, want) {
			t.Errorf("prompt is missing %q:\n%s", want, joined)
		}
	}
}

func TestEvalAnswerFn_streamErrorFailsTheCase(t *testing.T) {
	tmpl := buildQATemplate(t)
	answer := cli.EvalAnswerFn(tmpl, func(_ context.Context, _ []llm.Message, _ ...llm.CallOptions) (<-chan llm.Token, error) {
		ch := make(chan llm.Token, 1)
		ch <- llm.Token{Error: errors.New("the provider hung up")}
		close(ch)
		return ch, nil
	}, 0, 0)

	if _, _, err := answer(context.Background(), "q", nil, nil); err == nil {
		t.Fatal("a stream error scored the case instead of failing it")
	}
}

// TestRunEval_recordsTheHost covers D10: latency belongs to a machine, so the
// report names the one that produced it without the caller having to.
func TestRunEval_recordsTheHost(t *testing.T) {
	var seen [][]string
	report, err := cli.RunEval(context.Background(), evalSet(t, twoCaseSet),
		recordingRetriever([]retrieval.RetrievedChunk{chunkAt("/n/go/slices.md")}, &seen),
		cli.EvalOptions{Mode: "keyword", TopK: 5})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}
	if report.Run.Host == "" {
		t.Fatal("the report names no host, so its latency cannot be traced to a machine")
	}
}

// TestRunEval_recordsAQueryPlanThatFellBack is the end of the chain the
// harness was missing: Condense degrades instead of failing, so without this
// a sweep where the model timed out on every case still reports as a condense
// sweep, with numbers that are really the window's.
func TestRunEval_recordsAQueryPlanThatFellBack(t *testing.T) {
	var seen [][]string
	// A planner that always falls back, reporting it the way Condense does.
	planner := fallingBackPlanner{reason: "the model did not answer within 20s"}

	report, err := cli.RunEval(context.Background(), evalSet(t, twoCaseSet),
		recordingRetriever([]retrieval.RetrievedChunk{chunkAt("/n/go/slices.md")}, &seen),
		cli.EvalOptions{Mode: "keyword", TopK: 5, Rewrite: "condense", Planner: planner})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}

	if report.Degraded != 2 {
		t.Fatalf("Degraded = %d, want both cases counted", report.Degraded)
	}
	for _, row := range report.Cases {
		if row.Degraded == "" {
			t.Errorf("case %s records no reason", row.ID)
		}
	}
}

// fallingBackPlanner stands in for a Condense whose model never answers.
type fallingBackPlanner struct{ reason string }

func (p fallingBackPlanner) Queries(_ context.Context, _ []conversation.Turn, question string) ([]string, error) {
	return []string{question}, nil
}

func (p fallingBackPlanner) Fallbacks() []string { return []string{p.reason} }
