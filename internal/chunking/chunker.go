package chunking

import "strings"

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

	sentences := splitSentences(text)
	if len(sentences) == 0 {
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

		// Find a sentence boundary at or before end.
		boundary := findBoundary(text, start, end)

		ch := Chunk{
			Index:      len(chunks),
			Text:       text[start:boundary],
			TokenCount: CountTokens(text[start:boundary]),
			StartByte:  start,
			EndByte:    boundary,
		}
		chunks = append(chunks, ch)

		// Next chunk starts overlapBytes before the boundary.
		next := boundary - overlapBytes
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

// splitSentences splits text on sentence delimiters, preserving delimiters.
func splitSentences(text string) []string {
	// Simple split; used only to confirm text is non-empty.
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []string{text}
}
