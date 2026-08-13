package normalize_test

import (
	"strings"
	"testing"

	"github.com/gotofritz/timbuktu/internal/normalize"
)

// ankiConfig is the pipeline the builtin anki template declares.
func ankiConfig() normalize.Config {
	return normalize.Config{
		Filters: []string{"strip_preamble", "strip_fences", "strip_list_markers", "collapse_blank_lines"},
		Records: &normalize.RecordsConfig{Separator: "----", Fields: []string{"lead", "note", "body"}},
	}
}

// Real output from a long run: the model holds the format for the first records,
// then drifts back into markdown lists and stops emitting separators.
const driftedOutput = `Here are the flashcards:

----

What are the key features of CSDS volume curves?
- Aggregated as a volume-weighted average of period feed-in
- Distributed across consuming malos by existing share of consumption

How are CSDS customers invoiced?
- Consumer pays 0 for energy consumed
- Producer receives revenues for excess energy via direct marketing invoice:
  Revenues = excess energy volume curve × day-ahead price

What are the two main tariff models for CSDS?
1. **Model A**: Pay as produced with PPA price fixed at 0
   - Consumer receives excess energy revenues via supply invoice

2. **Model B**: Pay as produced with PPA price based on day-ahead price
   - Consumer pays day-ahead price for energy
`

const driftedNormalized = `----

What are the key features of CSDS volume curves?

Aggregated as a volume-weighted average of period feed-in
Distributed across consuming malos by existing share of consumption

----

How are CSDS customers invoiced?

Consumer pays 0 for energy consumed
Producer receives revenues for excess energy via direct marketing invoice:
  Revenues = excess energy volume curve × day-ahead price

----

What are the two main tariff models for CSDS?

**Model A**: Pay as produced with PPA price fixed at 0
Consumer receives excess energy revenues via supply invoice
**Model B**: Pay as produced with PPA price based on day-ahead price
Consumer pays day-ahead price for energy
`

func TestApply_records(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "drifted output regains separators and loses list markup",
			in:   driftedOutput,
			want: driftedNormalized,
		},
		{
			name: "already canonical output is unchanged",
			in:   driftedNormalized,
			want: driftedNormalized,
		},
		{
			name: "a parenthesised second line becomes the note field",
			in:   "What is prefill?\n(LLM inference)\nStage where the model processes the whole prompt\n",
			want: "----\n\nWhat is prefill?\n(LLM inference)\nStage where the model processes the whole prompt\n",
		},
		{
			name: "records without a note keep an empty note line",
			in:   "What is tokenization?\nConverting text into tokens\n",
			want: "----\n\nWhat is tokenization?\n\nConverting text into tokens\n",
		},
		{
			name: "code fences and preamble are dropped",
			in:   "Sure, here are your cards:\n\n```markdown\nWhat is decode?\nGenerating tokens one at a time\n```\n",
			want: "----\n\nWhat is decode?\n\nGenerating tokens one at a time\n",
		},
		{
			name: "bullet characters and numbering are stripped",
			in:   "What are the phases?\n* Prefill\n• Decode\n2) Sampling\n",
			want: "----\n\nWhat are the phases?\n\nPrefill\nDecode\nSampling\n",
		},
		{
			name: "separator variants are all recognised",
			in:   "---\nWhat is a KV cache?\nCached attention keys and values\n-----\nWhat is a token?\nA unit of text\n",
			want: "----\n\nWhat is a KV cache?\n\nCached attention keys and values\n\n----\n\nWhat is a token?\n\nA unit of text\n",
		},
		{
			name: "blank-line blocks split records when no lead-line marker is present",
			in:   "Tokenization\nSplitting text into tokens\n\nDecode phase\nGenerating tokens one at a time\n",
			want: "----\n\nTokenization\n\nSplitting text into tokens\n\n----\n\nDecode phase\n\nGenerating tokens one at a time\n",
		},
		{
			name: "a question with no answer is dropped",
			in:   "What is tokenization?\nConverting text into tokens\n\nWhat is decode?\n",
			want: "----\n\nWhat is tokenization?\n\nConverting text into tokens\n",
		},
		{
			name: "windows line endings are handled",
			in:   "What is a token?\r\n- A unit of text\r\n",
			want: "----\n\nWhat is a token?\n\nA unit of text\n",
		},
		{
			name: "empty input yields empty output",
			in:   "   \n\n",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalize.Apply(tt.in, ankiConfig())
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got != tt.want {
				t.Errorf("Apply() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tt.want)
			}
		})
	}
}

// A template that wants only lead + body omits the note field; the
// parenthesised line is then ordinary body text.
func TestApply_recordsWithoutNoteField(t *testing.T) {
	cfg := normalize.Config{
		Filters: []string{"strip_list_markers"},
		Records: &normalize.RecordsConfig{Separator: "===", Fields: []string{"lead", "body"}},
	}
	got, err := normalize.Apply("Tokenization\n(text processing)\n- Splitting text into tokens\n", cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "===\n\nTokenization\n\n(text processing)\nSplitting text into tokens\n"
	if got != want {
		t.Errorf("Apply() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Fields default to lead + body when the template does not say.
func TestApply_recordsDefaultFields(t *testing.T) {
	cfg := normalize.Config{Records: &normalize.RecordsConfig{Separator: "----"}}
	got, err := normalize.Apply("Lead line\nBody line\n", cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := "----\n\nLead line\n\nBody line\n"; got != want {
		t.Errorf("Apply() = %q, want %q", got, want)
	}
}

func TestApply_isIdempotent(t *testing.T) {
	once, err := normalize.Apply(driftedOutput, ankiConfig())
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normalize.Apply(once, ankiConfig())
	if err != nil {
		t.Fatal(err)
	}
	if twice != once {
		t.Errorf("Apply is not idempotent\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// Filters compose without the record assembler: a template may want only line
// cleanup and keep the model's own structure.
func TestApply_filtersWithoutRecords(t *testing.T) {
	cfg := normalize.Config{Filters: []string{"strip_fences", "strip_headings", "trim_trailing_space"}}
	got, err := normalize.Apply("```\n# Heading\nBody text   \n```\n", cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != "Body text\n" {
		t.Errorf("Apply() = %q, want %q", got, "Body text\n")
	}
}

func TestApply_emptyConfigReturnsInputUnchanged(t *testing.T) {
	raw := "  anything at all\n\n\nwith odd   spacing\n"
	got, err := normalize.Apply(raw, normalize.Config{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != raw {
		t.Errorf("empty config must not touch the output: got %q", got)
	}
}

func TestApply_unknownFilterErrors(t *testing.T) {
	_, err := normalize.Apply("x\n", normalize.Config{Filters: []string{"make_it_nice"}})
	if err == nil {
		t.Fatal("expected an error for an unknown filter")
	}
	if !strings.Contains(err.Error(), "make_it_nice") {
		t.Errorf("error should name the unknown filter, got %q", err)
	}
}

func TestConfig_Declared(t *testing.T) {
	if (normalize.Config{}).Declared() {
		t.Error("an empty config declares no pipeline")
	}
	if !(normalize.Config{Filters: []string{"strip_fences"}}).Declared() {
		t.Error("filters alone declare a pipeline")
	}
	if !(normalize.Config{Records: &normalize.RecordsConfig{}}).Declared() {
		t.Error("a records block alone declares a pipeline")
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     normalize.Config
		wantErr string
	}{
		{name: "anki pipeline is valid", cfg: ankiConfig()},
		{name: "empty is valid", cfg: normalize.Config{}},
		{
			name:    "unknown filter is rejected",
			cfg:     normalize.Config{Filters: []string{"strip_fences", "polish"}},
			wantErr: "polish",
		},
		{
			name:    "empty separator is rejected",
			cfg:     normalize.Config{Records: &normalize.RecordsConfig{Separator: ""}},
			wantErr: "separator",
		},
		{
			name: "unknown field name is rejected",
			cfg: normalize.Config{Records: &normalize.RecordsConfig{
				Separator: "----", Fields: []string{"lead", "question", "body"},
			}},
			wantErr: "question",
		},
		{
			name: "fields must start with lead and end with body",
			cfg: normalize.Config{Records: &normalize.RecordsConfig{
				Separator: "----", Fields: []string{"body", "lead"},
			}},
			wantErr: "lead",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestFilterNames_listsEveryRegisteredFilter(t *testing.T) {
	names := normalize.FilterNames()
	if len(names) == 0 {
		t.Fatal("expected registered filters")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("FilterNames must be sorted for stable error messages, got %v", names)
			break
		}
	}
	cfg := normalize.Config{Filters: names}
	if err := cfg.Validate(); err != nil {
		t.Errorf("every listed filter must validate: %v", err)
	}
}

// The separator a template declares is the one that splits records. Markdown
// rules are recognised too, because a model asked for "----" often emits "---",
// but a template is free to pick something that is not a markdown rule at all.
func TestApply_recognisesTheConfiguredSeparator(t *testing.T) {
	cfg := normalize.Config{
		Records: &normalize.RecordsConfig{Separator: "@@", Fields: []string{"lead", "body"}},
	}
	in := "@@\n\nTerm one\nMeaning one\n\n@@\n\nTerm two\nMeaning two\n"
	want := "@@\n\nTerm one\n\nMeaning one\n\n@@\n\nTerm two\n\nMeaning two\n"

	got, err := normalize.Apply(in, cfg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != want {
		t.Errorf("Apply() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestApply_customSeparatorIsIdempotent(t *testing.T) {
	cfg := normalize.Config{
		Records: &normalize.RecordsConfig{Separator: "%%%", Fields: []string{"lead", "note", "body"}},
	}
	once, err := normalize.Apply("Term\nMeaning\n\nOther term\nOther meaning\n", cfg)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normalize.Apply(once, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if twice != once {
		t.Errorf("custom separator round-trip differs\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}
