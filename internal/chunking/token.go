package chunking

import (
	"unicode"
	"unicode/utf8"
)

// quartersPerToken is the fixed-point scale the per-rune weights are held in.
// Quarter-tokens are the coarsest unit that still expresses "four ASCII
// characters to a token" as an integer, so the estimator needs no floating
// point and ASCII text keeps the exact count it has always returned.
const quartersPerToken = 4

// CountTokens returns an approximation of the token count for text.
//
// It weighs each rune by the script it belongs to rather than counting bytes.
// A byte count reads multi-byte text as *cheaper* per character than it is —
// a han ideograph is three bytes and roughly one token, so len/4 under-counts
// it threefold — which let CJK chunks overrun the embedding server's batch and
// left `retrieval.max_tokens` trimming too little (issue #120). The weights are
// deliberately coarse: no tokenizer, no vocabulary, no allocation.
func CountTokens(text string) int {
	return countQuarters(text) / quartersPerToken
}

// countQuarters sums the per-rune weights of text in quarter-tokens.
func countQuarters(text string) int {
	quarters := 0
	for _, r := range text {
		quarters += runeQuarters(r)
	}
	return quarters
}

// runeQuarters is what one rune costs, in quarter-tokens. The bands are
// calibrated against how BPE vocabularies behave, not measured per model: any
// real tokenizer differs, so the point is to stop being wrong by a factor of
// three on dense scripts, not to be exact on any of them.
func runeQuarters(r rune) int {
	switch {
	case r < utf8.RuneSelf:
		// ASCII: the original calibration — English averages about four
		// characters to a token, and every existing chunk size is set in it.
		return 1
	case r > 0xFFFF:
		// Beyond the BMP: emoji and rare ideographs sit outside the learned
		// vocabulary and come back as several byte-level pieces each.
		return 2 * quartersPerToken
	case unicode.Is(unicode.Han, r),
		unicode.Is(unicode.Hiragana, r),
		unicode.Is(unicode.Katakana, r),
		unicode.Is(unicode.Hangul, r):
		// Ideographs, kana and hangul syllables each carry a word's worth of
		// meaning and cost about a token apiece.
		return quartersPerToken
	case unicode.IsLetter(r), unicode.IsMark(r), unicode.IsDigit(r):
		// Alphabetic scripts outside ASCII — accented Latin, Greek, Cyrillic,
		// Hebrew, Arabic, Devanagari, Thai. Merged into multi-character pieces
		// like English, but shorter ones: about two characters to a token.
		return quartersPerToken / 2
	default:
		// Non-ASCII punctuation and symbols (dashes, typographic quotes,
		// currency, arrows): usually a token of their own. RuneError lands
		// here too, so text that is not valid UTF-8 still costs something.
		return quartersPerToken
	}
}

// tokenEnd returns the byte offset at or after start where text[start:] has
// spent budget tokens, or len(text) if the text runs out first. It stops before
// the rune that would exceed the budget, so text[start:tokenEnd(...)] counts as
// at most budget tokens — except when the very first rune already costs more
// than the whole budget, where it takes that rune rather than return an empty
// span. The offset lands on a rune start.
func tokenEnd(text string, start, budget int) int {
	if budget <= 0 || start >= len(text) {
		return start
	}
	quarters := budget * quartersPerToken
	spent := 0
	i := start
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		q := runeQuarters(r)
		if spent+q > quarters {
			if i == start {
				// One rune costs more than the entire budget — an emoji under
				// a Size of 1. Take it anyway: an empty span would leave the
				// caller unable to advance.
				return i + size
			}
			return i
		}
		spent += q
		i += size
	}
	return len(text)
}

// tokenStart returns the byte offset at or before end where the last budget
// tokens of text[:end] begin. It is tokenEnd walked backwards, and stops short
// of the rune that would exceed the budget for the same reason. The offset
// lands on a rune start.
func tokenStart(text string, end, budget int) int {
	if end > len(text) {
		end = len(text)
	}
	if budget <= 0 || end <= 0 {
		return end
	}
	quarters := budget * quartersPerToken
	spent := 0
	i := end
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		q := runeQuarters(r)
		if spent+q > quarters {
			break
		}
		spent += q
		i -= size
	}
	return i
}
