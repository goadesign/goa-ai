package runtime

import (
	"strings"

	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/tools"
)

// formatResultPreview renders the user-facing tool result preview for toolName.
//
// This must run while the tool result is still strongly typed (i.e. before the
// hook event crosses the hook-activity JSON boundary). Once hook events are
// serialized, Result is decoded as a generic map with JSON (snake_case) keys,
// which breaks templates that reference Go field names.
func formatResultPreview(toolName tools.Ident, result any) string {
	return clampPreview(rthints.FormatResultHint(toolName, result))
}

// clampPreview normalizes whitespace and bounds previews to a reasonable length
// for UI display. It mirrors the normalization logic used by stream subscribers
// so callers can safely pass previews through hook events unchanged.
func clampPreview(in string) string {
	if in == "" {
		return ""
	}
	// normalize whitespace
	out := make([]rune, 0, len(in))
	prevSpace := false
	for _, r := range in {
		switch r {
		case '\n', '\r', '\t', ' ':
			if !prevSpace {
				out = append(out, ' ')
			}
			prevSpace = true
		default:
			out = append(out, r)
			prevSpace = false
		}
	}
	const max = 140
	if len(out) <= max {
		return strings.TrimSpace(string(out))
	}
	return strings.TrimSpace(string(out[:max]))
}
