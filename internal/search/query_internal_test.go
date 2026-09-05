package search

import "testing"

func TestParseQuery_lenientTreatsEveryFieldAsATerm(t *testing.T) {
	q := parseQuery(`main consumption -main_consumption "a phrase"`, false)
	if len(q.terms) != 5 {
		t.Fatalf("want 5 bare terms, got %d: %+v", len(q.terms), q.terms)
	}
	for _, tm := range q.terms {
		if tm.negate || tm.phrase {
			t.Errorf("lenient mode must not read operators, got %+v", tm)
		}
	}
}

func TestParseQuery_operators(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []queryTerm
	}{
		{
			name:  "bare words",
			query: "main consumption",
			want:  []queryTerm{{text: "main"}, {text: "consumption"}},
		},
		{
			name:  "exact term keeps its punctuation",
			query: "main_consumption",
			want:  []queryTerm{{text: "main_consumption"}},
		},
		{
			name:  "leading dash negates",
			query: "main consumption -main_consumption",
			want: []queryTerm{
				{text: "main"}, {text: "consumption"},
				{text: "main_consumption", negate: true},
			},
		},
		{
			name:  "an inner dash is part of the term",
			query: "check-ci",
			want:  []queryTerm{{text: "check-ci"}},
		},
		{
			name:  "quoted run is one phrase",
			query: `"main consumption"`,
			want:  []queryTerm{{text: "main consumption", phrase: true}},
		},
		{
			name:  "a phrase can be negated",
			query: `register -"main consumption"`,
			want: []queryTerm{
				{text: "register"},
				{text: "main consumption", phrase: true, negate: true},
			},
		},
		{
			name:  "unterminated quote runs to the end",
			query: `"main consumption`,
			want:  []queryTerm{{text: "main consumption", phrase: true}},
		},
		{
			name:  "an empty phrase contributes nothing",
			query: `register "" ""`,
			want:  []queryTerm{{text: "register"}},
		},
		{
			name:  "a lone dash is a term, not a negation",
			query: "- register",
			want:  []queryTerm{{text: "-"}, {text: "register"}},
		},
		{
			name:  "a quote inside a bare word stays in it",
			query: `it"s`,
			want:  []queryTerm{{text: `it"s`}},
		},
		{
			name:  "empty query",
			query: "   ",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseQuery(tt.query, true).terms
			if len(got) != len(tt.want) {
				t.Fatalf("want %+v, got %+v", tt.want, got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("term %d: want %+v, got %+v", i, tt.want[i], got[i])
				}
			}
		})
	}
}

func TestParsedQuery_match(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		operators bool
		want      string
	}{
		{
			name:  "lenient input is OR-combined as before",
			query: "what are PPAs",
			want:  `"PPAs"`,
		},
		{
			name:  "lenient input keeps operator characters as text",
			query: "main consumption -main_consumption",
			want:  `"main" OR "consumption" OR "-main_consumption"`,
		},
		{
			name:      "exact term",
			query:     "main_consumption",
			operators: true,
			want:      `"main_consumption"`,
		},
		{
			name:      "loose words",
			query:     "main consumption",
			operators: true,
			want:      `"main" OR "consumption"`,
		},
		{
			name:      "negation excludes",
			query:     "main consumption -main_consumption",
			operators: true,
			want:      `("main" OR "consumption") NOT ("main_consumption")`,
		},
		{
			name:      "phrase is a phrase",
			query:     `"main consumption"`,
			operators: true,
			want:      `"main consumption"`,
		},
		{
			name:      "stop words survive inside a phrase",
			query:     `"the main consumption"`,
			operators: true,
			want:      `"the main consumption"`,
		},
		{
			name:      "stop words are dropped from bare terms",
			query:     "what is the main consumption",
			operators: true,
			want:      `"main" OR "consumption"`,
		},
		{
			name:      "a query of nothing but stop words keeps them",
			query:     "what is it",
			operators: true,
			want:      `"what" OR "is" OR "it"`,
		},
		{
			name:      "negated terms are never dropped as stop words",
			query:     `register -the`,
			operators: true,
			want:      `("register") NOT ("the")`,
		},
		{
			name:      "an exclusion with nothing to exclude from matches nothing",
			query:     "-main_consumption",
			operators: true,
			want:      "",
		},
		{
			name:      "embedded quotes are doubled",
			query:     `say "hi`,
			operators: true,
			want:      `"say" OR "hi"`,
		},
		{
			name:  "embedded quotes are doubled in lenient mode too",
			query: `a"b`,
			want:  `"a""b"`,
		},
		{
			name:      "empty query",
			query:     "  ",
			operators: true,
			want:      "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseQuery(tt.query, tt.operators).match(); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestParsedQuery_negativeMatch(t *testing.T) {
	if got := parseQuery("main consumption", true).negativeMatch(); got != "" {
		t.Errorf("no exclusions: want %q, got %q", "", got)
	}
	got := parseQuery(`a -b -"c d"`, true).negativeMatch()
	want := `"b" OR "c d"`
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// The vector leg cannot honour an exclusion — cosine similarity has no NOT —
// so what it embeds is the positive half of the query, with the operator
// syntax gone. Embedding "-main_consumption" verbatim would pull the excluded
// chunks towards the query, which is the opposite of what was asked.
func TestParsedQuery_vectorText(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"main consumption -main_consumption", "main consumption"},
		{`"main consumption" register`, "main consumption register"},
		{"the main consumption", "the main consumption"}, // stop words stay: this is prose for an embedder
		{"-main_consumption", ""},
	}
	for _, tt := range tests {
		if got := parseQuery(tt.query, true).vectorText(); got != tt.want {
			t.Errorf("%q: want %q, got %q", tt.query, tt.want, got)
		}
	}
}
