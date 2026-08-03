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
// single unconsumed successful result has another page. Parallel source calls
// remain valid; an action is withheld while more than one invocation in its
// family could be continued because its empty payload cannot select a chain.
func (r *Runtime) availableContinuationTools(agentID agent.Ident, outputs []*planner.ToolOutput) (map[tools.Ident]struct{}, error) {
	available := make(map[tools.Ident]struct{})
	for _, spec := range r.ToolSpecsForAgent(agentID) {
		if !isDedicatedContinuationSpec(spec) {
			continue
		}
		states, err := r.continuationStates(spec, outputs)
		if err != nil {
			return nil, err
		}
		if len(states) == 1 {
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
	bound := make(map[tools.Ident]struct{})
	for i := range result.ToolCalls {
		call := &result.ToolCalls[i]
		spec, ok := r.toolSpec(call.Name)
		if !ok || !isDedicatedContinuationSpec(spec) {
			continue
		}
		if _, exists := bound[call.Name]; exists {
			return fmt.Errorf("runtime: continuation tool %q cannot be called more than once in one planner result", call.Name)
		}
		bound[call.Name] = struct{}{}
		if err := validateEmptyContinuationPayload(call.Payload); err != nil {
			return fmt.Errorf("runtime: continuation tool %q payload: %w", call.Name, err)
		}
		states, err := r.continuationStates(spec, outputs)
		if err != nil {
			return err
		}
		if len(states) == 0 {
			return fmt.Errorf("runtime: continuation tool %q has no available preceding page", call.Name)
		}
		if len(states) > 1 {
			return fmt.Errorf("runtime: continuation tool %q has multiple compatible chain heads", call.Name)
		}
		executable, err := r.continuationPayload(spec, states[0])
		if err != nil {
			return fmt.Errorf("runtime: bind continuation tool %q payload: %w", call.Name, err)
		}
		call.ModelPayload = append(rawjson.Message(nil), call.Payload...)
		call.Payload = executable
	}
	return nil
}

// continuationStates returns every unconsumed successful page belonging to the
// dedicated continuation or its source query. Each successful continuation
// consumes the page whose next cursor it received, so completed ancestors are
// omitted while independent parallel invocations remain separate live heads.
func (r *Runtime) continuationStates(spec tools.ToolSpec, outputs []*planner.ToolOutput) ([]continuationState, error) {
	consumed := make(map[string]struct{})
	for _, output := range outputs {
		if output == nil || output.Failure != nil || output.Name != spec.Name {
			continue
		}
		cursor, err := continuationInputCursor(spec, output.Payload)
		if err != nil {
			return nil, fmt.Errorf(
				"runtime: continuation tool %q history: %w",
				spec.Name,
				err,
			)
		}
		if _, exists := consumed[cursor]; exists {
			return nil, fmt.Errorf(
				"runtime: continuation tool %q cursor was consumed more than once",
				spec.Name,
			)
		}
		consumed[cursor] = struct{}{}
	}

	var states []continuationState
	for _, output := range outputs {
		if output == nil || output.Failure != nil || !isContinuationOutput(spec, output.Name) {
			continue
		}
		if output.Bounds == nil || output.Bounds.NextCursor == nil || *output.Bounds.NextCursor == "" {
			continue
		}
		if _, wasConsumed := consumed[*output.Bounds.NextCursor]; wasConsumed {
			continue
		}
		states = append(states, continuationState{
			cursor:  *output.Bounds.NextCursor,
			payload: output.Payload,
		})
	}
	return states, nil
}

// continuationInputCursor reads the runtime-authored cursor from a canonical
// continuation payload so history can identify the exact predecessor page.
func continuationInputCursor(spec tools.ToolSpec, payload rawjson.Message) (string, error) {
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(payload, &fields); err != nil {
		return "", fmt.Errorf("decode canonical payload: %w", err)
	}
	raw, ok := fields[spec.Bounds.Paging.CursorField]
	if !ok {
		return "", fmt.Errorf("canonical payload is missing cursor field %q", spec.Bounds.Paging.CursorField)
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return "", fmt.Errorf("decode cursor field %q: %w", spec.Bounds.Paging.CursorField, err)
	}
	if cursor == "" {
		return "", fmt.Errorf("canonical payload cursor field %q is empty", spec.Bounds.Paging.CursorField)
	}
	return cursor, nil
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
