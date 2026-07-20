package cli

import (
	"io"
	"strings"
	"unicode/utf8"
)

// isDisplayControl reports whether r is a control character that must not reach
// the terminal verbatim: C0 controls (except newline and tab), DEL, and C1
// controls (U+0080–U+009F). Ingested documents can carry ANSI/OSC escapes
// (OSC 52 writes the clipboard, OSC 0 retitles the window, CSI sequences rewrite
// the screen); stripping the introducers renders any such sequence inert while
// leaving ordinary text — including all printable multibyte UTF-8 — untouched.
func isDisplayControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	default:
		return false
	}
}

// stripControl removes display-control characters from s, returning it unchanged
// when it contains none. Use for discrete document-derived display strings
// (paths, titles, metadata values, citations) printed to a terminal.
func stripControl(s string) string {
	if !strings.ContainsFunc(s, isDisplayControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isDisplayControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sanitizeWriter wraps an io.Writer and drops display-control characters from
// everything written through it. It is rune-aware: a multibyte UTF-8 sequence
// split across two Write calls is buffered and reassembled, so continuation
// bytes (0x80–0xBF, which overlap the C1 byte range) are never mistaken for
// control characters. Used to wrap streamed `ask` output, where document text
// echoed by the model would otherwise reach the terminal raw.
type sanitizeWriter struct {
	w   io.Writer
	buf []byte // carried incomplete trailing UTF-8 sequence
}

func newSanitizeWriter(w io.Writer) *sanitizeWriter { return &sanitizeWriter{w: w} }

func (s *sanitizeWriter) Write(p []byte) (int, error) {
	data := p
	if len(s.buf) > 0 {
		data = append(s.buf, p...)
		s.buf = nil
	}

	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 && !utf8.FullRune(data) {
			// Incomplete trailing sequence — carry it to the next Write.
			s.buf = append(s.buf, data...)
			break
		}
		if !isDisplayControl(r) {
			out = append(out, data[:size]...)
		}
		data = data[size:]
	}

	if len(out) > 0 {
		if _, err := s.w.Write(out); err != nil {
			return 0, err
		}
	}
	// Report the whole input as consumed: buffered bytes are retained internally,
	// not dropped, so callers must not treat a short count as an error.
	return len(p), nil
}
