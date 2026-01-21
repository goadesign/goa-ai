package runtime

// tool_events.go contains helpers for encoding and decoding tool results into
// workflow-boundary safe envelopes.
//
// Contract:
// - planner.ToolResult contains `any` fields (Result, Artifact.Data). Crossing a
//   workflow boundary with those values allows engines/codecs (e.g. Temporal) to
//   rehydrate them as map[string]any, breaking tool and sidecar codecs.
// - encodeToolEvents converts typed tool results into api.ToolEvent values that
//   only contain canonical JSON bytes plus structured metadata.
// - decodeToolEvents converts api.ToolEvent values back into planner.ToolResult
//   values by decoding Result bytes via the registered tool codec.

import (
	"context"
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
)

// encodeToolEvents converts typed in-memory tool results into workflow-boundary safe
// envelopes.
//
// Contract:
//   - Input values are trusted to be runtime-produced tool results; nil entries are
//     a bug.
//   - Tool result values are encoded via the registered tool result codec and stored
//     as canonical JSON bytes on api.ToolEvent.Result.
//   - Artifacts are normalized and encoded as api.ToolArtifact JSON bytes so workflow
//     engines cannot rehydrate them into map[string]any.
func (r *Runtime) encodeToolEvents(ctx context.Context, events []*planner.ToolResult) ([]*api.ToolEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]*api.ToolEvent, 0, len(events))
	for _, ev := range events {
		result, err := r.marshalToolValue(ctx, ev.Name, ev.Result, false)
		if err != nil {
			return nil, fmt.Errorf("encode tool result for %s: %w", ev.Name, err)
		}
		// Normalize artifacts in place so Artifact.Data becomes json.RawMessage.
		if err := r.normalizeToolArtifacts(ctx, ev.Name, ev); err != nil {
			return nil, err
		}
		arts, err := encodeToolEventArtifacts(ev.Artifacts)
		if err != nil {
			return nil, fmt.Errorf("encode tool artifacts for %s: %w", ev.Name, err)
		}
		out = append(out, &api.ToolEvent{
			Name:          ev.Name,
			Result:        result,
			Artifacts:     arts,
			Bounds:        ev.Bounds,
			Error:         ev.Error,
			RetryHint:     ev.RetryHint,
			Telemetry:     ev.Telemetry,
			ToolCallID:    ev.ToolCallID,
			ChildrenCount: ev.ChildrenCount,
			RunLink:       ev.RunLink,
		})
	}
	return out, nil
}

// encodeToolEventArtifacts converts normalized planner artifacts into workflow-boundary
// safe api.ToolArtifact values.
//
// Contract:
// - Artifact.Data must already be json.RawMessage (or nil) by the time this is called.
// - Returns an error if any artifact entry is nil or carries a non-JSON payload.
func encodeToolEventArtifacts(in []*planner.Artifact) ([]*api.ToolArtifact, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]*api.ToolArtifact, 0, len(in))
	for _, a := range in {
		if a == nil {
			return nil, fmt.Errorf("CRITICAL: tool result contained nil artifact entry")
		}
		var raw json.RawMessage
		switch v := a.Data.(type) {
		case nil:
			raw = nil
		case json.RawMessage:
			raw = append(json.RawMessage(nil), v...)
		case []byte:
			if len(v) == 0 {
				raw = nil
				break
			}
			if !json.Valid(v) {
				return nil, fmt.Errorf("artifact data must be valid JSON, got []byte")
			}
			raw = json.RawMessage(append([]byte(nil), v...))
		default:
			return nil, fmt.Errorf("artifact data must be json.RawMessage, got %T", a.Data)
		}
		out = append(out, &api.ToolArtifact{
			Kind:       a.Kind,
			Data:       raw,
			SourceTool: a.SourceTool,
			RunLink:    a.RunLink,
		})
	}
	return out, nil
}

// decodeToolEvents converts workflow-boundary tool event envelopes back into typed
// planner tool results.
//
// Contract:
//   - Result bytes are decoded via the registered tool result codec.
//   - Artifact bytes are round-tripped through the sidecar codec to enforce schema
//     and canonical encoding (no map[string]any leakage).
func (r *Runtime) decodeToolEvents(ctx context.Context, events []*api.ToolEvent) ([]*planner.ToolResult, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]*planner.ToolResult, 0, len(events))
	for _, ev := range events {
		if ev == nil {
			return nil, fmt.Errorf("CRITICAL: nil tool event entry")
		}
		var decoded any
		if hasNonNullJSON(ev.Result) && ev.Error == nil {
			val, err := r.unmarshalToolValue(ctx, ev.Name, ev.Result, false)
			if err != nil {
				return nil, fmt.Errorf("decode tool result for %s: %w", ev.Name, err)
			}
			decoded = val
		}
		tr := &planner.ToolResult{
			Name:          ev.Name,
			Result:        decoded,
			Artifacts:     decodeToolEventArtifacts(ev.Artifacts),
			Bounds:        ev.Bounds,
			Error:         ev.Error,
			RetryHint:     ev.RetryHint,
			Telemetry:     ev.Telemetry,
			ToolCallID:    ev.ToolCallID,
			ChildrenCount: ev.ChildrenCount,
			RunLink:       ev.RunLink,
		}
		// Enforce artifact shape by round-tripping raw JSON through the sidecar codec.
		if err := r.normalizeToolArtifacts(ctx, ev.Name, tr); err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

// decodeToolEventArtifacts converts workflow-safe api.ToolArtifact values into
// planner.Artifact values suitable for runtime normalization and consumption.
//
// Contract:
//   - Artifact.Data is kept as json.RawMessage; callers must normalize via sidecar
//     codecs before emitting events to consumers that expect typed sidecars.
func decodeToolEventArtifacts(in []*api.ToolArtifact) []*planner.Artifact {
	if len(in) == 0 {
		return nil
	}
	out := make([]*planner.Artifact, 0, len(in))
	for _, a := range in {
		if a == nil {
			continue
		}
		out = append(out, &planner.Artifact{
			Kind:       a.Kind,
			Data:       a.Data,
			SourceTool: a.SourceTool,
			RunLink:    a.RunLink,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
