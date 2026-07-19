package chunking

import (
	"strings"
	"unicode/utf8"
)

// Chunk is a slice of text with position metadata.
type Chunk struct {
	Index      int
	Text       string
	TokenCount int
	StartByte  int
	EndByte    int
}

// Chunker splits text into overlapping chunks.
type Chunker struct {
	Size    int // target token count per chunk
	Overlap int // overlap in tokens between adjacent chunks
}

// sentenceSeps are the delimiters used to split text into sentences.
var sentenceSeps = []string{". ", "\n\n", "! ", "? "}

// Split splits text into overlapping chunks of approximately Size tokens,
// with Overlap tokens re-included at the start of each subsequent chunk.
func (c *Chunker) Split(text string) []Chunk {
	if text == "" {
		return nil
	}

	if strings.TrimSpace(text) == "" {
		return nil
	}

	sizeBytes := c.Size * 4
	overlapBytes := c.Overlap * 4

	var chunks []Chunk
	start := 0 // byte offset into text where the current chunk begins

	for start < len(text) {
		end := start + sizeBytes
		if end >= len(text) {
			// Last chunk: take everything remaining.
			ch := Chunk{
				Index:      len(chunks),
				Text:       text[start:],
				TokenCount: CountTokens(text[start:]),
				StartByte:  start,
				EndByte:    len(text),
			}
			chunks = append(chunks, ch)
			break
		}

		// Find a sentence boundary at or before end, snapped to a rune start
		// so we never slice through a multi-byte UTF-8 rune.
		boundary := snapRuneStart(text, findBoundary(text, start, end))

		ch := Chunk{
			Index:      len(chunks),
			Text:       text[start:boundary],
			TokenCount: CountTokens(text[start:boundary]),
			StartByte:  start,
			EndByte:    boundary,
		}
		chunks = append(chunks, ch)

		// Next chunk starts overlapBytes before the boundary, snapped to a
		// rune start so overlap never begins mid-rune.
		next := snapRuneStart(text, boundary-overlapBytes)
		if next <= start {
			next = boundary // no progress guard
		}
		start = next
	}

	return chunks
}

// findBoundary returns the byte offset of the best sentence break at or
// before maxEnd (but after minStart). Falls back to maxEnd if none found.
func findBoundary(text string, minStart, maxEnd int) int {
	if maxEnd > len(text) {
		maxEnd = len(text)
	}
	best := maxEnd
	for _, sep := range sentenceSeps {
		// Search backwards from maxEnd for sep.
		window := text[minStart:maxEnd]
		idx := strings.LastIndex(window, sep)
		if idx >= 0 {
			candidate := minStart + idx + len(sep)
			if candidate > minStart && candidate < best {
				best = candidate
			}
		}
	}
	return best
}

// snapRuneStart moves i backwards to the nearest UTF-8 rune boundary so a
// byte offset never falls in the middle of a multi-byte rune. Offsets at or
// past the end of s, and offsets already on a rune start, are returned as-is.
func snapRuneStart(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	if i < 0 {
		return 0
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
