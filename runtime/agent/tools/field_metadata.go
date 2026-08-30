package tools

// This file defines how generated field metadata paths match array indexes and
// caller-chosen map keys. Every generated codec and runtime consumer uses the
// same rule.

import "strings"

// LookupFieldMetadata returns metadata for a generated dotted path or an RFC
// 6901 JSON Pointer reported by a generated codec. "*" in generated metadata
// matches exactly one array index or caller-chosen map key. Exact matches take
// precedence; ambiguous matches return no value.
func LookupFieldMetadata[T any](metadata map[string]T, path string) (T, bool) {
	if value, ok := metadata[path]; ok {
		return value, true
	}
	var (
		exactMatch        T
		exactFound        bool
		exactAmbiguous    bool
		wildcardMatch     T
		wildcardFound     bool
		wildcardAmbiguous bool
	)
	actual, ok := fieldPathSegments(path)
	if !ok {
		var zero T
		return zero, false
	}
	for pattern, value := range metadata {
		parts := strings.Split(pattern, ".")
		if len(parts) != len(actual) {
			continue
		}
		match := true
		wildcard := false
		for index := range parts {
			if parts[index] == "*" {
				wildcard = true
				continue
			}
			if parts[index] != actual[index] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if !wildcard {
			if exactFound {
				exactAmbiguous = true
				continue
			}
			exactMatch = value
			exactFound = true
			continue
		}
		if wildcardFound {
			wildcardAmbiguous = true
			continue
		}
		wildcardMatch = value
		wildcardFound = true
	}
	if exactFound {
		return exactMatch, !exactAmbiguous
	}
	return wildcardMatch, wildcardFound && !wildcardAmbiguous
}

// fieldPathSegments converts a generated dotted path or JSON Pointer into
// comparable object-key segments.
func fieldPathSegments(path string) ([]string, bool) {
	if !strings.HasPrefix(path, "/") {
		return strings.Split(path, "."), true
	}
	encoded := strings.Split(path[1:], "/")
	segments := make([]string, len(encoded))
	for index, token := range encoded {
		decoded, ok := decodeJSONPointerToken(token)
		if !ok {
			return nil, false
		}
		segments[index] = decoded
	}
	return segments, true
}

// decodeJSONPointerToken restores one object key from RFC 6901 escaping.
func decodeJSONPointerToken(token string) (string, bool) {
	if !strings.Contains(token, "~") {
		return token, true
	}
	var decoded strings.Builder
	decoded.Grow(len(token))
	for index := 0; index < len(token); index++ {
		if token[index] != '~' {
			decoded.WriteByte(token[index])
			continue
		}
		if index+1 == len(token) {
			return "", false
		}
		index++
		switch token[index] {
		case '0':
			decoded.WriteByte('~')
		case '1':
			decoded.WriteByte('/')
		default:
			return "", false
		}
	}
	return decoded.String(), true
}
