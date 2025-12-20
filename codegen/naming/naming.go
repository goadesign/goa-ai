package naming

import (
	"strings"
	"unicode"

	"goa.design/goa/v3/codegen"
)

// SanitizeToken converts an arbitrary string into a filesystem-safe token.
// It is used to derive deterministic directory/package fragments from user
// input (agent names, toolset names, etc).
//
// The returned token:
//   - is lower snake_case
//   - contains only [a-z0-9_]
//   - never starts/ends with '_' and never contains repeated "__"
//
// When the sanitized result is empty, SanitizeToken returns fallback.
func SanitizeToken(name, fallback string) string {
	s := strings.ToLower(codegen.SnakeCase(name))
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, s)
	s = strings.Trim(s, "_")
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	if s == "" {
		return fallback
	}
	return s
}

// QueueName builds a deterministic queue identifier for workflows/activities by
// sanitizing its parts and joining them with underscores.
func QueueName(parts ...string) string {
	sanitized := make([]string, 0, len(parts))
	for _, part := range parts {
		token := SanitizeToken(part, "queue")
		if token != "" {
			sanitized = append(sanitized, token)
		}
	}
	if len(sanitized) == 0 {
		return "queue"
	}
	return strings.Join(sanitized, "_")
}

// Identifier builds a stable dotted identifier by sanitizing parts and joining
// them with '.'.
func Identifier(parts ...string) string {
	sanitized := make([]string, 0, len(parts))
	for _, part := range parts {
		token := SanitizeToken(part, "segment")
		if token != "" {
			sanitized = append(sanitized, token)
		}
	}
	if len(sanitized) == 0 {
		return "id"
	}
	return strings.Join(sanitized, ".")
}

// HumanizeTitle converts a slug-like name (snake_case, kebab-case, dotted)
// into a conservative Title Case string.
func HumanizeTitle(s string) string {
	if s == "" {
		return s
	}
	// use last segment after '.' when present
	if i := strings.LastIndexByte(s, '.'); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	parts := strings.Fields(s)
	for i := range parts {
		if len(parts[i]) == 0 {
			continue
		}
		r := []rune(parts[i])
		r[0] = unicode.ToUpper(r[0])
		parts[i] = string(r)
	}
	return strings.Join(parts, " ")
}
