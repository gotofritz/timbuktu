package cli

import (
	"database/sql"
	"fmt"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/embeddings"
	"github.com/gotofritz/timbuktu/internal/ingest"
	"github.com/gotofritz/timbuktu/internal/llm"
	"github.com/gotofritz/timbuktu/internal/storage"
)

// App is the assembled dependency graph for a single CLI command: an open
// database plus the embedder, repositories, ingester and LLM built from it.
// It centralises the open/close and construction boilerplate every command
// used to repeat, so adding a dependency means touching one builder instead of
// each command. Accessors are lazy and memoized — a command pays only for what
// it uses (stats never builds an embedder; keyword search never touches the
// LLM). App is not safe for concurrent use; each command builds its own.
type App struct {
	cfg config.Config
	db  *storage.DB

	docs *storage.DocumentRepo
	emb  embeddings.Embedder
}

// openApp opens the database at cfg.Database.Path and returns an App wrapping
// it. The caller must Close the returned App to release the connection.
func openApp(cfg config.Config) (*App, error) {
	db, err := storage.Open(cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &App{cfg: cfg, db: db}, nil
}

// Close releases the underlying database connection.
func (a *App) Close() error { return a.db.Close() }

// DB returns the underlying *sql.DB.
func (a *App) DB() *sql.DB { return a.db.DB() }

// Docs returns the document repository, building it once on first use.
func (a *App) Docs() *storage.DocumentRepo {
	if a.docs == nil {
		a.docs = storage.NewDocumentRepo(a.DB())
	}
	return a.docs
}

// Embedder returns the configured embedder, building it once on first use.
func (a *App) Embedder() (embeddings.Embedder, error) {
	if a.emb == nil {
		emb, err := embeddings.NewEmbedder(a.cfg.Embedding)
		if err != nil {
			return nil, fmt.Errorf("embedder: %w", err)
		}
		a.emb = emb
	}
	return a.emb, nil
}

// Ingester builds an ingester wired to the app's repositories, embedder,
// chunker and extracted-text output directory.
func (a *App) Ingester() (*ingest.Ingester, error) {
	emb, err := a.Embedder()
	if err != nil {
		return nil, err
	}
	sqlDB := a.DB()
	return ingest.NewIngester(
		a.Docs(),
		storage.NewChunkRepo(sqlDB),
		storage.NewMetadataRepo(sqlDB),
		&ingest.DefaultFileExtractor{},
		&chunking.Chunker{Size: a.cfg.Chunking.Size, Overlap: a.cfg.Chunking.Overlap},
		emb,
		a.cfg.Preprocess.OutputDir,
	), nil
}

// LLM returns the configured LLM provider.
func (a *App) LLM() (llm.LLM, error) {
	l, err := llm.NewLLM(&a.cfg.LLM)
	if err != nil {
		return nil, fmt.Errorf("create LLM: %w", err)
	}
	return l, nil
}
