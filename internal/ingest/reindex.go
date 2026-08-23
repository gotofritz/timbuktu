package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gotofritz/timbuktu/internal/storage"
)

// ErrSourceUnavailable reports that a document's text could not be had from
// anywhere: nothing in the extracted-text cache, no copy under the raw archive,
// and no readable file at its stored path. A ReindexResult carrying it is also
// marked Skipped — the run continues; only that document is left untouched.
var ErrSourceUnavailable = errors.New("ingest: no readable source")

// ReindexOptions controls a reindex run.
type ReindexOptions struct {
	// SourceDir resolves raw copies from somewhere other than the configured
	// raw archive (e.g. a raw/ restored from a backup). Empty uses the
	// Ingester's own raw dir.
	SourceDir string
	// DryRun resolves each document's source and reports it without extracting,
	// embedding or writing anything.
	DryRun bool
	// OnResult, when set, is called with each document's result as it completes
	// (index is 1-based), so a caller can stream progress through a corpus that
	// takes a while to re-embed rather than waiting for the whole run.
	OnResult func(index, total int, res ReindexResult)
}

// ReindexResult describes the outcome of re-embedding a single document.
//
// Skipped and Err are not exclusive: an unresolvable source sets both (Skipped
// for the summary, ErrSourceUnavailable for callers that need to tell it apart
// from a real failure). Check Skipped first when classifying.
type ReindexResult struct {
	Path   string // the document's stored path — its identity in the knowledge base
	Source string // file the text was read from; empty when nothing resolved
	// FromRaw and FromCache say which of the two Source is: the archived copy
	// of the original bytes, or the extracted-text cache, which needs no source
	// file at all and is the reason a document with none can still re-embed.
	FromRaw   bool
	FromCache bool
	// StaleText marks text taken from a cache written by an older extractor,
	// used only when the document could not be re-extracted from anything.
	StaleText bool
	Chunks    int  // chunks written; 0 on a dry run
	Skipped   bool // nothing to read the text from; the document was left untouched
	// Reason says, when Skipped, exactly what was looked for and did not
	// resolve — the cached text, the archived copy's expected path, and the
	// stored path. "No archived copy" alone cannot be told apart from a raw
	// directory pointing elsewhere or an ingest that never finished.
	Reason string
	Err    error
}

// ReindexAll re-embeds every document in the knowledge base. Listing failures
// are fatal (there is nothing to iterate); per-document failures are reported
// in the results and never abort the run.
func (ing *Ingester) ReindexAll(ctx context.Context, opts ReindexOptions) ([]ReindexResult, error) {
	docs, err := ing.docs.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("reindex: list documents: %w", err)
	}
	return ing.ReindexDocuments(ctx, docs, opts), nil
}

// ReindexDocuments re-embeds the given documents in order, one result each.
// A cancelled context stops the run, so the caller can print the summary
// accumulated so far — the same partial-progress behaviour IngestDir has.
func (ing *Ingester) ReindexDocuments(ctx context.Context, docs []*storage.Document, opts ReindexOptions) []ReindexResult {
	results := make([]ReindexResult, 0, len(docs))
	for i, doc := range docs {
		if ctx.Err() != nil {
			break
		}
		res := ing.ReindexDocument(ctx, doc, opts)
		results = append(results, res)
		if opts.OnResult != nil {
			opts.OnResult(i+1, len(docs), res)
		}
	}
	return results
}

// ReindexDocument re-derives doc's chunks and embeddings from the archived
// source, replacing whatever is indexed for it.
//
// It deliberately differs from IngestFile in three ways. It takes the text from
// the extracted-text cache, or the raw archive, or doc.Path — in that order —
// rather than the live path only, so an imported or relocated knowledge base
// still reindexes, and one whose files are gone entirely still reindexes from
// text already extracted. It never compares
// SHA256s or skips: the content is presumed unchanged and the trigger is an
// embedding config change, which the stored chunks carry no record of. And it
// does not enforce the knowledge base's existing embedding dimension — a
// mixed-dimension corpus is exactly what reindex exists to repair, so refusing
// on the first document would defeat it.
//
// doc's row is left as it is; only its chunks and automatic metadata are
// rewritten. Metadata is derived from doc.Path, never from the sha-named raw
// copy that supplied the bytes.
func (ing *Ingester) ReindexDocument(ctx context.Context, doc *storage.Document, opts ReindexOptions) ReindexResult {
	res := ReindexResult{Path: doc.Path}

	// The extracted text is what this actually needs; a source file is only how
	// it usually gets there. Ask the cache first — it is keyed on the document's
	// content, so it answers even when every file has gone — but still resolve
	// the archived copy, both to report it and to record where it lives.
	cached, cachePath, hit, err := ing.cachedText(doc.SHA256)
	if err != nil {
		res.Err = err
		return res
	}

	found := ing.resolveSource(doc, opts.SourceDir)

	// Nothing current and nothing to re-extract from: text cached by an older
	// extractor is the last resort. It came from a build that produced different
	// output, which is why it is not used while re-extraction is possible — but
	// it beats a document that cannot be read at all.
	var stale string
	if found.why != "" && !hit {
		var stalePath string
		var staleOK bool
		if stale, stalePath, staleOK, err = ing.staleCachedText(doc.SHA256); err != nil {
			res.Err = err
			return res
		}
		if !staleOK {
			res.Skipped = true
			res.Reason = fmt.Sprintf("no extracted text at %s, %s", cachePath, found.why)
			res.Err = fmt.Errorf("%w: %s", ErrSourceUnavailable, res.Reason)
			return res
		}
		res.Source, res.FromCache, res.StaleText = stalePath, true, true
	}

	switch {
	case res.StaleText:
		// already reported above
	case hit:
		// Credit the cache: naming the archived copy would claim the bytes came
		// from a file that was never opened.
		res.Source, res.FromCache = cachePath, true
	default:
		res.Source, res.FromRaw = found.path, found.fromRaw
	}
	if opts.DryRun {
		return res
	}

	// Record where the copy was found, so the next run is a lookup rather than
	// the derivation that happened to work this time. Only for the configured
	// archive: what --source-dir turns up lives somewhere else entirely. Worth
	// doing even when the cache supplied the text — the cache is disposable and
	// the archived copy is not.
	if found.rawRel != "" && found.rawRel != doc.RawPath && opts.SourceDir == "" {
		if err := ing.docs.SetRawPath(ctx, doc.ID, found.rawRel); err != nil {
			res.Err = fmt.Errorf("reindex: record archive location for %s: %w", doc.Path, err)
			return res
		}
		doc.RawPath = found.rawRel
	}

	text := cached
	switch {
	case hit:
	case res.StaleText:
		text = stale
	default:
		if text, err = ing.extractAndCache(ctx, found.path, doc.SHA256); err != nil {
			res.Err = err
			return res
		}
	}

	storageChunks, err := ing.embedChunks(ctx, doc.Path, ing.chunker.Split(text))
	if err != nil {
		res.Err = err
		return res
	}
	for _, c := range storageChunks {
		c.DocumentID = doc.ID
	}

	// Atomic per document: the old chunks survive until the new ones are ready,
	// so a failed re-embed never empties the index for that document.
	if err := ing.chunks.ReplaceForDocument(ctx, doc.ID, storageChunks); err != nil {
		res.Err = fmt.Errorf("reindex: store chunks for %s: %w", doc.Path, err)
		return res
	}

	if err := ing.writeAutoMetadata(ctx, doc.ID, doc.Path, doc.MimeType); err != nil {
		res.Err = err
		return res
	}

	res.Chunks = len(storageChunks)
	return res
}

// resolvedSource is where a document's content will be read from, or why it
// could not be found.
type resolvedSource struct {
	path    string // file to read
	fromRaw bool   // it is the archived copy rather than the stored path
	rawRel  string // its name relative to the raw directory, when fromRaw
	why     string // set when nothing resolved; names what was looked for
}

// resolveSource finds the file to re-read doc's content from, preferring the
// archived copy the document records, then the name a copy would have been
// given (<sha256><ext>) for documents indexed before that was recorded, then
// doc's own stored path.
//
// why is empty when one resolved, and otherwise names the candidates tried — a
// skipped document is something the user has to be able to act on, and the
// paths tried are what makes that possible.
func (ing *Ingester) resolveSource(doc *storage.Document, sourceDir string) resolvedSource {
	rawDir := sourceDir
	if rawDir == "" {
		rawDir = ing.rawDir
	}

	var archive string
	switch {
	case rawDir == "":
		archive = "the raw archive is switched off (ingest.raw_dir is empty)"
	case doc.RawPath != "":
		candidate := filepath.Join(rawDir, filepath.FromSlash(doc.RawPath))
		if isRegularFile(candidate) {
			return resolvedSource{path: candidate, fromRaw: true, rawRel: doc.RawPath}
		}
		archive = fmt.Sprintf("its archived copy is recorded as %s, which is not there", candidate)
	case doc.SHA256 == "":
		archive = "no archived copy is recorded for it, and it has no SHA256 to name one by " +
			"(an earlier ingest did not finish)"
	default:
		// Indexed before the location was recorded: fall back to the name a copy
		// would have been written under.
		rel := doc.SHA256 + filepath.Ext(doc.Path)
		candidate := filepath.Join(rawDir, rel)
		if isRegularFile(candidate) {
			return resolvedSource{path: candidate, fromRaw: true, rawRel: rel}
		}
		archive = fmt.Sprintf("no archived copy is recorded for it and none is at %s", candidate)
	}

	if isRegularFile(doc.Path) {
		return resolvedSource{path: doc.Path}
	}
	return resolvedSource{why: fmt.Sprintf("%s, and %s is unreadable", archive, doc.Path)}
}

// isRegularFile reports whether path is a file the extractor could open. A
// directory sitting at the content-addressed name is not a usable source.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
