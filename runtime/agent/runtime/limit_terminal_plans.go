package runtime

// This file validates terminal tool calls supplied when a run starts and
// selects the matching call when the workflow reaches a configured limit.

import (
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// cloneLimitTerminalPlans copies terminal call payloads so caller mutation
// cannot change a submitted run.
func cloneLimitTerminalPlans(plans *LimitTerminalPlans) *LimitTerminalPlans {
	if plans == nil {
		return nil
	}
	return &LimitTerminalPlans{
		TimeBudget:  cloneLimitTerminalCall(plans.TimeBudget),
		ToolCallCap: cloneLimitTerminalCall(plans.ToolCallCap),
		RecoveryCap: cloneLimitTerminalCall(plans.RecoveryCap),
	}
}

// validateLimitTerminalPlans checks the complete set against the tools
// registered for this agent before the first planner call.
func (r *Runtime) validateLimitTerminalPlans(reg AgentRegistration, plans *LimitTerminalPlans) error {
	if plans == nil {
		return nil
	}
	for _, entry := range []struct {
		reason planner.TerminationReason
		call   LimitTerminalCall
	}{
		{reason: planner.TerminationReasonTimeBudget, call: plans.TimeBudget},
		{reason: planner.TerminationReasonToolCap, call: plans.ToolCallCap},
		{reason: planner.TerminationReasonRecoveryCap, call: plans.RecoveryCap},
	} {
		if err := r.validateLimitTerminalCall(reg, entry.call); err != nil {
			return fmt.Errorf("runtime: invalid %s terminal call: %w", entry.reason, err)
		}
	}
	return nil
}

// validateLimitTerminalCall requires canonical JSON for a terminal bookkeeping
// tool owned by the agent that will execute the run.
func (r *Runtime) validateLimitTerminalCall(reg AgentRegistration, call LimitTerminalCall) error {
	spec, ok := agentToolSpec(reg.Specs, call.Name)
	if !ok {
		return fmt.Errorf("tool %q is not registered for agent %q", call.Name, reg.ID)
	}
	if !spec.Bookkeeping || !spec.TerminalRun {
		return fmt.Errorf("tool %q is not a terminal bookkeeping tool", call.Name)
	}
	if spec.Confirmation != nil {
		return fmt.Errorf("tool %q requires confirmation", call.Name)
	}
	if r.toolConfirmation != nil {
		if _, ok := r.toolConfirmation.Confirm[call.Name]; ok {
			return fmt.Errorf("tool %q requires confirmation", call.Name)
		}
	}
	if err := validatePlannerToolPayload(call.Payload); err != nil {
		return fmt.Errorf("tool %q payload: %w", call.Name, err)
	}
	if _, err := spec.Payload.Codec.FromJSON(call.Payload); err != nil {
		return fmt.Errorf("tool %q payload: %w", call.Name, err)
	}
	return nil
}

// limitTerminalCall returns the call assigned to a configured runtime limit.
// Tool failures always use saved messages because their final response may
// depend on the failed result.
func limitTerminalCall(
	plans *LimitTerminalPlans,
	reason planner.TerminationReason,
) (LimitTerminalCall, bool, error) {
	switch reason {
	case planner.TerminationReasonTimeBudget:
		if plans == nil {
			return LimitTerminalCall{}, false, nil
		}
		return cloneLimitTerminalCall(plans.TimeBudget), true, nil
	case planner.TerminationReasonToolCap:
		if plans == nil {
			return LimitTerminalCall{}, false, nil
		}
		return cloneLimitTerminalCall(plans.ToolCallCap), true, nil
	case planner.TerminationReasonRecoveryCap:
		if plans == nil {
			return LimitTerminalCall{}, false, nil
		}
		return cloneLimitTerminalCall(plans.RecoveryCap), true, nil
	case planner.TerminationReasonToolFailure:
		return LimitTerminalCall{}, false, nil
	default:
		return LimitTerminalCall{}, false, fmt.Errorf("unsupported termination reason %q", reason)
	}
}

// finishLimitTerminalCall executes the fixed terminal call selected for one
// runtime limit through the normal terminal-tool path.
func (r *Runtime) finishLimitTerminalCall(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	allToolResults []*planner.ToolResult,
	allToolOutputs []*planner.ToolOutput,
	aggUsage model.TokenUsage,
	nextAttempt int,
	turnID string,
	call LimitTerminalCall,
	reason planner.TerminationReason,
	hardDeadline time.Time,
) (*RunOutput, error) {
	result := &PlanResult{
		ToolCalls: []ToolCall{
			limitTerminalToolRequest(input.RunID, turnID, nextAttempt, call),
		},
	}
	out, err := r.finishFinalizationTerminalToolCalls(
		wfCtx,
		reg,
		input,
		base,
		result,
		nil,
		allToolResults,
		allToolOutputs,
		aggUsage,
		nextAttempt,
		turnID,
		reason,
		hardDeadline,
	)
	if err != nil {
		return nil, fmt.Errorf("%s finalization: %w", reason, err)
	}
	return out, nil
}

// limitTerminalToolRequest builds the fixed completion call selected when a run
// reaches a limit. This function creates the call, so it also assigns its ID.
// Current run labels are added after policy evaluation.
func limitTerminalToolRequest(
	runID, turnID string,
	attempt int,
	call LimitTerminalCall,
) ToolCall {
	return ToolCall{
		Name:       call.Name,
		ToolCallID: generateDeterministicToolCallID(runID, turnID, attempt, call.Name, 0),
		Payload:    rawjson.Message(append([]byte(nil), call.Payload...)),
	}
}

// cloneLimitTerminalCall copies one canonical JSON payload.
func cloneLimitTerminalCall(call LimitTerminalCall) LimitTerminalCall {
	return LimitTerminalCall{
		Name:    call.Name,
		Payload: rawjson.Message(append([]byte(nil), call.Payload...)),
	}
}

// agentToolSpec finds a tool in the registration that owns a run.
func agentToolSpec(specs []tools.ToolSpec, name tools.Ident) (tools.ToolSpec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return tools.ToolSpec{}, false
}
