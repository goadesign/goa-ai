package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// plannerActivityInvocation is the shared prepared state for one planner
// activity execution.
type plannerActivityInvocation struct {
	reg         *AgentRegistration
	agentCtx    planner.PlannerContext
	events      *runtimePlannerEvents
	invocations *modelInvocationJournal
	messages    []*model.Message
	reminders   []reminder.Reminder
}

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
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()

	if ended, err := r.sessionEndedForPlanning(ctx, input); err != nil {
		return nil, err
	} else if ended {
		return &PlanActivityOutput{SessionEnded: true}, nil
	}
	act, err := r.preparePlannerActivity(ctx, input, nil, nil)
	if err != nil {
		return nil, err
	}
	planInput := &planner.PlanInput{
		Messages:   act.messages,
		RunContext: input.RunContext,
		Agent:      act.agentCtx,
		Events:     act.events,
		Reminders:  act.reminders,
	}
	result, err := r.planStart(ctx, act.reg, planInput)
	if err != nil {
		act.notePlannerRateLimit(ctx, err)
		return nil, err
	}
	if err := r.bindContinuationCursors(result, nil); err != nil {
		return nil, err
	}
	r.logger.Info(ctx, "PlanStartActivity returning PlanResult", "tool_calls", len(result.ToolCalls), "final_response", result.FinalResponse != nil, "await", result.Await != nil)
	return act.output(ctx, result)
}

// PlanResumeActivity executes the planner's PlanResume method.
//
// Advanced & generated integration
//   - Intended to be registered by generated code with the workflow engine.
//   - Normal applications should use AgentClient (Runtime.Client(...).Run/Start)
//     instead of invoking activities directly.
//
// This activity is registered with the workflow engine and invoked after tool
// execution to produce the next plan. The activity creates an agent context,
// loads canonical tool outputs from the run log, and delegates to the planner's
// PlanResume implementation.
func (r *Runtime) PlanResumeActivity(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()

	if ended, err := r.sessionEndedForPlanning(ctx, input); err != nil {
		return nil, err
	} else if ended {
		return &PlanActivityOutput{SessionEnded: true}, nil
	}
	toolOutputs, err := r.loadPlannerToolOutputs(ctx, input.RunID, input.ToolOutputs)
	if err != nil {
		return nil, err
	}
	recoveryOutputs, err := selectRecoveryOutputs(toolOutputs, input.RecoveryToolCallIDs)
	if err != nil {
		return nil, err
	}
	if err := validateRecoveryPolicy(recoveryOutputs, input.Policy); err != nil {
		return nil, err
	}
	recoveryReminders := r.recoveryReminders(dominantRecoveryOutputSet(recoveryOutputs))
	var continuationActions []continuationAction
	if input.Finalize == nil && !input.SynthesisOnly {
		continuationActions, err = r.availableContinuationActions(input.AgentID, toolOutputs)
		if err != nil {
			return nil, err
		}
	}
	act, err := r.preparePlannerActivity(
		ctx,
		input,
		continuationActions,
		replanUnavailableTools(recoveryOutputs),
	)
	if err != nil {
		return nil, err
	}
	act.reminders = append(recoveryReminders, act.reminders...)
	if len(recoveryOutputs) == 0 {
		result, automatic, err := r.automaticContinuationPlan(continuationActions)
		if err != nil {
			return nil, err
		}
		if automatic {
			return act.output(result)
		}
	}
	planInput := &planner.PlanResumeInput{
		Messages:      act.messages,
		RunContext:    input.RunContext,
		Agent:         act.agentCtx,
		Events:        act.events,
		ToolOutputs:   toolOutputs,
		SynthesisOnly: input.SynthesisOnly,
		Finalize:      input.Finalize,
		Reminders:     act.reminders,
	}
	result, err := r.planResume(ctx, act.reg, planInput)
	if err != nil {
		act.notePlannerRateLimit(ctx, err)
		return nil, err
	}
	if err := r.bindContinuationCursors(result, continuationActions); err != nil {
		return nil, err
	}
	if input.SynthesisOnly {
		if result != nil && len(result.ToolCalls) > 0 {
			return nil, errors.New("synthesis-only planner result contains tool calls")
		}
		if err := validateTerminalPlanResult(result); err != nil {
			return nil, fmt.Errorf("synthesis-only planner result: %w", err)
		}
	}
	output, err := act.output(ctx, result)
	if err != nil {
		return nil, err
	}
	if len(input.RecoveryToolCallIDs) > 0 {
		definitions := act.agentCtx.AdvertisedToolDefinitions()
		advertised := make([]tools.Ident, len(definitions))
		for i, definition := range definitions {
			advertised[i] = tools.Ident(definition.Name)
		}
		output.RecoveryCatalog = &RecoveryCatalog{Tools: advertised}
	}
	return output, nil
}

// validateRecoveryPolicy proves that correction restrictions and selected
// canonical failures describe the same turn. Replan recovery needs no policy
// restriction because its catalog is derived directly from the selected
// outputs.
func validateRecoveryPolicy(outputs []*planner.ToolOutput, policy *PolicyOverrides) error {
	correctTools := correctCallToolCounts(outputs)
	if len(correctTools) == 0 {
		return nil
	}
	if len(correctTools) != 1 {
		return errors.New("recovery activity contains multiple correction tools")
	}
	var correctionTool tools.Ident
	for tool := range correctTools {
		correctionTool = tool
	}
	if policy == nil || policy.RestrictToTool != correctionTool {
		return fmt.Errorf(
			"recovery activity for %q is missing its matching tool restriction",
			correctionTool,
		)
	}
	return nil
}

// preparePlannerActivity constructs all shared planner activity state before
// the specific PlanStart or PlanResume payload is built.
func (r *Runtime) preparePlannerActivity(
	ctx context.Context,
	input *PlanActivityInput,
	continuationActions []continuationAction,
	unavailableTools []tools.Ident,
) (*plannerActivityInvocation, error) {
	events := newPlannerEvents(r, input.AgentID, input.RunID, input.RunContext.SessionID, input.RunContext.TurnID)
	invocations := &modelInvocationJournal{}
	reg, agentCtx, err := r.plannerContext(
		ctx,
		input,
		events,
		invocations,
		continuationActions,
		unavailableTools,
	)
	if err != nil {
		return nil, err
	}
	var rems []reminder.Reminder
	if r.reminders != nil {
		rems = r.reminders.Snapshot(input.RunID)
	}
	messages, err := r.applyHistoryPolicy(ctx, reg, input.Messages, agentCtx.AdvertisedToolDefinitions())
	if err != nil {
		return nil, err
	}
	return &plannerActivityInvocation{
		reg:         reg,
		agentCtx:    agentCtx,
		events:      events,
		invocations: invocations,
		messages:    messages,
		reminders:   rems,
	}, nil
}

// output validates hook publication and exports the workflow-safe planner
// activity result.
func (a *plannerActivityInvocation) output(ctx context.Context, result *planner.PlanResult) (*PlanActivityOutput, error) {
	if err := validatePlannerResultPayloads(result); err != nil {
		return nil, err
	}
	transcript, err := a.invocations.exportModelInvocation(result)
	if err != nil {
		return nil, err
	}
	a.invocations.publishSelectedPresentation(ctx, a.events)
	if err := a.events.hookError(); err != nil {
		return nil, err
	}
	if len(transcript) == 0 {
		if err := validatePlannerAuthoredFinalResponse(result); err != nil {
			return nil, err
		}
	}
	return &PlanActivityOutput{
		Result:     result,
		Transcript: transcript,
		Usage:      a.invocations.exportUsage(),
	}, nil
}

// validatePlannerResultPayloads enforces canonical tool JSON before Temporal
// serializes planner activity output.
func validatePlannerResultPayloads(result *planner.PlanResult) error {
	if result == nil {
		return errors.New("planner returned a nil result")
	}
	if result.FinalResponse != nil {
		if result.FinalResponse.Message == nil {
			return errors.New("planner final response is missing its message")
		}
		if err := model.ValidateResponse(&model.Response{
			Content:    []model.Message{*result.FinalResponse.Message},
			StopReason: "planner_final",
		}); err != nil {
			return fmt.Errorf("planner final response: %w", err)
		}
	}
	for index, call := range result.ToolCalls {
		if err := validatePlannerToolPayload(call.Payload); err != nil {
			return fmt.Errorf("planner tool call %d payload: %w", index, err)
		}
		if call.ModelPayload != nil {
			if err := validatePlannerToolPayload(call.ModelPayload); err != nil {
				return fmt.Errorf("planner tool call %d model payload: %w", index, err)
			}
		}
	}
	if result.Await == nil {
		return nil
	}
	for itemIndex, item := range result.Await.Items {
		switch item.Kind {
		case planner.AwaitItemKindClarification:
			if item.Clarification == nil {
				return fmt.Errorf("planner await item %d clarification payload is missing", itemIndex)
			}
			if item.Clarification.ExampleJSON != nil {
				if err := validatePlannerToolPayload(item.Clarification.ExampleJSON); err != nil {
					return fmt.Errorf("planner await item %d clarification example: %w", itemIndex, err)
				}
			}
		case planner.AwaitItemKindToolClarification:
			if item.ToolClarification == nil {
				return fmt.Errorf("planner await item %d tool clarification payload is missing", itemIndex)
			}
			if item.ToolClarification.ToolName == "" {
				return fmt.Errorf("planner await item %d tool clarification name is missing", itemIndex)
			}
			if item.ToolClarification.ToolCallID == "" {
				return fmt.Errorf("planner await item %d tool clarification call ID is missing", itemIndex)
			}
			if err := validatePlannerToolPayload(item.ToolClarification.Payload); err != nil {
				return fmt.Errorf("planner await item %d tool clarification payload: %w", itemIndex, err)
			}
		case planner.AwaitItemKindQuestions:
			if item.Questions == nil {
				return fmt.Errorf("planner await item %d questions payload is missing", itemIndex)
			}
			if err := validatePlannerToolPayload(item.Questions.Payload); err != nil {
				return fmt.Errorf("planner await item %d questions payload: %w", itemIndex, err)
			}
		case planner.AwaitItemKindExternalTools:
			if item.ExternalTools == nil {
				return fmt.Errorf("planner await item %d external tools payload is missing", itemIndex)
			}
			for toolIndex, tool := range item.ExternalTools.Items {
				if err := validatePlannerToolPayload(tool.Payload); err != nil {
					return fmt.Errorf("planner await item %d external tool %d payload: %w", itemIndex, toolIndex, err)
				}
			}
		default:
			return fmt.Errorf("planner await item %d has unsupported kind %q", itemIndex, item.Kind)
		}
	}
	return nil
}

// validatePlannerAuthoredFinalResponse ensures planners express tool
// declarations through PlanResult.ToolCalls. Provider-owned final messages are
// validated against their captured canonical response by the invocation journal.
func validatePlannerAuthoredFinalResponse(result *planner.PlanResult) error {
	if result.FinalResponse == nil {
		return nil
	}
	for index, part := range result.FinalResponse.Message.Parts {
		if _, ok := part.(model.ToolUsePart); ok {
			return fmt.Errorf(
				"planner-authored final response part %d contains tool use; return it through ToolCalls",
				index,
			)
		}
	}
	return nil
}

// validatePlannerToolPayload verifies a required generated tool payload is a
// non-empty JSON object.
func validatePlannerToolPayload(payload rawjson.Message) error {
	data := bytes.TrimSpace(payload)
	if len(data) == 0 {
		return errors.New("payload is empty")
	}
	if !json.Valid(data) {
		return errors.New("payload is not valid JSON")
	}
	if data[0] != '{' {
		return errors.New("payload must be a JSON object")
	}
	return nil
}

// notePlannerRateLimit emits a structured planner note for provider
// rate-limiting errors before the activity returns the failure.
func (a *plannerActivityInvocation) notePlannerRateLimit(ctx context.Context, err error) {
	if !errors.Is(err, model.ErrRateLimited) {
		return
	}
	a.events.PlannerThought(
		ctx,
		"Model provider is rate-limiting this request. It is safe to retry after a short delay.",
		map[string]string{"code": "rate_limited"},
	)
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
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()

	if req == nil {
		return nil, errors.New("tool input is required")
	}
	if req.ToolName == "" {
		return nil, errors.New("tool name is required")
	}
	if err := validatePlannerToolPayload(req.Payload); err != nil {
		return nil, fmt.Errorf("tool payload is invalid: %w", err)
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

	// The generated payload codec owns the model-authored JSON boundary. Tool
	// execution receives the exact planner bytes; typed service adaptation happens
	// only after successful decoding.
	raw := append(rawjson.Message(nil), req.Payload...)

	// For non DecodeInExecutor toolsets, validate payloads eagerly using the
	// generated codecs so we can surface structured correction contracts. Executors
	// still receive the canonical JSON payload and may decode again as needed.
	if !reg.DecodeInExecutor {
		spec, ok := r.toolSpec(req.ToolName)
		if !ok {
			return nil, fmt.Errorf("tool %q has no registered ToolSpec", req.ToolName)
		}
		if _, decErr := r.unmarshalToolValue(ctx, req.ToolName, raw.RawMessage(), true); decErr != nil {
			return &ToolOutput{
				Failure: buildToolFailureFromPayloadError(decErr, raw, spec),
			}, nil
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
		Labels:           cloneLabels(req.Labels),
		TurnID:           req.TurnID,
		ParentToolCallID: req.ParentToolCallID,
		ToolCallID:       req.ToolCallID,
	}
	meta := toolCallMeta(call)
	start := time.Now()
	execResult, err := reg.Execute(ctx, &call)
	if err != nil {
		return nil, err
	}
	if execResult == nil {
		return nil, errors.New("tool execution returned nil execution result")
	}
	// Enrich or build telemetry via registration builder when available.
	if reg.TelemetryBuilder != nil {
		if tel := reg.TelemetryBuilder(ctx, meta, req.ToolName, start, time.Now(), nil); tel != nil && execResult.ToolResult != nil && execResult.ToolResult.Telemetry == nil {
			execResult.ToolResult.Telemetry = tel
		}
	}
	result, resultJSON, pause, err := r.materializeToolExecutionResult(ctx, call, execResult)
	if err != nil {
		return nil, err
	}
	out := &ToolOutput{
		Payload:    resultJSON,
		Bounds:     result.Bounds,
		ServerData: result.ServerData,
		Telemetry:  result.Telemetry,
	}
	if result.Failure != nil {
		out.Failure = result.Failure
	}
	if pause != nil {
		out.Pause = pause
	}
	return out, nil
}

// buildToolFailureFromPayloadError converts a model-authored payload failure
// into the canonical same-tool correction contract. Generated validation
// issues remain structured data; the runtime does not reinterpret them into a
// second handwritten validation or prompting language.
func buildToolFailureFromPayloadError(err error, input rawjson.Message, spec tools.ToolSpec) *planner.ToolFailure {
	var issuer interface {
		Issues() []*tools.FieldIssue
	}
	var issues []*tools.FieldIssue
	if errors.As(err, &issuer) {
		issues = issuer.Issues()
	}
	return &planner.ToolFailure{
		Kind:  planner.FailureInvalidCall,
		Error: planner.ToolErrorFromError(err),
		Recovery: planner.RecoveryDirective{
			Action:      planner.RecoveryCorrectCall,
			Issues:      issues,
			PriorInput:  append(rawjson.Message(nil), input...),
			ExampleJSON: append(rawjson.Message(nil), spec.Payload.ExampleJSON...),
		},
	}
}

// buildToolFailureFromAgentToolRequestError classifies agent-tool request
// failures raised before the child run starts. Model payload defects —
// structured validation failures and payload decode failures marked by
// agentToolPayloadError — produce a correction directive so the parent run
// feeds the failure back to the model. Runtime configuration errors (missing
// ToolSpec registrations, prompt rendering failures) return nil and stay
// terminal workflow errors.
func buildToolFailureFromAgentToolRequestError(err error, input rawjson.Message, spec tools.ToolSpec) *planner.ToolFailure {
	var issuer interface {
		Issues() []*tools.FieldIssue
	}
	if errors.As(err, &issuer) {
		return buildToolFailureFromPayloadError(err, input, spec)
	}
	var payloadErr *agentToolPayloadError
	if errors.As(err, &payloadErr) {
		return buildToolFailureFromPayloadError(payloadErr.cause, input, spec)
	}
	return nil
}

// planStart invokes the planner's PlanStart method with tracing.
// sessionEndedForPlanning is the turn-boundary session lifecycle gate: every
// planner activity (start and resume) refuses to plan when the run's durable
// session has been ended, mirroring startRunOn's refusal to start new runs.
// Before reporting the refusal it records CancellationReasonSessionEnded on
// the run (idempotently), so the terminal RunCompleted event carries the
// canonical provenance even when engine cancellation never delivered —
// CancelRun rolls its provisional reason back in exactly that case. One-shot
// runs (empty SessionID) intentionally bypass SessionStore and are never
// gated.
func (r *Runtime) sessionEndedForPlanning(ctx context.Context, input *PlanActivityInput) (bool, error) {
	sessionID := input.RunContext.SessionID
	if sessionID == "" {
		return false, nil
	}
	sess, err := r.SessionStore.LoadSession(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("load session %q for planning: %w", sessionID, err)
	}
	if sess.Status != session.StatusEnded {
		return false, nil
	}
	if _, _, err := r.recordRunCancellation(ctx, CancelRequest{
		RunID:  input.RunID,
		Reason: run.CancellationReasonSessionEnded,
	}); err != nil {
		return false, fmt.Errorf("record session-ended cancellation for run %q: %w", input.RunID, err)
	}
	return true, nil
}

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
func (r *Runtime) plannerContext(
	ctx context.Context,
	input *PlanActivityInput,
	events planner.PlannerEvents,
	invocations modelInvocationSink,
	continuationActions []continuationAction,
	unavailableTools []tools.Ident,
) (*AgentRegistration, planner.PlannerContext, error) {
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
	runPolicy := compileToolPolicy(input.Policy)
	agentCtx := newAgentContext(agentContextOptions{
		runtime:             r,
		agentID:             input.AgentID,
		runID:               input.RunID,
		memory:              reader,
		sessionID:           input.RunContext.SessionID,
		labels:              input.RunContext.Labels,
		policy:              runPolicy,
		turnID:              input.RunContext.TurnID,
		events:              events,
		invocations:         invocations,
		cache:               reg.Policy.Cache,
		continuationActions: continuationActions,
		unavailableTools:    unavailableTools,
	})
	return &reg, agentCtx, nil
}

// marshalToolValue encodes a tool result using the registered result codec and,
// for bounded tools, projects canonical bounds metadata into the public JSON
// contract emitted by the runtime.
func (r *Runtime) marshalToolValue(ctx context.Context, toolName tools.Ident, value any, bounds *agent.Bounds) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	spec, ok := r.toolSpec(toolName)
	if !ok {
		r.logger.Error(ctx, "no codec found for tool", "tool", toolName, "payload", false)
		return nil, fmt.Errorf("no codec found for tool %s", toolName)
	}
	projected, err := EncodeCanonicalToolResult(spec, value, bounds)
	if err != nil {
		r.logger.Warn(ctx, "tool result encode failed", "tool", toolName, "payload", false, "err", err)
		return nil, err
	}
	return json.RawMessage(projected), nil
}

// unmarshalToolValue decodes a tool value using the registered codec or standard JSON.
func (r *Runtime) unmarshalToolValue(ctx context.Context, toolName tools.Ident, raw json.RawMessage, payload bool) (any, error) {
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
