package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestStripControl(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"keeps newline and tab", "a\nb\tc", "a\nb\tc"},
		{"strips ESC", "a\x1b[31mred\x1b[0m", "a[31mred[0m"},
		{"strips OSC 52 clipboard", "\x1b]52;c;Zm9v\x07x", "]52;c;Zm9vx"},
		{"strips carriage return", "line1\rline2", "line1line2"},
		{"strips DEL", "a\x7fb", "ab"},
		{"strips C1 NEL", "a\u0085b", "ab"},
		{"strips C1 CSI", "a\u009bb", "ab"},
		{"keeps multibyte utf8", "café — naïve 日本語 🚀", "café — naïve 日本語 🚀"},
	}
	for _, tt := range tests {
		if got := stripControl(tt.in); got != tt.want {
			t.Errorf("%s: stripControl(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestSanitizeWriter_stripsControlAcrossWrites(t *testing.T) {
	var buf bytes.Buffer
	w := newSanitizeWriter(&buf)

	inputs := []string{"a\x1b[31m", "b\x07c", "\x1b]0;title\x07d"}
	for _, in := range inputs {
		n, err := w.Write([]byte(in))
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		if n != len(in) {
			t.Fatalf("Write returned %d, want %d (must report full input consumed)", n, len(in))
		}
	}
	got := buf.String()
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("output still contains control bytes: %q", got)
	}
	if got != "a[31mbc]0;titled" {
		t.Errorf("got %q", got)
	}
}

func TestSanitizeWriter_reassemblesSplitRune(t *testing.T) {
	var buf bytes.Buffer
	w := newSanitizeWriter(&buf)

	// "é" = 0xC3 0xA9 split across two writes; 0xA9 alone is in the C1 byte
	// range and must NOT be mistaken for a control char.
	if _, err := w.Write([]byte{'c', 'a', 'f', 0xC3}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{0xA9, '!'}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "café!" {
		t.Errorf("got %q, want %q", got, "café!")
	}
}
