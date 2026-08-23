package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/importer"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// ConflictPolicy decides what happens to a document the archive supplies that
// the knowledge base already has at the same path.
type ConflictPolicy string

// The values --on-conflict accepts.
const (
	ConflictSkip      ConflictPolicy = "skip"
	ConflictOverwrite ConflictPolicy = "overwrite"
	ConflictAsk       ConflictPolicy = "ask"
)

// ImportScope selects what an import brings across. The zero value is
// everything, which is what `tbuk import` with no subcommand does.
type ImportScope string

// The scopes the import subcommands select.
const (
	ScopeAll       ImportScope = ""
	ScopeData      ImportScope = "data"
	ScopeTemplates ImportScope = "templates"
)

func (s ImportScope) includesData() bool      { return s != ScopeTemplates }
func (s ImportScope) includesTemplates() bool { return s != ScopeData }

// ImportOptions carries the choices `tbuk import` offers.
type ImportOptions struct {
	// Scope limits the import to documents or to templates. The zero value
	// brings both.
	Scope ImportScope
	// OnConflict handles a document already indexed at the same path. The zero
	// value is ConflictSkip.
	OnConflict ConflictPolicy
	// DryRun reports what would be imported without writing or embedding.
	DryRun bool
	// Yes answers the confirmation shown when a default config had to be
	// created, for non-interactive use. It has no effect on a templates-only
	// import, which spends nothing and so is never confirmed.
	Yes bool
	// Verbose prints a line per document and template. Off, only what needs
	// acting on is printed: skips, failures, and the closing counts.
	Verbose bool
}

// IngesterFactory opens the knowledge base named by cfg and returns an ingester
// for it together with a close function. Import cannot build its ingester up
// front — on a fresh machine the config it needs does not exist until the
// import has created it — so the construction is a parameter. Exported for
// testing.
type IngesterFactory func(cfg config.Config) (*ingest.Ingester, func() error, error)

// automaticMetadata are the keys every ingest derives from a document's path.
// They are re-derived here too, so the archive's copies are not carried over.
var automaticMetadata = map[string]bool{"filename": true, "extension": true, "mime": true, "dir": true}

func newImportCmd() *cobra.Command {
	var (
		onConflict string
		dryRun     bool
		yes        bool
		verbose    bool
	)

	run := func(scope ImportScope) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			policy := ConflictPolicy(onConflict)
			switch policy {
			case ConflictSkip, ConflictOverwrite, ConflictAsk:
			default:
				return fmt.Errorf("--on-conflict must be one of skip, overwrite, ask (got %q)", onConflict)
			}
			return RunImport(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args[0], configFrom(cmd), rootFrom(cmd), configPathFrom(cmd),
				ImportOptions{Scope: scope, OnConflict: policy, DryRun: dryRun, Yes: yes, Verbose: verbose}, openIngester)
		}
	}

	cmd := &cobra.Command{
		Use:   "import <archive>",
		Short: "Import an archive's documents and prompt templates, indexed with this machine's settings",
		Long: "Import takes from an archive written by `tbuk export` the things that are " +
			"yours rather than the exporting machine's: the archived source files under " +
			"raw/, the index that says what those files were — their paths, titles and " +
			"your own metadata — and your prompt templates. The sources are copied into " +
			"this machine's raw archive and indexed with this machine's settings.\n\n" +
			"`tbuk import data` and `tbuk import templates` do one half each; plain " +
			"`tbuk import` does both, templates first, so a slow or failing embedding " +
			"step does not also cost you your templates.\n\n" +
			"The archive's config is left where it is, because it describes the machine " +
			"it came from — its paths, providers and models — and its embeddings are " +
			"ignored outright, because vectors produced by another machine's model " +
			"cannot be searched here. Everything is embedded afresh from the archived " +
			"bytes.\n\n" +
			"A document already indexed at the same path, or a template already " +
			"installed under the same name, is left alone; --on-conflict overwrite " +
			"replaces it, and --on-conflict ask decides one at a time. Re-importing the " +
			"same archive therefore changes nothing. A template is replaced whole rather " +
			"than merged, so what lands is always a template that loads.\n\n" +
			"Importing into a directory with no config.yaml sets one up first, exactly " +
			"as `tbuk init` would, and asks before embedding anything with settings you " +
			"have not seen.",
		Args: cobra.ExactArgs(1),
		RunE: run(ScopeAll),
	}

	cmd.PersistentFlags().StringVar(&onConflict, "on-conflict", string(ConflictSkip),
		"a document or template already present: skip, overwrite, or ask")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false,
		"list what would be imported without writing or embedding anything")
	cmd.PersistentFlags().BoolVar(&yes, "yes", false,
		"skip the confirmation shown when a default config has to be created")
	cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false,
		"print a line per document and template; by default only problems and the summary are shown")

	cmd.AddCommand(&cobra.Command{
		Use:   "data <archive>",
		Short: "Import only the archive's documents",
		Long: "Import the archive's source files and index, embedding them with this " +
			"machine's settings, and leave prompt templates alone.",
		Args: cobra.ExactArgs(1),
		RunE: run(ScopeData),
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "templates <archive>",
		Short: "Import only the archive's prompt templates",
		Long: "Install the archive's prompt templates, leaving the knowledge base " +
			"untouched. Nothing is embedded, so this needs no embedding provider and " +
			"asks no questions about one. A template already installed under the same " +
			"name is left alone unless --on-conflict says otherwise.",
		Args: cobra.ExactArgs(1),
		RunE: run(ScopeTemplates),
	})
	return cmd
}

// RunImport imports the documents an archive carries into the knowledge base at
// root, embedding them with cfg's settings.
//
// The archive supplies two things and no more: the source files under raw/, and
// the index naming what they were. The archive's own config is never read, and
// its embeddings never trusted — a vector made by another machine's model is
// not searchable here, and nothing in the archive proves which model made it.
// Exported for testing.
func RunImport(
	ctx context.Context,
	in io.Reader,
	outW, errW io.Writer,
	archivePath string,
	cfg config.Config,
	root, configPath string,
	opts ImportOptions,
	newIngester IngesterFactory,
) error {
	if opts.OnConflict == "" {
		opts.OnConflict = ConflictSkip
	}
	// Buffer stdin once for the whole run. ConfirmYes reads a line at a time
	// through a bufio.Reader, which keeps whatever else arrived in the same read
	// — so a reader created per prompt would swallow the answers meant for the
	// documents after it. Handing every prompt the same buffered reader is what
	// makes `--on-conflict ask` able to ask more than once.
	prompts := bufio.NewReader(in)

	// A machine with no config gets one before anything else, so the settings
	// the documents are embedded with are on disk and reviewable. The archive's
	// config is never a candidate: it describes the machine it came from.
	if !opts.DryRun && !exists(configPath) {
		if err := Scaffold(outW, root, cfg.Prompts.Dir); err != nil {
			return err
		}
		reloaded, err := config.LoadForRoot(configPath, root)
		if err != nil {
			return fmt.Errorf("load config %s: %w", configPath, err)
		}
		cfg = reloaded
		// Only the embedding is worth confirming: it takes time and, on a paid
		// provider, money, under settings the user has not chosen. Installing
		// templates spends nothing, so a templates-only import never asks.
		if opts.Scope.includesData() && !opts.Yes && !confirmFreshConfig(prompts, outW, configPath, cfg) {
			fmt.Fprintf(outW, "Stopped. Edit %s and run the import again.\n", configPath) //nolint:errcheck
			return nil
		}
	}
	if opts.Scope.includesData() && cfg.Ingest.RawDir == "" {
		return fmt.Errorf("import needs somewhere to keep the archived sources, but ingest.raw_dir " +
			"is disabled in this config; set it and run the import again")
	}

	scratch, err := os.MkdirTemp("", "tbuk-import-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	extracted, err := extractArchive(archivePath, scratch, cfg, opts)
	if err != nil {
		return err
	}

	// Templates first: they are quick and cannot fail for want of a provider,
	// so a slow or broken embedding step never costs the user their templates.
	var templates importCount
	if opts.Scope.includesTemplates() {
		templates = importTemplates(prompts, outW, errW, cfg, scratch, extracted.PromptNames, opts,
			installedTemplates(cfg, configPath, opts))
	}

	var documents importCount
	if opts.Scope.includesData() {
		documents, err = importData(ctx, prompts, outW, errW, cfg, archivePath, extracted, opts, newIngester)
		if err != nil {
			return err
		}
	}

	return reportSummary(outW, opts, documents, templates)
}

// importCount tallies one half of an import for the closing summary.
type importCount struct{ done, skipped, errs int }

// importData does the document half: read the archive's index, then bring each
// document it offers into the knowledge base.
func importData(
	ctx context.Context,
	prompts io.Reader,
	outW, errW io.Writer,
	cfg config.Config,
	archivePath string,
	extracted importer.Result,
	opts ImportOptions,
	newIngester IngesterFactory,
) (importCount, error) {
	if extracted.DBPath == "" {
		return importCount{}, fmt.Errorf("%s: the archive carries no index, so there is nothing to import "+
			"(it was written without a database)", archivePath)
	}

	wanted, err := readManifest(ctx, extracted)
	if err != nil {
		return importCount{}, err
	}
	if len(wanted) == 0 {
		fmt.Fprintln(outW, "The archive indexes no documents.") //nolint:errcheck
		return importCount{}, nil
	}

	if opts.DryRun {
		return reportDryRun(ctx, outW, cfg, wanted)
	}

	ing, closeIngester, err := newIngester(cfg)
	if err != nil {
		return importCount{}, err
	}
	defer func() { _ = closeIngester() }()

	return importDocuments(ctx, prompts, outW, errW, cfg, ing, wanted, opts)
}

// reportSummary closes the run with one line per half that ran, and fails the
// command when anything errored.
func reportSummary(outW io.Writer, opts ImportOptions, documents, templates importCount) error {
	verb := "imported"
	if opts.DryRun {
		verb = "would be imported"
	}
	var parts []string
	if opts.Scope.includesData() {
		parts = append(parts, fmt.Sprintf("%d document(s) %s, %d skipped, %d errors",
			documents.done, verb, documents.skipped, documents.errs))
	}
	if opts.Scope.includesTemplates() {
		parts = append(parts, fmt.Sprintf("%d template(s) %s, %d skipped, %d errors",
			templates.done, verb, templates.skipped, templates.errs))
	}
	label := "Done"
	if opts.DryRun {
		label = "Dry run"
	}
	fmt.Fprintf(outW, "%s: %s\n", label, strings.Join(parts, "; ")) //nolint:errcheck

	if errs := documents.errs + templates.errs; errs > 0 {
		return fmt.Errorf("%d item(s) failed to import", errs)
	}
	return nil
}

// importTemplates installs the archive's prompt templates, one decision per
// template name. Extract has already unpacked them into the scratch directory,
// so this only has to decide what to keep and move it into place.
func importTemplates(
	in io.Reader,
	outW, errW io.Writer,
	cfg config.Config,
	scratch string,
	names []string,
	opts ImportOptions,
	installed func(name string) bool,
) importCount {
	var count importCount
	// A skipped template is the normal outcome — every machine has the built-ins
	// — so it is not news the way a document that could not be imported is. A
	// dry run lists regardless: saying what would happen is its whole job.
	listing := opts.Verbose || opts.DryRun
	sort.Strings(names) // stable order, so the prompts come in a predictable one
	for i, name := range names {
		prefix := fmt.Sprintf("[%d/%d]", i+1, len(names))
		dest := filepath.Join(cfg.Prompts.Dir, name)

		if installed(name) && !overwriteExisting(in, outW, "template "+name+" is already installed", opts.OnConflict) {
			count.skipped++
			if listing {
				fmt.Fprintf(outW, "%s template %s → skipped: already installed\n", prefix, name) //nolint:errcheck
			}
			continue
		}
		if opts.DryRun {
			count.done++
			fmt.Fprintf(outW, "%s template %s → would install\n", prefix, name) //nolint:errcheck
			continue
		}
		// Replace whole rather than merge: a template is a manifest plus the
		// files it names, and half of each would be a template that may not load.
		if err := replaceDir(filepath.Join(scratch, promptsScratch, name), dest); err != nil {
			count.errs++
			fmt.Fprintf(errW, "%s template %s → error: %v\n", prefix, name, err) //nolint:errcheck
			continue
		}
		count.done++
		if listing {
			fmt.Fprintf(outW, "%s template %s → installed\n", prefix, name) //nolint:errcheck
		}
	}
	return count
}

// installedTemplates reports whether a template of a given name is already in
// place. On a dry run against a root with no config it also counts the
// built-ins: the real run would scaffold first, and those copies are exactly
// what the archive's collide with. Reporting an install that would not happen
// makes the dry run useless for the case it matters most in — a fresh machine.
func installedTemplates(cfg config.Config, configPath string, opts ImportOptions) func(string) bool {
	wouldScaffold := opts.DryRun && !exists(configPath)
	return func(name string) bool {
		if wouldScaffold {
			for _, builtin := range builtinTemplateNames {
				if name == builtin {
					return true
				}
			}
		}
		return exists(filepath.Join(cfg.Prompts.Dir, name))
	}
}

// replaceDir puts src in dest's place, removing whatever dest held. The copy is
// a copy rather than a rename because src is under the system temp directory,
// which is often a different filesystem from the knowledge base.
func replaceDir(src, dest string) error {
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("remove %s: %w", dest, err)
	}
	return copyDir(src, dest)
}

// copyDir copies a directory tree, owner-only.
func copyDir(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

// manifestDoc is one document the archive offers: its identity, its user
// metadata, and whether the archive actually carries its bytes.
type manifestDoc struct {
	doc     *storage.Document
	meta    map[string]string
	hasRaw  bool
	rawName string
}

// promptsScratch is where Extract unpacks the archive's templates inside the
// run's scratch directory, before importTemplates decides which to keep.
const promptsScratch = "prompts"

// extractArchive opens the archive and pulls out what this run needs: the raw
// sources and index for a document import, the templates for a template one. A
// dry run reads the archive the same way but writes none of it.
func extractArchive(archivePath, scratch string, cfg config.Config, opts ImportOptions) (importer.Result, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return importer.Result{}, fmt.Errorf("open archive %s: %w", archivePath, err)
	}
	defer func() { _ = f.Close() }()

	var extract importer.Options
	if opts.Scope.includesData() {
		extract.DBDest = filepath.Join(scratch, "manifest.sqlite")
		if !opts.DryRun {
			extract.RawDir = cfg.Ingest.RawDir
		}
	}
	if opts.Scope.includesTemplates() && !opts.DryRun {
		// Unpacked aside, not straight into the prompts directory: which of
		// them replace an installed template is a decision for the caller,
		// taken per template name.
		extract.PromptsDir = filepath.Join(scratch, promptsScratch)
	}

	res, err := importer.Extract(f, extract)
	if err != nil {
		return res, fmt.Errorf("%s: %w", archivePath, err)
	}
	return res, nil
}

// readManifest reads the archive's index: every document it lists, with the
// metadata attached to it and whether its bytes are in the archive.
func readManifest(ctx context.Context, extracted importer.Result) ([]manifestDoc, error) {
	db, err := storage.Open(extracted.DBPath)
	if err != nil {
		return nil, fmt.Errorf("read the archive's index: %w", err)
	}
	defer func() { _ = db.Close() }()

	docs, err := storage.NewDocumentRepo(db.DB()).List(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the archive's index: %w", err)
	}
	metaRepo := storage.NewMetadataRepo(db.DB())

	inArchive := make(map[string]bool, len(extracted.RawNames))
	for _, n := range extracted.RawNames {
		inArchive[n] = true
	}

	out := make([]manifestDoc, 0, len(docs))
	for _, doc := range docs {
		rows, err := metaRepo.List(ctx, doc.ID)
		if err != nil {
			return nil, fmt.Errorf("read the archive's metadata for %s: %w", doc.Path, err)
		}
		meta := map[string]string{}
		for _, m := range rows {
			if !automaticMetadata[m.Key] {
				meta[m.Key] = m.Value
			}
		}
		// Where the archive itself says the copy is, falling back to the name a
		// copy would have been written under for archives made before that was
		// recorded. Deriving it is a guess; the recorded value is a fact, and
		// the only thing that works when the copy is named some other way.
		name := doc.RawPath
		if name == "" {
			name = doc.SHA256 + filepath.Ext(doc.Path)
		}
		doc.RawPath = name
		out = append(out, manifestDoc{doc: doc, meta: meta, hasRaw: inArchive[name], rawName: name})
	}
	return out, nil
}

// importDocuments indexes each offered document, one line of progress each.
func importDocuments(
	ctx context.Context,
	in io.Reader,
	outW, errW io.Writer,
	cfg config.Config,
	ing *ingest.Ingester,
	wanted []manifestDoc,
	opts ImportOptions,
) (importCount, error) {
	var count importCount
	db, err := storage.Open(cfg.Database.Path)
	if err != nil {
		return count, fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	docs := storage.NewDocumentRepo(db.DB())
	meta := storage.NewMetadataRepo(db.DB())

	for i, m := range wanted {
		if ctx.Err() != nil {
			break
		}
		prefix := fmt.Sprintf("[%d/%d]", i+1, len(wanted))

		if !m.hasRaw {
			count.skipped++
			fmt.Fprintf(outW, "%s %s → skipped: the archive holds no copy of this file\n", prefix, m.doc.Path) //nolint:errcheck
			continue
		}

		existing, err := lookup(ctx, docs, m.doc.Path)
		if err != nil {
			count.errs++
			fmt.Fprintf(errW, "%s %s → error: %v\n", prefix, m.doc.Path, err) //nolint:errcheck
			continue
		}
		if existing != nil && !overwriteExisting(in, outW, m.doc.Path+" is already indexed", opts.OnConflict) {
			count.skipped++
			fmt.Fprintf(outW, "%s %s → skipped: already indexed\n", prefix, m.doc.Path) //nolint:errcheck
			continue
		}

		// The identity has to be in place before the document can be embedded
		// (embedding writes chunks against its row), so keep what it replaced:
		// a failure below has to be able to put it back.
		var prior *storage.Document
		if existing != nil {
			clone := *existing
			prior = &clone
		}
		row, err := upsertDocument(ctx, docs, existing, m.doc)
		if err != nil {
			count.errs++
			fmt.Fprintf(errW, "%s %s → error: %v\n", prefix, m.doc.Path, err) //nolint:errcheck
			continue
		}

		res := ing.ReindexDocument(ctx, row, ingest.ReindexOptions{})
		if res.Err != nil {
			count.errs++
			// Leave nothing half-imported. A new row with no chunks would be
			// treated as "already indexed" by every later import, stranding the
			// document; a replaced row would carry the archive's identity over
			// the local copy's chunks.
			if rbErr := rollback(ctx, docs, row, prior); rbErr != nil {
				fmt.Fprintf(errW, "%s %s → error: %v (and could not undo: %v)\n", prefix, m.doc.Path, res.Err, rbErr) //nolint:errcheck
			} else {
				fmt.Fprintf(errW, "%s %s → error: %v\n", prefix, m.doc.Path, res.Err) //nolint:errcheck
			}
			continue
		}

		// Metadata last, once the document is really in: the automatic keys the
		// embedding step derives from the path never collide with these, since
		// the manifest read filters them out.
		if err := writeMetadata(ctx, meta, row.ID, m.meta); err != nil {
			count.errs++
			fmt.Fprintf(errW, "%s %s → error: %v\n", prefix, m.doc.Path, err) //nolint:errcheck
			continue
		}
		count.done++
		if opts.Verbose {
			fmt.Fprintf(outW, "%s %s → %d chunks embedded\n", prefix, m.doc.Path, res.Chunks) //nolint:errcheck
		}
	}

	return count, nil
}

// reportDryRun prints the decision for each offered document without writing.
func reportDryRun(ctx context.Context, outW io.Writer, cfg config.Config, wanted []manifestDoc) (importCount, error) {
	var count importCount
	var docs *storage.DocumentRepo
	if exists(cfg.Database.Path) {
		db, err := storage.Open(cfg.Database.Path)
		if err != nil {
			return count, fmt.Errorf("open database: %w", err)
		}
		defer func() { _ = db.Close() }()
		docs = storage.NewDocumentRepo(db.DB())
	}

	for i, m := range wanted {
		prefix := fmt.Sprintf("[%d/%d]", i+1, len(wanted))
		switch {
		case !m.hasRaw:
			count.skipped++
			fmt.Fprintf(outW, "%s %s → would skip: the archive holds no copy of this file\n", prefix, m.doc.Path) //nolint:errcheck
			continue
		case docs != nil:
			existing, err := lookup(ctx, docs, m.doc.Path)
			if err != nil {
				return count, err
			}
			if existing != nil {
				count.skipped++
				fmt.Fprintf(outW, "%s %s → already indexed (--on-conflict decides)\n", prefix, m.doc.Path) //nolint:errcheck
				continue
			}
		}
		count.done++
		fmt.Fprintf(outW, "%s %s → would import from %s\n", prefix, m.doc.Path, m.rawName) //nolint:errcheck
	}
	return count, nil
}

// lookup returns the local document at path, or nil when there is none.
func lookup(ctx context.Context, docs *storage.DocumentRepo, path string) (*storage.Document, error) {
	existing, err := docs.GetByPath(ctx, path)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return existing, nil
}

// upsertDocument makes the archive's identity the local one: a new row, or the
// existing row updated to the archive's hash, title and type.
func upsertDocument(ctx context.Context, docs *storage.DocumentRepo, existing, want *storage.Document) (*storage.Document, error) {
	if existing == nil {
		row := &storage.Document{
			Path: want.Path, SHA256: want.SHA256, Title: want.Title,
			MimeType: want.MimeType, RawPath: want.RawPath,
		}
		if err := docs.Create(ctx, row); err != nil {
			return nil, err
		}
		return row, nil
	}
	existing.SHA256 = want.SHA256
	existing.Title = want.Title
	existing.MimeType = want.MimeType
	existing.RawPath = want.RawPath
	if err := docs.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// rollback undoes a document that failed to embed: a row this run created is
// removed (its chunks and metadata cascade), and a row it overwrote gets its
// own identity back. The overwritten row's chunks were never replaced —
// ReplaceForDocument only runs after a successful embed — so restoring the
// identity restores the document.
func rollback(ctx context.Context, docs *storage.DocumentRepo, row, prior *storage.Document) error {
	if prior == nil {
		return docs.Delete(ctx, row.ID)
	}
	return docs.Update(ctx, prior)
}

func writeMetadata(ctx context.Context, meta *storage.MetadataRepo, docID int64, kv map[string]string) error {
	for k, v := range kv {
		if err := meta.Set(ctx, docID, k, v); err != nil {
			return fmt.Errorf("write metadata %s: %w", k, err)
		}
	}
	return nil
}

// overwriteExisting applies the conflict policy to one item the archive would
// replace. subject is the full clause naming it and what is already true of it
// ("… is already indexed", "template qa is already installed"), so documents
// and templates each read correctly. Under "ask" an unanswerable prompt (no
// terminal, EOF) reads as no, which is the safe answer and the one the [y/N]
// default shows.
func overwriteExisting(in io.Reader, outW io.Writer, subject string, policy ConflictPolicy) bool {
	switch policy {
	case ConflictOverwrite:
		return true
	case ConflictAsk:
		return ConfirmYes(in, outW, fmt.Sprintf("%s. Replace it with the archive's copy? [y/N] ", subject))
	default:
		return false
	}
}

// confirmFreshConfig shows the settings the documents would be embedded with —
// which the user has not chosen, having had no config until a moment ago — and
// asks before spending time and money on them.
func confirmFreshConfig(in io.Reader, outW io.Writer, configPath string, cfg config.Config) bool {
	model := cfg.Embedding.Model
	if model == "" {
		model = "the provider's default model"
	}
	fmt.Fprintf(outW, "Documents will be embedded with %s (%s), at %d dimensions, per %s.\n", //nolint:errcheck
		cfg.Embedding.Provider, model, cfg.Embedding.Dimension, configPath)
	return ConfirmYes(in, outW, "Continue? [y/N] ")
}

// exists reports whether path is present. An unreadable path counts as present:
// the caller's next step would fail on it anyway, and treating it as missing
// would silently overwrite it.
func exists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// openIngester is the production IngesterFactory: it opens the knowledge base
// and builds an ingester from the configured embedding provider.
func openIngester(cfg config.Config) (*ingest.Ingester, func() error, error) {
	app, err := openApp(cfg)
	if err != nil {
		return nil, nil, err
	}
	ing, err := app.Ingester()
	if err != nil {
		_ = app.Close()
		return nil, nil, err
	}
	return ing, app.Close, nil
}
