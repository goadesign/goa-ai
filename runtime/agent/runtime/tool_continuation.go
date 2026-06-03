package runtime

// tool_continuation.go owns the runtime continuation contract for bounded
// cursor-paged tool results.
//
// Providers return raw cursors in agent.Bounds.NextCursor. The runtime keeps
// those cursors in execution history, but the model-visible result projects the
// producing tool_call_id as the continuation reference. Follow-up tool calls
// that set the payload cursor field to that reference are hydrated back into the
// provider payload before execution.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// modelVisibleBoundsForCall clones bounds and replaces provider cursors with
// the tool_call_id reference that the model must pass to continue paging.
func modelVisibleBoundsForCall(spec tools.ToolSpec, toolCallID string, bounds *agent.Bounds) *agent.Bounds {
	if spec.Bounds == nil || spec.Bounds.Paging == nil || bounds == nil {
		return cloneBounds(bounds)
	}
	out := cloneBounds(bounds)
	if out.NextCursor != nil {
		ref := toolCallID
		out.NextCursor = &ref
	}
	return out
}

// modelVisibleBoundsForTool projects provider-owned bounds into the
// model-visible continuation-reference contract for a registered tool.
func (r *Runtime) modelVisibleBoundsForTool(name tools.Ident, toolCallID string, bounds *agent.Bounds) (*agent.Bounds, error) {
	if bounds == nil {
		return nil, nil
	}
	spec, ok := r.toolSpec(name)
	if !ok {
		return nil, fmt.Errorf("project model-visible bounds for %s: no tool spec found", name)
	}
	if spec.Bounds != nil && spec.Bounds.Paging != nil && bounds.NextCursor != nil && toolCallID == "" {
		return nil, fmt.Errorf("project model-visible bounds for %s: missing tool_call_id", name)
	}
	return modelVisibleBoundsForCall(spec, toolCallID, bounds), nil
}

// hydrateContinuationCalls converts model-authored continuation references into
// provider-owned cursor payloads before tools are scheduled.
func (r *Runtime) hydrateContinuationCalls(calls []planner.ToolRequest, history []*planner.ToolOutput) ([]planner.ToolRequest, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	out := make([]planner.ToolRequest, 0, len(calls))
	for _, call := range calls {
		hydrated, err := r.hydrateContinuationCall(call, history)
		if err != nil {
			return nil, err
		}
		out = append(out, hydrated)
	}
	return out, nil
}

// hydrateContinuationCall resolves one cursor reference against prior tool
// outputs and reconstructs the exact provider payload for the next page.
func (r *Runtime) hydrateContinuationCall(call planner.ToolRequest, history []*planner.ToolOutput) (planner.ToolRequest, error) {
	spec, ok := r.toolSpec(call.Name)
	if !ok || spec.Bounds == nil || spec.Bounds.Paging == nil || spec.Bounds.Paging.CursorField == "" {
		return call, nil
	}
	current, err := decodeJSONPayloadObject(call.Payload)
	if err != nil {
		return planner.ToolRequest{}, fmt.Errorf("hydrate continuation for %s: %w", call.Name, err)
	}
	cursorRaw, ok := current[spec.Bounds.Paging.CursorField]
	if !ok {
		return call, nil
	}
	ref, err := decodeContinuationReference(cursorRaw)
	if err != nil {
		return planner.ToolRequest{}, fmt.Errorf("hydrate continuation for %s: %w", call.Name, err)
	}
	prior, err := resolveContinuationOutput(call, spec.Bounds.Paging.CursorField, ref, history)
	if err != nil {
		return planner.ToolRequest{}, err
	}
	priorPayload, err := decodeJSONPayloadObject(prior.Payload)
	if err != nil {
		return planner.ToolRequest{}, fmt.Errorf("hydrate continuation for %s: decode prior payload: %w", call.Name, err)
	}
	if err := validateContinuationPayload(call, spec.Bounds.Paging.CursorField, current, priorPayload); err != nil {
		return planner.ToolRequest{}, err
	}
	nextCursor := *prior.ProviderBounds.NextCursor
	nextCursorJSON, err := json.Marshal(nextCursor)
	if err != nil {
		return planner.ToolRequest{}, fmt.Errorf("hydrate continuation for %s: encode provider cursor: %w", call.Name, err)
	}
	priorPayload[spec.Bounds.Paging.CursorField] = nextCursorJSON
	hydratedPayload, err := json.Marshal(priorPayload)
	if err != nil {
		return planner.ToolRequest{}, fmt.Errorf("hydrate continuation for %s: encode hydrated payload: %w", call.Name, err)
	}
	call.Payload = rawjson.Message(hydratedPayload)
	return call, nil
}

// cloneBounds copies bounds metadata so model-visible projection never mutates
// provider-owned cursor state kept in execution history.
func cloneBounds(bounds *agent.Bounds) *agent.Bounds {
	if bounds == nil {
		return nil
	}
	out := *bounds
	if bounds.Total != nil {
		total := *bounds.Total
		out.Total = &total
	}
	if bounds.NextCursor != nil {
		next := *bounds.NextCursor
		out.NextCursor = &next
	}
	return &out
}

// decodeJSONPayloadObject decodes tool payload JSON into raw object fields so
// continuation hydration can preserve provider payload structure.
func decodeJSONPayloadObject(payload rawjson.Message) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, fmt.Errorf("decode payload object: %w", err)
	}
	if out == nil {
		return nil, fmt.Errorf("payload must be a JSON object")
	}
	return out, nil
}

// decodeContinuationReference enforces the model-facing continuation reference
// shape before any provider cursor is injected.
func decodeContinuationReference(raw json.RawMessage) (string, error) {
	var ref string
	if err := json.Unmarshal(raw, &ref); err != nil {
		return "", fmt.Errorf("cursor must be a continuation reference string")
	}
	if ref == "" {
		return "", fmt.Errorf("cursor continuation reference is empty")
	}
	return ref, nil
}

// resolveContinuationOutput returns the prior output identified by ref and
// verifies that it can legally continue the current tool call.
func resolveContinuationOutput(call planner.ToolRequest, cursorField, ref string, history []*planner.ToolOutput) (*planner.ToolOutput, error) {
	for idx, output := range history {
		if output == nil || output.ToolCallID != ref {
			continue
		}
		if output.Name != call.Name {
			return nil, fmt.Errorf("continuation reference %q belongs to tool %s, not %s", ref, output.Name, call.Name)
		}
		if output.Error != nil {
			return nil, fmt.Errorf("continuation reference %q points to a failed tool result", ref)
		}
		if output.ProviderBounds == nil || output.ProviderBounds.NextCursor == nil {
			return nil, fmt.Errorf("continuation reference %q has no provider cursor", ref)
		}
		if continuationConsumed(call.Name, cursorField, *output.ProviderBounds.NextCursor, history[idx+1:]) {
			return nil, fmt.Errorf("continuation reference %q is stale", ref)
		}
		return output, nil
	}
	return nil, fmt.Errorf("unknown continuation reference %q for tool %s", ref, call.Name)
}

// continuationConsumed reports whether a later tool output already used the
// provider cursor exposed by a continuation reference.
func continuationConsumed(tool tools.Ident, cursorField, providerCursor string, history []*planner.ToolOutput) bool {
	for _, output := range history {
		if output == nil || output.Name != tool {
			continue
		}
		payload, err := decodeJSONPayloadObject(output.Payload)
		if err != nil {
			panic(fmt.Sprintf("runtime: decode historical tool payload for continuation: %v", err))
		}
		raw, ok := payload[cursorField]
		if !ok {
			continue
		}
		var used string
		if err := json.Unmarshal(raw, &used); err != nil {
			panic(fmt.Sprintf("runtime: decode historical continuation cursor: %v", err))
		}
		if used == providerCursor {
			return true
		}
	}
	return false
}

// validateContinuationPayload rejects model-authored parameter drift while still
// allowing the canonical payload shape of only the cursor reference.
func validateContinuationPayload(call planner.ToolRequest, cursorField string, current, prior map[string]json.RawMessage) error {
	for name, currentValue := range current {
		if name == cursorField {
			continue
		}
		priorValue, ok := prior[name]
		if !ok {
			return fmt.Errorf("continuation for %s changed parameter %q", call.Name, name)
		}
		equal, err := jsonValuesEqual(currentValue, priorValue)
		if err != nil {
			return fmt.Errorf("continuation for %s compare parameter %q: %w", call.Name, name, err)
		}
		if !equal {
			return fmt.Errorf("continuation for %s changed parameter %q", call.Name, name)
		}
	}
	return nil
}

// jsonValuesEqual compares JSON values semantically so whitespace and object key
// order do not affect continuation validation.
func jsonValuesEqual(left, right json.RawMessage) (bool, error) {
	var l any
	if err := json.Unmarshal(left, &l); err != nil {
		return false, err
	}
	var r any
	if err := json.Unmarshal(right, &r); err != nil {
		return false, err
	}
	return reflect.DeepEqual(l, r), nil
}
