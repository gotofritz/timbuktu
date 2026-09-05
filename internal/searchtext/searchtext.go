// Package searchtext builds the reduced encoding of a chunk: what the FTS5
// index stores and what the embedder is shown.
//
// A chunk's faithful text serves the reader and the model — code stays readable
// there, fences and all. It serves the index badly: syntax is most of the bytes
// and none of the meaning, and an embedding taken from it describes a wall of
// punctuation rather than what the code is about. What carries the signal is
// comments, identifiers, and the strings a person would search for.
//
// So a code region is reduced to those, with every identifier emitted
// alongside its split words (`main_consumption main consumption`), which is
// what lets an exact-term query and a loose word query reach the same chunk.
// Prose is passed through untouched, except that an inline code span gets the
// same identifier treatment — that is where identifiers appear in ordinary
// notes.
//
// There is no parser here and no per-language support beyond a small stop list
// the fence's language tag can extend: this is an encoding for retrieval, not
// an analysis of the code.
package searchtext

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// A fenced block runs from its opening fence to the matching closing fence,
	// or to the end of the text when the fence was never closed — which is what
	// a chunk boundary cutting a block through the middle leaves behind. Group 1
	// is the info string, group 2 the code.
	reFencedBlock = regexp.MustCompile("(?ms)^```([^\n]*)\n(.*?)(?:\n?```[^\n]*$|\\z)")
	reInlineCode  = regexp.MustCompile("`([^`\n]+)`")
)

// Reduce returns the indexed and embedded encoding of text.
func Reduce(text string) string {
	var w writer
	w.grow(len(text))

	last := 0
	for _, m := range reFencedBlock.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > last {
			w.prose(text[last:m[0]])
		}
		w.code(text[m[4]:m[5]], strings.ToLower(strings.TrimSpace(text[m[2]:m[3]])))
		last = m[1]
	}
	if last < len(text) {
		w.prose(text[last:])
	}
	return strings.TrimSpace(w.String())
}

// prose copies s through as written, giving each inline code span the
// identifier treatment a code region's terms get.
func (w *writer) prose(s string) {
	last := 0
	for _, m := range reInlineCode.FindAllStringSubmatchIndex(s, -1) {
		w.verbatim(s[last:m[0]])
		w.terms(s[m[2]:m[3]], nil)
		last = m[1]
	}
	w.verbatim(s[last:])
}

// code reduces one fenced region: comments and string contents keep their
// words, identifiers are emitted with their split forms, and everything else —
// keywords, operators, punctuation, numbers — is dropped. lang, when the fence
// carried one, is emitted as a term of its own (the language is a topic) and
// selects the extra stop words for that language.
func (w *writer) code(code, lang string) {
	stop := stopWordsFor(lang)
	if lang != "" {
		w.token(lang)
	}

	for i := 0; i < len(code); {
		switch {
		case startsLineComment(code, i):
			body, next := restOfLine(code, i+commentMarkerLen(code, i))
			w.comment(body)
			i = next
		case strings.HasPrefix(code[i:], "/*"):
			body, next := until(code, i+2, "*/")
			w.comment(body)
			i = next
		case strings.HasPrefix(code[i:], "<!--"):
			body, next := until(code, i+4, "-->")
			w.comment(body)
			i = next
		case code[i] == '"' || code[i] == '\'' || code[i] == '`':
			body, next := stringLiteral(code, i)
			w.terms(body, stop)
			i = next
		case isTermByte(code[i]):
			j := i
			for j < len(code) && isTermByte(code[j]) {
				j++
			}
			w.terms(code[i:j], stop)
			i = j
		default:
			i++
		}
	}
}

// comment writes a comment's text as it was written: a comment is prose, and
// prose is what this whole encoding is trying to keep.
func (w *writer) comment(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	w.token(s)
}

// terms emits every term in s. stop is the keyword list to apply — nil for an
// inline span in prose, which is an identifier, not code.
func (w *writer) terms(s string, stop map[string]bool) {
	for i := 0; i < len(s); {
		if !isTermByte(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isTermByte(s[j]) {
			j++
		}
		w.term(s[i:j], stop)
		i = j
	}
}

// term emits one term: the term as written, then each of its split words. A
// term that reduces to nothing — a keyword, a number, a single character — is
// dropped whole.
func (w *writer) term(term string, stop map[string]bool) {
	term = strings.Trim(term, "-./")
	if term == "" {
		return
	}
	lower := strings.ToLower(term)
	words := splitWords(term, stop)
	if len(words) == 0 {
		return
	}
	if !stop[lower] {
		w.token(term)
	}
	for _, word := range words {
		if word != lower {
			w.token(word)
		}
	}
}

// splitWords cuts a term at `_ - . /` and at camelCase boundaries, lowercasing
// each word. Words that carry no signal are dropped: keywords, anything with no
// letter in it (numeric literals, hex), and single characters (loop variables,
// format verbs).
func splitWords(term string, stop map[string]bool) []string {
	runes := []rune(term)
	var (
		words []string
		cur   []rune
	)
	flush := func() {
		if len(cur) == 0 {
			return
		}
		word := strings.ToLower(string(cur))
		cur = nil
		if len([]rune(word)) < 2 || !hasLetter(word) || stop[word] {
			return
		}
		words = append(words, word)
	}
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.' || r == '/':
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 && startsWord(runes, i):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

// startsWord reports whether the upper-case rune at i opens a new word:
// readMeter splits after "read", and HTTPServer after the acronym rather than
// at every capital in it.
func startsWord(runes []rune, i int) bool {
	prev := runes[i-1]
	if unicode.IsLower(prev) || unicode.IsDigit(prev) {
		return true
	}
	return i+1 < len(runes) && unicode.IsLower(runes[i+1])
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// isTermByte reports whether b can be part of a term. `_ - . /` are included so
// snake_case, kebab-case, dotted names and paths survive as one term before
// being split; every byte of a multi-byte rune is included so a non-ASCII
// identifier is never cut in half.
func isTermByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-' || b == '.' || b == '/':
		return true
	default:
		return b >= 0x80
	}
}

// startsLineComment reports whether a line comment opens at i. `//` is not one
// when it follows a colon — that is the scheme of a URL, and a URL is a term.
// `--` needs the space SQL writes after it, so a decrement stays arithmetic.
func startsLineComment(code string, i int) bool {
	switch {
	case code[i] == '#':
		return true
	case strings.HasPrefix(code[i:], "//"):
		return i == 0 || code[i-1] != ':'
	case strings.HasPrefix(code[i:], "--"):
		return len(code) > i+2 && (code[i+2] == ' ' || code[i+2] == '\t')
	default:
		return false
	}
}

// commentMarkerLen is the width of the marker startsLineComment matched at i.
func commentMarkerLen(code string, i int) int {
	if code[i] == '#' {
		return 1
	}
	return 2
}

// restOfLine returns the text from i to the end of its line, and the offset of
// the newline.
func restOfLine(code string, i int) (string, int) {
	end := strings.IndexByte(code[i:], '\n')
	if end < 0 {
		return code[i:], len(code)
	}
	return code[i : i+end], i + end
}

// until returns the text from i up to close, and the offset past it. An
// unterminated block runs to the end.
func until(code string, i int, closing string) (string, int) {
	end := strings.Index(code[i:], closing)
	if end < 0 {
		return code[i:], len(code)
	}
	return code[i : i+end], i + end + len(closing)
}

// stringLiteral returns the contents of the literal opening at i and the offset
// past its closing quote. A quote that never closes ends at the newline (or the
// end of the region for a backquoted literal, which may span lines) — a chunk
// boundary cuts literals like everything else, and the words in one are still
// worth having.
func stringLiteral(code string, i int) (string, int) {
	quote := code[i]
	for j := i + 1; j < len(code); j++ {
		switch {
		case code[j] == '\\' && quote != '`':
			j++
		case code[j] == quote:
			return code[i+1 : j], j + 1
		case code[j] == '\n' && quote != '`':
			return code[i+1 : j], j
		}
	}
	return code[i+1:], len(code)
}

// writer accumulates the encoding, keeping tokens apart without gluing them to
// the prose around them. It tracks the last byte written rather than reading
// the buffer back, so appending stays O(1).
type writer struct {
	sb   strings.Builder
	last byte
}

func (w *writer) grow(n int)     { w.sb.Grow(n) }
func (w *writer) String() string { return w.sb.String() }
func (w *writer) verbatim(s string) {
	if s == "" {
		return
	}
	w.sb.WriteString(s)
	w.last = s[len(s)-1]
}

// token writes s as a standalone term, separated from whatever precedes it.
func (w *writer) token(s string) {
	if s == "" {
		return
	}
	if w.last != 0 && w.last != ' ' && w.last != '\n' && w.last != '\t' {
		w.sb.WriteByte(' ')
	}
	w.sb.WriteString(s)
	w.last = s[len(s)-1]
}
