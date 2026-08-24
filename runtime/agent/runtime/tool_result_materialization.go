package runtime

// tool_result_materialization.go owns the runtime's typed tool-result
// enrichment path.
//
// Contract:
// - All successful tool results, whether executed directly or provided in a
//   continuation response, are materialized before canonical JSON
//   encoding and hook publication.
// - Toolset-owned server-only sidecars must be attached here so streamed
//   `tool_result` events and durable run logs observe the same canonical result
//   shape and planner-visible metadata, and planner resume hydration can
//   reconstruct them exactly.
// - External callers provide raw result JSON only; they never construct the
//   runtime's internal `api.ToolEvent` envelope.

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolserverdata"
)

// materializeToolResult prepares one result inside the workflow, replaces any
// executor-authored correction evidence with data from the retained model call
// and registered specification, and returns canonical result JSON.
func (r *Runtime) materializeToolResult(ctx context.Context, call ToolCall, result *planner.ToolResult) (rawjson.Message, error) {
	spec, ok := r.toolSpec(call.Name)
	if !ok {
		return nil, fmt.Errorf("tool %q is not registered", call.Name)
	}
	resultJSON, err := r.materializeToolResultData(ctx, spec, call, result)
	if err != nil {
		return nil, err
	}
	if err := canonicalizeAndValidateWorkflowToolResult(spec, call, result); err != nil {
		return nil, err
	}
	return resultJSON, nil
}

// materializeActivityToolResult prepares one activity result without creating
// model-facing correction evidence. The workflow still owns the complete call
// and adds that evidence after the activity returns.
func (r *Runtime) materializeActivityToolResult(ctx context.Context, call ToolCall, result *planner.ToolResult) (rawjson.Message, error) {
	spec, ok := r.toolSpec(call.Name)
	if !ok {
		return nil, fmt.Errorf("tool %q is not registered", call.Name)
	}
	resultJSON, err := r.materializeToolResultData(ctx, spec, call, result)
	if err != nil {
		return nil, err
	}
	return resultJSON, nil
}

// materializeToolResultData takes ownership of executor failures before
// validating them, then applies toolset-owned result conversion and server-data
// validation. Contract-invalid successes become malformed-result failures.
func (r *Runtime) materializeToolResultData(
	ctx context.Context,
	spec tools.ToolSpec,
	call ToolCall,
	result *planner.ToolResult,
) (rawjson.Message, error) {
	if result == nil {
		return nil, fmt.Errorf("nil tool result for %q (%s)", call.Name, call.ToolCallID)
	}
	if result.Name == "" {
		result.Name = call.Name
	}
	if err := sanitizeAndValidateExecutorToolResult(spec, call, result); err != nil {
		return nil, err
	}
	if result.Failure != nil {
		return nil, nil
	}
	if result.Result == nil && spec.Result.Codec.ToJSON != nil {
		setMalformedToolResult(result, call, errors.New("registered tool result is required"))
		return nil, nil
	}
	if err := r.applyResultMaterializer(ctx, spec, call, result); err != nil {
		setMalformedToolResult(result, call, err)
		return nil, nil
	}
	serverData, err := toolserverdata.Apply(spec.CanonicalizeServerData, result.ServerData)
	if err != nil {
		setMalformedToolResult(result, call, fmt.Errorf("validate %s server data: %w", call.Name, err))
		return nil, nil
	}
	result.ServerData = serverData
	if err := validateToolResultContract(spec, call, result); err != nil {
		setMalformedToolResult(result, call, err)
		return nil, nil
	}
	encoded, err := r.marshalToolValue(ctx, call.Name, result.Result, result.Bounds)
	if err != nil {
		setMalformedToolResult(result, call, fmt.Errorf("encode %s tool result: %w", call.Name, err))
		return nil, nil
	}
	return rawjson.Message(encoded), nil
}

// materializeToolExecutionResult validates the runtime-owned execution wrapper,
// materializes the durable tool result, and returns the current-batch user
// question separately from planner-visible history.
func (r *Runtime) materializeToolExecutionResult(
	ctx context.Context,
	call ToolCall,
	exec *ToolExecutionResult,
) (*planner.ToolResult, rawjson.Message, *ToolClarification, error) {
	if exec == nil {
		return nil, nil, nil, fmt.Errorf("tool %q returned nil execution result", call.Name)
	}
	if exec.ToolResult == nil {
		return nil, nil, nil, fmt.Errorf("tool %q returned nil tool result", call.Name)
	}
	exec.ToolResult.Name = call.Name
	exec.ToolResult.ToolCallID = call.ToolCallID
	resultJSON, err := r.materializeToolResult(ctx, call, exec.ToolResult)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := validateToolClarificationContract(call, exec.ToolResult, exec.Clarification); err != nil {
		return nil, nil, nil, err
	}
	return exec.ToolResult, resultJSON, exec.Clarification, nil
}

// materializeActivityToolExecutionResult prepares the result returned by an
// activity executor while leaving correct-call transcript evidence for the
// workflow that retained the original model call.
func (r *Runtime) materializeActivityToolExecutionResult(
	ctx context.Context,
	call ToolCall,
	exec *ToolExecutionResult,
) (*planner.ToolResult, rawjson.Message, *ToolClarification, error) {
	if exec == nil {
		return nil, nil, nil, fmt.Errorf("tool %q returned nil execution result", call.Name)
	}
	if exec.ToolResult == nil {
		return nil, nil, nil, fmt.Errorf("tool %q returned nil tool result", call.Name)
	}
	exec.ToolResult.Name = call.Name
	exec.ToolResult.ToolCallID = call.ToolCallID
	resultJSON, err := r.materializeActivityToolResult(ctx, call, exec.ToolResult)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := validateToolClarificationContract(call, exec.ToolResult, exec.Clarification); err != nil {
		return nil, nil, nil, err
	}
	return exec.ToolResult, resultJSON, exec.Clarification, nil
}

// applyResultMaterializer invokes the toolset-owned typed result materializer
// when the toolset registered one.
func (r *Runtime) applyResultMaterializer(ctx context.Context, spec tools.ToolSpec, call ToolCall, result *planner.ToolResult) error {
	r.mu.RLock()
	reg, ok := r.toolsets[spec.Toolset]
	r.mu.RUnlock()
	if !ok || reg.ResultMaterializer == nil {
		return nil
	}
	if err := reg.ResultMaterializer(ctx, ToolCallMetaFromCall(call), &call, result); err != nil {
		return fmt.Errorf("materialize %s tool result: %w", call.Name, err)
	}
	return nil
}

// decodeProvidedToolRecords decodes externally supplied raw tool results in the
// canonical awaited call order and materializes their runtime-owned sidecars.
func (r *Runtime) decodeProvidedToolRecords(ctx context.Context, allowed []ToolCall, provided map[string]*api.ProvidedToolResult) ([]stepToolRecord, error) {
	if len(allowed) == 0 {
		return nil, nil
	}
	records := make([]stepToolRecord, 0, len(allowed))
	for _, call := range allowed {
		item := provided[call.ToolCallID]
		if item == nil {
			return nil, fmt.Errorf("await: missing tool result for tool_call_id %q", call.ToolCallID)
		}
		if item.Name != call.Name {
			return nil, fmt.Errorf("await: result tool %q does not match awaited tool %q", item.Name, call.Name)
		}
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			return nil, fmt.Errorf("await: tool %q is not registered", call.Name)
		}
		result, resultJSON, err := r.decodeProvidedToolResult(ctx, spec, call, item)
		if err != nil {
			return nil, err
		}
		records = append(records, stepToolRecord{
			call:       call,
			result:     result,
			resultJSON: resultJSON,
		})
	}
	return records, nil
}

// decodeProvidedToolResult converts one externally supplied raw result into the
// typed planner result used by the runtime.
func (r *Runtime) decodeProvidedToolResult(ctx context.Context, spec tools.ToolSpec, call ToolCall, item *api.ProvidedToolResult) (*planner.ToolResult, rawjson.Message, error) {
	if item == nil {
		return nil, nil, fmt.Errorf("await: nil tool result")
	}
	if (item.Success == nil) == (item.Failure == nil) {
		return nil, nil, fmt.Errorf("await: tool result for %s must contain exactly one success or failure", call.Name)
	}
	var bounds *agent.Bounds
	var decoded any
	var err error
	if item.Success != nil {
		bounds = agent.CloneBounds(item.Success.Bounds)
		if spec.Result.Codec.FromJSON == nil {
			if len(bytes.TrimSpace(item.Success.Result)) > 0 {
				err = errors.New("tool has no result contract but success contains result JSON")
			}
		} else {
			decoded, err = spec.Result.Codec.FromJSON(item.Success.Result.RawMessage())
		}
	}
	result := &planner.ToolResult{
		Name:       call.Name,
		Result:     decoded,
		ServerData: nil,
		Bounds:     bounds,
		Failure:    canonicalProvidedToolFailure(item.Failure),
		ToolCallID: call.ToolCallID,
	}
	if err != nil {
		setMalformedToolResult(result, call, fmt.Errorf("decode provided result for %s: %w", call.Name, err))
	}
	resultJSON, err := r.materializeToolResult(ctx, call, result)
	if err != nil {
		return nil, nil, fmt.Errorf("await: %w", err)
	}
	return result, resultJSON, nil
}

// setMalformedToolResult replaces a nominal success that violated its
// registered result contract with the terminal failure observed by the model.
func setMalformedToolResult(result *planner.ToolResult, call ToolCall, cause error) {
	result.Name = call.Name
	result.Result = nil
	result.Bounds = nil
	result.ResultBytes = 0
	result.ResultOmitted = false
	result.ResultOmittedReason = ""
	result.ServerData = nil
	result.Failure = &planner.ToolFailure{
		Kind: planner.FailureMalformedResult,
		Error: planner.NewToolErrorWithCause(
			fmt.Sprintf("tool %s returned a malformed result", call.Name),
			cause,
		),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryFinish,
		},
	}
}

// canonicalProvidedToolFailure converts external failure facts into the
// planner's failure type. Runtime-owned correction context is attached by
// canonicalizeAndValidateWorkflowToolResult with the registered call and tool
// specification.
func canonicalProvidedToolFailure(in *api.ProvidedToolFailure) *planner.ToolFailure {
	if in == nil {
		return nil
	}
	failure := &planner.ToolFailure{
		Kind: in.Kind,
		Error: &planner.ToolError{
			Message: in.Message,
		},
		Recovery: planner.RecoveryDirective{
			Action: in.Action,
			Issues: tools.CloneFieldIssues(in.Issues),
		},
	}
	return failure
}

// ToolCallMetaFromCall copies runtime-owned invocation context from a
// canonical tool call. Generated executor adapters use this function so
// service, MCP, and custom executors all receive the same immutable labels and
// correlation identifiers.
func ToolCallMetaFromCall(call ToolCall) ToolCallMeta {
	return ToolCallMeta{
		RunID:            call.RunID,
		SessionID:        call.SessionID,
		TurnID:           call.TurnID,
		ToolCallID:       call.ToolCallID,
		ParentToolCallID: call.ParentToolCallID,
		Labels:           cloneLabels(call.Labels),
	}
}
