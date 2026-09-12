//go:build record

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// saveBaseline writes the recorded metrics next to the vectors they score.
func saveBaseline(b Baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal baseline: %w", err)
	}
	path := filepath.Join(fixtureCorpusDir, baselineFile)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}
	return nil
}

// TestRecordFixtureBaseline scores the fixture corpus against whatever is
// currently in vectors.json and writes the metrics next to it.
//
// It runs as the second half of `make eval-record`, after the vectors are
// written, so the two files always describe the same instrument. Recording the
// baseline rather than hand-writing it into the test is what makes switching
// embedder a one-command job: re-record, read the diff, commit it on purpose.
//
// It lives here rather than beside the vector recorder because scoring needs a
// database and an ingest pipeline, and internal/eval has neither by design.
func TestRecordFixtureBaseline(t *testing.T) {
	baseline, reports := scoreFixtureCorpus(t)
	if err := saveBaseline(baseline); err != nil {
		t.Fatalf("save baseline: %v", err)
	}
	for _, name := range sweepNames() {
		m := baseline.Runs[name]
		t.Logf("%-15s hit %.3f  recall %.3f  P %.3f  MRR %.3f  nDCG %.3f  (%d cases, %d skipped)",
			name, m.Hit, m.Recall, m.Precision, m.MRR, m.NDCG, m.Cases, len(reports[name].Skipped))
	}
	t.Logf("recorded baseline for %s at dimension %d", baseline.Model, baseline.Dimension)
}
