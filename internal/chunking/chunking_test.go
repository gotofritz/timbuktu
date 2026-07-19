package chunking_test

import (
	"strings"
	"testing"
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
