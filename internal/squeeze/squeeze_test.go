package squeeze_test

import (
	"testing"

	"github.com/gotofritz/timbuktu/internal/retrieval"
	"github.com/gotofritz/timbuktu/internal/squeeze"
)

func TestText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
		{
			name: "whitespace only collapses to nothing",
			in:   "   \n\t\n  ",
			want: "",
		},
		{
			name: "articles and filler go",
			in:   "The quick brown fox is really very fast.",
			want: "quick brown fox is fast.",
		},
		{
			name: "filler mid-sentence, case-insensitively",
			in:   "It is Basically an ACTUALLY simple idea.",
			want: "It is simple idea.",
		},
		{
			name: "runs of whitespace collapse",
			in:   "alpha  beta\t\tgamma   delta",
			want: "alpha beta gamma delta",
		},
		{
			name: "blank line runs collapse to one",
			in:   "one\n\n\n\ntwo\n\n\nthree",
			want: "one\n\ntwo\n\nthree",
		},
		{
			name: "leading and trailing blank lines go",
			in:   "\n\n  first\n\nlast  \n\n\n",
			want: "first\n\nlast",
		},
		{
			name: "fenced code is byte-exact",
			in:   "The setup:\n\n```go\nif the == a {\n\treturn  the\n}\n```\n\nThe end.",
			want: "setup:\n\n```go\nif the == a {\n\treturn  the\n}\n```\n\nend.",
		},
		{
			name: "tilde fence is byte-exact too",
			in:   "Note the shape:\n~~~\nthe  a   an\n~~~",
			want: "Note shape:\n~~~\nthe  a   an\n~~~",
		},
		{
			name: "unterminated fence keeps the rest verbatim",
			in:   "The list:\n```\nthe  a\nan  the",
			want: "list:\n```\nthe  a\nan  the",
		},
		{
			name: "indented code is byte-exact",
			in:   "The example:\n\n    the  answer = a + an\n\nThe end.",
			want: "example:\n\n    the  answer = a + an\n\nend.",
		},
		{
			name: "tab-indented code is byte-exact",
			in:   "Run it:\n\n\tthe  answer\n\nDone.",
			want: "Run it:\n\n\tthe  answer\n\nDone.",
		},
		{
			name: "inline code spans survive",
			in:   "Call `the.Thing()` on the handler",
			want: "Call `the.Thing()` on handler",
		},
		{
			name: "words carrying punctuation are kept",
			in:   "It matters: the, an. A?",
			want: "It matters: the, an. A?",
		},
		{
			name: "urls and identifiers are untouched",
			in:   "See https://example.com/the/a and the_the value",
			want: "See https://example.com/the/a and the_the value",
		},
		{
			name: "non-latin text is untouched",
			in:   "東京は とても 大きい 都市",
			want: "東京は とても 大きい 都市",
		},
		{
			name: "a line of only stop words disappears, not its neighbours",
			in:   "before\nthe a an\nafter",
			want: "before\n\nafter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := squeeze.Text(tt.in)
			if got != tt.want {
				t.Errorf("Text(%q):\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestText_idempotent(t *testing.T) {
	inputs := []string{
		"The quick brown fox is really very fast.\n\n\nAnd the dog.",
		"A note:\n\n```sh\nthe  command\n```\n\nThe end.",
		"    indented\n\nplain the text",
	}
	for _, in := range inputs {
		once := squeeze.Text(in)
		twice := squeeze.Text(once)
		if once != twice {
			t.Errorf("Text not idempotent for %q:\n once %q\ntwice %q", in, once, twice)
		}
	}
}

func TestText_shrinksProse(t *testing.T) {
	in := "The report is really quite long, and the author actually wrote " +
		"a very detailed summary of the results."
	got := squeeze.Text(in)
	if len(got) >= len(in) {
		t.Errorf("expected prose to shrink: %d bytes → %d bytes (%q)", len(in), len(got), got)
	}
}

func TestChunks(t *testing.T) {
	in := []retrieval.RetrievedChunk{
		{ChunkID: 1, Text: "The  first chunk is really long.", Citation: "a.md §0", Score: 0.9},
		{ChunkID: 2, Text: "the a an", Citation: "b.md §3", Score: 0.4},
	}
	original := in[0].Text

	got := squeeze.Chunks(in)

	if len(got) != len(in) {
		t.Fatalf("chunk count: want %d, got %d", len(in), len(got))
	}
	if got[0].Text != "first chunk is long." {
		t.Errorf("chunk 0 text: got %q", got[0].Text)
	}
	// Squeezing a chunk down to nothing would drop a retrieved passage without
	// saying so; keep the original text instead.
	if got[1].Text != in[1].Text {
		t.Errorf("chunk squeezed to empty should keep its text, got %q", got[1].Text)
	}
	if got[0].Citation != "a.md §0" || got[0].Score != 0.9 || got[0].ChunkID != 1 {
		t.Errorf("citation/score/id must survive: %+v", got[0])
	}
	if in[0].Text != original {
		t.Errorf("input mutated: %q", in[0].Text)
	}
}

func TestChunks_empty(t *testing.T) {
	if got := squeeze.Chunks(nil); got != nil {
		t.Errorf("Chunks(nil): want nil, got %v", got)
	}
}
