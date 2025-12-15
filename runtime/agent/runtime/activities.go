package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/reminder"
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
func (r *Runtime) PlanStartActivity(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
	events := newPlannerEvents(r, input.AgentID, input.RunID, input.RunContext.SessionID)
	reg, agentCtx, err := r.plannerContext(ctx, input, events)
	if err != nil {
		return nil, err
	}
	var rems []reminder.Reminder
	if r.reminders != nil {
		rems = r.reminders.Snapshot(input.RunID)
	}
	msgs := r.applyHistoryPolicy(ctx, reg, input.Messages)
	planInput := &planner.PlanInput{
		Messages:   msgs,
		RunContext: input.RunContext,
		Agent:      agentCtx,
		Events:     events,
		Reminders:  rems,
	}
	result, err := r.planStart(ctx, reg, planInput)
	if err != nil {
		if errors.Is(err, model.ErrRateLimited) {
			events.PlannerThought(
				ctx,
				"Model provider is rate-limiting this request. It is safe to retry after a short delay.",
				map[string]string{"code": "rate_limited"},
			)
		}
		return nil, err
	}
	r.logger.Info(ctx, "PlanStartActivity returning PlanResult", "tool_calls", len(result.ToolCalls), "final_response", result.FinalResponse != nil, "await", result.Await != nil)
	return &PlanActivityOutput{
		Result:     result,
		Transcript: events.exportTranscript(),
		Usage:      events.exportUsage(),
	}, nil
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
func (r *Runtime) PlanResumeActivity(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
	events := newPlannerEvents(r, input.AgentID, input.RunID, input.RunContext.SessionID)
	reg, agentCtx, err := r.plannerContext(ctx, input, events)
	if err != nil {
		return nil, err
	}
	var rems []reminder.Reminder
	if r.reminders != nil {
		rems = r.reminders.Snapshot(input.RunID)
	}
	msgs := r.applyHistoryPolicy(ctx, reg, input.Messages)
	planInput := &planner.PlanResumeInput{
		Messages:    msgs,
		RunContext:  input.RunContext,
		Agent:       agentCtx,
		Events:      events,
		ToolResults: input.ToolResults,
		Finalize:    input.Finalize,
		Reminders:   rems,
	}
	result, err := r.planResume(ctx, reg, planInput)
	if err != nil {
		if errors.Is(err, model.ErrRateLimited) {
			events.PlannerThought(
				ctx,
				"Model provider is rate-limiting this request. It is safe to retry after a short delay.",
				map[string]string{"code": "rate_limited"},
			)
		}
		return nil, err
	}
	return &PlanActivityOutput{
		Result:     result,
		Transcript: events.exportTranscript(),
		Usage:      events.exportUsage(),
	}, nil
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
func (r *Runtime) ExecuteToolActivity(ctx context.Context, req *ToolInput) (*ToolOutput, error) {
	if req == nil {
		return nil, errors.New("tool input is required")
	}
	if req.ToolName == "" {
		return nil, errors.New("tool name is required")
	}
	// Forbid agent-as-tool execution from activities. Agent-tools must execute inside
	// the workflow thread so child workflows can be started legally.
	if spec, ok := r.toolSpec(req.ToolName); ok && spec.IsAgentTool {
		// When the provider agent attempts to execute its own agent-as-tool via
		// ExecuteToolActivity, surface a precise error so callers fix the planner
		// tool list instead of routing through activities.
		if string(req.AgentID) == spec.AgentID {
			return nil, fmt.Errorf(
				"agent %q attempted to execute its own agent-as-tool %q via ExecuteToolActivity; "+
					"agent-as-tools must run inline in workflow context and must not be exposed to the provider's planner tool list",
				req.AgentID,
				req.ToolName,
			)
		}
		return nil, fmt.Errorf("agent-as-tool %q must run in workflow context", req.ToolName)
	}
	sName := req.ToolsetName
	if sName == "" {
		spec, ok := r.toolSpec(req.ToolName)
		if !ok {
			return nil, fmt.Errorf("unknown tool %q", req.ToolName)
		}
		sName = spec.Toolset
	}
	r.mu.RLock()
	reg, ok := r.toolsets[sName]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("toolset %q is not registered", sName)
	}

	// Apply optional payload adapter before decoding. Payloads are canonical
	// JSON (json.RawMessage) along the planner/runtime boundary; adapters may
	// normalize them before validation or execution.
	raw := req.Payload
	meta := ToolCallMeta{
		RunID:            req.RunID,
		SessionID:        req.SessionID,
		TurnID:           req.TurnID,
		ToolCallID:       req.ToolCallID,
		ParentToolCallID: req.ParentToolCallID,
	}
	if reg.PayloadAdapter != nil && len(raw) > 0 {
		if adapted, err := reg.PayloadAdapter(ctx, meta, req.ToolName, raw); err == nil && len(adapted) > 0 {
			raw = adapted
		} else if err != nil {
			return &ToolOutput{Error: fmt.Sprintf("payload adapter failed: %v", err)}, nil
		}
	}

	// For non DecodeInExecutor toolsets, validate payloads eagerly using the
	// generated codecs so we can surface structured retry hints. Executors
	// still receive the canonical JSON payload and may decode again as needed.
	if !reg.DecodeInExecutor && len(raw) > 0 {
		if _, decErr := r.unmarshalToolValue(ctx, req.ToolName, raw, true); decErr != nil {
			// Build structured retry hints using generated ValidationError when present.
			if fields, question, reason, ok := buildRetryHintFromValidation(decErr, req.ToolName); ok {
				return &ToolOutput{
					Error: decErr.Error(),
					RetryHint: &planner.RetryHint{
						Reason:             reason,
						Tool:               req.ToolName,
						MissingFields:      fields,
						ClarifyingQuestion: question,
					},
				}, nil
			}
			// Not a validation error: attempt to build a decode-oriented retry hint
			// (for example, malformed or wrong-shape JSON) so planners can guide the
			// caller toward a schema-compliant payload.
			var specPtr *tools.ToolSpec
			if spec, ok := r.toolSpec(req.ToolName); ok {
				cp := spec
				specPtr = &cp
			}
			if hint := buildRetryHintFromDecodeError(decErr, req.ToolName, specPtr); hint != nil {
				return &ToolOutput{
					Error:     decErr.Error(),
					RetryHint: hint,
				}, nil
			}
			// No structured hint available: return error only.
			return &ToolOutput{Error: decErr.Error()}, nil
		}
	}

	// Populate run context fields so tool implementations can access metadata.
	// Agent-tools use these to construct nested contexts; regular tools use
	// them for logging/telemetry. Payload is always canonical JSON.
	call := planner.ToolRequest{
		Name:             req.ToolName,
		Payload:          raw,
		RunID:            req.RunID,
		AgentID:          req.AgentID,
		SessionID:        req.SessionID,
		TurnID:           req.TurnID,
		ParentToolCallID: req.ParentToolCallID,
		ToolCallID:       req.ToolCallID,
	}
	start := time.Now()
	result, err := reg.Execute(ctx, &call)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("tool execution returned nil result")
	}
	// Enrich or build telemetry via registration builder when available.
	if reg.TelemetryBuilder != nil {
		if tel := reg.TelemetryBuilder(ctx, meta, req.ToolName, start, time.Now(), nil); tel != nil && result.Telemetry == nil {
			result.Telemetry = tel
		}
	}
	enc, encErr := r.marshalToolValue(ctx, req.ToolName, result.Result, false)
	if encErr != nil {
		// Tool result is not valid for its declared schema. This is a contract violation.
		return nil, fmt.Errorf("tool result encode %s: %w", req.ToolName, encErr)
	}
	// Apply optional result adapter after encoding.
	if reg.ResultAdapter != nil && len(enc) > 0 {
		if adapted, err := reg.ResultAdapter(ctx, meta, req.ToolName, enc); err == nil && len(adapted) > 0 {
			enc = adapted
		}
	}
	out := &ToolOutput{
		Payload:   enc,
		Artifacts: result.Artifacts,
		Telemetry: result.Telemetry,
	}
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

// buildRetryHintFromDecodeError examines JSON decode errors that occur before tool
// execution and attempts to build a structured RetryHint. It treats malformed or
// wrong-shape JSON as conceptually equivalent to missing required fields so that
// planners and UIs can guide callers toward a schema-compliant payload.
//
// When a payload example is available in the tool specs, the hint attaches it as
// ExampleInput so consumers can display a concrete, valid payload.
func buildRetryHintFromDecodeError(err error, toolName tools.Ident, spec *tools.ToolSpec) *planner.RetryHint {
	var (
		typeErr   *json.UnmarshalTypeError
		syntaxErr *json.SyntaxError
		fields    []string
		reason    planner.RetryReason
		question  string
	)

	switch {
	case errors.As(err, &typeErr):
		field := typeErr.Field
		if field == "" {
			field = "$payload"
		}
		fields = []string{field}
		reason = planner.RetryReasonMissingFields
		question = fmt.Sprintf(
			"I could not decode the %s tool input. The %s field has the wrong JSON shape. Please resend this tool call with a JSON object that matches the expected schema.",
			toolName,
			field,
		)
	case errors.As(err, &syntaxErr):
		fields = []string{"$payload"}
		reason = planner.RetryReasonMissingFields
		question = fmt.Sprintf(
			"I could not parse the %s tool input as JSON (syntax error near byte offset %d). Please resend this tool call with a valid JSON object payload.",
			toolName,
			syntaxErr.Offset,
		)
	default:
		// Not a JSON decode error we can interpret.
		return nil
	}

	var example map[string]any
	if spec != nil && len(spec.Payload.ExampleJSON) > 0 {
		if err := json.Unmarshal(spec.Payload.ExampleJSON, &example); err == nil && len(example) == 0 {
			// Leave example nil when decoding produced an empty map to avoid
			// surfacing a meaningless "{}" example.
			example = nil
		}
	}

	return &planner.RetryHint{
		Reason:             reason,
		Tool:               toolName,
		MissingFields:      fields,
		ExampleInput:       example,
		ClarifyingQuestion: question,
	}
}

// planStart invokes the planner's PlanStart method with tracing.
func (r *Runtime) planStart(ctx context.Context, reg *AgentRegistration, input *planner.PlanInput) (*planner.PlanResult, error) {
	if reg.Planner == nil {
		return nil, errors.New("planner not configured")
	}
	if input == nil {
		return nil, errors.New("plan input is required")
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
func (r *Runtime) planResume(ctx context.Context, reg *AgentRegistration, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	if reg.Planner == nil {
		return nil, errors.New("planner not configured")
	}
	if input == nil {
		return nil, errors.New("plan resume input is required")
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
func (r *Runtime) plannerContext(ctx context.Context, input *PlanActivityInput, events planner.PlannerEvents) (*AgentRegistration, planner.PlannerContext, error) {
	if input.AgentID == "" {
		return nil, nil, errors.New("agent id is required")
	}
	reg, ok := r.agentByID(input.AgentID)
	if !ok {
		return nil, nil, fmt.Errorf("agent %q is not registered", input.AgentID)
	}
	reader, err := r.memoryReader(ctx, string(input.AgentID), input.RunID)
	if err != nil {
		return nil, nil, err
	}
	agentCtx := newAgentContext(agentContextOptions{
		runtime: r,
		agentID: input.AgentID,
		runID:   input.RunID,
		memory:  reader,
		turnID:  input.RunContext.TurnID,
		events:  events,
		cache:   reg.Policy.Cache,
	})
	return &reg, agentCtx, nil
}

// marshalToolValue encodes a tool value using the registered codec or standard JSON.
func (r *Runtime) marshalToolValue(ctx context.Context, toolName tools.Ident, value any, payload bool) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	// Preserve raw JSON payloads/results without re-encoding.
	// Rationale:
	//   - The runtime is tool‑agnostic and does not import generated payload
	//     types. Callers may legitimately pass pre‑encoded JSON (json.RawMessage
	//     or a JSON []byte) to defer decoding to an executor (e.g., agent‑as‑tool
	//     DecodeInExecutor=true) or to avoid lossy/expensive round‑trips.
	//   - Re‑encoding already‑encoded JSON wastes CPU and can change formatting.
	//   - For non‑JSON []byte or typed values, we fall through to the tool codec
	//     (or json.Marshal as a last resort) to produce the canonical encoding.
	switch v := value.(type) {
	case json.RawMessage:
		if len(v) == 0 {
			return nil, nil
		}
		// Return a defensive copy.
		return append(json.RawMessage(nil), v...), nil
	case []byte:
		if len(v) == 0 {
			return nil, nil
		}
		if json.Valid(v) {
			// Treat as already-encoded JSON; return a defensive copy.
			return json.RawMessage(append([]byte(nil), v...)), nil
		}
		// Fall through to codec/json.Marshal for non-JSON []byte values.
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
		v, err := codec.FromJSON(raw)
		if err != nil {
			// Decode failures indicate a contract mismatch between the generated
			// codecs and the concrete payload/result JSON. Log a warning so
			// callers that fall back to raw JSON (e.g. for observability) still
			// surface a precise error for debugging.
			r.logger.Warn(ctx, "tool codec decode failed", "tool", toolName, "payload", payload, "err", err, "json", string(raw))
			return nil, err
		}
		return v, nil
	}
	r.logger.Error(ctx, "no codec found for tool", "tool", toolName, "payload", payload)
	return nil, fmt.Errorf("no codec found for tool %s", toolName)
}

// toolCodec retrieves the JSON codec for a tool's payload or result.
func (r *Runtime) toolCodec(toolName tools.Ident, payload bool) (*tools.JSONCodec[any], bool) {
	spec, ok := r.toolSpec(toolName)
	if !ok {
		return nil, false
	}
	if payload {
		return &spec.Payload.Codec, true
	}
	return &spec.Result.Codec, true
}
