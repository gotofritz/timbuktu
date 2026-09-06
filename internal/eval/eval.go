// Package eval scores retrieval and generation against a labelled set of
// cases, so a change to chunking, fusion, ranking or query planning can be
// argued from numbers rather than from an anecdote.
//
// It is deliberately pure — no database, no LLM call, no cobra — so every
// metric is arithmetic over a handwritten ranking and needs a fake of nothing.
// The command that fills it with real results lives in internal/cli.
//
// A label names a document and, optionally, a passage anchor; never a chunk id.
// Chunk ids are renumbered by every re-ingest and every reindex, and a chunk
// index moves whenever chunking changes — which is the first thing this package
// exists to measure, so a label that could not survive it would be useless.
package eval

// Version is the only label-set schema version this build reads. A file
// carrying any other version is an error rather than a best effort: a set
// silently read under the wrong rules measures something nobody wrote down.
const Version = 1

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
