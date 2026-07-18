package cli

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
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

	printSection("LLM (" + cfg.LLM.Provider + ")")
	printCheck("url", cfg.LLM.BaseURL, "")
	msg, ok = CheckHTTP(cfg.LLM.BaseURL+"/health", client)
	printCheck("status", msg, boolToStatus(ok))

	printSection("Embedding (" + cfg.Embedding.Provider + ")")
	printCheck("url", cfg.Embedding.BaseURL, "")
	if cfg.Embedding.BaseURL == cfg.LLM.BaseURL {
		printCheck("status", "same server as LLM", "✓")
	} else {
		msg, ok = CheckHTTP(cfg.Embedding.BaseURL+"/health", client)
		printCheck("status", msg, boolToStatus(ok))
	}

	return nil
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
