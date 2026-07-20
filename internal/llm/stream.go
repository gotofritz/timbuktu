package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// sendToken delivers tok on ch unless ctx is cancelled first. It returns false
// when ctx is done, signalling the stream goroutine to stop: without this, a
// consumer that abandons the channel (e.g. RunAsk returning on a mid-stream
// error) would leave the goroutine blocked on send forever, leaking it and
// holding the HTTP body open.
func sendToken(ctx context.Context, ch chan<- Token, tok Token) bool {
	select {
	case ch <- tok:
		return true
	case <-ctx.Done():
		return false
	}
}

// errorMessage reads up to 2 KB of an error response body and returns it as the
// message, falling back to the HTTP status text when the body is empty. The
// API's own error text ("model not found", "context length exceeded") is far
// more useful for debugging than a bare status line.
func errorMessage(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if msg := strings.TrimSpace(string(body)); msg != "" {
		return msg
	}
	return http.StatusText(resp.StatusCode)
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
