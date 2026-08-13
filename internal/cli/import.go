package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/importer"
)

func newImportCmd() *cobra.Command {
	var (
		merge       bool
		forceConfig bool
		forceData   bool
	)

	cmd := &cobra.Command{
		Use:   "import <archive>",
		Short: "Import a knowledge base from a tar archive",
		Long: "Import restores a knowledge base from an archive written by " +
			"`tbuk export`, placing the database, extracted-text cache, raw " +
			"archive and prompt templates under the target root.\n\n" +
			"The archive's config is adopted when the target root (--root, else " +
			"~/.tbuk) has no config.yaml yet, or when --force-config replaces the " +
			"one it has; every data folder is then re-homed at that root's default " +
			"locations. Otherwise the target's own config is kept and the imported " +
			"folders are copied into the locations it names — the same placement " +
			"--merge asks for explicitly — so the restored data always sits where " +
			"the config in effect points.\n\n" +
			"Existing files are left untouched unless the matching flag is given: " +
			"--force-config replaces the target's config.yaml with the archive's, " +
			"--force-data replaces the database, extracted text, raw archive and " +
			"prompts. A machine's own config is protected separately from the " +
			"knowledge base it points at.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunImport(cmd.OutOrStdout(), args[0], configFrom(cmd), rootFrom(cmd), configPathFrom(cmd), merge, forceConfig, forceData)
		},
	}

	cmd.Flags().BoolVar(&merge, "merge", false, "never adopt the archive config, even when the target has none")
	cmd.Flags().BoolVar(&forceConfig, "force-config", false, "replace the target's config.yaml with the archive's, and re-home the data under the root (ignored with --merge)")
	cmd.Flags().BoolVar(&forceData, "force-data", false, "overwrite existing database, extracted text, raw archive and prompt files")
	return cmd
}

// RunImport restores the archive at archivePath under root.
//
// The archive's config is adopted — written to configPath — only when there is
// no config there yet or forceConfig replaces it, and never under merge; data is
// then placed at the root's default locations, which is what the archive's
// commented paths resolve to. Otherwise the target's cfg survives and names
// where the data goes, so the restored folders match whichever config is in
// effect afterwards.
//
// Existing files are skipped unless the matching force flag is set: forceConfig
// for the archive's config, forceData for the knowledge base it describes.
// Exported for testing.
func RunImport(out io.Writer, archivePath string, cfg config.Config, root, configPath string, merge, forceConfig, forceData bool) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive %s: %w", archivePath, err)
	}
	defer func() { _ = f.Close() }()

	// Data goes where the config that will be in effect after the import says.
	// The archive's config is adopted only when the target has none yet or
	// --force-config replaces it; otherwise the target's config survives and
	// therefore governs placement, or the restored folders would sit somewhere
	// the live config does not point at.
	adopt := !merge && (forceConfig || !exists(configPath))

	opts := importer.Options{Root: root, ForceConfig: forceConfig, ForceData: forceData}
	if adopt {
		// The archive's commented paths resolve to the root's defaults.
		opts.Config = config.DefaultsForRoot(root)
		opts.ConfigDest = configPath
	} else {
		opts.Config = cfg
	}

	res, err := importer.Import(f, opts)
	if err != nil {
		return fmt.Errorf("%s: %w", archivePath, err)
	}

	fmt.Fprintf(out, "Imported %d file(s) to %s", len(res.Written), root) //nolint:errcheck
	if len(res.Skipped) > 0 {
		fmt.Fprintf(out, " (%d skipped; pass --force-config / --force-data to overwrite)", len(res.Skipped)) //nolint:errcheck
	}
	fmt.Fprintln(out) //nolint:errcheck
	if !merge && !adopt {
		fmt.Fprintf(out, "Kept the existing config at %s; data was placed in the folders it names "+ //nolint:errcheck
			"(pass --force-config to adopt the archive's config instead)\n", configPath)
	}
	return nil
}

// exists reports whether path is present. An unreadable path counts as present:
// the caller's next step would fail on it anyway, and treating it as missing
// would silently overwrite it.
func exists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}
