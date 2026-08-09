package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gotofritz/timbuktu/internal/config"
	"github.com/gotofritz/timbuktu/internal/export"
)

func newExportCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "export <path>",
		Short: "Export the knowledge base to a tar archive",
		Long: "Export bundles the config plus every configured data folder " +
			"(database, extracted text, raw archive, prompt templates) into a " +
			"single tar archive.\n\n" +
			"If <path> is an existing directory, a timestamped file is written " +
			"inside it; otherwise <path> is the archive file. An existing target " +
			"is overwritten only after confirmation, or immediately with --force. " +
			"Data-folder paths are commented out in the archived config so a later " +
			"import re-homes each component under its own --root.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := ResolveExportTarget(args[0], time.Now())
			if err != nil {
				return err
			}
			return RunExport(cmd.InOrStdin(), cmd.OutOrStdout(), configFrom(cmd), rootFrom(cmd), target, force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing archive without prompting")
	return cmd
}

// ResolveExportTarget maps the user's <path> argument to a concrete archive
// file. An existing directory yields a timestamped file inside it; an existing
// file, or a non-existent path, is treated as the archive path itself. A
// non-existent path with a trailing separator is rejected — it names a
// directory that does not exist. Exported for testing.
func ResolveExportTarget(arg string, now time.Time) (string, error) {
	info, err := os.Stat(arg)
	switch {
	case err == nil && info.IsDir():
		return filepath.Join(arg, "tbuk-export-"+now.Format("20060102-150405")+".tar"), nil
	case err == nil:
		return arg, nil
	case os.IsNotExist(err):
		if strings.HasSuffix(arg, string(os.PathSeparator)) || strings.HasSuffix(arg, "/") {
			return "", fmt.Errorf("directory does not exist: %s", arg)
		}
		return arg, nil
	default:
		return "", fmt.Errorf("stat %s: %w", arg, err)
	}
}

// RunExport writes the knowledge-base archive for cfg to target. When target
// already exists and force is false, it prompts for overwrite on in/out and
// aborts on anything but yes. The archive is written to a temporary file in the
// destination directory and renamed into place, so an interrupted run never
// leaves a half-written .tar. Exported for testing.
func RunExport(in io.Reader, out io.Writer, cfg config.Config, root, target string, force bool) error {
	switch _, err := os.Stat(target); {
	case err == nil && !force:
		if !ConfirmYes(in, out, fmt.Sprintf("Overwrite %s? [y/N] ", target)) {
			fmt.Fprintln(out, "Aborted.") //nolint:errcheck
			return nil
		}
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("stat %s: %w", target, err)
	}

	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create output dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tbuk-export-*.tar.tmp")
	if err != nil {
		return fmt.Errorf("create temp archive: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if err := export.Create(tmp, cfg, root); err != nil {
		_ = tmp.Close()
		return err
	}
	// Read the archive back before publishing it: a damaged snapshot must fail
	// here rather than at import time, when the source may be long gone.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind temp archive: %w", err)
	}
	if err := export.Verify(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp archive: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod archive: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("finalise archive %s: %w", target, err)
	}

	fmt.Fprintf(out, "Exported knowledge base to %s\n", target) //nolint:errcheck
	return nil
}
