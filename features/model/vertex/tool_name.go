package vertex

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
)

// sanitizeToolName rewrites a goa-ai tool identifier into a Gemini-legal
// function name: first char [a-zA-Z_], rest [a-zA-Z0-9_.:-], max 64 chars.
// The mapping is deterministic so buildToolNameMaps can invert it per request.
func sanitizeToolName(name string) string {
	if name == "" {
		return "_"
	}
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		legal := c == '_' || c == '.' || c == ':' || c == '-' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if i == 0 {
			first := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
			if !first {
				out = append(out, '_')
			}
			if legal {
				out = append(out, c)
			} else {
				continue
			}
			continue
		}
		if legal {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// buildToolNameMaps returns the bijective canonical↔provider name mapping for
// one request. It rejects sanitization collisions because the provider name
// would otherwise identify two executable tools.
func buildToolNameMaps(defs []*model.ToolDefinition) (map[string]string, map[string]string, error) {
	canonToProv := make(map[string]string, len(defs))
	provToCanon := make(map[string]string, len(defs))
	for _, def := range defs {
		prov := sanitizeToolName(def.Name)
		if existing, taken := provToCanon[prov]; taken {
			return nil, nil, fmt.Errorf(
				"vertex: tool names %q and %q both map to provider name %q",
				existing,
				def.Name,
				prov,
			)
		}
		canonToProv[def.Name] = prov
		provToCanon[prov] = def.Name
	}
	return canonToProv, provToCanon, nil
}
