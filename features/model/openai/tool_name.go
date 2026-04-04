package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SanitizeToolName maps a canonical goa-ai tool identifier (for example,
// "atlas.read.get_time_series") to an OpenAI-compatible function name.
//
// OpenAI function names are surfaced back in tool-call responses, so the
// adapter keeps the mapping deterministic and reversible per request.
//
// Contract:
//   - Dots are replaced with underscores to preserve namespace information.
//   - Only [a-zA-Z0-9_-] are emitted; all other runes become '_'.
//   - Names longer than 64 bytes are truncated and suffixed with a stable hash.
func SanitizeToolName(in string) string {
	if in == "" {
		return ""
	}
	const maxLen = 64
	const hashLen = 8

	allowed := true
	for _, r := range in {
		if r == '.' {
			r = '_'
		}
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
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
	sum := sha256.Sum256([]byte(in))
	suffix := hex.EncodeToString(sum[:])[:hashLen]
	prefixLen := max(maxLen-(1+hashLen), 1)
	return sanitized[:prefixLen] + "_" + suffix
}
