package search

import (
	"strings"
	"unicode"
)

// stopWords are the English function words a question is mostly made of. They
// are dropped from a MATCH expression because they carry no signal about which
// chunk is wanted: BM25 discounts them by IDF, but only once the corpus is big
// enough for them to be common in it, and a personal knowledge base of a few
// dozen documents is not. Removing them makes ranking depend on the terms the
// user actually meant, whatever the corpus size.
var stopWords = map[string]bool{
	"a": true, "about": true, "all": true, "am": true, "an": true, "and": true,
	"any": true, "are": true, "as": true, "at": true, "be": true, "been": true,
	"but": true, "by": true, "can": true, "did": true, "do": true, "does": true,
	"for": true, "from": true, "had": true, "has": true, "have": true, "how": true,
	"i": true, "if": true, "in": true, "is": true, "it": true, "its": true,
	"know": true, "me": true, "my": true, "no": true, "not": true, "of": true,
	"on": true, "or": true, "so": true, "some": true, "tell": true, "that": true,
	"the": true, "their": true, "them": true, "then": true, "there": true,
	"these": true, "they": true, "this": true, "to": true, "was": true, "we": true,
	"were": true, "what": true, "when": true, "where": true, "which": true,
	"who": true, "why": true, "will": true, "with": true, "would": true,
	"you": true, "your": true,
}

// queryTerm is one field of a parsed query: a bare word, or the contents of a
// quoted phrase, either of which may be an exclusion.
type queryTerm struct {
	text   string
	phrase bool
	negate bool
}

// parsedQuery is a user query read as terms rather than as one string.
type parsedQuery struct {
	terms []queryTerm
}

// parseQuery splits query into terms.
//
// With operators false every whitespace-separated field is a bare positive
// term, quotes and dashes included — the lenient reading `tbuk ask` needs,
// where the query is a natural-language question and any punctuation in it is
// incidental.
//
// With operators true the query is read as an expression: a double-quoted run
// is one phrase term, and a '-' immediately before a term excludes it. The
// dash counts as an operator only at the start of a field, so `check-ci` is a
// term and `-draft` is an exclusion. The FTS5 tokenizer treats '-' as a
// separator (see storage.FTSTokenizer), so `check-ci` reaches the index as the
// phrase "check ci" — adjacent, in that order, however it was written.
func parseQuery(query string, operators bool) parsedQuery {
	if !operators {
		fields := strings.Fields(query)
		terms := make([]queryTerm, 0, len(fields))
		for _, f := range fields {
			terms = append(terms, queryTerm{text: f})
		}
		return parsedQuery{terms: terms}
	}

	var terms []queryTerm
	runes := []rune(query)
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}
		var negate bool
		if runes[i] == '-' && i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			negate = true
			i++
		}
		if runes[i] == '"' {
			i++
			start := i
			for i < len(runes) && runes[i] != '"' {
				i++
			}
			text := strings.TrimSpace(string(runes[start:i]))
			if i < len(runes) {
				i++ // consume the closing quote
			}
			if text != "" {
				terms = append(terms, queryTerm{text: text, phrase: true, negate: negate})
			}
			continue
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		terms = append(terms, queryTerm{text: string(runes[start:i]), negate: negate})
	}
	return parsedQuery{terms: terms}
}

// match returns the FTS5 MATCH expression for the query, or "" when there is
// nothing to match.
//
// Positive terms are OR-combined rather than left to FTS5's implicit AND. A
// question ("what do you know about PPAs") carries words no single chunk has
// to contain, so an AND match returned nothing at all for exactly the queries
// `tbuk ask` sends — and a Hybrid whose keyword leg is always empty is vector
// search wearing a different name. Recall is what this leg is for; BM25
// ranking and TopK are what keep it precise.
//
// Stop words are dropped from bare terms, so what is OR-combined is the
// content of the query. They survive inside a quoted phrase, where dropping
// one would break the phrase, and inside an exclusion, which is explicit
// enough to be taken at face value. A query of nothing but stop words keeps
// them rather than matching everything or nothing.
//
// Exclusions become the right-hand side of FTS5's binary NOT. An exclusion
// with no positive term beside it yields "": FTS5 has no "everything except"
// expression, and there is nothing here to subtract it from.
func (q parsedQuery) match() string {
	var positive, dropped []string
	for _, t := range q.terms {
		if t.negate {
			continue
		}
		if !t.phrase && stopWords[strings.ToLower(t.text)] {
			dropped = append(dropped, ftsPhrase(t.text))
			continue
		}
		positive = append(positive, ftsPhrase(t.text))
	}
	if len(positive) == 0 {
		positive = dropped
	}
	if len(positive) == 0 {
		return ""
	}

	match := strings.Join(positive, " OR ")
	if neg := q.negativeMatch(); neg != "" {
		match = "(" + match + ") NOT (" + neg + ")"
	}
	return match
}

// negativeMatch returns the OR-combined exclusions, or "" when there are none.
// Hybrid needs them on their own: the keyword leg has already applied them,
// but the vector leg has not, and a fused result set would otherwise hand back
// the very chunks the user asked to leave out.
func (q parsedQuery) negativeMatch() string {
	var negative []string
	for _, t := range q.terms {
		if t.negate {
			negative = append(negative, ftsPhrase(t.text))
		}
	}
	return strings.Join(negative, " OR ")
}

// vectorText is what the vector leg embeds: the positive half of the query as
// prose, operator syntax removed. Stop words stay — an embedder is shown
// language, not a bag of index terms.
func (q parsedQuery) vectorText() string {
	parts := make([]string, 0, len(q.terms))
	for _, t := range q.terms {
		if !t.negate {
			parts = append(parts, t.text)
		}
	}
	return strings.Join(parts, " ")
}

// ftsPhrase wraps s as an FTS5 string literal, doubling embedded quotes, so
// arbitrary input is always a valid MATCH operand rather than syntax.
func ftsPhrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
