package searchtext_test

import (
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/searchtext"
)

// fields makes an assertion about token presence readable: order matters for
// the identifier-then-split-forms rule, but whitespace shape does not.
func fields(s string) []string { return strings.Fields(s) }

func contains(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

func TestReduce_passes_prose_through_unchanged(t *testing.T) {
	in := "The meter is read hourly. Consumption is logged to the database.\n\nSee the notes."
	if got := searchtext.Reduce(in); got != in {
		t.Errorf("prose was rewritten:\n got %q\nwant %q", got, in)
	}
}

func TestReduce_empty_input(t *testing.T) {
	if got := searchtext.Reduce(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// An inline span is where an identifier turns up in ordinary notes, so it gets
// the same treatment a code region's identifiers get: the term itself, plus its
// split words, with the backticks dropped.
func TestReduce_inline_span_gets_identifier_treatment(t *testing.T) {
	got := searchtext.Reduce("The `main_consumption` field is read hourly.")
	toks := fields(got)
	for _, want := range []string{"main_consumption", "main", "consumption", "field", "hourly."} {
		if !contains(toks, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "`") {
		t.Errorf("backticks kept: %q", got)
	}
	if !strings.HasPrefix(got, "The main_consumption") {
		t.Errorf("prose around the span was not preserved: %q", got)
	}
}

func TestReduce_inline_span_camel_case(t *testing.T) {
	got := searchtext.Reduce("Call `readMeter` at boot.")
	for _, want := range []string{"readMeter", "read", "meter"} {
		if !contains(fields(got), want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestReduce_code_keeps_comments_verbatim(t *testing.T) {
	in := "```go\n// poll the meter every minute\nfor i := 0; i < 10; i++ {\n}\n```"
	got := searchtext.Reduce(in)
	if !strings.Contains(got, "poll the meter every minute") {
		t.Errorf("comment prose lost: %q", got)
	}
	if strings.Contains(got, "//") {
		t.Errorf("comment marker kept: %q", got)
	}
}

func TestReduce_code_drops_syntax(t *testing.T) {
	in := "```go\nfunc readMeter(id int) error {\n\treturn nil\n}\n```"
	got := searchtext.Reduce(in)
	for _, unwanted := range []string{"{", "}", "(", ")", "func", "return", "int"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("syntax %q survived: %q", unwanted, got)
		}
	}
	for _, want := range []string{"readMeter", "read", "meter"} {
		if !contains(fields(got), want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestReduce_code_drops_numeric_literals(t *testing.T) {
	got := searchtext.Reduce("```go\ninterval = 3600\n```")
	if contains(fields(got), "3600") {
		t.Errorf("numeric literal kept: %q", got)
	}
	if !contains(fields(got), "interval") {
		t.Errorf("identifier lost: %q", got)
	}
}

func TestReduce_code_keeps_string_literal_contents(t *testing.T) {
	got := searchtext.Reduce("```go\nreturn fmt.Errorf(\"read holding: %w\", err)\n```")
	toks := fields(got)
	for _, want := range []string{"read", "holding"} {
		if !contains(toks, want) {
			t.Errorf("string content %q lost: %q", want, got)
		}
	}
	if strings.Contains(got, `"`) {
		t.Errorf("quotes kept: %q", got)
	}
}

func TestReduce_code_splits_identifiers(t *testing.T) {
	tests := []struct {
		name string
		code string
		want []string
	}{
		{"snake_case", "```go\nx := main_consumption\n```", []string{"main_consumption", "main", "consumption"}},
		{"camelCase", "```go\nx := readMeter\n```", []string{"readMeter", "read", "meter"}},
		{"kebab-case", "```sh\nmake check-ci\n```", []string{"check-ci", "check", "ci"}},
		{"dotted path", "```go\nx := internal/storage/migrate.go\n```",
			[]string{"internal/storage/migrate.go", "internal", "storage", "migrate"}},
		{"acronym run", "```go\nx := HTTPServer\n```", []string{"HTTPServer", "http", "server"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchtext.Reduce(tt.code)
			for _, want := range tt.want {
				if !contains(fields(got), want) {
					t.Errorf("missing %q in %q", want, got)
				}
			}
		})
	}
}

// The identifier is emitted before its split forms, so an exact-term query and
// a loose word query both reach the same chunk.
func TestReduce_emits_identifier_before_split_forms(t *testing.T) {
	got := searchtext.Reduce("```go\nvar main_consumption int\n```")
	toks := fields(got)
	idx := map[string]int{}
	for i, tok := range toks {
		if _, seen := idx[tok]; !seen {
			idx[tok] = i
		}
	}
	if idx["main_consumption"] > idx["main"] {
		t.Errorf("split form emitted before the identifier: %q", got)
	}
}

func TestReduce_code_emits_language_tag(t *testing.T) {
	got := searchtext.Reduce("```python\nread_meter()\n```")
	if !contains(fields(got), "python") {
		t.Errorf("language tag lost: %q", got)
	}
}

func TestReduce_language_tag_informs_the_stop_list(t *testing.T) {
	got := searchtext.Reduce("```sql\nSELECT reading FROM meters\n```")
	if contains(fields(got), "SELECT") || contains(fields(got), "FROM") {
		t.Errorf("SQL keywords survived: %q", got)
	}
	for _, want := range []string{"reading", "meters"} {
		if !contains(fields(got), want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// A chunk boundary can cut a fenced block, leaving an opening fence with no
// closing one. The code still has to be reduced, not passed through as prose.
func TestReduce_unclosed_fence_is_still_code(t *testing.T) {
	got := searchtext.Reduce("Notes.\n\n```go\nfunc readMeter() {\n")
	if strings.Contains(got, "func") || strings.Contains(got, "{") {
		t.Errorf("unclosed fence not reduced: %q", got)
	}
	if !contains(fields(got), "readMeter") {
		t.Errorf("identifier lost: %q", got)
	}
}

func TestReduce_mixes_prose_and_code(t *testing.T) {
	in := "Here is the poll loop.\n\n```go\n// read the meter\nreadMeter(id)\n```\n\nIt runs hourly."
	got := searchtext.Reduce(in)
	for _, want := range []string{"Here", "poll", "loop.", "read", "the", "meter", "readMeter", "runs", "hourly."} {
		if !contains(fields(got), want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestReduce_drops_fence_markers(t *testing.T) {
	got := searchtext.Reduce("```go\nreadMeter()\n```")
	if strings.Contains(got, "```") {
		t.Errorf("fence markers kept: %q", got)
	}
}

func TestReduce_code_of_pure_syntax_reduces_to_nothing(t *testing.T) {
	if got := searchtext.Reduce("```\n{ }\n```"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestReduce_comment_styles(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"hash", "```python\n# poll the meter\nx = 1\n```"},
		{"block", "```c\n/* poll the meter */\nint x;\n```"},
		{"sql dash", "```sql\n-- poll the meter\nSELECT 1\n```"},
		{"html", "```html\n<!-- poll the meter -->\n<p>x</p>\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchtext.Reduce(tt.code)
			if !strings.Contains(got, "poll the meter") {
				t.Errorf("comment lost: %q", got)
			}
		})
	}
}

// A URL in a code region is not a comment: the // belongs to the scheme.
func TestReduce_url_scheme_is_not_a_comment(t *testing.T) {
	got := searchtext.Reduce("```sh\ncurl https://example.com/meters\n```")
	for _, want := range []string{"example.com/meters", "example", "meters"} {
		if !contains(fields(got), want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestReduce_unterminated_string_still_yields_terms(t *testing.T) {
	got := searchtext.Reduce("```go\nx := \"read holding\n```")
	for _, want := range []string{"read", "holding"} {
		if !contains(fields(got), want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestReduce_single_character_code_tokens_are_dropped(t *testing.T) {
	got := searchtext.Reduce("```go\nfor i := range meters {\n}\n```")
	if contains(fields(got), "i") {
		t.Errorf("single-character token kept: %q", got)
	}
	if !contains(fields(got), "meters") {
		t.Errorf("identifier lost: %q", got)
	}
}

func TestReduce_non_ascii_identifiers_survive(t *testing.T) {
	got := searchtext.Reduce("```go\nx := zählerStand\n```")
	for _, want := range []string{"zählerStand", "zähler", "stand"} {
		if !contains(fields(got), want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestReduce_prose_with_no_code_is_byte_identical(t *testing.T) {
	in := "Meter readings\n\nThe main consumption register is read hourly, and logged."
	if got := searchtext.Reduce(in); got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}
