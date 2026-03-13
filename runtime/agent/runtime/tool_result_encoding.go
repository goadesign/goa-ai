// Package runtime owns execution-time result encoding and projection.
//
// This file owns canonical tool-result encoding across runtime-managed and
// custom workflow boundaries.
//
// Contract:
//   - Callers pass typed Go result values, never pre-encoded JSON bytes.
//   - Generated result codecs remain the single authority for semantic JSON.
//   - Bounded-result projection is applied exactly once after codec encoding.
package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/boundedresult"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// EncodeCanonicalToolResult encodes a typed tool result into the canonical JSON
// contract exposed by Goa-AI.
//
// It uses the generated result codec from spec as the sole semantic encoder,
// then overlays any runtime-owned bounded-result metadata from bounds. Callers
// that create workflow-safe envelopes outside ExecuteToolActivity should use
// this helper instead of duplicating codec and bounded-result projection logic.
func EncodeCanonicalToolResult(spec tools.ToolSpec, value any, bounds *agent.Bounds) (rawjson.Message, error) {
	if value == nil {
		return nil, nil
	}
	switch value.(type) {
	case json.RawMessage, rawjson.Message, []byte:
		return nil, fmt.Errorf("tool %s result must be a typed Go value, got %T", spec.Name, value)
	}
	if spec.Result.Codec.ToJSON == nil {
		return nil, fmt.Errorf("no result codec found for tool %s", spec.Name)
	}
	data, err := spec.Result.Codec.ToJSON(value)
	if err != nil {
		return nil, fmt.Errorf("encode tool %s result: %w", spec.Name, err)
	}
	projected, err := projectBoundedToolResultJSON(spec, json.RawMessage(data), bounds)
	if err != nil {
		return nil, fmt.Errorf("project bounded tool %s result: %w", spec.Name, err)
	}
	return append(rawjson.Message(nil), projected...), nil
}

// projectBoundedToolResultJSON overlays runtime-owned bounds metadata onto the
// canonical JSON result payload emitted to models, transcripts, and workflow
// boundaries.
//
// Contract:
//   - Authored tool result types remain semantic and domain-focused.
//   - planner.ToolResult.Bounds is the sole runtime-owned source for canonical
//     bounded fields.
//   - Any authored canonical bounded fields in the semantic JSON are discarded
//     before projection so stale values cannot leak past the bounds contract.
//   - The projected JSON contract is object-shaped; bounded tools that encode a
//     non-object semantic result fail loudly rather than silently drifting.
func projectBoundedToolResultJSON(spec tools.ToolSpec, raw json.RawMessage, bounds *agent.Bounds) (json.RawMessage, error) {
	if spec.Bounds == nil || bounds == nil {
		return raw, nil
	}

	var projected map[string]any
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")):
		projected = make(map[string]any)
	default:
		if err := json.Unmarshal(trimmed, &projected); err != nil {
			return nil, fmt.Errorf("decode bounded tool result JSON: %w", err)
		}
		if projected == nil {
			projected = make(map[string]any)
		}
	}

	for _, name := range canonicalBoundedResultJSONFields(spec) {
		delete(projected, name)
	}
	projected[boundedresult.FieldReturned] = bounds.Returned
	projected[boundedresult.FieldTruncated] = bounds.Truncated
	if bounds.Total != nil {
		projected[boundedresult.FieldTotal] = *bounds.Total
	}
	if bounds.RefinementHint != "" {
		projected[boundedresult.FieldRefinementHint] = bounds.RefinementHint
	}
	if spec.Bounds.Paging != nil && spec.Bounds.Paging.NextCursorField != "" && bounds.NextCursor != nil {
		projected[spec.Bounds.Paging.NextCursorField] = *bounds.NextCursor
	}

	out, err := json.Marshal(projected)
	if err != nil {
		return nil, fmt.Errorf("encode bounded tool result JSON: %w", err)
	}
	return out, nil
}

// canonicalBoundedResultJSONFields returns the reserved JSON property names that
// BoundedResult owns for runtime projection.
func canonicalBoundedResultJSONFields(spec tools.ToolSpec) []string {
	nextCursorField := ""
	if spec.Bounds != nil && spec.Bounds.Paging != nil {
		nextCursorField = spec.Bounds.Paging.NextCursorField
	}
	return boundedresult.CanonicalFieldNames(nextCursorField)
}
