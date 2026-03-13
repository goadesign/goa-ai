package runtime

// tool_result_materialization.go owns the runtime's typed tool-result
// enrichment path.
//
// Contract:
// - All successful tool results, whether executed directly or provided
//   externally through an await signal, are materialized before canonical JSON
//   encoding and hook publication.
// - Toolset-owned server-only sidecars must be attached here so streamed
//   `tool_result` events, durable run logs, and resume inputs all observe the
//   same result shape.
// - External callers provide raw result JSON only; they never construct the
//   runtime's internal `api.ToolEvent` envelope.

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// materializeToolResult runs the registered typed result materializer, enforces
// the tool contract, and returns canonical JSON for the final runtime-owned
// tool result payload.
func (r *Runtime) materializeToolResult(ctx context.Context, call planner.ToolRequest, result *planner.ToolResult) (rawjson.Message, error) {
	spec, ok := r.toolSpec(call.Name)
	if !ok {
		return nil, fmt.Errorf("tool %q is not registered", call.Name)
	}
	if err := r.applyResultMaterializer(ctx, spec, call, result); err != nil {
		return nil, err
	}
	if err := r.enforceToolResultContracts(spec, call, result.Error, result); err != nil {
		return nil, err
	}
	var resultJSON rawjson.Message
	if result.Error == nil {
		encoded, err := r.marshalToolValue(ctx, call.Name, result.Result, result.Bounds)
		if err != nil {
			return nil, fmt.Errorf("encode %s tool result: %w", call.Name, err)
		}
		resultJSON = rawjson.Message(encoded)
	}
	return resultJSON, nil
}

// applyResultMaterializer invokes the toolset-owned typed result materializer
// when the toolset registered one.
func (r *Runtime) applyResultMaterializer(ctx context.Context, spec tools.ToolSpec, call planner.ToolRequest, result *planner.ToolResult) error {
	if result == nil {
		return fmt.Errorf("CRITICAL: nil tool result for %q (%s)", call.Name, call.ToolCallID)
	}
	if result.Name == "" {
		result.Name = call.Name
	}
	r.mu.RLock()
	reg, ok := r.toolsets[spec.Toolset]
	r.mu.RUnlock()
	if !ok || reg.ResultMaterializer == nil {
		return nil
	}
	if err := reg.ResultMaterializer(ctx, toolCallMeta(call), &call, result); err != nil {
		return fmt.Errorf("materialize %s tool result: %w", call.Name, err)
	}
	return nil
}

// decodeProvidedToolResults decodes externally supplied raw tool results in the
// canonical awaited call order and materializes their runtime-owned sidecars.
func (r *Runtime) decodeProvidedToolResults(ctx context.Context, allowed []planner.ToolRequest, provided map[string]*api.ProvidedToolResult) ([]*planner.ToolResult, []rawjson.Message, error) {
	if len(allowed) == 0 {
		return nil, nil, nil
	}
	results := make([]*planner.ToolResult, 0, len(allowed))
	resultJSONs := make([]rawjson.Message, 0, len(allowed))
	for _, call := range allowed {
		item := provided[call.ToolCallID]
		if item == nil {
			return nil, nil, fmt.Errorf("await: missing tool result for tool_call_id %q", call.ToolCallID)
		}
		if item.Name != call.Name {
			return nil, nil, fmt.Errorf("await: result tool %q does not match awaited tool %q", item.Name, call.Name)
		}
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			return nil, nil, fmt.Errorf("await: tool %q is not registered", call.Name)
		}
		result, resultJSON, err := r.decodeProvidedToolResult(ctx, spec, call, item)
		if err != nil {
			return nil, nil, err
		}
		results = append(results, result)
		resultJSONs = append(resultJSONs, resultJSON)
	}
	return results, resultJSONs, nil
}

// decodeProvidedToolResult converts one externally supplied raw result into the
// typed planner result used by the runtime.
func (r *Runtime) decodeProvidedToolResult(ctx context.Context, spec tools.ToolSpec, call planner.ToolRequest, item *api.ProvidedToolResult) (*planner.ToolResult, rawjson.Message, error) {
	if item == nil {
		return nil, nil, fmt.Errorf("await: nil tool result")
	}
	bounds, err := cloneProvidedToolBounds(spec, item)
	if err != nil {
		return nil, nil, err
	}
	var decoded any
	if hasNonNullJSON(item.Result.RawMessage()) && item.Error == nil {
		decoded, err = spec.Result.Codec.FromJSON(item.Result.RawMessage())
		if err != nil {
			return nil, nil, fmt.Errorf("await: decode tool result for %s: %w", call.Name, err)
		}
	}
	result := &planner.ToolResult{
		Name:       call.Name,
		Result:     decoded,
		ServerData: nil,
		Bounds:     bounds,
		Error:      item.Error,
		RetryHint:  item.RetryHint,
		ToolCallID: call.ToolCallID,
	}
	resultJSON, err := r.materializeToolResult(ctx, call, result)
	if err != nil {
		return nil, nil, fmt.Errorf("await: %w", err)
	}
	return result, resultJSON, nil
}

// cloneProvidedToolBounds validates externally supplied bounds metadata against
// the tool contract and returns an internal copy for the decoded planner result.
func cloneProvidedToolBounds(spec tools.ToolSpec, item *api.ProvidedToolResult) (*agent.Bounds, error) {
	if item.Bounds == nil {
		if item.Error == nil && spec.Bounds != nil {
			return nil, fmt.Errorf("await: missing bounds metadata for bounded tool %q", item.Name)
		}
		return nil, nil
	}
	if item.Error != nil || spec.Bounds == nil {
		return nil, fmt.Errorf("await: unexpected bounds metadata for tool %q", item.Name)
	}
	if item.Bounds.Truncated && item.Bounds.NextCursor == nil && item.Bounds.RefinementHint == "" {
		return nil, fmt.Errorf("await: bounded tool %q returned truncated result without next_cursor or refinement_hint", item.Name)
	}
	bounds := *item.Bounds
	if item.Bounds.Total != nil {
		total := *item.Bounds.Total
		bounds.Total = &total
	}
	if item.Bounds.NextCursor != nil {
		next := *item.Bounds.NextCursor
		bounds.NextCursor = &next
	}
	return &bounds, nil
}

func toolCallMeta(call planner.ToolRequest) ToolCallMeta {
	return ToolCallMeta{
		RunID:            call.RunID,
		SessionID:        call.SessionID,
		TurnID:           call.TurnID,
		ToolCallID:       call.ToolCallID,
		ParentToolCallID: call.ParentToolCallID,
	}
}
