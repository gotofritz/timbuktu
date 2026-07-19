package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/storage"
)

const hostedNotProbed = "hosted API — not probed; set ANTHROPIC_API_KEY/OPENAI_API_KEY"

// isHostedProvider reports whether a provider is a hosted API (claude/openai)
// whose endpoints don't follow the llama.cpp/ollama /health & /v1/models
// conventions, so probing them yields misleading results.
func isHostedProvider(provider string) bool {
	return provider == "claude" || provider == "openai"
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
			if n, err := CountDocuments(sqlDB); err == nil {
				printCheck(w, "documents", fmt.Sprintf("%d", n), "")
			}
			if n, err := CountChunks(sqlDB); err == nil {
				printCheck(w, "chunks", fmt.Sprintf("%d", n), "")
			}
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
		msg, ok = CheckHTTP(cfg.LLM.BaseURL+"/health", client)
		printCheck(w, "status", msg, boolToStatus(ok))
		printCheck(w, "model", CheckLLMModel(cfg.LLM.BaseURL, cfg.LLM.Model, client), "")
	}
	printCheck(w, "max_tokens", fmt.Sprintf("%d", cfg.LLM.MaxTokens), "")

	printSection(w, "Embedding ("+cfg.Embedding.Provider+")")
	printCheck(w, "url", cfg.Embedding.BaseURL, "")
	switch {
	case isHostedProvider(cfg.Embedding.Provider):
		printCheck(w, "status", hostedNotProbed, "")
	case cfg.Embedding.BaseURL == cfg.LLM.BaseURL && !isHostedProvider(cfg.LLM.Provider):
		printCheck(w, "status", "same server as LLM", "✓")
	default:
		msg, ok = CheckHTTP(cfg.Embedding.BaseURL+"/health", client)
		printCheck(w, "status", msg, boolToStatus(ok))
	}
	printCheck(w, "dimension", fmt.Sprintf("%d (config)", cfg.Embedding.Dimension), "")
	if dbOK {
		dimMsg, dimStatus := CheckEmbeddingDimension(cfg.Database.Path, cfg.Embedding.Dimension)
		printCheck(w, "stored", dimMsg, dimStatus)
	}

	printSection(w, "Preprocessing")
	printCheck(w, "extractors", "markdown, text, html, pdf", "✓")

	printSection(w, "Search")
	// FTS5 health depends only on the database, not on any embedding server.
	ftsStatus := "✓"
	if dbOK {
		if db2, err2 := storage.Open(cfg.Database.Path); err2 == nil {
			if err3 := search.CheckFTS5(db2.DB()); err3 != nil {
				ftsStatus = "✗"
			}
			_ = db2.Close()
		}
	}
	printCheck(w, "fts5", "available", ftsStatus)
	printCheck(w, "vector", "available (cosine, in-process)", "✓")
	printCheck(w, "hybrid", "available (RRF)", "✓")

	printSection(w, "Prompts")
	printCheck(w, "dir", promptsRoot(), "")
	manifests, listErr := promptsDir().List()
	if listErr != nil || len(manifests) == 0 {
		printCheck(w, "templates", "none", "")
	} else {
		names := make([]string, len(manifests))
		for i, m := range manifests {
			names[i] = m.Name
		}
		printCheck(w, "templates", strings.Join(names, ", "), "✓")
	}

	return nil
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
				"re-ingest the corpus or restore embedding.dimension", dim, cfgDim), "✗"
	}
	return fmt.Sprintf("%d (matches config)", dim), "✓"
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
