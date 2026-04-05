// Package runtime owns execution-time result preview rendering.
//
// This file defines the template contract for `ResultHintTemplate(...)`.
//
// Contract:
//   - `Args` is the typed tool payload when the runtime can decode it from the
//     original tool call; otherwise nil.
//   - `Result` is the semantic typed tool result returned by the executor.
//   - `Bounds` is the runtime-owned bounded-result metadata when present.
//   - Preview rendering does not reshape semantic results or synthesize fields.
package runtime

import (
	"context"
	"strings"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// resultPreviewTemplateData is the explicit template root passed to
	// ResultHintTemplate renderers.
	//
	// `Args` carries the typed tool payload when the runtime can decode it from
	// the original tool call payload. `Result` carries the semantic typed result
	// as returned by the tool executor. `Bounds` exposes runtime-owned
	// bounded-result metadata when the tool declared BoundedResult and the
	// executor returned bounds; otherwise it remains nil.
	resultPreviewTemplateData struct {
		Args   any
		Result any
		Bounds *agent.Bounds
	}
)

// formatResultPreview renders the user-facing tool result preview for toolName.
//
// This must run while the tool result is still strongly typed (i.e. before the
// hook event crosses the hook-activity JSON boundary). The helper passes an
// explicit template root so ResultHintTemplate authors reference payload data
// via `.Args`, semantic data via `.Result`, and runtime-owned bounds via
// `.Bounds`.
func formatResultPreview(toolName tools.Ident, args, result any, bounds *agent.Bounds) string {
	return clampPreview(rthints.FormatResultHint(toolName, resultPreviewTemplateData{
		Args:   args,
		Result: result,
		Bounds: bounds,
	}))
}

// formatResultPreviewForCall decodes the original typed payload when available
// and renders the user-facing tool result preview for call.Name.
func formatResultPreviewForCall(ctx context.Context, rt *Runtime, call *planner.ToolRequest, result any, bounds *agent.Bounds) string {
	if call == nil {
		return ""
	}
	args := decodeResultPreviewArgs(ctx, rt, call)
	return formatResultPreview(call.Name, args, result, bounds)
}

// decodeResultPreviewArgs decodes the original tool payload into its typed Go
// value for result-hint rendering. Preview rendering is best-effort, so decode
// failures are logged and yield nil args rather than failing execution.
func decodeResultPreviewArgs(ctx context.Context, rt *Runtime, call *planner.ToolRequest) any {
	if rt == nil || call == nil || len(call.Payload) == 0 {
		return nil
	}
	spec, ok := rt.toolSpec(call.Name)
	if !ok || spec.Payload.Codec.FromJSON == nil {
		return nil
	}
	args, err := spec.Payload.Codec.FromJSON(call.Payload.RawMessage())
	if err != nil {
		if rt.logger != nil {
			rt.logger.Warn(ctx, "tool payload decode failed for result preview", "tool", call.Name, "err", err)
		}
		return nil
	}
	return args
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
