// Package transcript owns the provider-facing tool-result content contract used
// by both live runtime transcript generation and replay from durable events.
//
// Contract:
//   - Successful transcript tool results carry decoded semantic JSON values.
//   - Failed transcript tool results carry plain error text with IsError=true.
//   - Canonical raw JSON bytes stay at execution/history boundaries only.
//   - Oversized successful results are replaced with a documented omission object.
package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

const (
	// MaxToolResultContentBytes bounds the canonical successful result JSON that
	// can be embedded directly in transcript tool_result content.
	MaxToolResultContentBytes = 64 * 1024

	toolResultOmittedReasonSizeLimit = "size_limit"
)

// ProjectToolResultContent converts canonical result JSON plus transcript-side
// metadata into the semantic content stored in ToolResultPart.Content.
func ProjectToolResultContent(resultJSON rawjson.Message, bounds *agent.Bounds, preview string, errorMessage string) (any, error) {
	if errorMessage != "" {
		if hasNonNullToolResultJSON(resultJSON) {
			return nil, fmt.Errorf("transcript: tool_result is invalid: error and result are both set")
		}
		return errorMessage, nil
	}
	if !hasNonNullToolResultJSON(resultJSON) {
		return nil, nil
	}
	if len(resultJSON) > MaxToolResultContentBytes {
		return toolResultOmission(preview, bounds)
	}
	return decodeToolResultJSONValue(json.RawMessage(resultJSON))
}

// toolResultOmission builds the explicit omission object used when a canonical
// successful result exceeds the transcript size budget.
func toolResultOmission(preview string, bounds *agent.Bounds) (map[string]any, error) {
	content := map[string]any{
		"omitted": true,
		"reason":  toolResultOmittedReasonSizeLimit,
	}
	if preview != "" {
		content["preview"] = preview
	}
	if bounds != nil {
		boundsValue, err := decodeToolResultJSONValue(mustMarshalToolResultJSON(bounds))
		if err != nil {
			return nil, err
		}
		content["bounds"] = boundsValue
	}
	return content, nil
}

// decodeToolResultJSONValue decodes canonical tool-result JSON into plain
// JSON-compatible Go values suitable for transcript message content.
func decodeToolResultJSONValue(raw json.RawMessage) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("transcript: decode tool_result JSON: %w", err)
	}
	return value, nil
}

// hasNonNullToolResultJSON reports whether raw contains a non-empty, non-null
// canonical result payload.
func hasNonNullToolResultJSON(raw rawjson.Message) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// mustMarshalToolResultJSON encodes transcript-side metadata into canonical JSON
// for reuse by the semantic JSON decoder above.
func mustMarshalToolResultJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("transcript: encode tool_result JSON value: %v", err))
	}
	return raw
}
