package eval_test

import (
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/eval"
)

func TestMatchesPath(t *testing.T) {
	tests := []struct {
		name  string
		label string
		doc   string
		want  bool
	}{
		{"suffix on a separator boundary", "go/slices.md", "/home/u/notes/go/slices.md", true},
		{"bare filename", "slices.md", "/home/u/notes/go/slices.md", true},
		{"whole path", "/home/u/notes/go/slices.md", "/home/u/notes/go/slices.md", true},
		{"relative equal", "go/slices.md", "go/slices.md", true},
		// The boundary is the whole point: without it a label for slices.md
		// would silently also credit go-slices.md, and the number would be
		// wrong in the direction that flatters the retriever.
		{"not mid-segment", "slices.md", "/home/u/notes/go-slices.md", false},
		{"not a partial segment", "ices.md", "/home/u/notes/slices.md", false},
		// The same corpus indexed on Windows and on Linux has to score the
		// same, or a label set is worthless for the comparison it exists for.
		{"windows separators", "go/slices.md", `D:\notes\go\slices.md`, true},
		{"windows drive in the label", `D:\notes\go\slices.md`, "d:/notes/go/slices.md", true},
		{"case folded", "GO/Slices.MD", "/home/u/notes/go/slices.md", true},
		{"leading slash in the label", "/go/slices.md", "/home/u/notes/go/slices.md", true},
		{"label longer than the document path", "notes/go/slices.md", "/go/slices.md", false},
		{"empty label", "", "/home/u/notes/go/slices.md", false},
		{"empty document path", "slices.md", "", false},
		{"different document", "go/maps.md", "/home/u/notes/go/slices.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eval.MatchesPath(tt.label, tt.doc); got != tt.want {
				t.Errorf("MatchesPath(%q, %q) = %v, want %v", tt.label, tt.doc, got, tt.want)
			}
		})
	}
}

func TestMatchesText(t *testing.T) {
	tests := []struct {
		name   string
		anchor string
		text   string
		want   bool
	}{
		{"no anchor matches anything", "", "whatever the chunk says", true},
		{"plain substring", "capacity is doubled", "…the capacity is doubled…", true},
		{"case folded", "Capacity Is Doubled", "the capacity is doubled", true},
		// A chunk boundary that reflows, or a preprocessor that collapsed a
		// line break, must not turn a passing case into a failing one: that is
		// noise in the instrument, and noise reads exactly like a regression.
		{"whitespace collapsed", "capacity is doubled", "capacity  is\n\tdoubled", true},
		{"anchor written across lines", "capacity\nis doubled", "capacity is doubled", true},
		// Punctuation is signal, so it is not normalised away.
		{"spacing inside a token is not whitespace", "len == cap", "grows when len==cap", false},
		{"absent", "capacity is doubled", "maps are unordered", false},
		{"anchor longer than the text", "capacity is doubled today", "capacity is doubled", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eval.MatchesText(tt.anchor, tt.text); got != tt.want {
				t.Errorf("MatchesText(%q, %q) = %v, want %v", tt.anchor, tt.text, got, tt.want)
			}
		})
	}
}

func TestLabelMatches(t *testing.T) {
	label := eval.Label{Path: "go/slices.md", Contains: "capacity is doubled"}
	tests := []struct {
		name   string
		result eval.Result
		want   bool
	}{
		{"right document, right passage", eval.Result{Path: "/n/go/slices.md", Text: "the capacity is doubled"}, true},
		{"right document, wrong passage", eval.Result{Path: "/n/go/slices.md", Text: "a slice header is three words"}, false},
		{"wrong document, right passage", eval.Result{Path: "/n/go/maps.md", Text: "the capacity is doubled"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := label.Matches(tt.result); got != tt.want {
				t.Errorf("Matches(%+v) = %v, want %v", tt.result, got, tt.want)
			}
		})
	}

	// Without an anchor the document alone is the label.
	bare := eval.Label{Path: "go/slices.md"}
	if !bare.Matches(eval.Result{Path: "/n/go/slices.md", Text: "anything at all"}) {
		t.Error("a label with no anchor should be satisfied by any passage of its document")
	}
}

const pathCheckSet = `
version: 1
name: paths
cases:
  - id: found
    query: q
    relevant:
      - path: go/slices.md
  - id: missing
    query: q
    relevant:
      - path: go/generics.md
  - id: ambiguous
    query: q
    relevant:
      - path: notes.md
`

func TestSetCheckPaths(t *testing.T) {
	set, err := eval.ParseSet(strings.NewReader(pathCheckSet))
	if err != nil {
		t.Fatalf("ParseSet: %v", err)
	}
	indexed := []string{
		"/home/u/go/slices.md",
		"/home/u/work/notes.md",
		"/home/u/personal/notes.md",
	}

	missing, ambiguous := set.CheckPaths(indexed)

	if len(missing) != 1 || missing[0].CaseID != "missing" || missing[0].Path != "go/generics.md" {
		t.Fatalf("missing = %+v, want the one unindexed label", missing)
	}
	if len(missing[0].Matches) != 0 {
		t.Errorf("missing label matched %v, want nothing", missing[0].Matches)
	}

	if len(ambiguous) != 1 || ambiguous[0].CaseID != "ambiguous" {
		t.Fatalf("ambiguous = %+v, want the one label matching two documents", ambiguous)
	}
	// Naming both is the point: scoring against whichever sorted first is how
	// a harness quietly starts lying.
	if len(ambiguous[0].Matches) != 2 {
		t.Errorf("ambiguous matches = %v, want both documents named", ambiguous[0].Matches)
	}
}

func TestSetCheckPaths_duplicateIndexEntriesAreNotAmbiguity(t *testing.T) {
	set, err := eval.ParseSet(strings.NewReader(pathCheckSet))
	if err != nil {
		t.Fatalf("ParseSet: %v", err)
	}
	// The same document listed twice — different spellings of one path — is one
	// document, not two candidates.
	indexed := []string{
		"/home/u/go/slices.md",
		`\home\u\go\slices.md`,
		"/home/u/work/notes.md",
		"/home/u/personal/notes.md",
	}
	_, ambiguous := set.CheckPaths(indexed)
	for _, a := range ambiguous {
		if a.Path == "go/slices.md" {
			t.Errorf("one document spelled two ways was reported ambiguous: %+v", a)
		}
	}
}

func TestSetCheckPaths_allResolved(t *testing.T) {
	const y = "version: 1\ncases:\n  - id: a\n    query: q\n    relevant:\n      - path: go/slices.md\n"
	set, err := eval.ParseSet(strings.NewReader(y))
	if err != nil {
		t.Fatalf("ParseSet: %v", err)
	}
	missing, ambiguous := set.CheckPaths([]string{"/home/u/go/slices.md"})
	if len(missing) != 0 || len(ambiguous) != 0 {
		t.Errorf("missing = %v, ambiguous = %v, want neither", missing, ambiguous)
	}
}
