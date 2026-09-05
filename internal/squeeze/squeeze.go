// Package squeeze compacts retrieved text so more of it survives a bounded
// context window. It is the mirror of internal/normalize: normalize repairs the
// model's output, squeeze compacts the model's input.
//
// The compaction is deliberately dumb and deterministic — collapse repeated
// whitespace and blank lines, drop English articles and filler words — because
// its only job is to buy tokens before whole passages have to be dropped
// instead. It is lossy prose surgery, so it runs on retrieved chunk text only,
// never on the question or the template, and only when the prompt would
// otherwise overflow.
//
// Code is left byte-exact: fenced blocks (``` or ~~~) and indented lines pass
// through untouched, as do words carrying any non-letter character, so
// identifiers, URLs and inline code spans survive.
package squeeze

import (
	"strings"
	"unicode"

	"github.com/gotofritz/timbuktu/internal/retrieval"
)

// dropped are the words a model can reconstruct from what is left: articles,
// and the intensifiers and hedge-fillers that carry no fact. Anything that
// changes what a sentence claims (negations, modals such as "may"/"might",
// quantifiers) stays, because the chunks are evidence the answer is graded on.
var dropped = map[string]bool{
	"a": true, "an": true, "the": true,
	"actually":    true,
	"basically":   true,
	"essentially": true,
	"just":        true,
	"literally":   true,
	"quite":       true,
	"really":      true,
	"simply":      true,
	"very":        true,
}

// line is one output line and whether it must survive byte-exact.
type line struct {
	text      string
	protected bool
}

// Text returns s with prose compacted: repeated whitespace and blank lines
// collapsed, and stop words removed. Fenced and indented code is returned
// unchanged. The result is idempotent — squeezing it again is a no-op.
func Text(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}

	src := strings.Split(s, "\n")
	lines := make([]line, 0, len(src))
	inFence := false
	for _, raw := range src {
		trimmed := strings.TrimSpace(raw)
		switch {
		// The fence marker itself belongs to the block, and an unterminated
		// fence protects everything after it — text that looks like prose but
		// was never meant to be read as prose.
		case strings.HasPrefix(trimmed, "```"), strings.HasPrefix(trimmed, "~~~"):
			inFence = !inFence
			lines = append(lines, line{text: raw, protected: true})
		case inFence, strings.HasPrefix(raw, "    "), strings.HasPrefix(raw, "\t"):
			lines = append(lines, line{text: raw, protected: true})
		default:
			lines = append(lines, line{text: squeezeLine(raw)})
		}
	}
	return join(lines)
}

// Chunks returns copies of chunks with their text squeezed; citations, scores
// and identifiers are untouched, and the input is not modified. A chunk whose
// text squeezes away to nothing keeps its original text: losing a retrieved
// passage silently is worse than the tokens it costs.
func Chunks(chunks []retrieval.RetrievedChunk) []retrieval.RetrievedChunk {
	if len(chunks) == 0 {
		return nil
	}
	out := make([]retrieval.RetrievedChunk, len(chunks))
	copy(out, chunks)
	for i := range out {
		if squeezed := Text(out[i].Text); squeezed != "" {
			out[i].Text = squeezed
		}
	}
	return out
}

// squeezeLine collapses a prose line's whitespace and drops its stop words.
func squeezeLine(s string) string {
	words := strings.Fields(s)
	kept := words[:0]
	for _, w := range words {
		if droppable(w) {
			continue
		}
		kept = append(kept, w)
	}
	return strings.Join(kept, " ")
}

// droppable reports whether a word is a stop word. Only all-letter words
// qualify, so punctuation is never swallowed with the word it is attached to
// ("the," keeps its comma) and identifiers, URLs and inline code spans — none
// of which are all letters — are left alone.
func droppable(word string) bool {
	for _, r := range word {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return dropped[strings.ToLower(word)]
}

// join renders lines, collapsing runs of blank prose lines to one and dropping
// them at either end. Protected lines — code — are never treated as blank.
func join(lines []line) string {
	kept := make([]line, 0, len(lines))
	blankRun := false
	for _, l := range lines {
		if !l.protected && strings.TrimSpace(l.text) == "" {
			if len(kept) == 0 || blankRun {
				continue
			}
			blankRun = true
			kept = append(kept, line{text: ""})
			continue
		}
		blankRun = false
		kept = append(kept, l)
	}
	for len(kept) > 0 {
		last := kept[len(kept)-1]
		if last.protected || last.text != "" {
			break
		}
		kept = kept[:len(kept)-1]
	}

	texts := make([]string, len(kept))
	for i, l := range kept {
		texts[i] = l.text
	}
	return strings.Join(texts, "\n")
}
