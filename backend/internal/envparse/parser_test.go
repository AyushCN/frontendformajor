package envparse

import (
	"strings"
	"testing"
)

func TestParse_BasicAndWhitespace(t *testing.T) {
	input := `
# Configuration comment
PORT=8080
HOST=0.0.0.0
DEBUG=true

# Empty lines above and below
`
	entries, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]string{
		"PORT":  "8080",
		"HOST":  "0.0.0.0",
		"DEBUG": "true",
	}

	if len(entries) != len(expected) {
		t.Fatalf("expected %d entries, got %d", len(expected), len(entries))
	}
	for _, e := range entries {
		if expected[e.Key] != e.Value {
			t.Errorf("for key %s, expected %q, got %q", e.Key, expected[e.Key], e.Value)
		}
	}
}

func TestParse_ExportPrefix(t *testing.T) {
	input := `
export API_KEY=secret_123
export   DATABASE_URL=postgres://localhost/db
`
	entries, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Key != "API_KEY" || entries[0].Value != "secret_123" {
		t.Errorf("unexpected entry 0: %+v", entries[0])
	}
	if entries[1].Key != "DATABASE_URL" || entries[1].Value != "postgres://localhost/db" {
		t.Errorf("unexpected entry 1: %+v", entries[1])
	}
}

func TestParse_QuotedValues(t *testing.T) {
	input := `
DOUBLE_QUOTED="hello \"world\"\nnew line\ttab"
SINGLE_QUOTED='raw \'string\' \n not an escape'
EMPTY_DOUBLE=""
EMPTY_SINGLE=''
WITH_INLINE_COMMENT="quoted value" # this is a comment
`
	entries, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := make(map[string]string)
	for _, e := range entries {
		m[e.Key] = e.Value
	}

	if m["DOUBLE_QUOTED"] != "hello \"world\"\nnew line\ttab" {
		t.Errorf("unexpected DOUBLE_QUOTED: %q", m["DOUBLE_QUOTED"])
	}
	if m["SINGLE_QUOTED"] != "raw 'string' \\n not an escape" {
		t.Errorf("unexpected SINGLE_QUOTED: %q", m["SINGLE_QUOTED"])
	}
	if m["EMPTY_DOUBLE"] != "" {
		t.Errorf("unexpected EMPTY_DOUBLE: %q", m["EMPTY_DOUBLE"])
	}
	if m["EMPTY_SINGLE"] != "" {
		t.Errorf("unexpected EMPTY_SINGLE: %q", m["EMPTY_SINGLE"])
	}
	if m["WITH_INLINE_COMMENT"] != "quoted value" {
		t.Errorf("unexpected WITH_INLINE_COMMENT: %q", m["WITH_INLINE_COMMENT"])
	}
}

func TestParse_MultilineValues(t *testing.T) {
	input := `
PRIVATE_KEY="-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEA0
-----END RSA PRIVATE KEY-----"
CERT='line 1
line 2
line 3'
`
	entries, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := make(map[string]string)
	for _, e := range entries {
		m[e.Key] = e.Value
	}

	expectedKey := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0\n-----END RSA PRIVATE KEY-----"
	if m["PRIVATE_KEY"] != expectedKey {
		t.Errorf("unexpected PRIVATE_KEY: %q", m["PRIVATE_KEY"])
	}

	expectedCert := "line 1\nline 2\nline 3"
	if m["CERT"] != expectedCert {
		t.Errorf("unexpected CERT: %q", m["CERT"])
	}
}

func TestParse_InlineComments(t *testing.T) {
	input := `
VAR_A=123 # inline comment
VAR_B=http://example.com#anchor # url with hash
`
	entries, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := make(map[string]string)
	for _, e := range entries {
		m[e.Key] = e.Value
	}

	if m["VAR_A"] != "123" {
		t.Errorf("expected 123, got %q", m["VAR_A"])
	}
	// #anchor without preceding whitespace is part of value
	if m["VAR_B"] != "http://example.com#anchor" {
		t.Errorf("expected http://example.com#anchor, got %q", m["VAR_B"])
	}
}

func TestParse_NoShellEvaluation(t *testing.T) {
	// Values looking like shell injection or variable expansion must remain literal strings
	input := `
MALICIOUS_1=$(rm -rf /)
MALICIOUS_2="eval ` + "`cat /etc/passwd`" + `"
MALICIOUS_3=${SECRET_TOKEN:-fallback}
`
	entries, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := make(map[string]string)
	for _, e := range entries {
		m[e.Key] = e.Value
	}

	if m["MALICIOUS_1"] != "$(rm -rf /)" {
		t.Errorf("expected literal string, got %q", m["MALICIOUS_1"])
	}
	if !strings.Contains(m["MALICIOUS_2"], "cat /etc/passwd") {
		t.Errorf("expected literal string, got %q", m["MALICIOUS_2"])
	}
	if m["MALICIOUS_3"] != "${SECRET_TOKEN:-fallback}" {
		t.Errorf("expected literal string, got %q", m["MALICIOUS_3"])
	}
}

func TestParse_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "missing equals",
			input:   "NO_EQUALS_HERE",
			wantErr: "missing '=' delimiter",
		},
		{
			name:    "invalid key start",
			input:   "123BAD=value",
			wantErr: "invalid variable name",
		},
		{
			name:    "invalid key chars",
			input:   "BAD-KEY=value",
			wantErr: "invalid variable name",
		},
		{
			name:    "duplicate key",
			input:   "FOO=1\nFOO=2",
			wantErr: "duplicate variable \"FOO\"",
		},
		{
			name:    "unclosed double quote",
			input:   "FOO=\"unclosed string",
			wantErr: "unclosed quoted string",
		},
		{
			name:    "unclosed single quote",
			input:   "FOO='unclosed string",
			wantErr: "unclosed quoted string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestParse_Limits(t *testing.T) {
	// Exceed variable count
	var sb strings.Builder
	for i := 0; i < 205; i++ {
		sb.WriteString(strings.Repeat("A", 5) + string(rune('a'+(i%26))) + string(rune('0'+(i/26))) + "=val\n")
	}
	_, err := Parse(sb.String())
	if err == nil || !strings.Contains(err.Error(), "variable count exceeds") {
		t.Fatalf("expected variable count error, got: %v", err)
	}

	// Exceed file size
	huge := strings.Repeat("# huge padding\n", 20000)
	_, err = Parse(huge)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum limit") {
		t.Fatalf("expected file size error, got: %v", err)
	}
}
