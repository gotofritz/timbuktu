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
	"github.com/gotofritz/timbuktu/internal/normalize"
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/rewrite"
	"github.com/gotofritz/timbuktu/internal/search"
)

// AnswerFn produces one case's answer from the passages retrieval found.
//
// It returns the completion and the chunks that actually reached the prompt,
// which are not always the ones retrieval returned: the context ladder drops
// passages to make a prompt fit. Groundedness asks what the model could have
// read, so it has to be scored against what the model was shown.
type AnswerFn func(ctx context.Context, question string, thread []conversation.Turn,
	chunks []retrieval.RetrievedChunk) (string, []retrieval.RetrievedChunk, error)

// EvalOptions configures one run.
type EvalOptions struct {
	Mode    string
	TopK    int
	Rewrite string
	Expand  int
	// Stage is which halves are scored: eval.StageRetrieval (the default and
	// the free one), eval.StageGeneration, or eval.StageBoth.
	Stage string
	// Gold retrieves on each case's gold_query — the standalone question a
	// competent human would have typed — instead of planning one. It is the
	// ceiling a rewrite is trying to reach, and reaching for it costs no model
	// call, which is what makes the comparison worth having.
	Gold   bool
	CaseID string
	// Planner turns a case's thread and its question into the queries
	// retrieval runs. Nil retrieves on the question exactly as written.
	Planner rewrite.Planner
	// Answer produces a case's completion. Required by the generation stage
	// and unused by the retrieval one, which is what keeps a retrieval eval
	// free of a model.
	Answer AnswerFn
	// Judge adds the model's marks to the deterministic scoring. Nil is the
	// default: a metric that needs a model and an API key is a metric that
	// never runs in CI, so it is opt-in and it never replaces the free ones.
	Judge *eval.Judge
	// Indexed is every document path the knowledge base holds, for resolving
	// the citations an answer emitted.
	Indexed []string
	// Embedding, LLM and JudgeModel name the models that answered, for the
	// report's instrument block: two runs are comparable only as far as that
	// matches.
	Embedding  string
	LLM        string
	JudgeModel string
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
	unindexed, err := checkIndexedLabels(set, opts)
	if err != nil {
		return eval.Report{}, err
	}
	if eval.ScoresGeneration(opts.Stage) && opts.Answer == nil {
		return eval.Report{}, fmt.Errorf(
			"eval: the %s stage needs a model to answer with, and none was configured", opts.Stage)
	}
	if err := checkScorableAnswers(set, opts.Stage); err != nil {
		return eval.Report{}, err
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
		// A case neither stage asked for is skipped rather than scored zero: a
		// generation-only case under --stage retrieval has no ranked list to
		// mark, and counted as a zero it would drag every average and read on
		// the report exactly like a retrieval failure.
		scoreRetrieval := eval.ScoresRetrieval(opts.Stage) && len(c.Relevant) > 0
		scoreGeneration := eval.ScoresGeneration(opts.Stage) && scorableAnswer(c)
		if !scoreRetrieval && !scoreGeneration {
			skipped = append(skipped, eval.SkippedCase{ID: c.ID, Reason: nothingToScore(opts.Stage)})
			continue
		}

		planStart := time.Now()
		queries, skip, err := planCase(ctx, c, opts)
		planElapsed := time.Since(planStart)
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

		row := eval.CaseResult{
			ID:        c.ID,
			Query:     c.Query,
			Queries:   queries,
			LatencyMS: float64(elapsed.Microseconds()) / 1000,
			PlanMS:    float64(planElapsed.Microseconds()) / 1000,
			Degraded:  drainFallbacks(opts.Planner),
		}
		if scoreRetrieval {
			row.Metrics = eval.Score(evalResults(chunks), c.Relevant, opts.TopK)
		}
		if scoreGeneration {
			if err := scoreAnswer(ctx, &row, c, chunks, opts); err != nil {
				return eval.Report{}, err
			}
		}
		scored = append(scored, row)
	}

	report := eval.NewReport(set.Name, eval.Run{
		Mode:      opts.Mode,
		TopK:      opts.TopK,
		Rewrite:   rewriteLabel(opts),
		Expand:    opts.Expand,
		Stage:     stageLabel(opts.Stage),
		Embedding: opts.Embedding,
		LLM:       opts.LLM,
		Judge:     opts.JudgeModel,
		Host:      evalHost(),
		At:        time.Now().UTC(),
	}, scored)
	report.Skipped = skipped
	report.UnindexedLabels = unindexed
	return report, nil
}

// checkIndexedLabels resolves the set's labels against the documents the
// knowledge base holds, and refuses the run when none of them resolve.
//
// A label naming a document that was never ingested scores zero on every run,
// and a report of zeroes is indistinguishable from a retrieval failure — so a
// set where *nothing* resolves is not a bad score, it is the wrong corpus or an
// empty one, and scoring it would produce a complete report measuring nothing.
// Some labels missing is a partial corpus: score it, and count them.
//
// An empty Indexed means the caller did not look, which is not the same as
// looking and finding none.
func checkIndexedLabels(set eval.Set, opts EvalOptions) (int, error) {
	if len(opts.Indexed) == 0 || !eval.ScoresRetrieval(opts.Stage) {
		return 0, nil
	}
	missing, _ := set.CheckPaths(opts.Indexed)
	if len(missing) == 0 {
		return 0, nil
	}

	labels := 0
	for _, c := range set.Cases {
		labels += len(c.Relevant)
	}
	if len(missing) < labels {
		return len(missing), nil
	}

	names := make([]string, 0, len(missing))
	seen := make(map[string]bool, len(missing))
	for _, issue := range missing {
		if !seen[issue.Path] {
			seen[issue.Path] = true
			names = append(names, issue.Path)
		}
	}
	return 0, fmt.Errorf(
		"eval: not one of the %s in %q is in the index (%s) — "+
			"the knowledge base is empty or holds a different corpus, and scoring it "+
			"would report zeroes that read as a retrieval failure",
		eval.Plural(labels, "label"), set.Name, nameSome(names))
}

// checkScorableAnswers refuses a generation run over a set with nothing to mark
// an answer against.
//
// Same reasoning as checkIndexedLabels, and the same failure it prevents: under
// --stage both, a set with no reference answers scores its retrieval half
// normally and reports no generation block at all, which on the page is
// indistinguishable from a model that answered nothing. The run costs no model
// call either — every case falls out before the answer is asked for — so
// nothing announces that the stage the user named measured nothing.
//
// Some cases scorable and some not is a partial set, not a broken one: it
// scores what it can, and the skipped list records the rest.
func checkScorableAnswers(set eval.Set, stage string) error {
	if !eval.ScoresGeneration(stage) {
		return nil
	}
	for _, c := range set.Cases {
		if scorableAnswer(c) {
			return nil
		}
	}
	return fmt.Errorf(
		"eval: --stage %s scores answers, but not one of the %s in %q carries an answer or "+
			"must_include to score one against — the generation half would come back empty and "+
			"read exactly like a model that answered nothing; add a reference answer to the cases "+
			"you want marked, or score retrieval alone with --stage retrieval",
		stage, eval.Plural(len(set.Cases), "case"), set.Name)
}

// fallbackReporter is the half of a planner that admits to having degraded.
// Condense cannot fail — it falls back — so without something to ask, a sweep
// whose every model call timed out reports as a condense sweep carrying the
// window's numbers.
type fallbackReporter interface {
	Fallbacks() []string
}

// drainFallbacks takes the reasons the planner gave up for on the case just
// planned, and clears them for the next one. A planner that does not report is
// one that cannot degrade.
func drainFallbacks(p rewrite.Planner) string {
	r, ok := p.(fallbackReporter)
	if !ok {
		return ""
	}
	reasons := r.Fallbacks()
	if len(reasons) == 0 {
		return ""
	}
	return strings.Join(reasons, "; ")
}

// countingPlanner remembers what its planner fell back on, so RunEval can
// record it per case. It is a wrapper rather than a field on the planners
// because expansion composes over a mode, and either half can degrade.
type countingPlanner struct {
	inner   rewrite.Planner
	reasons []string
}

func (p *countingPlanner) Queries(ctx context.Context, thread []conversation.Turn, question string) ([]string, error) {
	return p.inner.Queries(ctx, thread, question) //nolint:wrapcheck // the caller wraps
}

// note records one give-up. It is what rewrite.Options.OnFallback is pointed at.
func (p *countingPlanner) note(reason string) { p.reasons = append(p.reasons, reason) }

// Fallbacks returns and clears the reasons since the last call.
func (p *countingPlanner) Fallbacks() []string {
	out := p.reasons
	p.reasons = nil
	return out
}

// evalHost names the machine the latency figures came from. An unreadable
// hostname is left empty rather than guessed: a wrong name next to a latency
// delta is worse than no name, because the reader would trust it.
func evalHost() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

// scoreAnswer produces the case's answer, marks it, and — under --judge —
// records what the judge made of it.
//
// A failed answer fails the run: an outage is not evidence that an answer was
// bad, and a report over the three cases that happened to succeed is exactly
// the kind of number this harness exists to stop people quoting. A failed
// judge is different, and stops at the case: the deterministic scores are still
// real, and the row says why it went unjudged.
func scoreAnswer(ctx context.Context, row *eval.CaseResult, c eval.Case,
	chunks []retrieval.RetrievedChunk, opts EvalOptions) error {
	start := time.Now()
	text, used, err := opts.Answer(ctx, c.Query, threadOf(c), chunks)
	row.GenLatencyMS = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		return fmt.Errorf("eval: case %q: answer: %w", c.ID, err)
	}

	answer := eval.Answer{Text: text, Passages: evalResults(used)}
	metrics := eval.ScoreAnswer(answer, c, opts.Indexed)
	_, row.UnresolvedCitations = eval.ResolveCitations(text, opts.Indexed)

	if opts.Judge != nil {
		verdict, err := opts.Judge.Judge(ctx, c, answer)
		if err != nil {
			row.Unjudged = err.Error()
		} else {
			row.Judge = &verdict
			metrics = metrics.WithJudgement(verdict)
		}
	}
	row.Generation = &metrics
	return nil
}

// scorableAnswer reports whether the generation stage has anything to mark this
// case's answer against.
func scorableAnswer(c eval.Case) bool {
	return c.Answer != "" || len(c.MustInclude) > 0
}

// nothingToScore names why a case fell out of the run, in the terms of the
// stage that was asked for — "no relevant labels" on a generation-only case
// would send the reader looking for a label set problem that is not there.
func nothingToScore(stage string) string {
	switch {
	case eval.ScoresRetrieval(stage) && eval.ScoresGeneration(stage):
		return "no relevant labels and no reference answer to score against"
	case eval.ScoresGeneration(stage):
		return "no answer or must_include to score the completion against"
	default:
		return "no relevant labels to score retrieval against"
	}
}

// stageLabel is what the report records as the stage that ran. An unset stage
// is the retrieval default, and a report that said nothing would leave an empty
// generation block indistinguishable from a failing one.
func stageLabel(stage string) string {
	if stage == "" {
		return eval.StageRetrieval
	}
	return stage
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

// RunEvalRepeated runs one sweep n times over the same knowledge base and
// summarises what moved between the runs.
//
// Every deterministic knob in this harness returns the same report twice, so
// repeating those proves only that they are deterministic. The knobs that spend
// a model do not: a condense sweep is a model writing a query per case, and a
// single run of one cannot say whether a gain belongs to the setting or to that
// afternoon's sampling. Nothing is ingested between runs — eval only reads —
// so the corpus the runs disagree about is the same corpus.
//
// The reports are returned alongside the spread so a caller can keep the
// individual runs: the spread is the finding, and the runs are the evidence.
func RunEvalRepeated(
	ctx context.Context,
	set eval.Set,
	retrieve retrieverFn,
	opts EvalOptions,
	n int,
) (eval.Spread, []eval.Report, error) {
	if n < 2 {
		return eval.Spread{}, nil, fmt.Errorf(
			"eval: --repeat samples what varies between runs, so it needs at least 2, got %d", n)
	}
	reports := make([]eval.Report, 0, n)
	for i := 0; i < n; i++ {
		report, err := RunEval(ctx, set, retrieve, opts)
		if err != nil {
			return eval.Spread{}, nil, fmt.Errorf("eval: run %d of %d: %w", i+1, n, err)
		}
		reports = append(reports, report)
	}
	spread, err := eval.NewSpread(reports)
	if err != nil {
		return eval.Spread{}, nil, err //nolint:wrapcheck // already "eval: …"
	}
	return spread, reports, nil
}

// EvalSpreadOutput writes a spread in the requested format.
func EvalSpreadOutput(out io.Writer, spread eval.Spread, format string) error {
	switch format {
	case "text":
		return spread.WriteText(out) //nolint:wrapcheck // already "eval: write spread: …"
	case "json":
		return spread.WriteJSON(out) //nolint:wrapcheck // already "eval: write spread: …"
	default:
		return fmt.Errorf("eval: invalid format %q: must be text or json", format)
	}
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

// EvalAnswerFn returns the answer function the generation stage runs: the same
// prompt `ask` renders, the same context ladder, the same normalization, and no
// output.
//
// It is `ask` without the terminal rather than a second answering path, because
// the generation stage measures the answers people actually get. A prompt
// assembled differently here would measure a prompt nobody runs, and the
// difference would be invisible in the numbers.
//
// Exported for testing.
func EvalAnswerFn(tmpl *prompts.Template, chat chatFn, contextWindow, outputReserve int) AnswerFn {
	return func(ctx context.Context, question string, thread []conversation.Turn,
		chunks []retrieval.RetrievedChunk) (string, []retrieval.RetrievedChunk, error) {
		manifest := tmpl.Manifest()

		variables := make(map[string]string, len(manifest.Variables))
		for name, v := range manifest.Variables {
			variables[name] = v.Default
		}
		chunks = trimToTokenBudget(chunks, manifest.Retrieval.MaxTokens)

		build := func(hs []conversation.Turn, cs []retrieval.RetrievedChunk) ([]llm.Message, error) {
			system, user, err := tmpl.Render(prompts.TemplateData{
				Question: question, Chunks: cs, Variables: variables})
			if err != nil {
				return nil, fmt.Errorf("render template: %w", err)
			}
			return conversation.Messages(system, user, hs), nil
		}

		window := manifest.ContextTokens
		if window <= 0 {
			window = contextWindow
		}
		reserve := manifest.MaxTokens
		if reserve <= 0 {
			reserve = outputReserve
		}

		fitted := fittedPrompt{chunks: chunks}
		var err error
		if window > 0 {
			fitted, err = fitToContext(build, thread, chunks, window-reserve)
		} else {
			fitted.messages, err = build(thread, chunks)
		}
		if err != nil {
			return "", nil, err
		}

		tokens, err := chat(ctx, fitted.messages, llm.CallOptions{
			Model: manifest.Model, Temperature: manifest.Temperature, MaxTokens: manifest.MaxTokens})
		if err != nil {
			return "", nil, fmt.Errorf("LLM chat: %w", err)
		}
		var sb strings.Builder
		for tok := range tokens {
			if tok.Error != nil {
				return "", nil, fmt.Errorf("LLM stream: %w", tok.Error)
			}
			sb.WriteString(tok.Text)
			if tok.Done {
				break
			}
		}
		// The normalized text, not the raw completion: a case is scored on what
		// the user would have seen.
		text, err := normalize.Apply(sb.String(), manifest.Normalize)
		if err != nil {
			return "", nil, fmt.Errorf("normalize output: %w", err)
		}
		return strings.TrimSpace(text), fitted.chunks, nil
	}
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
		stage        string
		judgeFlag    bool
		baselinePath string
		format       string
		verbose      bool
		repeat       int
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
no model call, so the gap a rewrite has left to close is free to measure.

--stage picks what is scored. Retrieval is the default and costs no model call
at all; generation answers each case and marks the completion, which costs one.
They are separate because attribution is: an answer that got worse because
retrieval got worse is a different bug from one that got worse on the same
evidence.

  tbuk eval go-docs --stage both
  tbuk eval go-docs --stage generation --judge --verbose

--repeat N runs the same sweep N times and reports the spread — mean, min, max
and standard deviation per metric — instead of one run's numbers, naming the
cases that did not score the same every time. Every deterministic mode returns
the same report every time, so this is for the ones that spend a model: a
condense sweep is a model writing a query per case, and one run of it cannot
say whether a gain is the setting or the sampling. Nothing is ingested between
runs, so the corpus the runs disagree about is one corpus.

  tbuk eval go-docs --rewrite condense --repeat 3

--judge adds a model's marks — correctness against the case's answer, and
faithfulness against the passages — to the deterministic scoring, which always
runs alongside it. The judge's prompt is versioned with this binary rather than
configurable, so two runs cannot be scored by two different instruments;
--judge --verbose prints it.`,
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
			if err := eval.ValidateStage(stage); err != nil {
				return fmt.Errorf("--stage: %w", err)
			}
			// A repeat of one is a single run, not a spread of one: the
			// summary would print a standard deviation of zero, which states
			// something false in the language of a measurement.
			if repeat < 1 {
				return fmt.Errorf("--repeat must be at least 1, got %d", repeat)
			}
			// A judge with no answers to judge would open a model connection,
			// score nothing, and print a report that looks exactly like one
			// where the judge disagreed with nothing.
			if judgeFlag && !eval.ScoresGeneration(stage) {
				return fmt.Errorf("--judge marks the generation stage, but --stage is %q; "+
					"add --stage generation or --stage both", stage)
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
				Gold: gold, CaseID: caseID, Stage: stage,
			}
			if mode != "keyword" {
				runOpts.Embedding = modelLabel(cfg.Embedding.Provider, cfg.Embedding.Model)
			}

			// Zero, not the template's: a grader that disagrees with itself
			// between two runs of the same set is not a measuring instrument.
			judgeTemperature := 0.0
			// The indexed paths are read once for the whole run rather than a
			// query a case: the index does not change under an eval (D7). Both
			// stages want them — generation resolves citations against them,
			// and retrieval checks that the labels name documents that are
			// actually there, without which an empty corpus scores zero and
			// reads as a retrieval failure.
			docs, err := app.Docs().List(cmd.Context())
			if err != nil {
				return fmt.Errorf("eval: read the indexed documents: %w", err)
			}
			runOpts.Indexed = make([]string, len(docs))
			for i, d := range docs {
				runOpts.Indexed[i] = d.Path
			}

			if eval.ScoresGeneration(stage) {
				tmpl, err := prompts.NewTemplateDir(cfg.Prompts.Dir).Load(templateName)
				if err != nil {
					return fmt.Errorf("load template %q: %w", templateName, err)
				}
				l, err := app.LLM()
				if err != nil {
					return err
				}
				runOpts.Answer = EvalAnswerFn(tmpl, l.Chat, cfg.LLM.ContextTokens, cfg.LLM.MaxTokens)
				runOpts.LLM = modelLabel(cfg.LLM.Provider, cfg.LLM.Model)

				if judgeFlag {
					manifest := tmpl.Manifest()
					// The judge is spent at the template's model, at a
					// temperature of its own.
					runOpts.Judge = &eval.Judge{
						Chat: l.Chat,
						Opts: llm.CallOptions{Model: manifest.Model, Temperature: &judgeTemperature},
					}
					runOpts.JudgeModel = modelLabel(cfg.LLM.Provider, cfg.LLM.Model)
					if verbose {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
							"the judge is asked this, and nothing else:\n\n%s\n\n", eval.JudgeSystem)
					}
				}
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

				// The counter is wired before the planner is built, so a
				// rewrite that gives up is recorded on the case it gave up on
				// rather than only warned about on stderr, where it scrolls
				// past and the report still reads as a clean measurement.
				counter := &countingPlanner{}
				plannerOpts.OnFallback = counter.note

				planner, err := rewrite.New(plannerOpts)
				if err != nil {
					return fmt.Errorf("plan queries: %w", err)
				}
				counter.inner = planner
				runOpts.Planner = counter
			}

			out := cmd.OutOrStdout()
			for i, set := range sets {
				if i > 0 {
					if _, err := io.WriteString(out, "\n"); err != nil {
						return fmt.Errorf("eval: write report: %w", err)
					}
				}
				if repeat > 1 {
					spread, _, err := RunEvalRepeated(cmd.Context(), set, ret.RetrieveMany, runOpts, repeat)
					if err != nil {
						return err
					}
					if err := EvalSpreadOutput(out, spread, format); err != nil {
						return err
					}
					continue
				}
				report, err := RunEval(cmd.Context(), set, ret.RetrieveMany, runOpts)
				if err != nil {
					return err
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
	cmd.Flags().StringVar(&stage, "stage", eval.StageRetrieval,
		"what to score: retrieval | generation | both (generation costs a model call a case)")
	cmd.Flags().BoolVar(&judgeFlag, "judge", false,
		"add an LLM judge's correctness and faithfulness marks to the generation stage")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "a previous --format json report to diff against")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "print a row per case, and the skipped ones")
	cmd.Flags().IntVar(&repeat, "repeat", 1,
		"run the sweep this many times and report the spread instead of one run's numbers")
	cmd.MarkFlagsMutuallyExclusive("gold", "rewrite")
	// A spread has no single report for a baseline to be diffed against, and
	// picking one of the runs to compare would be picking the answer.
	cmd.MarkFlagsMutuallyExclusive("repeat", "baseline")
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
