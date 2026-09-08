package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/gotofritz/timbuktu/internal/llm"
)

// GenMetrics is how one answer, or a whole set of them, scored on generation.
//
// Every rate carries the number of cases behind it, because a rate with no
// denominator is the one number this harness must never print. A case with no
// must_include scores 0 out of 0; averaged in as a zero it reads exactly like
// an answer that omitted everything it was asked for, and the reader has no way
// to tell the two apart.
type GenMetrics struct {
	Includes     float64 `json:"includes"`
	Citations    float64 `json:"citations"`
	Groundedness float64 `json:"groundedness"`
	// Correctness and Faithfulness are the judge's marks, rescaled from the
	// 0–JudgeScale scale it grades on. They are zero and uncounted unless the
	// judge ran and returned something usable.
	Correctness  float64 `json:"correctness"`
	Faithfulness float64 `json:"faithfulness"`

	Cases int `json:"cases"`
	// With* are the cases each rate averages over: the ones that had something
	// to score at all.
	WithIncludes  int `json:"with_includes"`
	WithCitations int `json:"with_citations"`
	WithWords     int `json:"with_words"`
	Judged        int `json:"judged"`
}

// Answer is what the generation stage produced for one case, reduced to what
// scoring needs of it: the completion, and the evidence it was given.
type Answer struct {
	Text string
	// Passages are the retrieved chunks that reached the prompt — not the ones
	// retrieval returned, when the context budget dropped some. Groundedness
	// asks what the model could have read, so it has to be what it was shown.
	Passages []Result
}

// ScoreAnswer marks one answer against its case, with no model involved.
//
// indexed is every document path the knowledge base holds, for resolving the
// citations the answer emitted. An empty index resolves nothing, which is true
// rather than convenient: an empty knowledge base can ground no citation.
func ScoreAnswer(a Answer, c Case, indexed []string) GenMetrics {
	m := GenMetrics{Cases: 1}

	if n := len(c.MustInclude); n > 0 {
		found := 0
		for _, want := range c.MustInclude {
			if MatchesText(want, a.Text) {
				found++
			}
		}
		m.Includes = float64(found) / float64(n)
		m.WithIncludes = 1
	}

	if resolved, unresolved := ResolveCitations(a.Text, indexed); len(resolved)+len(unresolved) > 0 {
		m.Citations = float64(len(resolved)) / float64(len(resolved)+len(unresolved))
		m.WithCitations = 1
	}

	if words := contentWords(a.Text); len(words) > 0 {
		var evidence strings.Builder
		for _, p := range a.Passages {
			evidence.WriteString(p.Text)
			evidence.WriteByte(' ')
		}
		in := wordSet(contentWords(evidence.String()))
		grounded := 0
		for _, w := range words {
			if in[w] {
				grounded++
			}
		}
		m.Groundedness = float64(grounded) / float64(len(words))
		m.WithWords = 1
	}
	return m
}

// AggregateGen macro-averages per-case generation metrics, each rate over the
// cases that had something to score rather than over all of them — so a set
// where half the cases carry must_include reports what those halves scored,
// not that number halved.
func AggregateGen(ms []GenMetrics) GenMetrics {
	var out GenMetrics
	for _, m := range ms {
		out.Includes += m.Includes
		out.Citations += m.Citations
		out.Groundedness += m.Groundedness
		out.Correctness += m.Correctness
		out.Faithfulness += m.Faithfulness

		out.Cases += m.Cases
		out.WithIncludes += m.WithIncludes
		out.WithCitations += m.WithCitations
		out.WithWords += m.WithWords
		out.Judged += m.Judged
	}
	out.Includes = mean(out.Includes, out.WithIncludes)
	out.Citations = mean(out.Citations, out.WithCitations)
	out.Groundedness = mean(out.Groundedness, out.WithWords)
	out.Correctness = mean(out.Correctness, out.Judged)
	out.Faithfulness = mean(out.Faithfulness, out.Judged)
	return out
}

// mean divides by n, reading a denominator of nothing as a rate of nothing
// rather than as NaN — a NaN in a report is a number nobody can diff.
func mean(total float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return total / float64(n)
}

// ResolveCitations splits the documents an answer named into those the index
// holds and those it does not.
//
// The unresolved half is what --verbose prints. A citations score below 1 is
// only actionable if you can see which name the index did not recognise: a
// hallucinated source and a filename mentioned in passing look identical as a
// number and nothing alike as a list.
func ResolveCitations(text string, indexed []string) (resolved, unresolved []string) {
	for _, cite := range ExtractCitations(text) {
		found := false
		for _, doc := range indexed {
			if MatchesPath(cite, doc) {
				found = true
				break
			}
		}
		if found {
			resolved = append(resolved, cite)
		} else {
			unresolved = append(unresolved, cite)
		}
	}
	return resolved, unresolved
}

// ExtractCitations returns the documents an answer named, deduplicated and in
// the order it named them.
//
// A citation is a filename-shaped token: something with a two-to-eight
// character extension carrying a letter, on a base of at least two characters.
// That deliberately excludes `e.g.`, `i.e.` and version numbers, and just as
// deliberately keeps a bare `notes.md` — a model shown `Source: path §index`
// cites by path, sometimes with the marker and sometimes without.
//
// It is a heuristic and it says so: prose naming a file the corpus does not
// hold ("edit your go.mod") reads as an unresolved citation. --verbose prints
// what was counted, so a surprising number can be traced rather than argued
// with.
func ExtractCitations(text string) []string {
	var (
		out  []string
		seen = map[string]bool{}
	)
	for _, tok := range strings.FieldsFunc(text, isCitationBreak) {
		tok = strings.Trim(tok, `.,;:!?"'`+"`*([{<>}])")
		if !looksLikeCitation(tok) {
			continue
		}
		key := normPath(tok)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tok)
	}
	return out
}

// isCitationBreak reports the runes that end a citation token. The chunk marker
// splits too, so `slices.md §3` and `slices.md§3` both yield the path.
func isCitationBreak(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("§,;()[]{}<>\"'`|", r)
}

// looksLikeCitation reports whether tok has the shape of a document reference.
func looksLikeCitation(tok string) bool {
	dot := strings.LastIndexByte(tok, '.')
	if dot <= 0 || dot == len(tok)-1 {
		return false
	}
	base, ext := tok[:dot], tok[dot+1:]
	if len(base) < 2 || !strings.ContainsFunc(base, unicode.IsLetter) {
		return false
	}
	if len(ext) < 2 || len(ext) > 8 || !strings.ContainsFunc(ext, unicode.IsLetter) {
		return false
	}
	for _, r := range ext {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// contentWords reduces text to the distinct words that carry what it claims,
// lowercased, in first-seen order.
//
// Distinct rather than counted: an answer that repeats one word from a passage
// forty times has not become forty times more grounded, and rewarding it that
// way would make the crudest possible answer the best-scoring one.
func contentWords(text string) []string {
	var (
		out  []string
		seen = map[string]bool{}
	)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) < 2 || functionWords[w] || seen[w] {
			continue
		}
		if !strings.ContainsFunc(w, unicode.IsLetter) {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

func wordSet(words []string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}

// functionWords are the closed-class English words an answer is made of rather
// than about. Overlap on them measures that both texts are English, which is
// not the question groundedness asks.
//
// Nothing that changes what a sentence claims is here — no negations, no
// modals, no quantifiers — because an answer that says "does not" where the
// passage says "does" is exactly the ungrounded answer this is looking for.
var functionWords = wordSet(strings.Fields(`
	about above across after again against all along also although among an and
	another any anything are around as at away
	back be because been before being below between beyond both but by
	came can come could
	did do does doing done down during
	each either else even ever every everything
	far few for from further
	get gets getting give given go goes going got
	had has have having he her here hers herself him himself his how however
	if in into is it its itself
	like
	made make makes making many me more most much my myself
	need needs new next now
	of off on once one only onto or other others our ours ourselves out over own
	per put
	rather really
	same say says see seen several she should since so some someone something
	still such
	take taken than that the their theirs them themselves then there these they
	thing things this those though through thus to together too toward towards
	two
	under until up upon us use used uses using usually
	very via
	want was way we well went were what whatever when where whether which while
	who whom whose why will with within without would
	yet you your yours yourself
`))

// ChatFn is the model call the judge spends. It is the shape every adapter
// already has, taken as a function rather than as llm.LLM so a fake chat drives
// the tests with no provider behind it — the seam internal/rewrite uses for the
// same reason.
type ChatFn func(ctx context.Context, messages []llm.Message, opts ...llm.CallOptions) (<-chan llm.Token, error)

const (
	// JudgeScale is the top of the scale the judge grades on. Three points —
	// wrong, partly, fully — is as fine a distinction as a model makes
	// repeatably; a 0–10 scale invents nine boundaries nobody could defend.
	JudgeScale = 2
	// JudgeMaxTokens caps a verdict. It is one JSON object with two short
	// reasons, and a judge writing an essay has stopped judging.
	JudgeMaxTokens = 512
	// JudgeTimeout bounds one call. A judge that hangs would hang the run, and
	// a case scored late is no better than one scored not at all.
	JudgeTimeout = 60 * time.Second
)

// JudgeSystem is the judge's prompt, versioned with the code that spends it.
//
// It is deliberately not a user template (D6). A tunable, exportable,
// overridable rubric is a way for two runs to be scored by two different
// instruments and compared anyway, which is the failure this whole package
// exists to prevent. `tbuk eval --judge --verbose` prints it, so a number can
// always be traced back to the question that produced it.
const JudgeSystem = `You are grading one answer produced by a retrieval-augmented question answering system.

Grade two things separately, each on this scale:

  2 - fully
  1 - partly
  0 - not at all

correctness: does the answer say what the reference answer says? Grade the facts, not the wording, the length or the style. Extra detail that is correct is not a fault; contradicting the reference is.

faithfulness: is every claim in the answer supported by the retrieved passages? Grade support only, never correctness. An answer that is right for a reason the passages do not give is unfaithful; an answer that is wrong in exactly the way the passages are wrong is faithful.

Reply with one JSON object and nothing else, in this shape:

{"correctness": 2, "correctness_reason": "one short sentence", "faithfulness": 1, "faithfulness_reason": "one short sentence"}`

// Verdict is the judge's mark on one axis: a score on the scale it was given,
// and the reason it gave for it. The reason is what makes a judged number
// arguable instead of oracular.
type Verdict struct {
	Score  int    `json:"score"`
	Reason string `json:"reason,omitempty"`
}

// Judgement is what one judge call returned.
type Judgement struct {
	Correctness  Verdict `json:"correctness"`
	Faithfulness Verdict `json:"faithfulness"`
}

// WithJudgement folds the judge's marks into the deterministic metrics,
// rescaled to the 0–1 every other metric here is on.
func (m GenMetrics) WithJudgement(j Judgement) GenMetrics {
	m.Correctness = float64(j.Correctness.Score) / JudgeScale
	m.Faithfulness = float64(j.Faithfulness.Score) / JudgeScale
	m.Judged = 1
	return m
}

// Judge scores an answer with a model, on the axes no arithmetic reaches.
//
// It is opt-in and it runs alongside the deterministic scoring rather than
// instead of it, so a judge upgrade is visible as a judge upgrade: the free
// numbers stay where they were while the judged ones move.
type Judge struct {
	// Chat is the model call. Nil is an error rather than a fallback — an
	// unjudged case is a fact the report states, not a hole to fill in.
	Chat ChatFn
	// Opts carries the model and temperature the judge is spent at. A zero
	// MaxTokens takes JudgeMaxTokens.
	Opts llm.CallOptions
	// Timeout bounds one call. Zero takes JudgeTimeout.
	Timeout time.Duration
}

// Judge returns the model's marks for one answer.
//
// Every failure — no model, a refused call, a timeout, a completion that is not
// a verdict, a score off the scale — is an error, and the caller records the
// case as unjudged. None of them is a zero: a judge that failed is not evidence
// that the answer was wrong, and scoring it as one would make an outage look
// like a regression.
func (j Judge) Judge(ctx context.Context, c Case, a Answer) (Judgement, error) {
	if j.Chat == nil {
		return Judgement{}, errors.New("no model is configured to judge with")
	}
	// Correctness is measured against `answer:`; without one there is nothing
	// to mark against, and half a verdict is not the metric anybody asked for.
	if strings.TrimSpace(c.Answer) == "" {
		return Judgement{}, errors.New("the case has no reference answer to judge against")
	}

	timeout := j.Timeout
	if timeout <= 0 {
		timeout = JudgeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := j.Opts
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = JudgeMaxTokens
	}
	tokens, err := j.Chat(ctx, JudgeMessages(c, a), opts)
	if err != nil {
		return Judgement{}, fmt.Errorf("the judge call failed: %w", err)
	}

	var sb strings.Builder
	for done := false; !done; {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Judgement{}, fmt.Errorf("the judge did not answer within %s", timeout)
			}
			return Judgement{}, ctx.Err() //nolint:wrapcheck // the caller's own cancellation, unadorned
		case tok, ok := <-tokens:
			if !ok {
				done = true
				break
			}
			if tok.Error != nil {
				return Judgement{}, fmt.Errorf("the judge stream failed: %w", tok.Error)
			}
			sb.WriteString(tok.Text)
			done = tok.Done
		}
	}
	return ParseJudgement(sb.String())
}

// JudgeMessages renders the judge's prompt for one case. Exported so
// --judge --verbose can show exactly what was asked.
func JudgeMessages(c Case, a Answer) []llm.Message {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Question:\n%s\n\nReference answer:\n%s\n\nRetrieved passages:\n", c.Query, c.Answer)
	if len(a.Passages) == 0 {
		sb.WriteString("(none were retrieved)\n")
	}
	for i, p := range a.Passages {
		fmt.Fprintf(&sb, "[%d] %s\n%s\n\n", i+1, p.Path, p.Text)
	}
	fmt.Fprintf(&sb, "\nAnswer to grade:\n%s", a.Text)
	return []llm.Message{
		{Role: llm.RoleSystem, Content: JudgeSystem},
		{Role: llm.RoleUser, Content: sb.String()},
	}
}

// rawJudgement is the wire shape. The scores are pointers so a missing one is
// malformed rather than a silent zero — the difference between "the judge said
// nothing" and "the judge said wrong" is the whole reason a failure is not a
// zero.
type rawJudgement struct {
	Correctness        *int   `json:"correctness"`
	CorrectnessReason  string `json:"correctness_reason"`
	Faithfulness       *int   `json:"faithfulness"`
	FaithfulnessReason string `json:"faithfulness_reason"`
}

// ParseJudgement reads a verdict out of a completion, tolerating the code fence
// and the preamble a model wraps JSON in, and refusing anything else.
func ParseJudgement(s string) (Judgement, error) {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return Judgement{}, fmt.Errorf("the judge did not return a verdict: %q", truncateForError(s))
	}

	var raw rawJudgement
	if err := json.Unmarshal([]byte(s[start:end+1]), &raw); err != nil {
		return Judgement{}, fmt.Errorf("the judge's verdict is not readable (%w): %q", err, truncateForError(s))
	}
	if raw.Correctness == nil || raw.Faithfulness == nil {
		return Judgement{}, fmt.Errorf("the judge graded only one of the two axes: %q", truncateForError(s))
	}
	for name, score := range map[string]int{"correctness": *raw.Correctness, "faithfulness": *raw.Faithfulness} {
		if score < 0 || score > JudgeScale {
			return Judgement{}, fmt.Errorf("the judge scored %s %d, off the 0-%d scale it was given",
				name, score, JudgeScale)
		}
	}
	return Judgement{
		Correctness:  Verdict{Score: *raw.Correctness, Reason: strings.TrimSpace(raw.CorrectnessReason)},
		Faithfulness: Verdict{Score: *raw.Faithfulness, Reason: strings.TrimSpace(raw.FaithfulnessReason)},
	}, nil
}

// truncateForError bounds what a parse failure quotes back. The point is to
// recognise what the model said, not to reprint an essay into a report.
func truncateForError(s string) string {
	const limit = 120
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
