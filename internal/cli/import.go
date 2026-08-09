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
		merge bool
		force bool
	)

	cmd := &cobra.Command{
		Use:   "import <archive>",
		Short: "Import a knowledge base from a tar archive",
		Long: "Import restores a knowledge base from an archive written by " +
			"`tbuk export`, placing the database, extracted-text cache, raw " +
			"archive and prompt templates under the target root.\n\n" +
			"By default the archive's config is adopted and every data folder is " +
			"re-homed under the target root (--root, else ~/.tbuk). With --merge, " +
			"the target's existing config is kept and the imported folders are " +
			"copied into the locations it names (defaults when there is no target " +
			"config). Existing files are left untouched unless --force is given.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunImport(cmd.OutOrStdout(), args[0], configFrom(cmd), rootFrom(cmd), configPathFrom(cmd), merge, force)
		},
	}

	cmd.Flags().BoolVar(&merge, "merge", false, "merge into the target config's folders instead of adopting the archive config")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files (default: skip files that already exist)")
	return cmd
}

// RunImport restores the archive at archivePath under root. Without merge it
// adopts the archive's config (written to configPath) and re-homes each data
// folder at the root's default locations; with merge it keeps the target's
// existing cfg and copies the imported folders into the paths cfg names. In both
// modes existing files are skipped unless force is set. Exported for testing.
func RunImport(out io.Writer, archivePath string, cfg config.Config, root, configPath string, merge, force bool) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive %s: %w", archivePath, err)
	}
	defer func() { _ = f.Close() }()

	opts := importer.Options{Root: root, Force: force}
	if merge {
		// Place data into the folders the existing target config names; leave
		// that config in place.
		opts.Config = cfg
	} else {
		// Adopt the archive's config and re-home every folder under the root; the
		// archive's commented paths resolve to these defaults.
		opts.Config = config.DefaultsForRoot(root)
		opts.ConfigDest = configPath
	}

	res, err := importer.Import(f, opts)
	if err != nil {
		return fmt.Errorf("%s: %w", archivePath, err)
	}

	fmt.Fprintf(out, "Imported %d file(s) to %s", len(res.Written), root) //nolint:errcheck
	if len(res.Skipped) > 0 {
		fmt.Fprintf(out, " (%d skipped; pass --force to overwrite)", len(res.Skipped)) //nolint:errcheck
	}
	fmt.Fprintln(out) //nolint:errcheck
	return nil
}
