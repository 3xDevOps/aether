// Package jsonc normalizes JSON configuration with comments and trailing commas.
package jsonc

import "bytes"

// Normalize preserves quoted strings and newlines while removing comments and
// trailing commas. The caller must still use a JSON decoder to validate syntax.
// The input is not modified; unterminated block comments remain invalid JSON.
func Normalize(data []byte) []byte {
	clean := bytes.Clone(data)
	quoted, escaped := false, false
	for i := 0; i < len(clean); i++ {
		c := clean[i]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			continue
		}
		if c != '/' || i+1 >= len(clean) {
			continue
		}
		if clean[i+1] == '/' {
			for ; i < len(clean) && clean[i] != '\n'; i++ {
				clean[i] = ' '
			}
		} else if clean[i+1] == '*' {
			clean[i], clean[i+1] = ' ', ' '
			i += 2
			closed := false
			for ; i < len(clean); i++ {
				if clean[i] == '*' && i+1 < len(clean) && clean[i+1] == '/' {
					clean[i], clean[i+1] = ' ', ' '
					i++
					closed = true
					break
				}
				if clean[i] != '\n' && clean[i] != '\r' {
					clean[i] = ' '
				}
			}
			if !closed {
				return data // Let the JSON decoder reject an unterminated comment.
			}
		}
	}
	quoted, escaped = false, false
	for i, c := range clean {
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case ',':
			j := i + 1
			for j < len(clean) && (clean[j] == ' ' || clean[j] == '\n' || clean[j] == '\r' || clean[j] == '\t') {
				j++
			}
			if j < len(clean) && (clean[j] == '}' || clean[j] == ']') {
				clean[i] = ' '
			}
		}
	}
	return clean
}
