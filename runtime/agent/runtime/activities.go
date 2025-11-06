package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// PlanStartActivity executes the planner's PlanStart method.
//
// Advanced & generated integration
//   - Intended to be registered by generated code with the workflow engine.
//   - Normal applications should use AgentClient (Runtime.Client(...).Run/Start)
//     instead of invoking activities directly.
//
// This activity is registered with the workflow engine and invoked at the
// beginning of a run to produce the initial plan. The activity creates an
// agent context with memory access and delegates to the planner's PlanStart
// implementation.
func (r *Runtime) PlanStartActivity(ctx context.Context, input PlanActivityInput) (PlanActivityOutput, error) {
	reg, agentCtx, err := r.plannerContext(ctx, input)
	if err != nil {
		return PlanActivityOutput{}, err
	}
	planInput := planner.PlanInput{
		Messages:   input.Messages,
		RunContext: input.RunContext,
		Agent:      agentCtx,
		Events:     newPlannerEvents(r, input.AgentID, input.RunID),
	}
	result, err := r.planStart(ctx, reg, planInput)
	if err != nil {
		return PlanActivityOutput{}, err
	}
	r.logger.Info(ctx, "PlanStartActivity returning PlanResult", "tool_calls", len(result.ToolCalls), "final_response", result.FinalResponse != nil, "await", result.Await != nil)
	return PlanActivityOutput{Result: result}, nil
}

// PlanResumeActivity executes the planner's PlanResume method.
//
// Advanced & generated integration
//   - Intended to be registered by generated code with the workflow engine.
//   - Normal applications should use AgentClient (Runtime.Client(...).Run/Start)
//     instead of invoking activities directly.
//
// This activity is registered with the workflow engine and invoked after tool
// execution to produce the next plan. The activity creates an agent context
// with memory access and delegates to the planner's PlanResume implementation.
func (r *Runtime) PlanResumeActivity(ctx context.Context, input PlanActivityInput) (PlanActivityOutput, error) {
	reg, agentCtx, err := r.plannerContext(ctx, input)
	if err != nil {
		return PlanActivityOutput{}, err
	}
	planInput := planner.PlanResumeInput{
		Messages:    input.Messages,
		RunContext:  input.RunContext,
		Agent:       agentCtx,
		Events:      newPlannerEvents(r, input.AgentID, input.RunID),
		ToolResults: input.ToolResults,
	}
	result, err := r.planResume(ctx, reg, planInput)
	if err != nil {
		return PlanActivityOutput{}, err
	}
	return PlanActivityOutput{Result: result}, nil
}

// ExecuteToolActivity runs a tool invocation as a workflow activity.
//
// Advanced & generated integration
//   - Intended to be registered by generated code with the workflow engine.
//   - Normal applications should use AgentClient (Runtime.Client(...).Run/Start)
//     rather than invoking activities directly.
//
// It decodes the tool payload, runs the registered tool implementation, and
// encodes the result using the tool‑specific codec. Returns an error if the
// toolset is not registered or if encoding/decoding fails.
func (r *Runtime) ExecuteToolActivity(ctx context.Context, req ToolInput) (ToolOutput, error) {
	if req.ToolName == "" {
		return ToolOutput{}, errors.New("tool name is required")
	}
	sName := req.ToolsetName
	if sName == "" {
		sName = toolsetIdentifier(req.ToolName)
	}
	reg, ok := r.toolsets[sName]
	if !ok {
		return ToolOutput{}, fmt.Errorf("toolset %q is not registered", sName)
	}

	decoded, decErr := r.unmarshalToolValue(ctx, req.ToolName, req.Payload, true)
	if decErr != nil {
		// Build structured retry hints using generated ValidationError when present.
		if fields, question, reason, ok := buildRetryHintFromValidation(decErr, req.ToolName); ok {
			return ToolOutput{
				Error: decErr.Error(),
				RetryHint: &planner.RetryHint{
					Reason:             reason,
					Tool:               req.ToolName,
					MissingFields:      fields,
					ClarifyingQuestion: question,
				},
			}, nil
		}
		// Not a validation error: no retry hint.
		return ToolOutput{Error: decErr.Error()}, nil
	}

	// Populate run context fields so tool implementations can access metadata.
	// Agent-tools use these to construct nested contexts; regular tools use them for logging/telemetry.
	call := planner.ToolRequest{
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
	raw, encErr := r.marshalToolValue(ctx, req.ToolName, result.Result, false)
	if encErr != nil {
		// Result could not be encoded. Forward best-effort JSON and no retry hint.
		var best json.RawMessage
		if b, e := json.Marshal(result.Result); e == nil {
			best = json.RawMessage(b)
		}
		return ToolOutput{Error: encErr.Error(), Payload: best}, nil
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

// buildRetryHintFromValidation attempts to extract structured validation issues from
// a generated ValidationError (emitted by tool codecs) and build a precise retry hint.
// It returns (fields, question, reason, true) when successful; otherwise ok is false.
func buildRetryHintFromValidation(err error, toolName tools.Ident) ([]string, string, planner.RetryReason, bool) {
	// Match generated ValidationError via method set (no concrete type import).
	var ip interface {
		Issues() []*tools.FieldIssue
		Descriptions() map[string]string
	}
	if !errors.As(err, &ip) {
		return nil, "", planner.RetryReasonInvalidArguments, false
	}
	issues := ip.Issues()
	if len(issues) == 0 {
		return nil, "", planner.RetryReasonInvalidArguments, false
	}
	descs := ip.Descriptions()
	missing := make([]string, 0)
	fields := make([]string, 0, len(issues))
	for _, is := range issues {
		if is.Field == "" {
			continue
		}
		fields = append(fields, is.Field)
		if is.Constraint == "missing_field" {
			missing = append(missing, is.Field)
		}
	}
	// Build a concise, description-enriched question for up to three fields.
	var question string
	if n := len(fields); n > 0 {
		max := n
		if max > 3 {
			max = 3
		}
		parts := make([]string, 0, max)
		for i := 0; i < max; i++ {
			f := fields[i]
			label := f
			if d, ok := descs[f]; ok && d != "" {
				label = f + " (" + d + ")"
			}
			// If enum allowed values exist, append hint.
			for _, is := range issues {
				if is.Field == f && len(is.Allowed) > 0 {
					label = label + " — one of: " + strings.Join(is.Allowed, ", ")
					break
				}
			}
			parts = append(parts, label)
		}
		list := strings.Join(parts, ", ")
		if toolName != "" {
			question = "I need additional information to run " + string(toolName) + ". Please provide: " + list + "."
		} else {
			question = "I need additional information. Please provide: " + list + "."
		}
	}
	reason := planner.RetryReasonInvalidArguments
	if len(missing) > 0 {
		reason = planner.RetryReasonMissingFields
	}
	return missing, question, reason, true
}

// planStart invokes the planner's PlanStart method with tracing.
func (r *Runtime) planStart(ctx context.Context, reg AgentRegistration, input planner.PlanInput) (*planner.PlanResult, error) {
	if reg.Planner == nil {
		return nil, errors.New("planner not configured")
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
func (r *Runtime) planResume(ctx context.Context, reg AgentRegistration, input planner.PlanResumeInput) (*planner.PlanResult, error) {
	if reg.Planner == nil {
		return nil, errors.New("planner not configured")
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
func (r *Runtime) plannerContext(ctx context.Context, input PlanActivityInput) (AgentRegistration, planner.PlannerContext, error) {
	if input.AgentID == "" {
		return AgentRegistration{}, nil, errors.New("agent id is required")
	}
	reg, ok := r.agentByID(input.AgentID)
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
	ctx context.Context, toolName tools.Ident, value any, payload bool,
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
func (r *Runtime) unmarshalToolValue(ctx context.Context, toolName tools.Ident, raw json.RawMessage, payload bool) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	codec, ok := r.toolCodec(toolName, payload)
	if ok && codec.FromJSON != nil {
		return codec.FromJSON(raw)
	}
	r.logger.Error(ctx, "no codec found for tool", "tool", toolName, "payload", payload)
	return nil, fmt.Errorf("no codec found for tool %s", toolName)
}

// toolCodec retrieves the JSON codec for a tool's payload or result.
func (r *Runtime) toolCodec(toolName tools.Ident, payload bool) (tools.JSONCodec[any], bool) {
	spec, ok := r.toolSpec(toolName)
	if !ok {
		return tools.JSONCodec[any]{}, false
	}
	if payload {
		return spec.Payload.Codec, true
	}
	return spec.Result.Codec, true
}
