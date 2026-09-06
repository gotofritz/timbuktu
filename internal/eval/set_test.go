package eval_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/eval"
)

const fullSet = `
version: 1
name: go-docs
description: follow-up questions over the Go corpus
cases:
  - id: slices-growth
    query: how do slices grow?
    relevant:
      - path: go/slices.md
        contains: capacity is doubled
        grade: 2
      - path: go/internals.md
    answer: |
      append reallocates when len == cap.
    must_include: [append, cap]
  - id: maps-followup
    thread:
      - question: how do slices grow?
        answer: A slice grows when append finds len == cap.
    query: and maps?
    gold_query: how do Go maps grow as they fill up?
    relevant:
      - path: go/maps.md
`

func TestParseSet_full(t *testing.T) {
	set, err := eval.ParseSet(strings.NewReader(fullSet))
	if err != nil {
		t.Fatalf("ParseSet: %v", err)
	}
	if set.Name != "go-docs" || set.Version != 1 {
		t.Fatalf("set = %+v, want name go-docs version 1", set)
	}
	if len(set.Cases) != 2 {
		t.Fatalf("got %d cases, want 2", len(set.Cases))
	}

	c := set.Cases[0]
	if c.ID != "slices-growth" || c.Query != "how do slices grow?" {
		t.Errorf("case 0 = %+v", c)
	}
	if len(c.Relevant) != 2 {
		t.Fatalf("case 0 labels = %d, want 2", len(c.Relevant))
	}
	if got := c.Relevant[0]; got.Path != "go/slices.md" || got.Contains != "capacity is doubled" || got.Grade != 2 {
		t.Errorf("label 0 = %+v", got)
	}
	// An unset grade is 1: every label is relevant, and only nDCG cares how much.
	if got := c.Relevant[1].Grade; got != 1 {
		t.Errorf("unset grade = %d, want the default 1", got)
	}
	// A block scalar carries a trailing newline that would break must_include
	// comparisons and print oddly in a report.
	if strings.HasSuffix(c.Answer, "\n") {
		t.Errorf("answer = %q, want the trailing newline trimmed", c.Answer)
	}
	if len(c.MustInclude) != 2 {
		t.Errorf("must_include = %v, want 2 entries", c.MustInclude)
	}

	f := set.Cases[1]
	if f.GoldQuery != "how do Go maps grow as they fill up?" {
		t.Errorf("gold_query = %q", f.GoldQuery)
	}
	if len(f.Thread) != 1 || f.Thread[0].Question != "how do slices grow?" {
		t.Errorf("thread = %+v", f.Thread)
	}
}

func TestParseSet_errors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string // substring the error must name
	}{
		{
			name: "no version",
			yaml: "name: x\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: p.md\n",
			want: "version",
		},
		{
			name: "future version",
			yaml: "version: 2\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: p.md\n",
			want: "version 2",
		},
		{
			name: "no cases",
			yaml: "version: 1\nname: empty\n",
			want: "no cases",
		},
		{
			name: "case without id",
			yaml: "version: 1\ncases:\n  - query: q\n    relevant:\n      - path: p.md\n",
			want: "id",
		},
		{
			name: "duplicate ids",
			yaml: "version: 1\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: p.md\n  - id: a\n    query: r\n    relevant:\n      - path: p.md\n",
			want: "duplicate",
		},
		{
			name: "case without query",
			yaml: "version: 1\ncases:\n  - id: a\n    relevant:\n      - path: p.md\n",
			want: "query",
		},
		{
			name: "label without path",
			yaml: "version: 1\ncases:\n  - id: a\n    query: q\n    relevant:\n      - contains: hello\n",
			want: "path",
		},
		{
			name: "negative grade",
			yaml: "version: 1\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: p.md\n        grade: -1\n",
			want: "grade",
		},
		{
			// A case with no labels, no reference answer and no required
			// substrings cannot be scored by any stage; it is dead weight that
			// silently dilutes every macro-average it sits in.
			name: "case measures nothing",
			yaml: "version: 1\ncases:\n  - id: a\n    query: q\n",
			want: "nothing to score",
		},
		{
			name: "thread turn without question",
			yaml: "version: 1\ncases:\n  - id: a\n    query: q\n    thread:\n      - answer: hi\n    relevant:\n      - path: p.md\n",
			want: "question",
		},
		{
			// A typo in a key is a label set that silently measures something
			// other than what was written down.
			name: "unknown key",
			yaml: "version: 1\ncases:\n  - id: a\n    query: q\n    relevent:\n      - path: p.md\n",
			want: "relevent",
		},
		{
			name: "malformed yaml",
			yaml: "version: 1\ncases: [oops\n",
			want: "parse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := eval.ParseSet(strings.NewReader(tt.yaml))
			if err == nil {
				t.Fatalf("ParseSet(%q) = nil error, want one naming %q", tt.name, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to name %q", err, tt.want)
			}
		})
	}
}

func TestParseSet_generationOnlyCaseIsValid(t *testing.T) {
	// No labels, but a reference answer: scorable by the generation stage, so
	// it is a legitimate case rather than dead weight.
	const y = "version: 1\ncases:\n  - id: a\n    query: q\n    answer: the answer\n"
	set, err := eval.ParseSet(strings.NewReader(y))
	if err != nil {
		t.Fatalf("ParseSet: %v", err)
	}
	if len(set.Cases[0].Relevant) != 0 {
		t.Errorf("relevant = %v, want none", set.Cases[0].Relevant)
	}
}

func TestLoadSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go-docs.yaml")
	writeFile(t, path, "version: 1\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: p.md\n")

	set, err := eval.LoadSet(path)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}
	// An unnamed set takes the file's name, so a report is self-describing and
	// --baseline has something to compare identities with.
	if set.Name != "go-docs" {
		t.Errorf("name = %q, want it defaulted from the filename", set.Name)
	}

	if _, err := eval.LoadSet(filepath.Join(dir, "absent.yaml")); err == nil {
		t.Error("LoadSet(absent) = nil error, want one")
	}
}

func TestLoadSet_namedSetKeepsItsName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go-docs.yaml")
	writeFile(t, path, "version: 1\nname: chosen\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: p.md\n")

	set, err := eval.LoadSet(path)
	if err != nil {
		t.Fatalf("LoadSet: %v", err)
	}
	if set.Name != "chosen" {
		t.Errorf("name = %q, want the file's own name to win", set.Name)
	}
}
