package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/agents/runtime/planner"
	"goa.design/goa-ai/agents/runtime/telemetry"
	"goa.design/goa-ai/agents/runtime/tools"
)

// PlanStartActivity executes the planner's PlanStart method. This activity is
// registered with the workflow engine and invoked at the beginning of a run to
// produce the initial plan. The activity creates an agent context with memory
// access and delegates to the planner's PlanStart implementation.
func (r *Runtime) PlanStartActivity(ctx context.Context, input PlanActivityInput) (PlanActivityOutput, error) {
	reg, agentCtx, err := r.plannerContext(ctx, input)
	if err != nil {
		return PlanActivityOutput{}, err
	}
	planInput := planner.PlanInput{
		Messages:   input.Messages,
		RunContext: input.RunContext,
		Agent:      agentCtx,
	}
	result, err := r.planStart(ctx, reg, planInput)
	if err != nil {
		return PlanActivityOutput{}, err
	}
	return PlanActivityOutput{Result: result}, nil
}

// PlanResumeActivity executes the planner's PlanResume method. This activity is
// registered with the workflow engine and invoked after tool execution to produce
// the next plan. The activity creates an agent context with memory access and
// delegates to the planner's PlanResume implementation.
func (r *Runtime) PlanResumeActivity(ctx context.Context, input PlanActivityInput) (PlanActivityOutput, error) {
	reg, agentCtx, err := r.plannerContext(ctx, input)
	if err != nil {
		return PlanActivityOutput{}, err
	}
	planInput := planner.PlanResumeInput{
		Messages:    input.Messages,
		RunContext:  input.RunContext,
		Agent:       agentCtx,
		ToolResults: input.ToolResults,
	}
	result, err := r.planResume(ctx, reg, planInput)
	if err != nil {
		return PlanActivityOutput{}, err
	}
	return PlanActivityOutput{Result: result}, nil
}

// ExecuteToolActivity is invoked by generated activities (or inline during tests) to
// decode a tool payload, run the registered tool implementation, and encode the
// result using the tool-specific codec. Returns an error if the toolset is not
// registered or if encoding/decoding fails.
func (r *Runtime) ExecuteToolActivity(ctx context.Context, req ToolInput) (ToolOutput, error) {
	if req.ToolName == "" {
		return ToolOutput{}, errors.New("tool name is required")
	}
	sName := toolsetIdentifier(req.ToolName)
	reg, ok := r.toolsets[sName]
	if !ok {
		return ToolOutput{}, fmt.Errorf("toolset %q is not registered", sName)
	}

	decoded, err := r.unmarshalToolValue(ctx, req.ToolName, req.Payload, true)
	if err != nil {
		return ToolOutput{}, err
	}

	// Populate run context fields so tool implementations can access metadata.
	// Agent-tools use these to construct nested contexts; regular tools use them for logging/telemetry.
	call := planner.ToolCallRequest{
		Name:             req.ToolName,
		Payload:          decoded,
		RunID:            req.RunID,
		SessionID:        req.SessionID,
		TurnID:           req.TurnID,
		ParentToolCallID: req.ParentToolCallID,
		ToolCallID:       req.ToolCallID,
	}
	result, err := reg.Execute(ctx, call)
	if err != nil {
		return ToolOutput{}, err
	}
	raw, err := r.marshalToolValue(ctx, req.ToolName, result.Payload, false)
	if err != nil {
		return ToolOutput{}, err
	}
	out := ToolOutput{Payload: raw, Telemetry: result.Telemetry}
	if result.Error != nil {
		out.Error = result.Error.Error()
	}
	if result.RetryHint != nil {
		out.RetryHint = result.RetryHint
	}
	return out, nil
}

// planStart invokes the planner's PlanStart method with tracing.
func (r *Runtime) planStart(
	ctx context.Context, reg AgentRegistration, input planner.PlanInput,
) (planner.PlanResult, error) {
	if reg.Planner == nil {
		return planner.PlanResult{}, errors.New("planner not configured")
	}
	tracer := r.tracer
	if tracer == nil {
		tracer = telemetry.NoopTracer{}
	}
	ctx, span := tracer.Start(ctx, "planner.plan_start")
	defer span.End()
	return reg.Planner.PlanStart(ctx, input)
}

// planResume invokes the planner's PlanResume method with tracing.
func (r *Runtime) planResume(
	ctx context.Context, reg AgentRegistration, input planner.PlanResumeInput,
) (planner.PlanResult, error) {
	if reg.Planner == nil {
		return planner.PlanResult{}, errors.New("planner not configured")
	}
	tracer := r.tracer
	if tracer == nil {
		tracer = telemetry.NoopTracer{}
	}
	ctx, span := tracer.Start(ctx, "planner.plan_resume")
	defer span.End()
	return reg.Planner.PlanResume(ctx, input)
}

// plannerContext constructs the agent registration and context needed for planner execution.
func (r *Runtime) plannerContext(
	ctx context.Context, input PlanActivityInput,
) (AgentRegistration, planner.AgentContext, error) {
	if input.AgentID == "" {
		return AgentRegistration{}, nil, errors.New("agent id is required")
	}
	reg, ok := r.Agent(input.AgentID)
	if !ok {
		return AgentRegistration{}, nil, fmt.Errorf("agent %q is not registered", input.AgentID)
	}
	reader := r.memoryReader(ctx, input.AgentID, input.RunID)
	agentCtx := newAgentContext(agentContextOptions{
		runtime: r,
		agentID: input.AgentID,
		runID:   input.RunID,
		memory:  reader,
		turnID:  input.RunContext.TurnID,
	})
	return reg, agentCtx, nil
}

// marshalToolValue encodes a tool value using the registered codec or standard JSON.
func (r *Runtime) marshalToolValue(
	ctx context.Context, toolName string, value any, payload bool,
) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	codec, ok := r.toolCodec(toolName, payload)
	if !ok || codec.ToJSON == nil {
		data, err := json.Marshal(value)
		if err != nil {
			r.logger.Warn(ctx, "tool fallback encode failed", "tool", toolName, "payload", payload, "err", err)
			return nil, err
		}
		return json.RawMessage(data), nil
	}
	data, err := codec.ToJSON(value)
	if err != nil {
		r.logger.Warn(ctx, "tool codec encode failed", "tool", toolName, "payload", payload, "err", err)
		return nil, err
	}
	return json.RawMessage(data), nil
}

// unmarshalToolValue decodes a tool value using the registered codec or standard JSON.
func (r *Runtime) unmarshalToolValue(
	ctx context.Context, toolName string, raw json.RawMessage, payload bool,
) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	codec, ok := r.toolCodec(toolName, payload)
	if ok && codec.FromJSON != nil {
		return codec.FromJSON(raw)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		r.logger.Warn(ctx, "tool fallback decode failed", "tool", toolName, "payload", payload, "err", err)
		return nil, err
	}
	return v, nil
}

// toolCodec retrieves the JSON codec for a tool's payload or result.
func (r *Runtime) toolCodec(toolName string, payload bool) (tools.JSONCodec[any], bool) {
	spec, ok := r.toolSpec(toolName)
	if !ok {
		return tools.JSONCodec[any]{}, false
	}
	if payload {
		return spec.Payload.Codec, true
	}
	return spec.Result.Codec, true
}
