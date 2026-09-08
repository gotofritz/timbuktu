package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/conversation"
	"github.com/gotofritz/timbuktu/internal/embeddings"
	"github.com/gotofritz/timbuktu/internal/eval"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/rewrite"
	"github.com/gotofritz/timbuktu/internal/search"
)

// EvalOptions configures one run of the retrieval stage.
type EvalOptions struct {
	Mode    string
	TopK    int
	Rewrite string
	Expand  int
	// Gold retrieves on each case's gold_query — the standalone question a
	// competent human would have typed — instead of planning one. It is the
	// ceiling a rewrite is trying to reach, and reaching for it costs no model
	// call, which is what makes the comparison worth having.
	Gold   bool
	CaseID string
	// Planner turns a case's thread and its question into the queries
	// retrieval runs. Nil retrieves on the question exactly as written.
	Planner rewrite.Planner
	// Embedding and LLM name the models that answered, for the report's
	// instrument block: two runs are comparable only as far as that matches.
	Embedding string
	LLM       string
}

// RunEval scores a label set's retrieval and returns the report.
//
// It is the testable core of `tbuk eval`: everything it needs arrives as an
// argument, so the whole command is exercised without a database, an embedding
// server or a model.
//
// It reads the knowledge base and writes nothing to it. A case's thread is
// replayed to the planner exactly as a stored one would be, and is never
// persisted — an eval run that left conversation threads behind would change
// the corpus it is supposed to be measuring.
func RunEval(ctx context.Context, set eval.Set, retrieve retrieverFn, opts EvalOptions) (eval.Report, error) {
	if opts.CaseID != "" {
		if err := requireCase(set, opts.CaseID); err != nil {
			return eval.Report{}, err
		}
	}

	var (
		scored  []eval.CaseResult
		skipped []eval.SkippedCase
	)
	for _, c := range set.Cases {
		// A filtered-out case is not a skipped one: nothing was asked of it.
		if opts.CaseID != "" && c.ID != opts.CaseID {
			continue
		}
		// Scoring a generation-only case for retrieval is a question with no
		// answer. Counted as a zero it would drag every average and read on the
		// report exactly like a retrieval failure.
		if len(c.Relevant) == 0 {
			skipped = append(skipped, eval.SkippedCase{
				ID: c.ID, Reason: "no relevant labels to score retrieval against"})
			continue
		}

		queries, skip, err := planCase(ctx, c, opts)
		if err != nil {
			return eval.Report{}, err
		}
		if skip != "" {
			skipped = append(skipped, eval.SkippedCase{ID: c.ID, Reason: skip})
			continue
		}

		start := time.Now()
		chunks, err := retrieve(ctx, queries, opts.TopK, nil)
		elapsed := time.Since(start)
		if err != nil {
			return eval.Report{}, fmt.Errorf("eval: case %q: retrieve: %w", c.ID, err)
		}

		scored = append(scored, eval.CaseResult{
			ID:        c.ID,
			Query:     c.Query,
			Queries:   queries,
			Metrics:   eval.Score(evalResults(chunks), c.Relevant, opts.TopK),
			LatencyMS: float64(elapsed.Microseconds()) / 1000,
		})
	}

	report := eval.NewReport(set.Name, eval.Run{
		Mode:      opts.Mode,
		TopK:      opts.TopK,
		Rewrite:   rewriteLabel(opts),
		Expand:    opts.Expand,
		Embedding: opts.Embedding,
		LLM:       opts.LLM,
		At:        time.Now().UTC(),
	}, scored)
	report.Skipped = skipped
	return report, nil
}

// planCase returns the queries to retrieve on, or the reason this case cannot
// be scored under these options.
func planCase(ctx context.Context, c eval.Case, opts EvalOptions) (queries []string, skip string, err error) {
	if opts.Gold {
		if c.GoldQuery == "" {
			return nil, "no gold_query to measure the ceiling against", nil
		}
		return []string{c.GoldQuery}, "", nil
	}
	if opts.Planner == nil {
		return []string{c.Query}, "", nil
	}
	planned, err := opts.Planner.Queries(ctx, threadOf(c), c.Query)
	if err != nil {
		return nil, "", fmt.Errorf("eval: case %q: plan query: %w", c.ID, err)
	}
	// A plan of nothing usable leaves the question standing, exactly as `ask`
	// does: an empty query matches the whole corpus in rank order.
	if usable := nonBlank(planned); len(usable) > 0 {
		return usable, "", nil
	}
	return []string{c.Query}, "", nil
}

// rewriteLabel is what the report records as the query planning that ran. The
// ceiling is not a planner, so it gets a name of its own rather than being
// filed under whichever mode happened to be set.
func rewriteLabel(opts EvalOptions) string {
	if opts.Gold {
		return "gold"
	}
	return opts.Rewrite
}

// requireCase fails before any search runs when --case names nothing, listing
// what the set does hold — a typo should cost a message, not an empty report.
func requireCase(set eval.Set, id string) error {
	ids := make([]string, 0, len(set.Cases))
	for _, c := range set.Cases {
		if c.ID == id {
			return nil
		}
		ids = append(ids, c.ID)
	}
	return fmt.Errorf("eval: label set %q has no case %q; it has: %s",
		set.Name, id, strings.Join(ids, ", "))
}

// evalResults reduces retrieved chunks to what scoring needs of them.
func evalResults(chunks []retrieval.RetrievedChunk) []eval.Result {
	out := make([]eval.Result, len(chunks))
	for i, ch := range chunks {
		out[i] = eval.Result{Path: ch.Path, Text: ch.Text}
	}
	return out
}

// threadOf turns a case's written thread into the turns a planner expects.
// These exist for the length of one case and are never stored (D7).
func threadOf(c eval.Case) []conversation.Turn {
	turns := make([]conversation.Turn, len(c.Thread))
	for i, t := range c.Thread {
		turns[i] = conversation.Turn{Question: t.Question, Answer: t.Answer}
	}
	return turns
}

// setExtensions are the file extensions a label set is looked for under.
var setExtensions = []string{".yaml", ".yml"}

// ResolveSetPaths turns the command's argument into the label-set files to run.
//
// An argument that names an existing file is used as given, so a set can live
// anywhere; otherwise it is a name looked up under dir. With no argument at all
// every set in dir runs, sorted, so a multi-set run reports in a stable order.
func ResolveSetPaths(arg, dir string) ([]string, error) {
	if arg != "" {
		if info, err := os.Stat(arg); err == nil && !info.IsDir() {
			return []string{arg}, nil
		}
	}
	if dir == "" {
		return nil, fmt.Errorf("eval: no label set given and eval.dir is not set; " +
			"pass a path, or point eval.dir at a directory of label sets")
	}

	found, err := labelSetsIn(dir)
	if err != nil {
		return nil, err
	}
	if arg == "" {
		if len(found) == 0 {
			return nil, fmt.Errorf("eval: no label set found in %s; write one there, "+
				"or pass the path to one", dir)
		}
		return found, nil
	}

	for _, ext := range setExtensions {
		candidate := filepath.Join(dir, arg+ext)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return []string{candidate}, nil
		}
	}

	names := make([]string, len(found))
	for i, p := range found {
		base := filepath.Base(p)
		names[i] = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("eval: no label set named %q, and none in %s", arg, dir)
	}
	return nil, fmt.Errorf("eval: no label set named %q in %s; it has: %s",
		arg, dir, strings.Join(names, ", "))
}

// labelSetsIn lists the label-set files in dir, sorted. A directory that is not
// there yet is empty rather than an error: eval.dir has a default long before
// anyone writes a set into it.
func labelSetsIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("eval: read %s: %w", dir, err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".yaml" || ext == ".yml" {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(found)
	return found, nil
}

// EvalOutput renders a report, against a baseline when one is given.
//
// Text prints the absolute numbers and then the deltas, because the question
// "is this better" is usually asked alongside "and how good is it". JSON prints
// the diff alone — it already carries both sides' values, so nothing is lost,
// and a report without a baseline stays exactly the document --baseline reads,
// which is what lets one run become the next run's baseline.
func EvalOutput(out io.Writer, report eval.Report, baseline *eval.Report, format string, verbose bool) error {
	switch format {
	case "text", "json":
	default:
		return fmt.Errorf("eval: invalid format %q: must be text or json", format)
	}

	if baseline == nil {
		if format == "json" {
			return report.WriteJSON(out)
		}
		return report.WriteText(out, verbose)
	}

	diff, err := eval.Diff(report, *baseline)
	if err != nil {
		return err //nolint:wrapcheck // already "eval: baseline is for label set …"
	}
	if format == "json" {
		data, err := json.MarshalIndent(diff, "", "  ")
		if err != nil {
			return fmt.Errorf("eval: render diff as JSON: %w", err)
		}
		if _, err := out.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("eval: write diff: %w", err)
		}
		return nil
	}
	if err := report.WriteText(out, verbose); err != nil {
		return err //nolint:wrapcheck // already "eval: write report: …"
	}
	if _, err := io.WriteString(out, "\n"); err != nil {
		return fmt.Errorf("eval: write diff: %w", err)
	}
	return diff.WriteText(out) //nolint:wrapcheck // already "eval: write diff: …"
}

// LoadBaselineReport reads a report written by a previous --format json run.
func LoadBaselineReport(path string) (eval.Report, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the user's own argument
	if err != nil {
		return eval.Report{}, fmt.Errorf("eval: read baseline: %w", err)
	}
	var report eval.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return eval.Report{}, fmt.Errorf("eval: parse baseline %s: %w "+
			"(a baseline is a report written by `tbuk eval --format json`)", path, err)
	}
	return report, nil
}

// modeSearcher adapts a search.Searcher to the single-method interface the
// retriever wants, running whichever leg --mode names.
//
// It exists so `tbuk eval` sweeps the search mode without RetrieveMany or
// FuseRRF learning about modes: the fusion of several queries is the same
// arithmetic whichever leg produced the rankings.
type modeSearcher struct {
	searcher *search.Searcher
	mode     string
}

func (m modeSearcher) Hybrid(ctx context.Context, query string, opts search.Options) ([]search.SearchResult, error) {
	switch m.mode {
	case "vector":
		return m.searcher.Vector(ctx, query, opts) //nolint:wrapcheck // the retriever wraps
	case "keyword":
		return m.searcher.Keyword(ctx, query, opts) //nolint:wrapcheck // the retriever wraps
	default:
		return m.searcher.Hybrid(ctx, query, opts) //nolint:wrapcheck // the retriever wraps
	}
}

// modelLabel names a model for the report's instrument block.
func modelLabel(provider, model string) string {
	if model == "" {
		return provider
	}
	return provider + "/" + model
}

func newEvalCmd() *cobra.Command {
	var (
		mode         string
		topK         int
		templateName string
		rewriteFlag  string
		expandFlag   int
		gold         bool
		caseID       string
		baselinePath string
		format       string
		verbose      bool
	)

	cmd := &cobra.Command{
		Use:   "eval [set]",
		Short: "Score retrieval against a labelled set of cases",
		Long: `Score retrieval against a labelled set of cases.

The set is a path, or a name under eval.dir; with no argument every set in
eval.dir runs. Nothing is written to the knowledge base — eval searches it, and
a case's thread is replayed to the query planner without ever being stored.

--mode, --top, --rewrite and --expand mean what they mean on search and ask, so
comparing two settings is two runs of one command rather than two builds:

  tbuk eval go-docs --format json > before.json
  tbuk eval go-docs --rewrite condense --baseline before.json

--gold retrieves on each case's gold_query instead — the standalone question a
human would have typed, and the ceiling a rewrite is trying to reach. It costs
no model call, so the gap a rewrite has left to close is free to measure.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("invalid format %q: must be text or json", format)
			}
			switch mode {
			case "vector", "keyword", "hybrid":
			default:
				return fmt.Errorf("invalid mode %q: must be vector, keyword, or hybrid", mode)
			}
			if err := rewrite.ValidateMode(rewriteFlag); err != nil {
				return fmt.Errorf("--rewrite: %w", err)
			}
			if err := rewrite.ValidateExpand(expandFlag); err != nil {
				return fmt.Errorf("--expand: %w", err)
			}

			cfg := configFrom(cmd)
			var arg string
			if len(args) == 1 {
				arg = args[0]
			}
			paths, err := ResolveSetPaths(arg, cfg.Eval.Dir)
			if err != nil {
				return err
			}
			// One baseline is one set's history. Diffing several sets against
			// it would compare a corpus with a different corpus four times.
			if baselinePath != "" && len(paths) > 1 {
				return fmt.Errorf("--baseline compares one label set, but %d are about to run; "+
					"name the set to compare", len(paths))
			}

			// Label sets are parsed before the knowledge base is opened, so a
			// typo in one fails on its own terms rather than behind a
			// connection error.
			sets := make([]eval.Set, len(paths))
			for i, p := range paths {
				if sets[i], err = eval.LoadSet(p); err != nil {
					return err
				}
			}

			var baseline *eval.Report
			if baselinePath != "" {
				loaded, err := LoadBaselineReport(baselinePath)
				if err != nil {
					return err
				}
				baseline = &loaded
			}

			app, err := openApp(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = app.Close() }()

			// Keyword retrieval needs no embedder at all, which is what makes
			// a keyword-mode eval free to run anywhere, CI included.
			var emb embeddings.Embedder
			if mode == "vector" || mode == "hybrid" {
				if emb, err = app.Embedder(); err != nil {
					return err
				}
			}
			ret := retrieval.New(modeSearcher{searcher: search.New(app.DB(), emb), mode: mode})

			runOpts := EvalOptions{
				Mode: mode, TopK: topK, Rewrite: rewriteFlag, Expand: expandFlag,
				Gold: gold, CaseID: caseID,
			}
			if mode != "keyword" {
				runOpts.Embedding = modelLabel(cfg.Embedding.Provider, cfg.Embedding.Model)
			}

			// The planner is built last, and each of its dependencies only when
			// something actually spends it.
			if !gold && (rewriteFlag != rewrite.ModeOff || expandFlag > 0) {
				needsModel := rewriteFlag == rewrite.ModeCondense || expandFlag > 0
				plannerOpts := rewrite.Options{
					Mode:   rewriteFlag,
					Expand: expandFlag,
					Warn:   cmd.ErrOrStderr(),
				}

				// A template is read only when something needs what it holds:
				// the model and temperature a rewrite spends, or a
				// window_turns the user asked for by naming a template. A
				// window-mode eval therefore needs no prompt directory at all,
				// which is what keeps `--mode keyword` free to run anywhere —
				// CI included, where there is no model and no reason to have
				// installed templates.
				if needsModel || cmd.Flags().Changed("template") {
					tmpl, err := prompts.NewTemplateDir(cfg.Prompts.Dir).Load(templateName)
					if err != nil {
						return fmt.Errorf("load template %q: %w", templateName, err)
					}
					manifest := tmpl.Manifest()
					plannerOpts.WindowTurns = manifest.Retrieval.WindowTurns
					plannerOpts.CallOptions = llm.CallOptions{
						Model: manifest.Model, Temperature: manifest.Temperature}
				}
				if needsModel {
					l, err := app.LLM()
					if err != nil {
						return err
					}
					plannerOpts.Chat = l.Chat
					runOpts.LLM = modelLabel(cfg.LLM.Provider, cfg.LLM.Model)
				}

				planner, err := rewrite.New(plannerOpts)
				if err != nil {
					return fmt.Errorf("plan queries: %w", err)
				}
				runOpts.Planner = planner
			}

			out := cmd.OutOrStdout()
			for i, set := range sets {
				report, err := RunEval(cmd.Context(), set, ret.RetrieveMany, runOpts)
				if err != nil {
					return err
				}
				if i > 0 {
					if _, err := io.WriteString(out, "\n"); err != nil {
						return fmt.Errorf("eval: write report: %w", err)
					}
				}
				if err := EvalOutput(out, report, baseline, format, verbose); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&mode, "mode", "hybrid", "search mode: vector, keyword, or hybrid")
	cmd.Flags().IntVar(&topK, "top", search.DefaultTopK, "retrieval depth the metrics are cut at")
	cmd.Flags().StringVarP(&templateName, "template", "t", "qa", "template whose model and temperature a rewrite spends")
	cmd.Flags().StringVar(&rewriteFlag, "rewrite", rewrite.ModeWindow, "how the retrieval query is planned: off | window | condense")
	cmd.Flags().IntVar(&expandFlag, "expand", 0, "extra wordings of the query to retrieve on and fuse (0 = off)")
	cmd.Flags().BoolVar(&gold, "gold", false, "retrieve on each case's gold_query — the ceiling a rewrite is reaching for")
	cmd.Flags().StringVar(&caseID, "case", "", "score only the case with this id")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "a previous --format json report to diff against")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "print a row per case, and the skipped ones")
	cmd.MarkFlagsMutuallyExclusive("gold", "rewrite")
	return cmd
}

// maxNamed bounds how many names a doctor line prints before it says "and N
// more". A line that wraps four times is a line nobody reads.
const maxNamed = 5

// CheckEvalSets reports the label sets under dir, and returns the ones that
// loaded so the labels can be checked against the index.
//
// A set that will not parse is named here rather than left for `tbuk eval` to
// refuse over: doctor is where a knowledge base is asked what is wrong with it.
// Exported for testing.
func CheckEvalSets(dir string) (msg, status string, sets []eval.Set) {
	if dir == "" {
		return "not configured — set eval.dir to a directory of label sets", "", nil
	}
	paths, err := labelSetsIn(dir)
	if err != nil {
		return "not checked (" + err.Error() + ")", "", nil
	}
	if len(paths) == 0 {
		// Every knowledge base is in this state until someone writes a set, so
		// it is a fact rather than a fault.
		return "none in " + dir, "", nil
	}

	var (
		names  []string
		broken []string
		cases  int
	)
	for _, p := range paths {
		set, err := eval.LoadSet(p)
		if err != nil {
			base := filepath.Base(p)
			broken = append(broken, strings.TrimSuffix(base, filepath.Ext(base)))
			continue
		}
		sets = append(sets, set)
		names = append(names, set.Name)
		cases += len(set.Cases)
	}

	if len(broken) > 0 {
		return fmt.Sprintf("%d will not load (%s) — tbuk eval cannot run them",
			len(broken), strings.Join(broken, ", ")), "✗", sets
	}
	return fmt.Sprintf("%s (%s) — %s", eval.Plural(len(names), "set"),
		strings.Join(names, ", "), eval.Plural(cases, "case")), "✓", sets
}

// CheckEvalLabels resolves every label path against the indexed documents.
//
// This is the check that earns the section. A label naming a document that was
// never ingested scores zero on every run and reads on the report exactly like
// a retrieval failure, so the difference between "the retriever missed it" and
// "it was never there" has to be said out loud somewhere. An ambiguous label is
// the same problem wearing the opposite hat: it would be scored against
// whichever document happened to sort first.
//
// Exported for testing.
func CheckEvalLabels(sets []eval.Set, indexed []string) (msg, status string) {
	if len(sets) == 0 {
		return "no label sets to check", ""
	}
	var missing, ambiguous []string
	for _, set := range sets {
		m, a := set.CheckPaths(indexed)
		for _, issue := range m {
			missing = append(missing, issue.Path)
		}
		for _, issue := range a {
			ambiguous = append(ambiguous, issue.Path)
		}
	}
	if len(missing) == 0 && len(ambiguous) == 0 {
		return "every labelled document is in the index", "✓"
	}

	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("%d not in the index (%s)",
			len(missing), nameSome(missing)))
	}
	if len(ambiguous) > 0 {
		parts = append(parts, fmt.Sprintf("%d matching several documents (%s)",
			len(ambiguous), nameSome(ambiguous)))
	}
	return strings.Join(parts, "; "), "✗"
}

// nameSome lists up to maxNamed names, counting the rest.
func nameSome(names []string) string {
	if len(names) <= maxNamed {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxNamed], ", "), len(names)-maxNamed)
}
