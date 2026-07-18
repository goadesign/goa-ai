// Package toolname owns the provider-visible projection of canonical goa-ai
// tool identifiers for providers whose function-name contract is
// [a-zA-Z0-9_-] capped at 64 bytes: the Claude Messages API (direct
// Anthropic, header-compatible gateways, Bedrock Converse) and the OpenAI
// Responses API. Adapters project canonical identifiers with Sanitize — or
// BuildMaps for a whole request — when building a tool list and invert the
// per-request reverse map when translating provider tool calls back to
// canonical identifiers, so the projection must be deterministic and
// injective per request. Transcripts store canonical identifiers only; the
// provider form is derived per request and never persisted. Gemini's
// different name contract keeps its own projection in features/model/vertex.
package toolname

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
)

// BuildMaps projects defs into the bijective canonical↔provider name mapping
// for one request. It owns the injectivity invariant: a sanitization
// collision is rejected because the provider name would otherwise identify
// two executable tools. Definitions must be non-nil with non-empty names —
// they are constructed by generated code and the runtime, so a violation is
// a caller bug, not an input to tolerate. Adapters wrap the error with their
// provider prefix.
func BuildMaps(defs []*model.ToolDefinition) (canonToProv, provToCanon map[string]string, err error) {
	canonToProv = make(map[string]string, len(defs))
	provToCanon = make(map[string]string, len(defs))
	for i, def := range defs {
		if def == nil {
			return nil, nil, fmt.Errorf("tool[%d] is nil", i)
		}
		if def.Name == "" {
			return nil, nil, fmt.Errorf("tool[%d] is missing name", i)
		}
		prov := Sanitize(def.Name)
		if prev, ok := provToCanon[prov]; ok && prev != def.Name {
			return nil, nil, fmt.Errorf(
				"tool name %q sanitizes to %q which collides with %q",
				def.Name, prov, prev,
			)
		}
		canonToProv[def.Name] = prov
		provToCanon[prov] = def.Name
	}
	return canonToProv, provToCanon, nil
}

// Sanitize maps a canonical tool identifier to the Claude provider contract:
// [a-zA-Z0-9_-]+ and at most 64 bytes. Namespace dots become underscores, and
// overlong names receive a stable hash suffix.
func Sanitize(input string) string {
	if input == "" {
		return ""
	}
	const maxLen = 64
	const hashLen = 8

	allowed := true
	for _, r := range input {
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
		sanitized = strings.ReplaceAll(input, ".", "_")
	} else {
		out := make([]rune, 0, len(input))
		for _, r := range input {
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

	sum := sha256.Sum256([]byte(input))
	suffix := hex.EncodeToString(sum[:])[:hashLen]
	prefixLen := max(maxLen-(1+hashLen), 1)
	return sanitized[:prefixLen] + "_" + suffix
}
