package chunking_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gotofritz/timbuktu/internal/chunking"
)

// ── CountTokens ───────────────────────────────────────────────────────────────

func TestCountTokens_approximation(t *testing.T) {
	// 4 chars ≈ 1 token
	text := strings.Repeat("a", 400)
	got := chunking.CountTokens(text)
	if got != 100 {
		t.Errorf("CountTokens(%d chars) = %d, want 100", len(text), got)
	}
}

func TestCountTokens_empty(t *testing.T) {
	if got := chunking.CountTokens(""); got != 0 {
		t.Errorf("CountTokens(\"\") = %d, want 0", got)
	}
}

func TestCountTokens_scriptAware(t *testing.T) {
	// The estimator has no tokenizer to check itself against, so each case
	// states the bound that matters: ASCII keeps the chars/4 calibration it
	// has always had, and every denser script must cost more per rune than
	// that, because under-counting is what lets a chunk overrun the embedding
	// server's batch (issue #120).
	cases := []struct {
		name string
		text string
		want int
	}{
		{"ascii keeps chars/4", strings.Repeat("a", 400), 100},
		{"han is a token per rune", strings.Repeat("世", 100), 100},
		{"hiragana is a token per rune", strings.Repeat("あ", 100), 100},
		{"katakana is a token per rune", strings.Repeat("カ", 100), 100},
		{"hangul is a token per rune", strings.Repeat("한", 100), 100},
		{"accented latin is half a token per rune", strings.Repeat("é", 100), 50},
		{"cyrillic is half a token per rune", strings.Repeat("д", 100), 50},
		{"greek is half a token per rune", strings.Repeat("λ", 100), 50},
		{"arabic is half a token per rune", strings.Repeat("م", 100), 50},
		{"non-ascii punctuation is a token per rune", strings.Repeat("—", 100), 100},
		{"emoji is two tokens per rune", strings.Repeat("🙂", 100), 200},
		{"empty", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chunking.CountTokens(tc.text); got != tc.want {
				t.Errorf("CountTokens(%q...) = %d, want %d", firstRunes(tc.text, 3), got, tc.want)
			}
		})
	}
}

func TestCountTokens_nonASCIIBeatsByteHeuristic(t *testing.T) {
	// The bug in #120: len(s)/4 reads multi-byte text as *cheaper* per rune
	// than it is. Every dense script must now come out above that byte count.
	cases := []struct {
		name string
		text string
	}{
		{"han", strings.Repeat("世界你好乾坤", 50)},
		{"hangul", strings.Repeat("안녕하세요", 50)},
		{"kana", strings.Repeat("こんにちは", 50)},
		{"emoji", strings.Repeat("🙂🚀", 50)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bytesOver4 := len(tc.text) / 4
			got := chunking.CountTokens(tc.text)
			if got <= bytesOver4 {
				t.Errorf("CountTokens = %d, want > the old byte heuristic %d", got, bytesOver4)
			}
		})
	}
}

func TestCountTokens_mixedScriptIsTheSumOfItsParts(t *testing.T) {
	// A mixed-script document must not be estimated by whichever script it
	// starts with: the count is per rune, so the parts add up.
	latin := "The report says: "
	han := strings.Repeat("世界你好", 25)
	mixed := latin + han

	want := chunking.CountTokens(latin) + chunking.CountTokens(han)
	if got := chunking.CountTokens(mixed); got != want {
		t.Errorf("CountTokens(mixed) = %d, want %d (latin %d + han %d)",
			got, want, chunking.CountTokens(latin), chunking.CountTokens(han))
	}
}

func TestCountTokens_invalidUTF8DoesNotUndercount(t *testing.T) {
	// Extracted PDF text is not guaranteed to be valid UTF-8. A replacement
	// rune must still cost something, or a corrupt document reads as free.
	if got := chunking.CountTokens(strings.Repeat("\xff", 100)); got <= 0 {
		t.Errorf("CountTokens(invalid utf-8) = %d, want > 0", got)
	}
}

// firstRunes shortens a repeated-text case for an error message.
func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}

// ── Chunker.Split ─────────────────────────────────────────────────────────────

func TestChunker_basic_single_chunk(t *testing.T) {
	c := &chunking.Chunker{Size: 800, Overlap: 100}
	text := "Short text."
	chunks := c.Split(text)

	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	ch := chunks[0]
	if ch.Index != 0 {
		t.Errorf("Index = %d, want 0", ch.Index)
	}
	if ch.Text != text {
		t.Errorf("Text = %q, want %q", ch.Text, text)
	}
	if ch.TokenCount <= 0 {
		t.Errorf("TokenCount = %d, want > 0", ch.TokenCount)
	}
	if ch.StartByte != 0 {
		t.Errorf("StartByte = %d, want 0", ch.StartByte)
	}
	if ch.EndByte != len(text) {
		t.Errorf("EndByte = %d, want %d", ch.EndByte, len(text))
	}
}

func TestChunker_empty_returns_no_chunks(t *testing.T) {
	c := &chunking.Chunker{Size: 800, Overlap: 100}
	chunks := c.Split("")
	if len(chunks) != 0 {
		t.Errorf("got %d chunks, want 0", len(chunks))
	}
}

func TestChunker_whitespace_only_returns_no_chunks(t *testing.T) {
	c := &chunking.Chunker{Size: 800, Overlap: 100}
	chunks := c.Split("   \n\t  ")
	if len(chunks) != 0 {
		t.Errorf("got %d chunks, want 0", len(chunks))
	}
}

func TestChunker_overlap_reincluded(t *testing.T) {
	// 3 sentences each ~400 tokens (1600 chars); total ~1200 tokens.
	// Size=400, Overlap=100 → should produce multiple chunks.
	sentence := strings.Repeat("x", 1596) + ". "
	text := sentence + sentence + sentence

	c := &chunking.Chunker{Size: 400, Overlap: 100}
	chunks := c.Split(text)

	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want ≥2", len(chunks))
	}
	// Overlap means chunk[1].StartByte < chunk[0].EndByte.
	if chunks[1].StartByte >= chunks[0].EndByte {
		t.Errorf("no overlap: chunk[1].StartByte=%d >= chunk[0].EndByte=%d",
			chunks[1].StartByte, chunks[0].EndByte)
	}
}

func TestChunker_chunks_indexed_sequentially(t *testing.T) {
	sentence := strings.Repeat("w", 1596) + ". "
	text := sentence + sentence + sentence

	c := &chunking.Chunker{Size: 400, Overlap: 100}
	chunks := c.Split(text)

	for i, ch := range chunks {
		if ch.Index != i {
			t.Errorf("chunks[%d].Index = %d, want %d", i, ch.Index, i)
		}
	}
}

func TestChunker_multibyte_utf8_chunks_valid(t *testing.T) {
	// CJK text (3 bytes/rune) with no sentence separators forces the
	// byte-offset boundary logic to fall back to maxEnd, which can land
	// mid-rune. Every chunk must still be valid UTF-8.
	text := strings.Repeat("世界你好乾坤", 60) // 360 runes, 1080 bytes
	c := &chunking.Chunker{Size: 10, Overlap: 2}
	chunks := c.Split(text)

	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want ≥2", len(chunks))
	}
	var reassembledFirst string
	for i, ch := range chunks {
		if !utf8.ValidString(ch.Text) {
			t.Errorf("chunk[%d] is not valid UTF-8: %q", i, ch.Text)
		}
		if !utf8.RuneStart(text[ch.StartByte]) {
			t.Errorf("chunk[%d].StartByte=%d lands mid-rune", i, ch.StartByte)
		}
		if ch.EndByte < len(text) && !utf8.RuneStart(text[ch.EndByte]) {
			t.Errorf("chunk[%d].EndByte=%d lands mid-rune", i, ch.EndByte)
		}
		if i == 0 {
			reassembledFirst = ch.Text
		}
	}
	// Sanity: first chunk starts at the document start.
	if !strings.HasPrefix(text, reassembledFirst) {
		t.Errorf("first chunk is not a prefix of the source text")
	}
}

func TestChunker_accented_utf8_chunks_valid(t *testing.T) {
	// Accented Latin (2 bytes/rune) with sentence separators.
	sentence := strings.Repeat("café résumé naïve ", 5) + ". "
	text := strings.Repeat(sentence, 4)
	c := &chunking.Chunker{Size: 20, Overlap: 5}
	for _, ch := range c.Split(text) {
		if !utf8.ValidString(ch.Text) {
			t.Errorf("chunk not valid UTF-8: %q", ch.Text)
		}
	}
}

func TestChunker_boundary_picks_latest_separator(t *testing.T) {
	// Mixed separator types: an early "! " must not win over the many later
	// ". " breaks. With Size=800 (sizeBytes=3200) the first chunk should snap
	// to a sentence break near 3200 bytes, not to the "! " at byte 7.
	text := "Hello! " + strings.Repeat("This is a normal sentence. ", 300)

	c := &chunking.Chunker{Size: 800, Overlap: 100}
	chunks := c.Split(text)

	if len(chunks) == 0 {
		t.Fatal("got 0 chunks")
	}
	first := chunks[0]
	if first.EndByte < 3000 {
		t.Errorf("first chunk EndByte = %d, want near sizeBytes (3200); "+
			"boundary search picked an early separator", first.EndByte)
	}
	if first.EndByte > 3200 {
		t.Errorf("first chunk EndByte = %d, want <= sizeBytes 3200", first.EndByte)
	}
	// Boundary must land right after a separator (a "sentence. ").
	if !strings.HasSuffix(first.Text, ". ") && !strings.HasSuffix(first.Text, ".") {
		t.Errorf("first chunk does not end on a sentence break: %q",
			first.Text[max(0, len(first.Text)-20):])
	}
}

func TestChunker_boundary_falls_back_to_maxEnd_when_no_separator(t *testing.T) {
	// No separators at all: boundary should fall back to maxEnd (sizeBytes),
	// producing a full-size first chunk rather than a degenerate one.
	text := strings.Repeat("abcdefghij", 400) // 4000 bytes, no separators
	c := &chunking.Chunker{Size: 800, Overlap: 100}
	chunks := c.Split(text)

	if len(chunks) == 0 {
		t.Fatal("got 0 chunks")
	}
	if chunks[0].EndByte != 3200 {
		t.Errorf("first chunk EndByte = %d, want 3200 (maxEnd fallback)",
			chunks[0].EndByte)
	}
}

func TestChunker_nonpositive_size_does_not_hang(t *testing.T) {
	// Size <= 0 (e.g. a hand-edited config) previously made Split loop forever
	// appending empty chunks. It must terminate and return the whole text as a
	// single chunk instead.
	cases := []struct {
		name string
		size int
	}{
		{"zero", 0},
		{"negative", -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := "One sentence. Two sentence. Three sentence."
			c := &chunking.Chunker{Size: tc.size, Overlap: 100}

			done := make(chan []chunking.Chunk, 1)
			go func() { done <- c.Split(text) }()

			select {
			case chunks := <-done:
				if len(chunks) != 1 {
					t.Fatalf("got %d chunks, want 1", len(chunks))
				}
				if chunks[0].Text != text {
					t.Errorf("Text = %q, want %q", chunks[0].Text, text)
				}
				if chunks[0].EndByte != len(text) {
					t.Errorf("EndByte = %d, want %d", chunks[0].EndByte, len(text))
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Split did not terminate within 2s (infinite loop)")
			}
		})
	}
}

func TestChunker_last_chunk_covers_remainder(t *testing.T) {
	sentence := strings.Repeat("z", 1596) + ". "
	text := sentence + sentence + sentence

	c := &chunking.Chunker{Size: 400, Overlap: 100}
	chunks := c.Split(text)

	last := chunks[len(chunks)-1]
	if last.EndByte != len(text) {
		t.Errorf("last chunk EndByte = %d, want %d", last.EndByte, len(text))
	}
}

// ── Chunker.Split: token budget across scripts ────────────────────────────────

func TestChunker_respectsTokenBudgetAcrossScripts(t *testing.T) {
	// The acceptance criterion from #120: a chunk must not exceed the
	// configured token size whatever script it is written in, because
	// chunking.size is what keeps an embedding request inside the server's
	// batch (llama.cpp answers HTTP 500 when it does not).
	const size = 100
	cases := []struct {
		name string
		text string
	}{
		{"ascii", strings.Repeat("word ", 2000)},
		{"ascii sentences", strings.Repeat("This is a normal sentence. ", 400)},
		{"han", strings.Repeat("世界你好乾坤", 400)},
		{"hangul", strings.Repeat("안녕하세요", 400)},
		{"kana", strings.Repeat("こんにちは世界", 400)},
		{"cyrillic", strings.Repeat("привет мир ", 400)},
		{"accented latin", strings.Repeat("café résumé naïve ", 300)},
		{"mixed", strings.Repeat("The 世界 says café. ", 400)},
		{"emoji", strings.Repeat("🙂🚀 ok ", 400)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &chunking.Chunker{Size: size, Overlap: 10}
			chunks := c.Split(tc.text)
			if len(chunks) < 2 {
				t.Fatalf("got %d chunks, want ≥2 (fixture too small to exercise the budget)", len(chunks))
			}
			for i, ch := range chunks {
				if ch.TokenCount > size {
					t.Errorf("chunk[%d].TokenCount = %d, want ≤ Size %d", i, ch.TokenCount, size)
				}
				if got := chunking.CountTokens(ch.Text); got != ch.TokenCount {
					t.Errorf("chunk[%d].TokenCount = %d, but CountTokens(Text) = %d", i, ch.TokenCount, got)
				}
			}
		})
	}
}

func TestChunker_cjkChunksAreShorterInBytesThanASCII(t *testing.T) {
	// Same token budget, denser script: a CJK chunk has to hold fewer bytes
	// than an English one, or the budget is not doing its job. Under the byte
	// estimator both came out at Size*4 bytes.
	c := &chunking.Chunker{Size: 100, Overlap: 0}

	ascii := c.Split(strings.Repeat("abcdefghij", 400))
	han := c.Split(strings.Repeat("世界你好乾坤", 400))
	if len(ascii) == 0 || len(han) == 0 {
		t.Fatal("expected chunks for both fixtures")
	}
	// Three bytes per han rune against one per ascii rune: at the same token
	// budget the han chunk should be roughly three quarters the size, not the
	// identical Size*4 bytes the byte estimator gave both.
	if len(han[0].Text)*4 > len(ascii[0].Text)*3 {
		t.Errorf("first han chunk is %d bytes, first ascii chunk %d — want the han one clearly smaller",
			len(han[0].Text), len(ascii[0].Text))
	}
}

func TestChunker_overlapIsMeasuredInTokensNotBytes(t *testing.T) {
	// Overlap is a token count too: on CJK it must re-include about Overlap
	// tokens' worth of runes, not Overlap*4 bytes' worth.
	const overlap = 10
	text := strings.Repeat("世界你好乾坤", 400)
	c := &chunking.Chunker{Size: 100, Overlap: overlap}
	chunks := c.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want ≥2", len(chunks))
	}
	reincluded := text[chunks[1].StartByte:chunks[0].EndByte]
	if got := chunking.CountTokens(reincluded); got != overlap {
		t.Errorf("overlap re-included %d tokens (%q), want %d", got, reincluded, overlap)
	}
}

func TestChunker_tinySizeDoesNotHang(t *testing.T) {
	// A rune can cost more than the whole budget: an emoji is worth two
	// tokens, so Size=1 leaves no room for even one of them. The span logic
	// must still advance rather than emit empty chunks forever.
	cases := []struct {
		name string
		text string
		size int
	}{
		{"emoji under a one-token budget", strings.Repeat("🙂", 20), 1},
		{"han under a one-token budget", strings.Repeat("世界", 20), 1},
		{"mixed under a one-token budget", strings.Repeat("a🙂世", 20), 1},
		{"overlap larger than size", strings.Repeat("世界你好乾坤", 20), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &chunking.Chunker{Size: tc.size, Overlap: 5}

			done := make(chan []chunking.Chunk, 1)
			go func() { done <- c.Split(tc.text) }()

			select {
			case chunks := <-done:
				if len(chunks) == 0 {
					t.Fatal("got 0 chunks")
				}
				for i, ch := range chunks {
					if ch.Text == "" {
						t.Errorf("chunk[%d] is empty", i)
					}
					if !utf8.ValidString(ch.Text) {
						t.Errorf("chunk[%d] is not valid UTF-8: %q", i, ch.Text)
					}
				}
				if last := chunks[len(chunks)-1]; last.EndByte != len(tc.text) {
					t.Errorf("last chunk EndByte = %d, want %d", last.EndByte, len(tc.text))
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Split did not terminate within 2s (infinite loop)")
			}
		})
	}
}

func TestChunker_invalidUTF8DoesNotHang(t *testing.T) {
	// Extracted text is not guaranteed to be valid UTF-8; decoding must still
	// advance a byte at a time rather than stall on an undecodable one.
	text := strings.Repeat("ok \xff\xfe text. ", 40)
	c := &chunking.Chunker{Size: 5, Overlap: 1}

	done := make(chan []chunking.Chunk, 1)
	go func() { done <- c.Split(text) }()

	select {
	case chunks := <-done:
		if len(chunks) < 2 {
			t.Fatalf("got %d chunks, want ≥2", len(chunks))
		}
		if last := chunks[len(chunks)-1]; last.EndByte != len(text) {
			t.Errorf("last chunk EndByte = %d, want %d", last.EndByte, len(text))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Split did not terminate within 2s (infinite loop)")
	}
}
