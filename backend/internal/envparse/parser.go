package envparse

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	// MaxFileSize bounds uploaded or parsed .env files (256 KB).
	MaxFileSize = 256 * 1024

	// MaxVariableCount bounds the number of variables in a single .env file.
	MaxVariableCount = 200

	// MaxKeyLength bounds variable name length.
	MaxKeyLength = 128

	// MaxValueLength bounds an individual variable value length (8 KB).
	MaxValueLength = 8 * 1024
)

var (
	// validKeyRegex enforces standard POSIX/UNIX environment variable names.
	validKeyRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Entry represents one successfully parsed key-value pair.
type Entry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Line  int    `json:"line"`
}

// ParseOption configures the parser behavior.
type ParseOption struct {
	AllowDuplicates bool // If false, returns an error on duplicate key
}

// Parse reads .env file content and returns the parsed entries.
// It supports:
//   - Blank lines and full-line comments starting with '#'
//   - Optional 'export ' prefix
//   - Single-quoted values (literal, can be multiline)
//   - Double-quoted values (escapes: \n, \r, \t, \", \\, can be multiline)
//   - Unquoted values with inline comments (following whitespace)
//   - Strict key name validation (^[A-Za-z_][A-Za-z0-9_]*$)
//   - Denial of service limits (file size, count, key length, value length)
//
// Crucially: it never evaluates subshells, parameter expansions, or shell commands.
func Parse(content string) ([]Entry, error) {
	if len(content) > MaxFileSize {
		return nil, fmt.Errorf("file size (%d bytes) exceeds maximum limit of %d bytes", len(content), MaxFileSize)
	}

	var entries []Entry
	seenKeys := make(map[string]int)

	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		rawLine := scanner.Text()
		trimmed := strings.TrimSpace(rawLine)

		// Skip blank lines and pure comments
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Strip optional 'export ' prefix (with any amount of whitespace)
		if strings.HasPrefix(trimmed, "export") && len(trimmed) > 6 && unicode.IsSpace(rune(trimmed[6])) {
			trimmed = strings.TrimSpace(trimmed[6:])
		}

		// Find '=' separator
		eqIdx := strings.IndexByte(trimmed, '=')
		if eqIdx == -1 {
			return nil, fmt.Errorf("line %d: missing '=' delimiter", lineNum)
		}

		key := strings.TrimSpace(trimmed[:eqIdx])
		if key == "" {
			return nil, fmt.Errorf("line %d: empty variable name", lineNum)
		}
		if len(key) > MaxKeyLength {
			return nil, fmt.Errorf("line %d: variable name exceeds maximum length of %d", lineNum, MaxKeyLength)
		}
		if !validKeyRegex.MatchString(key) {
			return nil, fmt.Errorf("line %d: invalid variable name %q (must start with a letter/underscore and contain only alphanumeric/underscore)", lineNum, key)
		}

		valRest := strings.TrimLeftFunc(trimmed[eqIdx+1:], unicode.IsSpace)
		val, nextLines, err := parseValue(valRest, scanner, &lineNum)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		_ = nextLines

		if len(val) > MaxValueLength {
			return nil, fmt.Errorf("line %d: variable %q value exceeds maximum length of %d", lineNum, key, MaxValueLength)
		}

		if firstLine, exists := seenKeys[key]; exists {
			return nil, fmt.Errorf("line %d: duplicate variable %q (previously defined at line %d)", lineNum, key, firstLine)
		}
		seenKeys[key] = lineNum

		entries = append(entries, Entry{
			Key:   key,
			Value: val,
			Line:  lineNum,
		})

		if len(entries) > MaxVariableCount {
			return nil, fmt.Errorf("variable count exceeds maximum limit of %d", MaxVariableCount)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading .env content: %w", err)
	}

	return entries, nil
}

// ParseMap parses .env content and returns a map[string]string.
func ParseMap(content string) (map[string]string, error) {
	entries, err := Parse(content)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.Key] = e.Value
	}
	return m, nil
}

// parseValue extracts single-quoted, double-quoted, or unquoted values.
func parseValue(valRest string, scanner *bufio.Scanner, lineNum *int) (string, int, error) {
	if valRest == "" {
		return "", 0, nil
	}

	// Double-quoted
	if strings.HasPrefix(valRest, "\"") {
		return parseQuoted(valRest[1:], '"', scanner, lineNum)
	}

	// Single-quoted
	if strings.HasPrefix(valRest, "'") {
		return parseQuoted(valRest[1:], '\'', scanner, lineNum)
	}

	// Unquoted: scan until '#' preceded by whitespace, or trailing whitespace
	var sb strings.Builder
	runes := []rune(valRest)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '#' && (i == 0 || unicode.IsSpace(runes[i-1])) {
			break
		}
		sb.WriteRune(r)
	}

	return strings.TrimRightFunc(sb.String(), unicode.IsSpace), 0, nil
}

// parseQuoted handles quoted strings, including multiline values and escape characters.
func parseQuoted(firstLine string, quoteChar rune, scanner *bufio.Scanner, lineNum *int) (string, int, error) {
	var sb strings.Builder
	current := firstLine
	linesConsumed := 0

	for {
		escaped := false
		closed := false

		runes := []rune(current)
		for i := 0; i < len(runes); i++ {
			r := runes[i]

			if escaped {
				if quoteChar == '"' {
					switch r {
					case 'n':
						sb.WriteRune('\n')
					case 'r':
						sb.WriteRune('\r')
					case 't':
						sb.WriteRune('\t')
					case '\\':
						sb.WriteRune('\\')
					case '"':
						sb.WriteRune('"')
					case '$':
						// Literal $, never expand
						sb.WriteRune('$')
					default:
						sb.WriteRune('\\')
						sb.WriteRune(r)
					}
				} else {
					// Single quotes: escape sequence \' or \\
					if r == '\'' || r == '\\' {
						sb.WriteRune(r)
					} else {
						sb.WriteRune('\\')
						sb.WriteRune(r)
					}
				}
				escaped = false
				continue
			}

			if r == '\\' {
				escaped = true
				continue
			}

			if r == quoteChar {
				// Closed quote! Check that remaining characters on this line are only whitespace or comment
				rest := strings.TrimSpace(string(runes[i+1:]))
				if rest != "" && !strings.HasPrefix(rest, "#") {
					return "", linesConsumed, fmt.Errorf("unexpected content after closing quote")
				}
				closed = true
				break
			}

			sb.WriteRune(r)
		}

		if closed {
			return sb.String(), linesConsumed, nil
		}

		// Needs multiline continuation
		if !scanner.Scan() {
			return "", linesConsumed, errors.New("unclosed quoted string at end of file")
		}
		*lineNum++
		linesConsumed++
		sb.WriteRune('\n')
		current = scanner.Text()
	}
}
