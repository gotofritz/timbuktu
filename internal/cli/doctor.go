package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/chunking"
	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/prompts"
	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/storage"
)

const hostedNotProbed = "hosted API — not probed; set ANTHROPIC_API_KEY/OPENAI_API_KEY"

// isHostedProvider reports whether a provider is a hosted API (claude/openai)
// whose endpoints don't follow the local-server /health & /v1/models
// conventions, so probing them yields misleading results.
func isHostedProvider(provider string) bool {
	return provider == "claude" || provider == "openai"
}

// statusProbeURL picks the liveness endpoint for a local provider. llama.cpp
// and ollama expose /health; MLX servers (mlx_lm.server & co.) don't, so mlx
// is probed via /v1/models — the one endpoint every OpenAI-compatible server
// has.
func statusProbeURL(provider, baseURL string) string {
	if provider == "mlx" {
		return baseURL + "/v1/models"
	}
	return baseURL + "/health"
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, database, and service connectivity",
		RunE:  makeDoctorRunner(nil),
	}
}

func makeDoctorRunner(client *http.Client) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		if client == nil {
			client = &http.Client{Timeout: 3 * time.Second}
		}
		cfg := configFrom(cmd)
		cfgPath := configPathFrom(cmd)
		if cfgPath == "" {
			cfgPath = config.DefaultPath()
		}
		return runDoctor(cmd.OutOrStdout(), client, cfg, cfgPath)
	}
}

// RunDoctor executes the doctor checks, writing to stdout. Exported for testing.
func RunDoctor(client *http.Client, cfg config.Config, cfgPath string) error {
	return runDoctor(os.Stdout, client, cfg, cfgPath)
}

// RunDoctorTo executes the doctor checks, writing the report to w.
// Exported for testing.
func RunDoctorTo(w io.Writer, client *http.Client, cfg config.Config, cfgPath string) error {
	return runDoctor(w, client, cfg, cfgPath)
}

func runDoctor(w io.Writer, client *http.Client, cfg config.Config, cfgPath string) error {
	printSection(w, "Platform")
	printCheck(w, "os", runtime.GOOS+"/"+runtime.GOARCH, "")
	homeMsg, homeStatus := CheckHome()
	printCheck(w, "home", homeMsg, homeStatus)

	printSection(w, "Config")
	printCheck(w, "path", cfgPath, "")
	msg, ok := CheckConfig(cfgPath)
	printCheck(w, "status", msg, boolToStatus(ok))

	printSection(w, "Database")
	printCheck(w, "path", cfg.Database.Path, "")
	msg, dbOK := CheckDB(cfg.Database.Path)
	printCheck(w, "status", msg, boolToStatus(dbOK))
	if dbOK {
		db, err := storage.Open(cfg.Database.Path)
		if err == nil {
			sqlDB := db.DB()
			ctx := context.Background()
			if n, err := storage.NewDocumentRepo(sqlDB).Count(ctx); err == nil {
				printCheck(w, "documents", fmt.Sprintf("%d", n), "")
			}
			if n, err := storage.NewChunkRepo(sqlDB).Count(ctx); err == nil {
				printCheck(w, "chunks", fmt.Sprintf("%d", n), "")
			}
			msg, status := sessionsMsg(ctx, sqlDB)
			printCheck(w, "sessions", msg, status)
			_ = db.Close()
		}
		if info, err := os.Stat(cfg.Database.Path); err == nil {
			printCheck(w, "size", humanBytes(info.Size()), "")
		}
	}

	printSection(w, "LLM ("+cfg.LLM.Provider+")")
	printCheck(w, "url", cfg.LLM.BaseURL, "")
	if isHostedProvider(cfg.LLM.Provider) {
		printCheck(w, "status", hostedNotProbed, "")
		printCheck(w, "model", cfg.LLM.Model, "")
	} else {
		msg, ok = CheckHTTP(statusProbeURL(cfg.LLM.Provider, cfg.LLM.BaseURL), client)
		printCheck(w, "status", msg, boolToStatus(ok))
		printCheck(w, "model", CheckLLMModel(cfg.LLM.BaseURL, cfg.LLM.Model, client), "")
	}
	printCheck(w, "max_tokens", fmt.Sprintf("%d", cfg.LLM.MaxTokens), "")
	ctxMsg, ctxStatus := contextBudgetMsg(cfg.LLM)
	printCheck(w, "context", ctxMsg, ctxStatus)

	printSection(w, "Embedding ("+cfg.Embedding.Provider+")")
	printCheck(w, "url", cfg.Embedding.BaseURL, "")
	switch {
	case isHostedProvider(cfg.Embedding.Provider):
		printCheck(w, "status", hostedNotProbed, "")
	case cfg.Embedding.BaseURL == cfg.LLM.BaseURL && !isHostedProvider(cfg.LLM.Provider):
		printCheck(w, "status", "same server as LLM", "✓")
	default:
		msg, ok = CheckHTTP(statusProbeURL(cfg.Embedding.Provider, cfg.Embedding.BaseURL), client)
		printCheck(w, "status", msg, boolToStatus(ok))
	}
	printCheck(w, "dimension", fmt.Sprintf("%d (config)", cfg.Embedding.Dimension), "")
	if dbOK {
		dimMsg, dimStatus := CheckEmbeddingDimension(cfg.Database.Path, cfg.Embedding.Dimension)
		printCheck(w, "stored", dimMsg, dimStatus)
	}

	printSection(w, "Preprocessing")
	printCheck(w, "extractors", "markdown, text, html, pdf", "✓")

	printSection(w, "Chunking")
	printCheck(w, "size", fmt.Sprintf("%d tokens (keep ≤ the embedding server's batch)", cfg.Chunking.Size), "")
	printCheck(w, "overlap", fmt.Sprintf("%d tokens", cfg.Chunking.Overlap), "")
	printCheck(w, "estimator", "script-aware (~4 ASCII chars, ~1 CJK rune, ~2 Latin accents per token)", "")
	if dbOK {
		budgetMsg, budgetStatus := CheckChunkBudget(cfg.Database.Path, cfg.Chunking.Size)
		printCheck(w, "stored", budgetMsg, budgetStatus)
	}

	printSection(w, "Search")
	// FTS5 health depends only on the database, not on any embedding server.
	ftsStatus := "✓"
	encodingMsg, encodingStatus := "search_text (reduced encoding)", "✓"
	tokenizerMsg, tokenizerStatus := storage.FTSTokenizer+" (exact terms, phrases, exclusions)", "✓"
	if dbOK {
		if db2, err2 := storage.Open(cfg.Database.Path); err2 == nil {
			if err3 := search.CheckFTS5(db2.DB()); err3 != nil {
				ftsStatus = "✗"
			}
			if ok, err3 := storage.HasSearchTextColumn(db2.DB()); err3 == nil && !ok {
				encodingMsg = "chunks.search_text missing — run scripts/add-search-text/, then tbuk reindex"
				encodingStatus = "✗"
			}
			// An index built under an earlier tokenizer answers every query
			// without error and gets the punctuation-sensitive ones wrong, so
			// say it out loud rather than leave the user to notice. Which
			// earlier one it is decides what is wrong with the answers, so
			// name it.
			if tok, err3 := storage.ReadFTSTokenizer(db2.DB()); err3 == nil && tok != storage.FTSTokenizer {
				tokenizerMsg, tokenizerStatus = staleTokenizerMsg(tok), "✗"
			}
			_ = db2.Close()
		}
	}
	printCheck(w, "fts5", "available", ftsStatus)
	printCheck(w, "indexed", encodingMsg, encodingStatus)
	printCheck(w, "tokenizer", tokenizerMsg, tokenizerStatus)
	printCheck(w, "vector", "available (cosine, in-process)", "✓")
	printCheck(w, "hybrid", "available (RRF)", "✓")

	printSection(w, "Prompts")
	printCheck(w, "dir", promptsRoot(cfg), "")
	manifests, listErr := promptsDir(cfg).List()
	if listErr != nil || len(manifests) == 0 {
		printCheck(w, "templates", "none", "")
	} else {
		names := make([]string, len(manifests))
		for i, m := range manifests {
			names[i] = m.Name
		}
		printCheck(w, "templates", strings.Join(names, ", "), "✓")
		budgetMsg, budgetStatus := templateBudgetsMsg(manifests, cfg.LLM)
		printCheck(w, "budgets", budgetMsg, budgetStatus)
	}

	return nil
}

// sessionsMsg reports the conversation threads this knowledge base holds, or
// that it cannot hold any.
//
// A knowledge base built before the tables existed opens, ingests and searches
// fine — nothing but `tbuk ask --session` touches them — so without this line
// the first threaded question is the first anyone hears of it, as a raw
// "no such table" (issue #157).
func sessionsMsg(ctx context.Context, db *sql.DB) (msg, status string) {
	ok, err := storage.HasSessionTables(db)
	if err != nil {
		return "not checked (" + err.Error() + ")", ""
	}
	if !ok {
		return "tables missing — ask --session cannot record a thread; run scripts/add-sessions/", "✗"
	}
	n, err := storage.NewSessionRepo(db).List(ctx)
	if err != nil {
		return "available", "✓"
	}
	return fmt.Sprintf("%d threads", len(n)), "✓"
}

// contextBudgetMsg describes the context window and what it leaves for the
// prompt once the reply's max_tokens are held back. A window of 0 means the
// guard is off — legal, but worth saying, since an oversized prompt is then
// only caught by the provider.
func contextBudgetMsg(cfg config.LLMConfig) (msg, status string) {
	if cfg.ContextTokens <= 0 {
		return "0 — budget guard off; an oversized prompt is only caught by the provider", ""
	}
	if cfg.ContextTokens <= cfg.MaxTokens {
		return fmt.Sprintf(
			"%d ≤ max_tokens %d — no tokens left for the prompt; raise llm.context_tokens",
			cfg.ContextTokens, cfg.MaxTokens), "✗"
	}
	return fmt.Sprintf("%d (%d for the prompt after max_tokens %d)",
		cfg.ContextTokens, cfg.ContextTokens-cfg.MaxTokens, cfg.MaxTokens), "✓"
}

// templateBudgetsMsg names the templates whose own max_tokens swallows the
// window they run in — every ask on one fails the budget check, so it is worth
// hearing before the first ask does.
func templateBudgetsMsg(manifests []prompts.Manifest, cfg config.LLMConfig) (msg, status string) {
	var over []string
	for _, m := range manifests {
		window := m.ContextTokens
		if window <= 0 {
			window = cfg.ContextTokens
		}
		reserve := m.MaxTokens
		if reserve <= 0 {
			reserve = cfg.MaxTokens
		}
		if window > 0 && reserve >= window {
			over = append(over, fmt.Sprintf("%s: max_tokens %d ≥ window %d", m.Name, reserve, window))
		}
	}
	if len(over) > 0 {
		return strings.Join(over, "; ") + " — no room for a prompt; lower max_tokens or raise context_tokens", "✗"
	}
	if cfg.ContextTokens <= 0 {
		return "not checked (context guard off)", ""
	}
	return "every template leaves room for a prompt", "✓"
}

// staleTokenizerMsg describes an index built under an earlier tokenizer and
// names the script that rebuilds it. The FTS5 default splits every term at
// '_'; tokenchars '_-' (issue #136) instead holds hyphenated English together
// as one token, so the words it is made of stop reaching it (issue #143).
func staleTokenizerMsg(tokenizer string) string {
	what := "'_' splits terms"
	if tokenizer == "" {
		tokenizer = "default unicode61"
	} else {
		what = "'-' locks hyphenated words into one term"
	}
	return fmt.Sprintf("%s — %s; run scripts/retokenize-fts/", tokenizer, what)
}

// CheckHome reports the home directory the default data root is derived from —
// $HOME on Unix, %USERPROFILE% on Windows. When it cannot be resolved,
// config.DefaultRoot falls back to a relative ".tbuk", which quietly moves the
// whole knowledge base with the working directory, so that case is a failure
// rather than a note.
func CheckHome() (msg, status string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Sprintf("unresolved (%v) — every default path falls back to a relative %q "+
			"under the working directory; pass --root or set the home variable", err, config.DefaultRoot()), "✗"
	}
	return home, "✓"
}

// CheckLLMModel tries GET {baseURL}/v1/models and returns the first model ID.
// Falls back to the model name from config if the probe fails or returns no models.
func CheckLLMModel(baseURL, cfgModel string, client *http.Client) string {
	resp, err := client.Get(baseURL + "/v1/models") //nolint:noctx
	if err != nil {
		return cfgModel
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return cfgModel
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return cfgModel
	}
	if len(body.Data) == 0 {
		return cfgModel
	}
	return body.Data[0].ID
}

// CheckEmbeddingDimension reports the dimension of the embeddings stored in the
// database and whether it matches the configured embedding dimension. A
// mismatch means vector search will silently return nothing, so it is flagged
// as a failure. An empty knowledge base or an unreadable DB is reported
// neutrally (no status marker).
func CheckEmbeddingDimension(dbPath string, cfgDim int) (msg, status string) {
	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Sprintf("cannot open: %v", err), ""
	}
	defer func() { _ = db.Close() }()

	dim, found, err := storage.NewChunkRepo(db.DB()).EmbeddingDimension(context.Background(), 0)
	if err != nil {
		return fmt.Sprintf("inconsistent stored dimensions: %v", err), "✗"
	}
	if !found {
		return "no embeddings stored yet", ""
	}
	if cfgDim > 0 && dim != cfgDim {
		return fmt.Sprintf(
			"stored %d, config %d — MISMATCH: vector search will return nothing; "+
				"run `tbuk reindex` or restore embedding.dimension", dim, cfgDim), "✗"
	}
	return fmt.Sprintf("%d (matches config)", dim), "✓"
}

// chunkBudgetSample bounds what CheckChunkBudget re-measures. The longest
// chunks are the only ones that can overrun a batch, so a sample of them
// answers the question without reading the whole knowledge base.
const chunkBudgetSample = 20

// CheckChunkBudget re-measures the largest stored chunks against the
// configured chunking.size with the current estimator. Chunks written before
// the estimator became script-aware (issue #120) were sized in bytes, so a CJK
// chunk holds roughly three times the tokens the budget allowed — which is
// what makes an embedding server answer HTTP 500 on a re-embed. Nothing else
// in the report would say so, and re-chunking is the only fix.
func CheckChunkBudget(dbPath string, size int) (msg, status string) {
	if size <= 0 {
		return fmt.Sprintf("chunking.size is %d — text is not split at all", size), "✗"
	}
	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Sprintf("cannot open: %v", err), ""
	}
	defer func() { _ = db.Close() }()

	texts, err := storage.NewChunkRepo(db.DB()).LongestChunkTexts(context.Background(), chunkBudgetSample)
	if err != nil {
		return fmt.Sprintf("cannot measure: %v", err), ""
	}
	if len(texts) == 0 {
		return "no chunks stored yet", ""
	}

	over, largest := 0, 0
	for _, text := range texts {
		n := chunking.CountTokens(text)
		if n > size {
			over++
		}
		if n > largest {
			largest = n
		}
	}
	if over > 0 {
		subject := "chunks are"
		if over == 1 {
			subject = "chunk is"
		}
		return fmt.Sprintf(
			"%d of %d sampled %s over chunking.size %d (largest %d) — indexed under an "+
				"older estimator or a larger size; re-chunk with `tbuk reindex`",
			over, len(texts), subject, size, largest), "✗"
	}
	return fmt.Sprintf("largest of %d sampled: %d tokens (within chunking.size %d)",
		len(texts), largest, size), "✓"
}

// CheckConfig verifies the config file exists and contains valid YAML.
func CheckConfig(path string) (string, bool) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "not found (run tbuk init)", false
	}
	_, err := config.Load(path)
	if err != nil {
		return fmt.Sprintf("invalid YAML: %v", err), false
	}
	return "valid", true
}

// CheckDB verifies the SQLite database can be opened.
func CheckDB(path string) (string, bool) {
	db, err := storage.Open(path)
	if err != nil {
		return fmt.Sprintf("cannot open: %v", err), false
	}
	_ = db.Close()
	return "open", true
}

// CheckHTTP verifies an HTTP endpoint returns 2xx.
func CheckHTTP(url string, client *http.Client) (string, bool) {
	resp, err := client.Get(url) //nolint:noctx // doctor is a CLI diagnostic; context cancellation not needed
	if err != nil {
		return fmt.Sprintf("unreachable: %v", err), false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf("HTTP %d", resp.StatusCode), false
	}
	return fmt.Sprintf("healthy (HTTP %d)", resp.StatusCode), true
}

func printSection(w io.Writer, name string) {
	fmt.Fprintf(w, "\n%s\n", name) //nolint:errcheck
}

func printCheck(w io.Writer, key, val, status string) {
	if status != "" {
		fmt.Fprintf(w, "  %-10s %s %s\n", key+":", status, val) //nolint:errcheck
	} else {
		fmt.Fprintf(w, "  %-10s %s\n", key+":", val) //nolint:errcheck
	}
}

func boolToStatus(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
