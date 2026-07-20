package llm

import (
	"bufio"
	"strings"
	"testing"
)

func TestParseSSELine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantField string
		wantValue string
	}{
		{"blank", "", "", ""},
		{"comment", ":this is a comment", "", ""},
		{"data with space", "data: hello world", "data", "hello world"},
		{"event", "event:message_stop", "event", "message_stop"},
		{"field no colon", "retry", "retry", ""},
		{"field no value", "data:", "data", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			field, value := parseSSELine(tc.line)
			if field != tc.wantField || value != tc.wantValue {
				t.Errorf("parseSSELine(%q) = (%q, %q), want (%q, %q)",
					tc.line, field, value, tc.wantField, tc.wantValue)
			}
		})
	}
}

// SSE streams from the providers use CRLF line endings. Scanning them must not
// leave a trailing carriage return on the parsed value (which would corrupt the
// data payload and break JSON parsing / the [DONE] sentinel).
func TestSSEScan_stripsTrailingCR(t *testing.T) {
	raw := "data: hello\r\ndata: [DONE]\r\n"
	s := bufio.NewScanner(strings.NewReader(raw))
	var values []string
	for s.Scan() {
		if field, value := parseSSELine(s.Text()); field == "data" {
			values = append(values, value)
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := []string{"hello", "[DONE]"}
	if len(values) != len(want) {
		t.Fatalf("got %q, want %q", values, want)
	}
	for i, v := range values {
		if v != want[i] {
			t.Errorf("value[%d] = %q, want %q (trailing CR not stripped?)", i, v, want[i])
		}
	}
}
