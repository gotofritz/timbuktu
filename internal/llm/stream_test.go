package llm

import (
	"testing"
)

func TestParseSSELine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantField  string
		wantValue  string
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
