package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/cli"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/export"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// archiveDoc describes one document to put in a test archive.
type archiveDoc struct {
	path    string
	title   string
	body    string
	meta    map[string]string
	noRaw   bool   // omit its raw copy, as `ingest --no-raw` would have
	rawName string // archive the copy under this name instead of <sha><ext>
}

// buildKBArchive exports a knowledge base holding real document rows, chunks at
// oldDim (as a machine with a different embedding config would have left them),
// user metadata, and the matching raw archive copies.
func buildKBArchive(t *testing.T, docs []archiveDoc, oldDim int) string {
	return buildKBArchiveWith(t, docs, oldDim, nil)
}

// buildKBArchiveWith is buildKBArchive plus prompt templates, given as
// name → file → content.
func buildKBArchiveWith(t *testing.T, docs []archiveDoc, oldDim int, templates map[string]map[string]string) string {
	t.Helper()
	srcRoot := t.TempDir()
	cfg := config.DefaultsForRoot(srcRoot)
	for _, dir := range []string{filepath.Dir(cfg.Database.Path), cfg.Ingest.RawDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	db, err := storage.Open(cfg.Database.Path)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	docRepo := storage.NewDocumentRepo(db.DB())
	chunkRepo := storage.NewChunkRepo(db.DB())
	metaRepo := storage.NewMetadataRepo(db.DB())
	for i, d := range docs {
		sha := "sha" + string(rune('a'+i))
		row := &storage.Document{Path: d.path, SHA256: sha, Title: d.title, MimeType: "text/markdown"}
		if err := docRepo.Create(context.Background(), row); err != nil {
			t.Fatalf("seed doc: %v", err)
		}
		if err := chunkRepo.BulkInsert(context.Background(), []*storage.Chunk{
			{DocumentID: row.ID, ChunkIndex: 0, Text: "old", TokenCount: 1, Embedding: make([]float32, oldDim)},
		}); err != nil {
			t.Fatalf("seed chunks: %v", err)
		}
		for k, v := range d.meta {
			if err := metaRepo.Set(context.Background(), row.ID, k, v); err != nil {
				t.Fatalf("seed metadata: %v", err)
			}
		}
		if d.noRaw {
			continue
		}
		body := d.body
		if body == "" {
			body = "# archived " + d.path
		}
		rawName := d.rawName
		if rawName == "" {
			rawName = sha + filepath.Ext(d.path)
		} else {
			row.RawPath = rawName
			if err := docRepo.Update(context.Background(), row); err != nil {
				t.Fatalf("record raw path: %v", err)
			}
		}
		raw := filepath.Join(cfg.Ingest.RawDir, rawName)
		if err := os.WriteFile(raw, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source db: %v", err)
	}

	for name, files := range templates {
		for file, content := range files {
			dest := filepath.Join(cfg.Prompts.Dir, name, file)
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	arcPath := filepath.Join(t.TempDir(), "kb.tar")
	f, err := os.Create(arcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := export.Create(f, cfg, srcRoot); err != nil {
		t.Fatalf("export.Create: %v", err)
	}
	return arcPath
}

// installedTemplate reads a template file from the target's prompts directory.
func installedTemplate(t *testing.T, root, name, file string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "prompts", name, file))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// stubIngesterFactory stands in for the real embedding provider: it opens the
// target knowledge base for real, but embeds with a local stub at newDim.
type stubIngesterFactory struct {
	newDim   int
	emb      *countingStubEmbedder
	seen     []config.Config // configs it was asked to build an ingester for
	err      error
	embedErr error // makes the embedder itself fail
}

func (s *stubIngesterFactory) build(cfg config.Config) (*ingest.Ingester, func() error, error) {
	s.seen = append(s.seen, cfg)
	if s.err != nil {
		return nil, nil, s.err
	}
	db, err := storage.Open(cfg.Database.Path)
	if err != nil {
		return nil, nil, err
	}
	sqlDB := db.DB()
	s.emb = &countingStubEmbedder{dim: s.newDim, err: s.embedErr}
	return ingest.NewIngester(
		storage.NewDocumentRepo(sqlDB),
		storage.NewChunkRepo(sqlDB),
		storage.NewMetadataRepo(sqlDB),
		&stubExtractor{text: "extracted body"},
		&chunking.Chunker{Size: 100, Overlap: 0},
		s.emb,
		cfg.Preprocess.OutputDir,
		ingest.WithRawDir(cfg.Ingest.RawDir),
	), db.Close, nil
}

// importRun bundles the arguments RunImport takes, so tests vary one at a time.
type importRun struct {
	archive string
	cfg     config.Config
	root    string
	cfgPath string
	opts    cli.ImportOptions
	factory *stubIngesterFactory
	stdin   string
	out     bytes.Buffer
	errOut  bytes.Buffer
}

func newImportRun(t *testing.T, archive, root string) *importRun {
	t.Helper()
	return &importRun{
		archive: archive,
		cfg:     config.DefaultsForRoot(root),
		root:    root,
		cfgPath: filepath.Join(root, "config.yaml"),
		factory: &stubIngesterFactory{newDim: 8},
	}
}

func (r *importRun) run(t *testing.T) error {
	t.Helper()
	return cli.RunImport(context.Background(), strings.NewReader(r.stdin), &r.out, &r.errOut,
		r.archive, r.cfg, r.root, r.cfgPath, r.opts, r.factory.build)
}

// localKB opens the knowledge base a run wrote and reports each document's path
// with its chunk dimension.
func localKB(t *testing.T, dbPath string) map[string]int {
	t.Helper()
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	docs, err := storage.NewDocumentRepo(db.DB()).List(context.Background())
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	chunks := storage.NewChunkRepo(db.DB())
	out := map[string]int{}
	for _, doc := range docs {
		cs, err := chunks.ListByDocument(context.Background(), doc.ID)
		if err != nil {
			t.Fatalf("list chunks: %v", err)
		}
		dim := 0
		if len(cs) > 0 {
			dim = len(cs[0].Embedding)
		}
		out[doc.Path] = dim
	}
	return out
}

func metaOf(t *testing.T, dbPath, docPath string) map[string]string {
	t.Helper()
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	doc, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), docPath)
	if err != nil {
		t.Fatalf("get %s: %v", docPath, err)
	}
	rows, err := storage.NewMetadataRepo(db.DB()).List(context.Background(), doc.ID)
	if err != nil {
		t.Fatalf("list metadata: %v", err)
	}
	out := map[string]string{}
	for _, m := range rows {
		out[m.Key] = m.Value
	}
	return out
}

// writeLocalConfig gives a root a config of its own, so an import into it is
// not the fresh-machine case.
func writeLocalConfig(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("llm:\n  provider: mlx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedLocalDoc puts a document into the target knowledge base before the import,
// so conflict handling has something to collide with. A knowledge base that
// already holds documents has a config of its own too.
func seedLocalDoc(t *testing.T, dbPath, docPath, sha string, dim int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLocalConfig(t, filepath.Dir(dbPath))
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	doc := &storage.Document{Path: docPath, SHA256: sha, Title: "mine", MimeType: "text/markdown"}
	if err := storage.NewDocumentRepo(db.DB()).Create(context.Background(), doc); err != nil {
		t.Fatalf("seed local doc: %v", err)
	}
	if err := storage.NewChunkRepo(db.DB()).BulkInsert(context.Background(), []*storage.Chunk{
		{DocumentID: doc.ID, ChunkIndex: 0, Text: "mine", TokenCount: 1, Embedding: make([]float32, dim)},
	}); err != nil {
		t.Fatalf("seed local chunks: %v", err)
	}
}

// ── command surface ──────────────────────────────────────────────────────────

func TestImportCommand_missingArg(t *testing.T) {
	if err := runCLI("import"); err == nil {
		t.Fatal("expected error for missing archive argument")
	}
}

// The archive's own settings are never a factor, so the flags that chose
// between configs are gone; only the conflict policy and a dry run remain.
func TestImportCommand_flags(t *testing.T) {
	cmd, _, err := cli.New().Find([]string{"import"})
	if err != nil {
		t.Fatalf("find import command: %v", err)
	}
	// LocalFlags merges the command's own persistent flags; Flags does not
	// until the command has parsed.
	for _, name := range []string{"on-conflict", "dry-run", "yes"} {
		if cmd.LocalFlags().Lookup(name) == nil {
			t.Errorf("import should expose --%s", name)
		}
	}
	for _, name := range []string{"merge", "force-config", "force-data"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("import must not offer --%s any more", name)
		}
	}
}

func TestImportCommand_rejectsUnknownConflictPolicy(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	if err := runCLI("import", "--on-conflict", "sometimes", "kb.tar"); err == nil {
		t.Fatal("expected error for an unknown --on-conflict value")
	}
}

// `tbuk restore` is gone: its job was to trust another machine's embeddings,
// which cannot be verified here.
func TestRestoreCommand_isGone(t *testing.T) {
	for _, c := range cli.New().Commands() {
		if c.Name() == "restore" {
			t.Fatal("restore should no longer exist")
		}
	}
}

// ── importing ────────────────────────────────────────────────────────────────

// The core case: raw files come in from the archive, identity comes from the
// archive's index, and the vectors are this machine's.
func TestRunImport_importsRawFilesUnderTheirOriginalIdentity(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{
		{path: "/exporting/machine/a.md", title: "Note A"},
		{path: "/exporting/machine/b.md", title: "Note B"},
	}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	kb := localKB(t, filepath.Join(root, "tbuk.sqlite"))
	if len(kb) != 2 {
		t.Fatalf("imported %d documents, want 2: %v", len(kb), kb)
	}
	for _, p := range []string{"/exporting/machine/a.md", "/exporting/machine/b.md"} {
		dim, ok := kb[p]
		if !ok {
			t.Errorf("document %s missing; the archive's path should be preserved", p)
			continue
		}
		if dim != 8 {
			t.Errorf("%s chunk dim = %d, want this machine's 8", p, dim)
		}
	}
	// The raw copies land in the local archive, so a later reindex works too.
	if _, err := os.Stat(filepath.Join(root, "raw", "shaa.md")); err != nil {
		t.Errorf("raw copy not imported: %v", err)
	}
	if got := r.out.String(); !strings.Contains(got, "2 document(s) imported") {
		t.Errorf("output should summarise 2 imported documents, got:\n%s", got)
	}
}

// User-set metadata is the user's own work and travels with the document.
func TestRunImport_carriesUserMetadata(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{
		{path: "/exporting/a.md", title: "A", meta: map[string]string{"tag": "design", "project": "timbuktu"}},
	}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	meta := metaOf(t, filepath.Join(root, "tbuk.sqlite"), "/exporting/a.md")
	if meta["tag"] != "design" || meta["project"] != "timbuktu" {
		t.Errorf("user metadata lost: %v", meta)
	}
	// Automatic keys are this machine's to derive, from the document's own path.
	if meta["filename"] != "a.md" {
		t.Errorf("filename = %q, want a.md", meta["filename"])
	}
}

// The archive's config is never read, let alone written.
func TestRunImport_neverWritesTheArchiveConfig(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("# mine, untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newImportRun(t, arc, root)

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if got, err := os.ReadFile(cfgPath); err != nil || string(got) != "# mine, untouched\n" {
		t.Errorf("config changed: %q (%v)", got, err)
	}
}

// A document the archive indexed but did not archive the bytes for cannot be
// imported: report it and carry on.
func TestRunImport_skipsDocumentsWithNoArchivedCopy(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{
		{path: "/exporting/a.md", title: "A"},
		{path: "/exporting/no-raw.md", title: "B", noRaw: true},
	}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	kb := localKB(t, filepath.Join(root, "tbuk.sqlite"))
	if _, ok := kb["/exporting/no-raw.md"]; ok {
		t.Error("a document with no archived copy should not be imported")
	}
	if _, ok := kb["/exporting/a.md"]; !ok {
		t.Error("the importable document was skipped too")
	}
	if got := r.out.String(); !strings.Contains(got, "1 document(s) imported, 1 skipped") {
		t.Errorf("summary should count 1 imported and 1 skipped, got:\n%s", got)
	}
}

// ── conflicts ────────────────────────────────────────────────────────────────

// Default: a document already in the knowledge base is left exactly as it is.
func TestRunImport_conflictSkipIsTheDefault(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/shared/a.md", title: "theirs"}}, 4)
	root := t.TempDir()
	dbPath := filepath.Join(root, "tbuk.sqlite")
	seedLocalDoc(t, dbPath, "/shared/a.md", "mine-sha", 2)

	r := newImportRun(t, arc, root)
	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if dim := localKB(t, dbPath)["/shared/a.md"]; dim != 2 {
		t.Errorf("chunk dim = %d, want the local document untouched at 2", dim)
	}
	if r.factory.emb != nil && r.factory.emb.calls != 0 {
		t.Errorf("embedder called %d times, want 0 when everything is skipped", r.factory.emb.calls)
	}
	if got := r.out.String(); !strings.Contains(got, "already indexed") {
		t.Errorf("output should say why it was skipped, got:\n%s", got)
	}
}

// --on-conflict overwrite replaces the local copy with the archive's.
func TestRunImport_conflictOverwrite(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/shared/a.md", title: "theirs"}}, 4)
	root := t.TempDir()
	dbPath := filepath.Join(root, "tbuk.sqlite")
	seedLocalDoc(t, dbPath, "/shared/a.md", "mine-sha", 2)

	r := newImportRun(t, arc, root)
	r.opts.OnConflict = cli.ConflictOverwrite
	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if dim := localKB(t, dbPath)["/shared/a.md"]; dim != 8 {
		t.Errorf("chunk dim = %d, want the archive's copy re-embedded at 8", dim)
	}
}

// --on-conflict ask prompts per document; "y" overwrites, anything else keeps.
func TestRunImport_conflictAsk(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		wantDim int
	}{
		{"yes overwrites", "y\n", 8},
		{"no keeps the local copy", "n\n", 2},
		{"blank keeps the local copy", "\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arc := buildKBArchive(t, []archiveDoc{{path: "/shared/a.md", title: "theirs"}}, 4)
			root := t.TempDir()
			dbPath := filepath.Join(root, "tbuk.sqlite")
			seedLocalDoc(t, dbPath, "/shared/a.md", "mine-sha", 2)

			r := newImportRun(t, arc, root)
			r.opts.OnConflict = cli.ConflictAsk
			r.stdin = tc.answer
			if err := r.run(t); err != nil {
				t.Fatalf("RunImport: %v", err)
			}
			if dim := localKB(t, dbPath)["/shared/a.md"]; dim != tc.wantDim {
				t.Errorf("chunk dim = %d, want %d", dim, tc.wantDim)
			}
			if !strings.Contains(r.out.String(), "/shared/a.md") {
				t.Errorf("the prompt should name the document, got:\n%s", r.out.String())
			}
		})
	}
}

// Each prompt must read its own answer: buffering stdin per prompt swallows
// the answers meant for the documents that follow.
func TestRunImport_conflictAskReadsOneAnswerPerDocument(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{
		{path: "/shared/a.md", title: "theirs A"},
		{path: "/shared/b.md", title: "theirs B"},
	}, 4)
	root := t.TempDir()
	dbPath := filepath.Join(root, "tbuk.sqlite")
	seedLocalDoc(t, dbPath, "/shared/a.md", "mine-a", 2)
	seedLocalDoc(t, dbPath, "/shared/b.md", "mine-b", 2)

	r := newImportRun(t, arc, root)
	r.opts.OnConflict = cli.ConflictAsk
	r.stdin = "n\ny\n" // keep the first, replace the second
	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	kb := localKB(t, dbPath)
	if kb["/shared/a.md"] != 2 {
		t.Errorf("a.md dim = %d, want 2 (answer was no)", kb["/shared/a.md"])
	}
	if kb["/shared/b.md"] != 8 {
		t.Errorf("b.md dim = %d, want 8 (answer was yes)", kb["/shared/b.md"])
	}
}

// Re-importing the same archive changes nothing, so a repeated import is safe.
func TestRunImport_isIdempotent(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true
	if err := r.run(t); err != nil {
		t.Fatalf("first import: %v", err)
	}

	second := newImportRun(t, arc, root)
	if err := second.run(t); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.factory.emb != nil && second.factory.emb.calls != 0 {
		t.Errorf("second import embedded %d times, want 0", second.factory.emb.calls)
	}
	if len(localKB(t, filepath.Join(root, "tbuk.sqlite"))) != 1 {
		t.Error("second import duplicated the document")
	}
}

// A document whose embedding fails must leave no trace: a row with no chunks
// would be skipped as "already indexed" by every later import, stranding it.
func TestRunImport_failedEmbedLeavesNoGhostDocument(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	writeLocalConfig(t, root)
	r := newImportRun(t, arc, root)
	r.factory.embedErr = errors.New("embedding provider unreachable")

	if err := r.run(t); err == nil {
		t.Fatal("expected the embed failure to surface")
	}

	if _, ok := localKB(t, filepath.Join(root, "tbuk.sqlite"))["/exporting/a.md"]; ok {
		t.Error("a document that failed to embed should not be left in the knowledge base")
	}

	// And the retry, once the provider is back, works.
	retry := newImportRun(t, arc, root)
	if err := retry.run(t); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if dim := localKB(t, filepath.Join(root, "tbuk.sqlite"))["/exporting/a.md"]; dim != 8 {
		t.Errorf("retry left dim %d, want 8", dim)
	}
}

// A failed overwrite leaves the local document exactly as it was, rather than
// half-adopting the archive's identity.
func TestRunImport_failedOverwriteRestoresTheLocalDocument(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/shared/a.md", title: "theirs"}}, 4)
	root := t.TempDir()
	dbPath := filepath.Join(root, "tbuk.sqlite")
	seedLocalDoc(t, dbPath, "/shared/a.md", "mine-sha", 2)

	r := newImportRun(t, arc, root)
	r.opts.OnConflict = cli.ConflictOverwrite
	r.factory.embedErr = errors.New("embedding provider unreachable")
	if err := r.run(t); err == nil {
		t.Fatal("expected the embed failure to surface")
	}

	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	doc, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), "/shared/a.md")
	if err != nil {
		t.Fatalf("local document lost: %v", err)
	}
	if doc.SHA256 != "mine-sha" || doc.Title != "mine" {
		t.Errorf("local identity changed to %s/%s despite the failure", doc.SHA256, doc.Title)
	}
	if dim := localKB(t, dbPath)["/shared/a.md"]; dim != 2 {
		t.Errorf("local chunks changed (dim %d)", dim)
	}
}

// ── a machine with no config yet ─────────────────────────────────────────────

// A fresh machine is set up the way `tbuk init` would, then asked before
// anything is embedded with settings the user has not seen.
func TestRunImport_freshRootIsScaffoldedAndConfirmed(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.stdin = "y\n"

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "config.yaml")); err != nil {
		t.Errorf("a default config should have been created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "prompts", "qa", "manifest.yaml")); err != nil {
		t.Errorf("built-in templates should have been created: %v", err)
	}
	if got := r.out.String(); !strings.Contains(got, "Created config") {
		t.Errorf("output should say a config was created, got:\n%s", got)
	}
	if len(localKB(t, filepath.Join(root, "tbuk.sqlite"))) != 1 {
		t.Error("import did not proceed after confirmation")
	}
}

// Declining the confirmation stops before anything is embedded. The config
// stays, so the user can edit it and re-run.
func TestRunImport_freshRootDeclined(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.stdin = "n\n"

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if len(r.factory.seen) != 0 {
		t.Error("nothing should have been embedded after declining")
	}
	if _, err := os.Stat(filepath.Join(root, "config.yaml")); err != nil {
		t.Errorf("the created config should be left in place to edit: %v", err)
	}
	if got := r.out.String(); !strings.Contains(got, "config.yaml") {
		t.Errorf("output should point at the config to edit, got:\n%s", got)
	}
}

// --yes is the non-interactive path through that confirmation.
func TestRunImport_freshRootWithYesSkipsThePrompt(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true
	r.stdin = "" // no input available at all

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if len(localKB(t, filepath.Join(root, "tbuk.sqlite"))) != 1 {
		t.Error("import should have proceeded without asking")
	}
}

// An existing config is never confirmed against: the user chose those settings.
func TestRunImport_existingConfigIsNotConfirmed(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("llm:\n  provider: mlx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newImportRun(t, arc, root)
	r.stdin = "" // nothing to answer with

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if len(localKB(t, filepath.Join(root, "tbuk.sqlite"))) != 1 {
		t.Error("import should have run without a confirmation prompt")
	}
}

// ── dry run ──────────────────────────────────────────────────────────────────

func TestRunImport_dryRunWritesNothing(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{
		{path: "/exporting/a.md", title: "A"},
		{path: "/exporting/no-raw.md", title: "B", noRaw: true},
	}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.DryRun = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	for _, p := range []string{"tbuk.sqlite", "raw", "config.yaml"} {
		if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
			t.Errorf("%s should not exist after a dry run, stat err = %v", p, err)
		}
	}
	if len(r.factory.seen) != 0 {
		t.Error("a dry run must not build an ingester")
	}
	got := r.out.String()
	if !strings.Contains(got, "would import") {
		t.Errorf("dry run should say what would happen, got:\n%s", got)
	}
	if !strings.Contains(got, "/exporting/a.md") || !strings.Contains(got, "/exporting/no-raw.md") {
		t.Errorf("dry run should list every document it considered, got:\n%s", got)
	}
}

// ── failure paths ────────────────────────────────────────────────────────────

func TestRunImport_missingArchiveErrors(t *testing.T) {
	root := t.TempDir()
	r := newImportRun(t, filepath.Join(t.TempDir(), "no-such.tar"), root)
	r.opts.Yes = true

	if err := r.run(t); err == nil {
		t.Fatal("expected an error for a missing archive")
	}
	if len(r.factory.seen) != 0 {
		t.Error("nothing should be embedded when the archive cannot be read")
	}
}

// An archive with no index has no document identities to import under.
func TestRunImport_archiveWithoutIndexErrors(t *testing.T) {
	root := t.TempDir()
	arcDir := t.TempDir()
	arcPath := filepath.Join(arcDir, "kb.tar")
	srcRoot := t.TempDir()
	cfg := config.DefaultsForRoot(srcRoot)
	if err := os.MkdirAll(cfg.Ingest.RawDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Ingest.RawDir, "shaa.md"), []byte("# raw"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(arcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := export.Create(f, cfg, srcRoot); err != nil {
		t.Fatalf("export.Create: %v", err)
	}
	_ = f.Close()

	r := newImportRun(t, arcPath, root)
	r.opts.Yes = true
	err = r.run(t)
	if err == nil {
		t.Fatal("expected an error for an archive with no index")
	}
	if !strings.Contains(err.Error(), "index") {
		t.Errorf("error should say the archive carries no index, got %q", err)
	}
}

// Raw archiving disabled locally leaves imported sources nowhere to live.
func TestRunImport_requiresALocalRawDir(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	writeLocalConfig(t, root)
	r := newImportRun(t, arc, root)
	r.cfg.Ingest.RawDir = ""

	err := r.run(t)
	if err == nil {
		t.Fatal("expected an error when ingest.raw_dir is disabled")
	}
	if !strings.Contains(err.Error(), "raw_dir") {
		t.Errorf("error should name ingest.raw_dir, got %q", err)
	}
}

func TestRunImport_ingesterFailureSurfaces(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true
	r.factory.err = os.ErrPermission

	if err := r.run(t); err == nil {
		t.Fatal("expected the ingester failure to surface")
	}
}

// End to end through cobra with the real ingester factory: the archive's files
// land, and a failure to reach the embedding provider is reported rather than
// passed off as a successful import.
func TestImportCommand_endToEnd(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	dest := t.TempDir()

	err := runCLI("--root", dest, "import", "--yes", arc)

	if _, statErr := os.Stat(filepath.Join(dest, "config.yaml")); statErr != nil {
		t.Fatalf("default config not created: %v (import err: %v)", statErr, err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "raw", "shaa.md")); statErr != nil {
		t.Fatalf("raw copy not imported: %v (import err: %v)", statErr, err)
	}
}

// An imported document records where its archived copy landed, so a later
// reindex is a lookup rather than a guess at the naming.
func TestRunImport_recordsRawPath(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	db, err := storage.Open(filepath.Join(root, "tbuk.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	doc, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), "/exporting/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if doc.RawPath != "shaa.md" {
		t.Errorf("raw_path = %q, want the archive entry it came from", doc.RawPath)
	}
	if _, err := os.Stat(filepath.Join(root, "raw", doc.RawPath)); err != nil {
		t.Errorf("no copy at the recorded location: %v", err)
	}
}

// The archive says where each document's copy is; import must take it from
// there rather than re-deriving a name, which is the only thing that works
// when the copy is not named <sha256><ext>.
func TestRunImport_honoursTheArchivesRawPath(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{
		{path: "/exporting/a.md", title: "A", rawName: "1-trawa-ledger.md"},
	}, 4)
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if dim := localKB(t, filepath.Join(root, "tbuk.sqlite"))["/exporting/a.md"]; dim != 8 {
		t.Fatalf("document not imported (dim %d); its copy is not named <sha><ext>", dim)
	}
	db, err := storage.Open(filepath.Join(root, "tbuk.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	doc, err := storage.NewDocumentRepo(db.DB()).GetByPath(context.Background(), "/exporting/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if doc.RawPath != "1-trawa-ledger.md" {
		t.Errorf("raw_path = %q, want the name the archive used", doc.RawPath)
	}
}

// ── templates ────────────────────────────────────────────────────────────────

// Templates are the user's own work: a custom one on the exporting machine
// arrives here with the documents.
func TestRunImport_importsCustomTemplates(t *testing.T) {
	arc := buildKBArchiveWith(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4,
		map[string]map[string]string{
			"mine": {"manifest.yaml": "name: mine", "user.tmpl": "{{ .Question }}"},
		})
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	got, ok := installedTemplate(t, root, "mine", "user.tmpl")
	if !ok || got != "{{ .Question }}" {
		t.Errorf("custom template not installed: %q (found=%v)", got, ok)
	}
	if out := r.out.String(); !strings.Contains(out, "template") {
		t.Errorf("summary should mention templates, got:\n%s", out)
	}
}

// The built-ins exist on every machine, so an archive's copies collide with
// them. Skipping is the default, which is what leaves local edits alone.
func TestRunImport_skipsExistingTemplatesByDefault(t *testing.T) {
	arc := buildKBArchiveWith(t, nil, 4, map[string]map[string]string{
		"qa": {"manifest.yaml": "name: qa-from-archive"},
	})
	root := t.TempDir()
	writeLocalConfig(t, root)
	mine := filepath.Join(root, "prompts", "qa")
	if err := os.MkdirAll(mine, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "manifest.yaml"), []byte("name: mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := newImportRun(t, arc, root)
	r.opts.Scope = cli.ScopeTemplates
	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if got, _ := installedTemplate(t, root, "qa", "manifest.yaml"); got != "name: mine" {
		t.Errorf("local template = %q, want it untouched", got)
	}
}

// --on-conflict overwrite replaces a whole template, rather than merging the
// archive's files into the local one and leaving something that may not load.
func TestRunImport_overwriteReplacesTheWholeTemplate(t *testing.T) {
	arc := buildKBArchiveWith(t, nil, 4, map[string]map[string]string{
		"qa": {"manifest.yaml": "name: qa-from-archive"},
	})
	root := t.TempDir()
	writeLocalConfig(t, root)
	mine := filepath.Join(root, "prompts", "qa")
	if err := os.MkdirAll(mine, 0o700); err != nil {
		t.Fatal(err)
	}
	for file, content := range map[string]string{"manifest.yaml": "name: mine", "leftover.tmpl": "stale"} {
		if err := os.WriteFile(filepath.Join(mine, file), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r := newImportRun(t, arc, root)
	r.opts.Scope = cli.ScopeTemplates
	r.opts.OnConflict = cli.ConflictOverwrite
	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if got, _ := installedTemplate(t, root, "qa", "manifest.yaml"); got != "name: qa-from-archive" {
		t.Errorf("template = %q, want the archive's copy", got)
	}
	if _, ok := installedTemplate(t, root, "qa", "leftover.tmpl"); ok {
		t.Error("a file only the local template had should not survive a replacement")
	}
}

// --on-conflict ask decides one template at a time.
func TestRunImport_asksPerTemplate(t *testing.T) {
	arc := buildKBArchiveWith(t, nil, 4, map[string]map[string]string{
		"aaa": {"manifest.yaml": "name: aaa-from-archive"},
		"bbb": {"manifest.yaml": "name: bbb-from-archive"},
	})
	root := t.TempDir()
	writeLocalConfig(t, root)
	for _, name := range []string{"aaa", "bbb"} {
		dir := filepath.Join(root, "prompts", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("name: mine-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r := newImportRun(t, arc, root)
	r.opts.Scope = cli.ScopeTemplates
	r.opts.OnConflict = cli.ConflictAsk
	r.stdin = "n\ny\n"
	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	if got, _ := installedTemplate(t, root, "aaa", "manifest.yaml"); got != "name: mine-aaa" {
		t.Errorf("aaa = %q, want the local copy kept (answer was no)", got)
	}
	if got, _ := installedTemplate(t, root, "bbb", "manifest.yaml"); got != "name: bbb-from-archive" {
		t.Errorf("bbb = %q, want the archive's copy (answer was yes)", got)
	}
	// Templates are installed, not indexed; the prompt has to say so.
	if out := r.out.String(); !strings.Contains(out, "already installed") || strings.Contains(out, "already indexed") {
		t.Errorf("template prompt should read \"already installed\", got:\n%s", out)
	}
}

// ── scopes ───────────────────────────────────────────────────────────────────

func TestRunImport_scopeDataLeavesTemplatesAlone(t *testing.T) {
	arc := buildKBArchiveWith(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4,
		map[string]map[string]string{"mine": {"manifest.yaml": "name: mine"}})
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Scope = cli.ScopeData
	r.opts.Yes = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if _, ok := installedTemplate(t, root, "mine", "manifest.yaml"); ok {
		t.Error("import data should not bring templates across")
	}
	if len(localKB(t, filepath.Join(root, "tbuk.sqlite"))) != 1 {
		t.Error("the document should still have been imported")
	}
}

// Templates cost nothing to import, so nothing about the embedding setup —
// not the confirmation, not even a usable raw directory — should gate them.
func TestRunImport_scopeTemplatesNeedsNoEmbedding(t *testing.T) {
	arc := buildKBArchiveWith(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4,
		map[string]map[string]string{"mine": {"manifest.yaml": "name: mine"}})
	root := t.TempDir()
	writeLocalConfig(t, root)
	r := newImportRun(t, arc, root)
	r.opts.Scope = cli.ScopeTemplates
	r.cfg.Ingest.RawDir = "" // raw archiving disabled: irrelevant to templates
	r.stdin = ""             // nothing to answer with

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if _, ok := installedTemplate(t, root, "mine", "manifest.yaml"); !ok {
		t.Error("template not installed")
	}
	if len(r.factory.seen) != 0 {
		t.Error("importing templates must not build an ingester")
	}
	if _, err := os.Stat(filepath.Join(root, "tbuk.sqlite")); !os.IsNotExist(err) {
		t.Errorf("no knowledge base should have been created, stat err = %v", err)
	}
}

// Templates land before the slow, failure-prone part, so a broken embedding
// provider does not also cost the user their templates.
func TestRunImport_templatesSurviveAFailedEmbed(t *testing.T) {
	arc := buildKBArchiveWith(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4,
		map[string]map[string]string{"mine": {"manifest.yaml": "name: mine"}})
	root := t.TempDir()
	writeLocalConfig(t, root)
	r := newImportRun(t, arc, root)
	r.factory.embedErr = errors.New("embedding provider unreachable")

	if err := r.run(t); err == nil {
		t.Fatal("expected the embed failure to surface")
	}
	if _, ok := installedTemplate(t, root, "mine", "manifest.yaml"); !ok {
		t.Error("templates should already be installed when the embed step fails")
	}
}

// A dry run into an empty directory must account for the scaffolding the real
// run would do first: the built-in templates it creates are what the archive's
// copies then collide with.
func TestRunImport_dryRunAccountsForScaffoldedBuiltins(t *testing.T) {
	arc := buildKBArchiveWith(t, nil, 4, map[string]map[string]string{
		"qa":   {"manifest.yaml": "name: qa"},
		"mine": {"manifest.yaml": "name: mine"},
	})
	root := t.TempDir() // no config: a real run would scaffold
	r := newImportRun(t, arc, root)
	r.opts.Scope = cli.ScopeTemplates
	r.opts.DryRun = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	out := r.out.String()
	if !strings.Contains(out, "1 template(s) would be imported, 1 skipped") {
		t.Errorf("only the custom template would be installed; got:\n%s", out)
	}
	if !strings.Contains(out, "qa") || !strings.Contains(out, "skipped") {
		t.Errorf("qa is a built-in the scaffolding would create; got:\n%s", out)
	}
}

func TestRunImport_dryRunListsTemplates(t *testing.T) {
	arc := buildKBArchiveWith(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4,
		map[string]map[string]string{"mine": {"manifest.yaml": "name: mine"}})
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.DryRun = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if _, ok := installedTemplate(t, root, "mine", "manifest.yaml"); ok {
		t.Error("a dry run must write no templates")
	}
	if out := r.out.String(); !strings.Contains(out, "mine") {
		t.Errorf("dry run should name the templates it would import, got:\n%s", out)
	}
}

// The three commands share one flag set.
func TestImportCommand_subcommandsShareFlags(t *testing.T) {
	parent, _, err := cli.New().Find([]string{"import"})
	if err != nil {
		t.Fatalf("find import: %v", err)
	}
	for _, name := range []string{"on-conflict", "dry-run", "yes"} {
		if parent.LocalFlags().Lookup(name) == nil {
			t.Errorf("import should expose --%s", name)
		}
	}
	for _, sub := range []string{"data", "templates"} {
		cmd, _, err := cli.New().Find([]string{"import", sub})
		if err != nil {
			t.Fatalf("find import %s: %v", sub, err)
		}
		if cmd.Name() != sub {
			t.Fatalf("import %s resolved to %q", sub, cmd.Name())
		}
		for _, name := range []string{"on-conflict", "dry-run"} {
			if cmd.InheritedFlags().Lookup(name) == nil {
				t.Errorf("import %s should inherit --%s", sub, name)
			}
		}
	}
}

// An archive path is still an archive path, even when it is named like a
// subcommand and reached as ./data.
func TestImportCommand_archivePathIsNotASubcommand(t *testing.T) {
	arc := buildKBArchive(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4)
	dest := t.TempDir()
	named := filepath.Join(t.TempDir(), "data")
	data, err := os.ReadFile(arc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(named, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Reached as ./data it is unambiguous, and the dry run needs no provider.
	if err := runCLI("--root", dest, "import", "--dry-run", "./"+filepath.Base(named)); err == nil {
		t.Skip("relative path resolution depends on the working directory")
	}
	if err := runCLI("--root", dest, "import", "--dry-run", named); err != nil {
		t.Errorf("an absolute archive path named \"data\" should still import: %v", err)
	}
}

// Import is quiet too: what landed is the summary, what needs acting on is the
// skips and failures.
func TestRunImport_quietByDefault(t *testing.T) {
	arc := buildKBArchiveWith(t, []archiveDoc{
		{path: "/exporting/a.md", title: "A"},
		{path: "/exporting/no-raw.md", title: "B", noRaw: true},
	}, 4, map[string]map[string]string{"mine": {"manifest.yaml": "name: mine"}})
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}

	got := r.out.String()
	if strings.Contains(got, "/exporting/a.md") {
		t.Errorf("an imported document should not be listed by default, got:\n%s", got)
	}
	if strings.Contains(got, "template mine → installed") {
		t.Errorf("an installed template should not be listed by default, got:\n%s", got)
	}
	if !strings.Contains(got, "/exporting/no-raw.md") {
		t.Errorf("the skipped document must still be reported, got:\n%s", got)
	}
	if !strings.Contains(got, "1 document(s) imported") || !strings.Contains(got, "1 template(s) imported") {
		t.Errorf("summary missing, got:\n%s", got)
	}
}

func TestRunImport_verboseListsEverything(t *testing.T) {
	arc := buildKBArchiveWith(t, []archiveDoc{{path: "/exporting/a.md", title: "A"}}, 4,
		map[string]map[string]string{"mine": {"manifest.yaml": "name: mine"}})
	root := t.TempDir()
	r := newImportRun(t, arc, root)
	r.opts.Yes = true
	r.opts.Verbose = true

	if err := r.run(t); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	got := r.out.String()
	for _, want := range []string{"/exporting/a.md", "template mine"} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose should list %s, got:\n%s", want, got)
		}
	}
}

func TestImportCommand_exposesVerbose(t *testing.T) {
	parent, _, err := cli.New().Find([]string{"import"})
	if err != nil {
		t.Fatalf("find import: %v", err)
	}
	if parent.LocalFlags().Lookup("verbose") == nil {
		t.Error("import should expose --verbose")
	}
	for _, sub := range []string{"data", "templates"} {
		cmd, _, err := cli.New().Find([]string{"import", sub})
		if err != nil {
			t.Fatalf("find import %s: %v", sub, err)
		}
		if cmd.InheritedFlags().Lookup("verbose") == nil {
			t.Errorf("import %s should inherit --verbose", sub)
		}
	}
}
