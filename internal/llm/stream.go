package llm

import (
	"bufio"
	"io"
	"strings"
)

// sseScanner returns a scanner that yields SSE lines from r.
// It strips the trailing carriage return if present.
func sseScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	return s
}

// parseSSELine splits a raw SSE line into field and value.
// Returns ("", "") for blank lines or comments.
func parseSSELine(line string) (field, value string) {
	if strings.HasPrefix(line, ":") || line == "" {
		return "", ""
	}
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	field = line[:idx]
	value = strings.TrimPrefix(line[idx+1:], " ")
	return field, value
}
