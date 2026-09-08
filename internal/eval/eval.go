// Package eval scores retrieval and generation against a labelled set of
// cases, so a change to chunking, fusion, ranking or query planning can be
// argued from numbers rather than from an anecdote.
//
// Every metric here is arithmetic over a handwritten ranking or a handwritten
// answer, so it needs a fake of nothing: there is no database and no cobra, and
// the one thing that does spend a model — the judge — sits behind a ChatFn
// seam that a three-line fake drives. The command that fills the package with
// real results lives in internal/cli.
//
// A label names a document and, optionally, a passage anchor; never a chunk id.
// Chunk ids are renumbered by every re-ingest and every reindex, and a chunk
// index moves whenever chunking changes — which is the first thing this package
// exists to measure, so a label that could not survive it would be useless.
package eval

import "fmt"

// Version is the only label-set schema version this build reads. A file
// carrying any other version is an error rather than a best effort: a set
// silently read under the wrong rules measures something nobody wrote down.
const Version = 1

// The stages a run can score. They are separate because attribution is the
// point: an answer that got worse because retrieval got worse is a different
// bug from an answer that got worse on the same evidence, and a single number
// covering both cannot tell you which one you have.
//
// Retrieval is the default because it is the free one — an embedding call a
// query and no model at all — and a free stage people actually run beats a
// complete one they do not.
const (
	StageRetrieval  = "retrieval"
	StageGeneration = "generation"
	StageBoth       = "both"
)

// ValidateStage rejects a stage this build does not score.
func ValidateStage(stage string) error {
	switch stage {
	case StageRetrieval, StageGeneration, StageBoth:
		return nil
	default:
		return fmt.Errorf("invalid stage %q: must be %s, %s, or %s",
			stage, StageRetrieval, StageGeneration, StageBoth)
	}
}

// ScoresRetrieval reports whether stage marks the ranked list.
func ScoresRetrieval(stage string) bool {
	return stage == StageRetrieval || stage == StageBoth || stage == ""
}

// ScoresGeneration reports whether stage marks the answer.
func ScoresGeneration(stage string) bool {
	return stage == StageGeneration || stage == StageBoth
}

// Result is one retrieved chunk, reduced to what scoring needs of it.
//
// It is deliberately not retrieval.RetrievedChunk. Scoring cares about where a
// passage came from and what it says, and about nothing else; keeping the
// dependency out means the metrics can be tested against three lines of literal
// data instead of a search result assembled by hand.
type Result struct {
	Path string
	Text string
}
