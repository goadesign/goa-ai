package runtime

// continuation.go keeps dedicated pagination correlation inside the runtime.
// The planner chooses the domain action to continue; the runtime binds the
// cursor from the single compatible bounded result before execution.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// continuationState is the successful page available to a dedicated
	// continuation action.
	continuationState struct {
		cursor  string
		payload rawjson.Message
	}
)

// availableContinuationTools returns the dedicated continuation actions whose
// single compatible successful result has another page.
func (r *Runtime) availableContinuationTools(agentID agent.Ident, outputs []*planner.ToolOutput) (map[tools.Ident]struct{}, error) {
	available := make(map[tools.Ident]struct{})
	for _, spec := range r.ToolSpecsForAgent(agentID) {
		if !isDedicatedContinuationSpec(spec) {
			continue
		}
		_, ok, err := r.continuationState(spec, outputs)
		if err != nil {
			return nil, err
		}
		if ok {
			available[spec.Name] = struct{}{}
		}
	}
	return available, nil
}

// bindContinuationCursors converts model-authored empty continuation actions
// into executable cursor payloads while preserving the empty model payload for
// transcript replay.
func (r *Runtime) bindContinuationCursors(result *planner.PlanResult, outputs []*planner.ToolOutput) error {
	if result == nil {
		return errors.New("runtime: cannot bind continuation cursor on nil planner result")
	}
	if err := r.validateContinuationBatch(result); err != nil {
		return err
	}
	for i := range result.ToolCalls {
		call := &result.ToolCalls[i]
		spec, ok := r.toolSpec(call.Name)
		if !ok || !isDedicatedContinuationSpec(spec) {
			continue
		}
		if err := validateEmptyContinuationPayload(call.Payload); err != nil {
			return fmt.Errorf("runtime: continuation tool %q payload: %w", call.Name, err)
		}
		state, ok, err := r.continuationState(spec, outputs)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("runtime: continuation tool %q has no available preceding page", call.Name)
		}
		executable, err := r.continuationPayload(spec, state)
		if err != nil {
			return fmt.Errorf("runtime: bind continuation tool %q payload: %w", call.Name, err)
		}
		call.ModelPayload = append(rawjson.Message(nil), call.Payload...)
		call.Payload = executable
	}
	return nil
}

// continuationState returns the only successful page belonging to the
// dedicated continuation or its source query. Multiple compatible outputs are
// ambiguous because no model-visible selector identifies a result chain.
func (r *Runtime) continuationState(spec tools.ToolSpec, outputs []*planner.ToolOutput) (continuationState, bool, error) {
	var compatible *planner.ToolOutput
	for _, output := range outputs {
		if output == nil || output.Failure != nil || !isContinuationOutput(spec, output.Name) {
			continue
		}
		if compatible != nil {
			return continuationState{}, false, fmt.Errorf(
				"runtime: continuation tool %q has multiple compatible preceding pages",
				spec.Name,
			)
		}
		compatible = output
	}
	if compatible == nil || compatible.Bounds == nil || compatible.Bounds.NextCursor == nil || *compatible.Bounds.NextCursor == "" {
		return continuationState{}, false, nil
	}
	return continuationState{
		cursor:  *compatible.Bounds.NextCursor,
		payload: compatible.Payload,
	}, true, nil
}

// validateContinuationBatch rejects planner batches that fork a dedicated
// pagination chain. The next resume receives one result batch, so accepting
// multiple source or continuation calls would make cursor correlation
// ambiguous without adding a model-visible chain selector.
func (r *Runtime) validateContinuationBatch(result *planner.PlanResult) error {
	calls := make(map[tools.Ident]tools.Ident)
	for _, call := range result.ToolCalls {
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			continue
		}
		chain, ok := dedicatedContinuationChain(spec)
		if !ok {
			continue
		}
		if prior, exists := calls[chain]; exists {
			return fmt.Errorf(
				"runtime: continuation chain %q cannot include multiple calls in one planner result (%q and %q)",
				chain,
				prior,
				call.Name,
			)
		}
		calls[chain] = call.Name
	}
	return nil
}

// isContinuationOutput reports whether a tool result belongs to the result set
// advanced by spec.
func isContinuationOutput(spec tools.ToolSpec, output tools.Ident) bool {
	return output == spec.Name || output == spec.Bounds.Paging.SourceTool
}

// continuationPayload builds the canonical executable payload. Replaying
// continuations retain the exact prior query arguments and replace only the
// generated cursor field; self-contained continuations need only the cursor.
func (r *Runtime) continuationPayload(spec tools.ToolSpec, state continuationState) (rawjson.Message, error) {
	fields := make(map[string]json.RawMessage)
	if spec.Bounds.Paging.ReplayPayload {
		if err := json.Unmarshal(state.payload, &fields); err != nil {
			return nil, fmt.Errorf("decode prior query payload: %w", err)
		}
	}
	cursor, err := json.Marshal(state.cursor)
	if err != nil {
		return nil, fmt.Errorf("encode cursor: %w", err)
	}
	fields[spec.Bounds.Paging.CursorField] = cursor
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode query payload: %w", err)
	}
	value, err := spec.Payload.Codec.FromJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("decode retained query with generated codec: %w", err)
	}
	canonical, err := spec.Payload.Codec.ToJSON(value)
	if err != nil {
		return nil, fmt.Errorf("encode retained query with generated codec: %w", err)
	}
	return canonical, nil
}

// isDedicatedContinuationSpec identifies the no-argument action generated for
// a separate ContinueWith target.
func isDedicatedContinuationSpec(spec tools.ToolSpec) bool {
	return spec.Bounds != nil &&
		spec.Bounds.Paging != nil &&
		spec.Bounds.Paging.ContinueTool == spec.Name &&
		spec.Bounds.Paging.SourceTool != ""
}

// dedicatedContinuationChain identifies the generated continuation action
// shared by a source query and its dedicated continuation tool.
func dedicatedContinuationChain(spec tools.ToolSpec) (tools.Ident, bool) {
	if spec.Bounds == nil || spec.Bounds.Paging == nil {
		return "", false
	}
	paging := spec.Bounds.Paging
	if paging.SourceTool != "" && paging.ContinueTool == spec.Name {
		return spec.Name, true
	}
	if paging.SourceTool == "" && paging.ContinueTool != "" {
		return paging.ContinueTool, true
	}
	return "", false
}

// validateEmptyContinuationPayload rejects any model-authored argument because
// the advertised continuation contract is an empty object.
func validateEmptyContinuationPayload(payload rawjson.Message) error {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("must be an empty JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var value struct{}
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
