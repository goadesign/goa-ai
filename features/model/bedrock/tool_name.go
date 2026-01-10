package bedrock

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SanitizeToolName maps a canonical tool identifier (for example, "atlas.read.get_time_series")
// to a Bedrock-compatible tool name.
//
// Bedrock imposes stricter tool name constraints than other providers. The tool name
// string surfaced to the model (and echoed back in tool_use blocks) must match the
// name registered in the tool configuration. This function implements the exact
// mapping used by the Bedrock adapter when constructing tool configurations.
//
// Contract:
//   - The mapping is deterministic.
//   - The mapping preserves canonical namespace information (".") by replacing dots
//     with underscores.
//   - The result contains only characters allowed by Bedrock: [a-zA-Z0-9_-]+.
//     Any other rune is replaced with '_'.
//   - The result is at most 64 bytes long. If the sanitized name exceeds the limit,
//     it is truncated and a stable hash suffix is appended to preserve uniqueness.
//
// Note: Callers should treat the output as provider-visible. Internally, goa-ai
// continues to use canonical tool identifiers; the adapter translates tool_use names
// back to canonical IDs using the per-request reverse map.
func SanitizeToolName(in string) string {
	if in == "" {
		return ""
	}
	const maxLen = 64
	const hashLen = 8

	// Fast path: if all runes are already allowed after mapping '.' to '_', keep
	// the string allocation-free.
	allowed := true
	for _, r := range in {
		if r == '.' {
			r = '_'
		}
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		case r == '-':
		default:
			allowed = false
		}
		if !allowed {
			break
		}
	}

	var sanitized string
	if allowed {
		sanitized = strings.ReplaceAll(in, ".", "_")
	} else {
		out := make([]rune, 0, len(in))
		for _, r := range in {
			if r == '.' {
				r = '_'
			}
			switch {
			case r >= 'a' && r <= 'z':
				out = append(out, r)
			case r >= 'A' && r <= 'Z':
				out = append(out, r)
			case r >= '0' && r <= '9':
				out = append(out, r)
			case r == '_' || r == '-':
				out = append(out, r)
			default:
				out = append(out, '_')
			}
		}
		sanitized = string(out)
	}

	if len(sanitized) <= maxLen {
		return sanitized
	}

	// Truncate and append a stable hash suffix to keep names within Bedrock's
	// documented 64-character limit while preserving uniqueness.
	sum := sha256.Sum256([]byte(in))
	suffix := hex.EncodeToString(sum[:])[:hashLen]

	// Reserve "_" + hashLen at the end.
	prefixLen := maxLen - (1 + hashLen)
	if prefixLen < 1 {
		prefixLen = 1
	}
	return sanitized[:prefixLen] + "_" + suffix
}

