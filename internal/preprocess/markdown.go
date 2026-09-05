package preprocess

import (
	"context"
	"io"
	"regexp"
	"strings"
	"unicode"
)

var (
	reFrontmatter = regexp.MustCompile(`(?s)^---\n.*?\n---\n?`)
	// A fenced block runs from its opening fence to the matching closing fence,
	// or to the end of the document when the fence was never closed. The capture
	// is the code itself, which is kept verbatim.
	reFencedBlock = regexp.MustCompile("(?ms)^```[^\n]*\n(.*?)(?:\n?```[^\n]*$|\\z)")
	reInlineCode  = regexp.MustCompile("`([^`\n]+)`")
	reBold        = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reHeading     = regexp.MustCompile(`(?m)^#{1,6}\s+`)
)

type markdownExtractor struct{}

// Extract strips markdown markup and keeps the text. Anything inside backticks
// — a fenced block or an inline span — is code, so it is copied out untouched:
// the punctuation in `main_consumption` or `__init__` is part of the term, and
// rewriting it makes the term unfindable in the index and wrong on the page.
func (e *markdownExtractor) Extract(_ context.Context, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	s := reFrontmatter.ReplaceAllString(string(b), "")

	var sb strings.Builder
	sb.Grow(len(s))
	for _, block := range splitCode(s, reFencedBlock) {
		if block.isCode {
			sb.WriteString(block.text)
			continue
		}
		for _, span := range splitCode(block.text, reInlineCode) {
			if span.isCode {
				sb.WriteString(span.text)
				continue
			}
			sb.WriteString(cleanProse(span.text))
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// segment is one run of the document: prose to clean, or code to copy.
type segment struct {
	text   string
	isCode bool
}

// splitCode cuts text on every match of re, marking each match's captured group
// as code and the text between matches as prose. The fence and backtick markers
// themselves fall outside the capture, so they are dropped.
func splitCode(text string, re *regexp.Regexp) []segment {
	var segs []segment
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > last {
			segs = append(segs, segment{text: text[last:m[0]]})
		}
		segs = append(segs, segment{text: text[m[2]:m[3]], isCode: true})
		last = m[1]
	}
	if last < len(text) {
		segs = append(segs, segment{text: text[last:]})
	}
	return segs
}

// cleanProse removes the markup that carries no text of its own.
func cleanProse(s string) string {
	s = reBold.ReplaceAllString(s, "$1")
	s = stripUnderscoreEmphasis(s)
	return reHeading.ReplaceAllString(s, "")
}

// stripUnderscoreEmphasis removes _emphasis_ markers while leaving snake_case
// identifiers alone. Markdown opens or closes an underscore run only at a word
// boundary, so main_consumption stays one term instead of becoming "main"
// emphasised into "consumption" — the reading the earlier `_(.+?)_` pass took,
// which welded two identifiers together whenever one line held both.
func stripUnderscoreEmphasis(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	runes := []rune(s)
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(runes); i++ {
		end := -1
		if runes[i] == '_' && opensEmphasis(runes, i) {
			end = closingUnderscore(runes, i)
		}
		if end < 0 {
			sb.WriteRune(runes[i])
			continue
		}
		sb.WriteString(string(runes[i+1 : end]))
		i = end
	}
	return sb.String()
}

// opensEmphasis reports whether the underscore at i can open a run: it follows a
// word boundary and is followed by content rather than a space or another
// underscore (so __init__ opens nothing).
func opensEmphasis(runes []rune, i int) bool {
	if i > 0 && isWordRune(runes[i-1]) {
		return false
	}
	next := i + 1
	return next < len(runes) && runes[next] != '_' && !unicode.IsSpace(runes[next])
}

// closingUnderscore returns the index of the underscore closing the run opened
// at i, or -1 when the run never closes. A run stays on its line and closes only
// at a word boundary, so the underscore in a following value_x cannot close it.
func closingUnderscore(runes []rune, i int) int {
	for j := i + 1; j < len(runes); j++ {
		switch {
		case runes[j] == '\n':
			return -1
		case runes[j] != '_':
			continue
		case unicode.IsSpace(runes[j-1]):
			continue
		case j+1 < len(runes) && isWordRune(runes[j+1]):
			continue
		}
		return j
	}
	return -1
}

// isWordRune reports whether r counts as part of a word for the flanking rules.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
