package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/search"
	"github.com/gotofritz/timbuktu/internal/storage"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, database, and service connectivity",
		RunE:  makeDoctorRunner(nil),
	}
}

func makeDoctorRunner(client *http.Client) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		if client == nil {
			client = &http.Client{Timeout: 3 * time.Second}
		}
		cfg := Config()
		cfgPath := cfgFile
		if cfgPath == "" {
			cfgPath = config.DefaultPath()
		}
		return runDoctor(client, cfg, cfgPath)
	}
}

// RunDoctor executes the doctor checks. Exported for testing.
func RunDoctor(client *http.Client, cfg config.Config, cfgPath string) error {
	return runDoctor(client, cfg, cfgPath)
}

func runDoctor(client *http.Client, cfg config.Config, cfgPath string) error {
	printSection("Config")
	printCheck("path", cfgPath, "")
	msg, ok := CheckConfig(cfgPath)
	printCheck("status", msg, boolToStatus(ok))

	printSection("Database")
	printCheck("path", cfg.Database.Path, "")
	msg, ok = CheckDB(cfg.Database.Path)
	printCheck("status", msg, boolToStatus(ok))
	if ok {
		db, err := storage.Open(cfg.Database.Path)
		if err == nil {
			sqlDB := db.DB()
			if n, err := CountDocuments(sqlDB); err == nil {
				printCheck("documents", fmt.Sprintf("%d", n), "")
			}
			if n, err := CountChunks(sqlDB); err == nil {
				printCheck("chunks", fmt.Sprintf("%d", n), "")
			}
			_ = db.Close()
		}
	}

	printSection("LLM (" + cfg.LLM.Provider + ")")
	printCheck("url", cfg.LLM.BaseURL, "")
	msg, ok = CheckHTTP(cfg.LLM.BaseURL+"/health", client)
	printCheck("status", msg, boolToStatus(ok))
	printCheck("model", CheckLLMModel(cfg.LLM.BaseURL, cfg.LLM.Model, client), "")
	printCheck("max_tokens", fmt.Sprintf("%d", cfg.LLM.MaxTokens), "")

	printSection("Embedding (" + cfg.Embedding.Provider + ")")
	printCheck("url", cfg.Embedding.BaseURL, "")
	if cfg.Embedding.BaseURL == cfg.LLM.BaseURL {
		printCheck("status", "same server as LLM", "✓")
	} else {
		msg, ok = CheckHTTP(cfg.Embedding.BaseURL+"/health", client)
		printCheck("status", msg, boolToStatus(ok))
	}

	printSection("Preprocessing")
	printCheck("extractors", "markdown, text, html, pdf", "✓")

	printSection("Search")
	ftsStatus := "✓"
	if ok {
		if db2, err2 := storage.Open(cfg.Database.Path); err2 == nil {
			if err3 := search.CheckFTS5(db2.DB()); err3 != nil {
				ftsStatus = "✗"
			}
			_ = db2.Close()
		}
	}
	printCheck("fts5", "available", ftsStatus)
	printCheck("vector", "available (cosine, in-process)", "✓")
	printCheck("hybrid", "available (RRF)", "✓")

	printSection("Prompts")
	printCheck("dir", promptsRoot(), "")
	manifests, listErr := promptsDir().List()
	if listErr != nil || len(manifests) == 0 {
		printCheck("templates", "none", "")
	} else {
		names := make([]string, len(manifests))
		for i, m := range manifests {
			names[i] = m.Name
		}
		printCheck("templates", strings.Join(names, ", "), "✓")
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

func printSection(name string) {
	fmt.Printf("\n%s\n", name)
}

func printCheck(key, val, status string) {
	if status != "" {
		fmt.Printf("  %-10s %s %s\n", key+":", status, val)
	} else {
		fmt.Printf("  %-10s %s\n", key+":", val)
	}
}

func boolToStatus(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
