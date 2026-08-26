package runtime

// This file implements the workflow activities that call planners, execute
// tools, and validate their values before they cross the workflow boundary.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
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
	runtime            *Runtime
	reg                *AgentRegistration
	agentCtx           planner.PlannerContext
	events             *runtimePlannerEvents
	invocations        *modelInvocationJournal
	messages           []*model.Message
	reminders          []reminder.Reminder
	runContext         run.Context
	publicationBatchID string
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
	var continuationActions []continuationAction
	if input.Finalize == nil && !input.SynthesisOnly {
		historicalOutputs, err := r.loadHistoricalContinuationOutputs(ctx, input)
		if err != nil {
			return nil, err
		}
		continuationActions, err = r.availableContinuationActions(input.AgentID, historicalOutputs)
		if err != nil {
			return nil, err
		}
	}
	act, err := r.preparePlannerActivity(ctx, input, continuationActions, nil)
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
	err = act.planningError(err)
	if err != nil {
		return act.outputContractFailure(ctx, err)
	}
	if err := validatePlannerActivityResult(r, result, input.RunContext.Tool, false); err != nil {
		return act.outputContractFailure(ctx, err)
	}
	if err := validatePlannerAdvertisedTools(result, act.agentCtx.AdvertisedToolDefinitions()); err != nil {
		return act.outputContractFailure(ctx, planner.NewOutputContractError(err))
	}
	if err := validatePlannerResultPayloadCodecs(ctx, r, result, continuationActions); err != nil {
		return act.outputContractFailure(ctx, planner.NewOutputContractError(err))
	}
	output, err := act.output(ctx, r, result, false, continuationActions)
	if err != nil {
		return act.outputContractFailure(ctx, err)
	}
	r.logger.Info(ctx, "PlanStartActivity returning PlanResult", "tool_calls", len(result.ToolCalls), "final_response", result.FinalResponse != nil, "await", result.Await != nil)
	return output, nil
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

	if err := validatePlanResumeRecoveryInput(input); err != nil {
		return nil, err
	}
	synthesisOnly := input.SynthesisOnly || input.ModelOutputRecovery != nil
	if ended, err := r.sessionEndedForPlanning(ctx, input); err != nil {
		return nil, err
	} else if ended {
		return &PlanActivityOutput{SessionEnded: true}, nil
	}
	toolOutputs, err := r.loadPlannerToolOutputs(ctx, input.ToolOutputs)
	if err != nil {
		return nil, err
	}
	recoveryOutputs, err := selectRecoveryOutputs(toolOutputs, input.RecoveryToolCallIDs)
	if err != nil {
		return nil, err
	}
	correctCallSpecs, err := r.correctCallSpecs(recoveryOutputs)
	if err != nil {
		return nil, err
	}
	exactCorrectCall := len(correctCallSpecs) > 0
	recoveryReminders := r.recoveryReminders(recoveryOutputs)
	var continuationActions []continuationAction
	if !exactCorrectCall && input.Finalize == nil && !synthesisOnly {
		continuationActions, err = r.availableContinuationActions(input.AgentID, toolOutputs)
		if err != nil {
			return nil, err
		}
	}
	var unavailableTools []tools.Ident
	if !exactCorrectCall {
		unavailableTools = r.recoveryUnavailableTools(input.AgentID, recoveryOutputs, input.Finalize != nil)
	}
	if synthesisOnly {
		specs := r.ToolSpecsForAgent(input.AgentID)
		unavailableTools = make([]tools.Ident, len(specs))
		for index, spec := range specs {
			unavailableTools[index] = spec.Name
		}
	}
	var act *plannerActivityInvocation
	if exactCorrectCall {
		act, err = r.preparePlannerActivityWithSpecs(
			ctx,
			input,
			continuationActions,
			unavailableTools,
			correctCallSpecs,
		)
	} else {
		act, err = r.preparePlannerActivity(
			ctx,
			input,
			continuationActions,
			unavailableTools,
		)
	}
	if err != nil {
		return nil, err
	}
	if exactCorrectCall {
		definitions := act.agentCtx.AdvertisedToolDefinitions()
		expected := correctCallCatalog(recoveryOutputs)
		if advertised := toolDefinitionNames(definitions); !slices.Equal(advertised, expected) {
			return nil, fmt.Errorf(
				"correct-call catalog %v resolved to advertised tools %v",
				expected,
				advertised,
			)
		}
	}
	act.reminders = append(recoveryReminders, act.reminders...)
	if input.ModelOutputRecovery != nil {
		act.reminders = append([]reminder.Reminder{{
			ID: "model_output_recovery",
			Text: "Your previous final answer was rejected.\n" +
				input.ModelOutputRecovery.Correction +
				"\nProduce a replacement final answer now. Do not mention this reminder to the user.",
			Priority: reminder.TierSafety,
			Attachment: reminder.Attachment{
				Kind: reminder.AttachmentUserTurn,
			},
		}}, act.reminders...)
	}
	if input.ModelInvocationRecovery != nil {
		act.reminders = append([]reminder.Reminder{{
			ID:       "model_invocation_recovery",
			Text:     modelInvocationRecoveryReminder(input.ModelInvocationRecovery),
			Priority: reminder.TierSafety,
			Attachment: reminder.Attachment{
				Kind: reminder.AttachmentUserTurn,
			},
		}}, act.reminders...)
	}
	if len(recoveryOutputs) == 0 &&
		input.ModelOutputRecovery == nil &&
		input.ModelInvocationRecovery == nil {
		result, automatic := r.automaticContinuationPlan(input.RunContext, continuationActions)
		if automatic {
			return act.runtimeOutput(ctx, result)
		}
	}
	planInput := &planner.PlanResumeInput{
		Messages:      act.messages,
		RunContext:    input.RunContext,
		Agent:         act.agentCtx,
		Events:        act.events,
		ToolOutputs:   toolOutputs,
		SynthesisOnly: synthesisOnly,
		Finalize:      input.Finalize,
		Reminders:     act.reminders,
	}
	result, err := r.planResume(ctx, act.reg, planInput)
	err = act.planningError(err)
	if err != nil {
		return act.outputContractFailure(ctx, err)
	}
	if err := validatePlannerActivityResult(r, result, input.RunContext.Tool, synthesisOnly); err != nil {
		return act.outputContractFailure(ctx, err)
	}
	if err := validatePlannerAdvertisedTools(result, act.agentCtx.AdvertisedToolDefinitions()); err != nil {
		return act.outputContractFailure(ctx, planner.NewOutputContractError(err))
	}
	if err := validatePlannerResultPayloadCodecs(ctx, r, result, continuationActions); err != nil {
		return act.outputContractFailure(ctx, planner.NewOutputContractError(err))
	}
	output, err := act.output(ctx, r, result, synthesisOnly, continuationActions)
	if err != nil {
		return act.outputContractFailure(ctx, err)
	}
	if len(input.RecoveryToolCallIDs) > 0 {
		definitions := act.agentCtx.AdvertisedToolDefinitions()
		output.RecoveryCatalog = &RecoveryCatalog{Tools: toolDefinitionNames(definitions)}
	}
	if budgetErr := checkPlanActivityOutputBudget(output); budgetErr != nil {
		return act.outputContractFailure(ctx, planner.NewOutputContractError(budgetErr))
	}
	return output, nil
}

// correctCallSpecs resolves each typed correct-call failure through the
// toolset registration that owns both its generated contract and executable
// path. It runs before planner setup, so removing that complete registration
// revokes recovery without starting a model invocation.
func (r *Runtime) correctCallSpecs(outputs []*planner.ToolOutput) ([]tools.ToolSpec, error) {
	names := correctCallCatalog(outputs)
	if len(names) == 0 {
		return nil, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	specs := make([]tools.ToolSpec, 0, len(names))
	for _, name := range names {
		globalSpec, registered := r.toolSpecs[name]
		if !registered {
			return nil, fmt.Errorf("correct-call recovery references unregistered tool %q", name)
		}
		toolsetName, registered := r.toolsetNames[name]
		if !registered {
			return nil, fmt.Errorf(
				"correct-call recovery tool %q has no executable toolset registration",
				name,
			)
		}
		registration, registered := r.toolsets[toolsetName]
		if !registered || registration.Execute == nil {
			return nil, fmt.Errorf(
				"correct-call recovery tool %q has no executable toolset registration",
				name,
			)
		}
		spec, registered := toolsetSpec(registration, name)
		if !registered {
			return nil, fmt.Errorf(
				"correct-call recovery toolset %q no longer admits tool %q",
				registration.Name,
				name,
			)
		}
		if registration.Name != toolsetName ||
			!equivalentToolSpec(globalSpec, spec) {
			return nil, fmt.Errorf(
				"correct-call recovery tool %q has mismatched global and executable registrations",
				name,
			)
		}
		if err := validateAgentToolRegistration(registration); err != nil {
			return nil, fmt.Errorf(
				"correct-call recovery tool %q has invalid executable registration: %w",
				name,
				err,
			)
		}
		specs = append(specs, cloneToolSpec(spec))
	}
	return specs, nil
}

// toolsetSpec returns the generated contract that one executable toolset
// registration currently admits for the requested tool name.
func toolsetSpec(registration ToolsetRegistration, name tools.Ident) (tools.ToolSpec, bool) {
	for _, spec := range registration.Specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return tools.ToolSpec{}, false
}

// toolDefinitionNames returns model-facing tool names in advertised order.
func toolDefinitionNames(definitions []*model.ToolDefinition) []tools.Ident {
	names := make([]tools.Ident, len(definitions))
	for i, definition := range definitions {
		names[i] = tools.Ident(definition.Name)
	}
	return names
}

// validatePlanResumeRecoveryInput requires one honest resume mode. Finalization
// may retain failed tool IDs as evidence, while either model recovery variant
// cannot combine with another resume directive.
func validatePlanResumeRecoveryInput(input *PlanActivityInput) error {
	if input == nil {
		return errors.New("plan resume input is required")
	}
	if input.Finalize != nil && input.SynthesisOnly {
		return errors.New("finalization cannot combine with synthesis-only planning")
	}
	if input.SynthesisOnly && len(input.RecoveryToolCallIDs) > 0 {
		return errors.New("synthesis-only planning cannot combine with tool recovery")
	}
	if input.ModelOutputRecovery != nil && input.ModelInvocationRecovery != nil {
		return errors.New("model-output and model-invocation recovery cannot be combined")
	}
	if input.ModelOutputRecovery == nil && input.ModelInvocationRecovery == nil {
		return nil
	}
	if input.ModelInvocationRecovery != nil {
		if err := validateModelInvocationRecovery(input.ModelInvocationRecovery); err != nil {
			return err
		}
		if input.SynthesisOnly {
			return errors.New("model-invocation recovery cannot combine with synthesis-only planning")
		}
		if len(input.RecoveryToolCallIDs) > 0 {
			return errors.New("model-invocation recovery cannot combine with tool recovery")
		}
		if input.Finalize != nil {
			return errors.New("model-invocation recovery cannot combine with finalization")
		}
		return nil
	}
	if strings.TrimSpace(input.ModelOutputRecovery.Correction) == "" {
		return errors.New("model-output correction requires non-blank guidance")
	}
	if len(input.ModelOutputRecovery.Correction) > outputcontract.MaxCorrectionBytes {
		return errors.New("model-output correction exceeds workflow boundary limit")
	}
	if input.SynthesisOnly {
		return errors.New("model-output recovery implies synthesis-only planning")
	}
	if len(input.RecoveryToolCallIDs) > 0 {
		return errors.New("model-output correction cannot combine with tool recovery")
	}
	if input.Finalize != nil {
		return errors.New("model-output correction cannot combine with finalization")
	}
	return nil
}

// modelInvocationRecoveryReminder turns one validated invocation recovery fact
// into the exact instruction prepended to the replacement planner call. The
// rejected name is quoted as data; no response arguments, call identifier, or
// copied tool catalog reaches the planner.
func modelInvocationRecoveryReminder(recovery *ModelInvocationRecovery) string {
	if recovery.UnadvertisedToolName != "" {
		return fmt.Sprintf(
			"Your previous tool call used the unavailable name %q.\n"+
				"Choose the needed tool from the tools available now, copy its name exactly, "+
				"and return a replacement tool call. Do not mention this reminder to the user.",
			recovery.UnadvertisedToolName,
		)
	}
	return "Your previous tool call was rejected before it could run.\n" +
		recovery.Correction +
		"\nReturn a replacement tool call now. Do not mention this reminder to the user."
}

// validatePlannerAdvertisedTools requires every model-selected tool name to
// come from the exact catalog shown for this planner activity.
func validatePlannerAdvertisedTools(result *planner.PlanResult, definitions []*model.ToolDefinition) error {
	if result == nil {
		return errors.New("planner returned a nil result")
	}
	allowed := make(map[tools.Ident]struct{}, len(definitions))
	for _, definition := range definitions {
		allowed[tools.Ident(definition.Name)] = struct{}{}
	}
	for _, call := range result.ToolCalls {
		if _, ok := allowed[call.Name]; !ok {
			return fmt.Errorf("planner called tool %q outside the advertised catalog", call.Name)
		}
	}
	if result.Await != nil {
		for _, call := range awaitToolRequests(result.Await.Items) {
			if _, ok := allowed[call.Name]; !ok {
				return fmt.Errorf("planner called tool %q outside the advertised catalog", call.Name)
			}
		}
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
	return r.preparePlannerActivityWithSpecs(
		ctx,
		input,
		continuationActions,
		unavailableTools,
		nil,
	)
}

// preparePlannerActivityWithSpecs constructs planner activity state with an
// explicit tool catalog for one correction turn.
func (r *Runtime) preparePlannerActivityWithSpecs(
	ctx context.Context,
	input *PlanActivityInput,
	continuationActions []continuationAction,
	unavailableTools []tools.Ident,
	advertisedSpecs []tools.ToolSpec,
) (*plannerActivityInvocation, error) {
	events := newPlannerEvents(input.AgentID, input.RunID, input.RunContext.SessionID)
	publicationBatchID := uuid.NewString()
	invocations := &modelInvocationJournal{
		runtime:        r,
		runID:          input.RunID,
		sessionID:      input.RunContext.SessionID,
		presentationID: publicationBatchID,
	}
	if err := invocations.startPresentation(ctx); err != nil {
		return nil, fmt.Errorf("start provisional model presentation: %w", err)
	}
	reg, agentCtx, err := r.plannerContext(
		ctx,
		input,
		events,
		invocations,
		continuationActions,
		unavailableTools,
		advertisedSpecs,
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
		runtime:            r,
		reg:                reg,
		agentCtx:           agentCtx,
		events:             events,
		invocations:        invocations,
		messages:           messages,
		reminders:          rems,
		runContext:         input.RunContext,
		publicationBatchID: publicationBatchID,
	}, nil
}

// output rejects planner values that the workflow cannot execute, then exports
// the accepted value and the exact model response that produced it. Hook
// failures remain ordinary runtime errors because they are not planner output.
func (a *plannerActivityInvocation) output(
	ctx context.Context,
	r *Runtime,
	result *planner.PlanResult,
	synthesisOnly bool,
	continuationActions []continuationAction,
) (*PlanActivityOutput, error) {
	if err := validatePlannerActivityResult(r, result, a.runContext.Tool, synthesisOnly); err != nil {
		return nil, err
	}
	transcript, err := a.invocations.exportModelInvocation(result)
	if err != nil {
		var outputErr *planner.OutputContractError
		if errors.As(err, &outputErr) {
			return nil, err
		}
		return nil, planner.NewOutputContractError(err)
	}
	if len(transcript) == 0 {
		if err := validatePlannerAuthoredFinalResponse(result); err != nil {
			return nil, planner.NewOutputContractError(err)
		}
	}
	modelCalls, err := a.invocations.selectedCompiledModelCalls(result)
	if err != nil {
		return nil, planner.NewOutputContractError(err)
	}
	toolCalls, err := r.compilePlannerToolCallsForRun(a.runContext, result.ToolCalls, continuationActions, modelCalls)
	if err != nil {
		return nil, err
	}
	runtimeResult := &PlanResult{
		ToolCalls:            toolCalls,
		SynthesizeAfterTools: result.SynthesizeAfterTools,
		FinalResponse:        result.FinalResponse,
		FinalToolResult:      result.FinalToolResult,
		Await:                result.Await,
		ExpectedChildren:     result.ExpectedChildren,
		Notes:                result.Notes,
	}
	return a.acceptedOutput(ctx, runtimeResult, transcript)
}

// runtimeOutput exports a runtime-authored execution result that did not call a
// planner, such as an automatic continuation of an empty bounded page.
func (a *plannerActivityInvocation) runtimeOutput(ctx context.Context, result *PlanResult) (*PlanActivityOutput, error) {
	return a.acceptedOutput(ctx, result, nil)
}

// acceptedOutput attaches planner events and usage to one validated result
// before it crosses the activity/workflow boundary.
func (a *plannerActivityInvocation) acceptedOutput(
	ctx context.Context,
	result *PlanResult,
	transcript []*model.Message,
) (*PlanActivityOutput, error) {
	a.invocations.publishUsage(ctx, a.events)
	output := &PlanActivityOutput{
		PublicationBatchID: a.publicationBatchID,
		Result:             result,
		Transcript:         transcript,
		Usage:              a.invocations.exportUsage(),
	}
	budget := &planActivityOutputBudget{}
	if budgetErr := budget.add(output); budgetErr != nil {
		return nil, planner.NewOutputContractError(budgetErr)
	}
	events, err := a.events.acceptedRecords(budget)
	if err != nil {
		var budgetErr *planActivityOutputBudgetError
		if errors.As(err, &budgetErr) {
			return nil, planner.NewOutputContractError(budgetErr)
		}
		return nil, err
	}
	output.PlannerEvents = events
	if budgetErr := checkPlanActivityOutputBudget(output); budgetErr != nil {
		return nil, planner.NewOutputContractError(budgetErr)
	}
	return output, nil
}

// outputContractFailure returns rejected model evidence and usage as a
// successful activity value. Generated input issues and unadvertised tool names
// may instead produce an invocation-recovery result; the workflow publishes
// usage before scheduling the replacement planner activity or raising a
// terminal error.
func (a *plannerActivityInvocation) outputContractFailure(
	ctx context.Context,
	err error,
) (*PlanActivityOutput, error) {
	if discardErr := a.invocations.discardPresentations(ctx); discardErr != nil {
		return nil, rejectedPresentationFailure(err, discardErr)
	}
	var outputErr *planner.OutputContractError
	if !errors.As(err, &outputErr) {
		return nil, err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, errors.Join(err, contextErr)
	}
	staged := a.invocations.hasStagedModelOutput()
	if staged && !a.invocations.stagedModelCallsClean() {
		return nil, errors.Join(err, a.invocations.outcomeErrors())
	}
	if a.invocations.hasRecoverableModelOutput() &&
		!a.invocations.commitModelInvocationRecovery(err) {
		return nil, errors.Join(err, a.invocations.outcomeErrors())
	}
	usage := a.invocations.exportUsage()
	invocationRecovery := a.invocations.recoverableModelInvocationRecovery()
	var failure *OutputContractFailure
	if invocationRecovery == nil {
		var metadataErr error
		failure, metadataErr = a.outputContractFailureMetadata(outputErr)
		if metadataErr != nil {
			failure = terminalPlannerOutputContractFailure(metadataErr)
		}
	}
	var originalBudgetErr *planActivityOutputBudgetError
	if errors.As(err, &originalBudgetErr) {
		return boundedPlanActivityOutputFailure(a.publicationBatchID, usage, failure, originalBudgetErr), nil
	}
	failureEvents := newPlannerEvents(
		a.events.agentID,
		a.events.runID,
		a.events.sessionID,
	)
	a.invocations.publishUsage(ctx, failureEvents)
	output := &PlanActivityOutput{
		PublicationBatchID: a.publicationBatchID,
		Usage:              usage,
	}
	if invocationRecovery != nil {
		output.ModelInvocationRecovery = invocationRecovery
	} else {
		output.OutputContractFailure = failure
	}
	budget := &planActivityOutputBudget{}
	if budgetErr := budget.add(output); budgetErr != nil {
		return boundedPlanActivityOutputFailure(a.publicationBatchID, usage, failure, budgetErr), nil
	}
	events, recordErr := failureEvents.acceptedRecords(budget)
	if recordErr != nil {
		var budgetErr *planActivityOutputBudgetError
		if errors.As(recordErr, &budgetErr) {
			return boundedPlanActivityOutputFailure(a.publicationBatchID, usage, failure, budgetErr), nil
		}
		return nil, recordErr
	}
	output.PlannerEvents = events
	if budgetErr := checkPlanActivityOutputBudget(output); budgetErr != nil {
		return boundedPlanActivityOutputFailure(a.publicationBatchID, usage, failure, budgetErr), nil
	}
	return output, nil
}

// rejectedPresentationFailure preserves the model rejection before any failure
// raised while discarding its provisional output. A single failure remains the
// original error chain; two failures are returned in execution order.
func rejectedPresentationFailure(modelErr, discardErr error) error {
	if discardErr == nil {
		return modelErr
	}
	cleanupErr := fmt.Errorf("discard rejected model presentation: %w", discardErr)
	if modelErr == nil {
		return cleanupErr
	}
	return errors.Join(modelErr, cleanupErr)
}

// terminalPlannerOutputContractFailure records a deterministic planner
// contract violation that cannot be corrected by rerunning the activity. It
// carries no model evidence or correction because the rejected message did not
// belong to a completed model invocation in this activity.
func terminalPlannerOutputContractFailure(err error) *OutputContractFailure {
	reasonSHA256, reasonSize := errorevidence.Fingerprint(err)
	return &OutputContractFailure{
		Origin:       planner.OutputContractOriginPlanner,
		ReasonSHA256: reasonSHA256,
		ReasonSize:   int64(reasonSize),
	}
}

// outputContractFailureMetadata keeps only a private-cause fingerprint,
// rejected-response evidence, and optional replacement guidance. It never
// copies validation-cause text into the activity result.
func (a *plannerActivityInvocation) outputContractFailureMetadata(
	outputErr *planner.OutputContractError,
) (*OutputContractFailure, error) {
	reasonSHA256, reasonSize := errorevidence.Fingerprint(outputContractFailureReason(outputErr))
	responseEvidence := a.invocations.rejectedModelResponseEvidence()
	if outputErr.Correction() != "" {
		var err error
		responseEvidence, err = a.invocations.recoverableModelResponseEvidence(outputErr.ModelMessage())
		if err != nil {
			return nil, err
		}
	}
	origin := outputErr.Origin()
	if origin == "" && responseEvidence.Present {
		origin = planner.OutputContractOriginModel
	}
	if origin == "" {
		origin = planner.OutputContractOriginPlanner
	}
	return &OutputContractFailure{
		Origin:                          origin,
		ReasonSHA256:                    reasonSHA256,
		ReasonSize:                      int64(reasonSize),
		ModelResponsePresent:            responseEvidence.Present,
		ModelResponseFingerprintVersion: responseEvidence.Version,
		ModelResponseSHA256:             responseEvidence.SHA256,
		ModelResponseSize:               responseEvidence.Size,
		Correction:                      outputErr.Correction(),
	}, nil
}

// outputContractFailureReason selects the private validation cause only when
// every leaf is the same exact OutputValidationError. Planner and mixed
// failures keep their complete existing cause as the fingerprint input.
func outputContractFailureReason(outputErr *planner.OutputContractError) error {
	cause := outputErr.Unwrap()
	if validationErr, ok := exactModelOutputValidation(cause); ok {
		return errors.Unwrap(validationErr)
	}
	return cause
}

// boundedPlanActivityOutputFailure replaces oversized auxiliary output with
// one small budget-rejection reason. It retains numeric usage and, for an
// earlier model rejection, that rejection's origin and response fingerprint.
func boundedPlanActivityOutputFailure(
	publicationBatchID string,
	usage model.TokenUsage,
	failure *OutputContractFailure,
	budgetErr error,
) *PlanActivityOutput {
	if failure == nil {
		failure = terminalPlannerOutputContractFailure(budgetErr)
	}
	boundedFailure := *failure
	boundedFailure.ReasonSHA256, boundedFailure.ReasonSize = fingerprintBytes(
		[]byte("planner activity output rejected before Temporal encoding: " + budgetErr.Error()),
	)
	return &PlanActivityOutput{
		PublicationBatchID:    publicationBatchID,
		Usage:                 usage,
		OutputContractFailure: &boundedFailure,
	}
}

// planningError makes the first malformed model response fail the activity,
// even when planner code catches that error and returns another result.
func (a *plannerActivityInvocation) planningError(err error) error {
	if sealErr := a.invocations.seal(); sealErr != nil {
		return sealErr
	}
	if outputErr := a.invocations.outputContractError(); outputErr != nil {
		return errors.Join(outputErr, err)
	}
	return err
}

// validatePlannerActivityResult rejects planner values that cannot be
// executed before any selected model presentation is published.
func validatePlannerActivityResult(r *Runtime, result *planner.PlanResult, parentTool tools.Ident, synthesisOnly bool) error {
	if err := r.validatePlannerResultPayloads(result, parentTool); err != nil {
		return planner.NewOutputContractError(err)
	}
	if err := validatePlannerToolCallIDs(result); err != nil {
		return planner.NewOutputContractError(err)
	}
	terminalPayloads := 0
	if result.FinalResponse != nil {
		terminalPayloads++
	}
	if result.FinalToolResult != nil {
		terminalPayloads++
	}
	if terminalPayloads > 1 {
		return planner.NewOutputContractError(errors.New("planner returned both FinalResponse and FinalToolResult"))
	}
	hasCalls := len(result.ToolCalls) > 0
	hasTerminal := terminalPayloads == 1
	hasAwait := result.Await != nil
	if hasAwait {
		if len(result.Await.Items) == 0 {
			return planner.NewOutputContractError(errors.New("planner returned empty await"))
		}
		if err := validateAwaitItems(result.Await.Items); err != nil {
			return planner.NewOutputContractError(err)
		}
		if err := r.validateAwaitTools(result.Await.Items); err != nil {
			return planner.NewOutputContractError(err)
		}
	}
	if !hasCalls && !hasTerminal && !hasAwait {
		return planner.NewOutputContractError(errors.New("planner returned empty PlanResult"))
	}
	if result.SynthesizeAfterTools && (!hasCalls || hasTerminal || hasAwait) {
		return planner.NewOutputContractError(errors.New("planner synthesis-after-tools requires only tool calls"))
	}
	if result.SynthesizeAfterTools {
		for _, call := range result.ToolCalls {
			spec, ok := r.toolSpec(call.Name)
			if !ok {
				return planner.NewOutputContractError(fmt.Errorf("planner synthesis-after-tools references unknown tool %q", call.Name))
			}
			if spec.Bookkeeping {
				return planner.NewOutputContractError(fmt.Errorf("planner synthesis-after-tools cannot include bookkeeping tool %q", call.Name))
			}
		}
	}
	if hasTerminal && hasAwait {
		return planner.NewOutputContractError(errors.New("planner cannot combine terminal payload and await"))
	}
	if hasTerminal && hasCalls {
		for _, call := range result.ToolCalls {
			spec, ok := r.toolSpec(call.Name)
			if !ok {
				return planner.NewOutputContractError(fmt.Errorf("planner terminal result references unknown tool %q", call.Name))
			}
			if !spec.Bookkeeping {
				return planner.NewOutputContractError(fmt.Errorf("planner terminal result cannot include budgeted tool %q", call.Name))
			}
		}
	}
	if synthesisOnly {
		if len(result.ToolCalls) > 0 {
			return planner.NewOutputContractError(errors.New("synthesis-only planner result contains tool calls"))
		}
		if result.FinalResponse == nil && result.FinalToolResult == nil {
			return planner.NewOutputContractError(errors.New("synthesis-only planner result has no terminal payload"))
		}
		if result.FinalResponse != nil && result.FinalToolResult != nil {
			return planner.NewOutputContractError(errors.New("synthesis-only planner result has both terminal payloads"))
		}
		if result.Await != nil {
			return planner.NewOutputContractError(errors.New("synthesis-only planner result contains await"))
		}
	}
	return nil
}

// validatePlannerToolCallIDs keeps provider correlation separate from
// runtime-owned execution identity before planner output crosses the activity
// boundary. Ordinary planner-authored tool requests may omit provider identity;
// every tool-bound await is model-authored and must include it.
func validatePlannerToolCallIDs(result *planner.PlanResult) error {
	seen := make(map[string]struct{})
	addModelID := func(context, id string) error {
		if id == "" {
			return fmt.Errorf("%s is missing model tool call ID", context)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%s repeats model tool call ID %q", context, id)
		}
		seen[id] = struct{}{}
		return nil
	}
	for index, call := range result.ToolCalls {
		if call.ModelToolCallID == "" {
			continue
		}
		if err := addModelID(fmt.Sprintf("planner tool call %d", index), call.ModelToolCallID); err != nil {
			return err
		}
	}
	if result.Await == nil {
		return nil
	}
	for itemIndex, item := range result.Await.Items {
		context := fmt.Sprintf("planner await item %d", itemIndex)
		switch item.Kind {
		case planner.AwaitItemKindClarification:
		case planner.AwaitItemKindToolClarification:
			if item.ToolClarification == nil {
				continue
			}
			if item.ToolClarification.ToolCallID != "" {
				return fmt.Errorf("%s tool clarification must not set runtime tool call ID", context)
			}
			if err := addModelID(context+" tool clarification", item.ToolClarification.ModelToolCallID); err != nil {
				return err
			}
		case planner.AwaitItemKindQuestions:
			if item.Questions == nil {
				continue
			}
			if item.Questions.ToolCallID != "" {
				return fmt.Errorf("%s questions must not set runtime tool call ID", context)
			}
			if err := addModelID(context+" questions", item.Questions.ModelToolCallID); err != nil {
				return err
			}
		case planner.AwaitItemKindExternalTools:
			if item.ExternalTools == nil {
				continue
			}
			for toolIndex, tool := range item.ExternalTools.Items {
				toolContext := fmt.Sprintf("%s external tool %d", context, toolIndex)
				if tool.ToolCallID != "" {
					return fmt.Errorf("%s must not set runtime tool call ID", toolContext)
				}
				if err := addModelID(toolContext, tool.ModelToolCallID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validatePlannerResultPayloads enforces canonical tool JSON before Temporal
// serializes planner activity output.
func (r *Runtime) validatePlannerResultPayloads(result *planner.PlanResult, parentTool tools.Ident) error {
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
	if result.FinalToolResult != nil {
		if parentTool == "" {
			return errors.New("planner final tool result requires a parent tool")
		}
		spec, ok := r.toolSpec(parentTool)
		if !ok {
			return fmt.Errorf("planner final tool result references unregistered parent tool %q", parentTool)
		}
		if err := validatePlannerFinalToolResult(spec, result.FinalToolResult); err != nil {
			return err
		}
	}
	for index, call := range result.ToolCalls {
		if err := validatePlannerToolPayload(call.Payload); err != nil {
			return fmt.Errorf("planner tool call %d payload: %w", index, err)
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
			if item.ToolClarification.ModelToolCallID == "" {
				return fmt.Errorf("planner await item %d tool clarification model call ID is missing", itemIndex)
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

// validatePlannerFinalToolResult rejects malformed or contradictory values
// before a planner result crosses the activity boundary.
func validatePlannerFinalToolResult(spec tools.ToolSpec, final *planner.FinalToolResult) error {
	if final == nil {
		return nil
	}
	resultJSON := bytes.TrimSpace(final.Result)
	serverJSON := bytes.TrimSpace(final.ServerData)
	if len(resultJSON) > 0 && !json.Valid(resultJSON) {
		return errors.New("planner final tool result is not valid JSON")
	}
	if len(serverJSON) > 0 && !json.Valid(serverJSON) {
		return errors.New("planner final tool server data is not valid JSON")
	}
	if final.Failure != nil {
		if len(final.Result) > 0 {
			return errors.New("planner final tool result contains both a failure and a result")
		}
		return nil
	}
	if _, err := decodeSuccessfulToolResult(spec, final.Result); err != nil {
		return fmt.Errorf("planner final tool result: %w", err)
	}
	return nil
}

// validatePlannerAuthoredFinalResponse ensures planners express tool
// declarations through PlanResult.ToolCalls. Provider-owned final messages are
// matched to the exact complete response saved for that model call.
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

// validatePlannerResultPayloadCodecs applies each visible tool's exact payload
// codec before the planner result can cross the activity boundary.
func validatePlannerResultPayloadCodecs(
	ctx context.Context,
	r *Runtime,
	result *planner.PlanResult,
	continuations []continuationAction,
) error {
	for index, call := range result.ToolCalls {
		if err := validatePlannerToolPayloadWithCodec(ctx, r, continuations, call.Name, call.Payload); err != nil {
			return fmt.Errorf("planner tool call %d payload: %w", index, err)
		}
	}
	if result.Await == nil {
		return nil
	}
	for itemIndex, item := range result.Await.Items {
		switch item.Kind {
		case planner.AwaitItemKindClarification:
			continue
		case planner.AwaitItemKindToolClarification:
			if err := validatePlannerToolPayloadWithCodec(
				ctx,
				r,
				nil,
				item.ToolClarification.ToolName,
				item.ToolClarification.Payload,
			); err != nil {
				return fmt.Errorf("planner await item %d tool clarification payload: %w", itemIndex, err)
			}
		case planner.AwaitItemKindQuestions:
			if err := validatePlannerToolPayloadWithCodec(
				ctx,
				r,
				nil,
				item.Questions.ToolName,
				item.Questions.Payload,
			); err != nil {
				return fmt.Errorf("planner await item %d questions payload: %w", itemIndex, err)
			}
		case planner.AwaitItemKindExternalTools:
			for toolIndex, tool := range item.ExternalTools.Items {
				if err := validatePlannerToolPayloadWithCodec(ctx, r, nil, tool.Name, tool.Payload); err != nil {
					return fmt.Errorf("planner await item %d external tool %d payload: %w", itemIndex, toolIndex, err)
				}
			}
		}
	}
	return nil
}

// validatePlannerToolPayloadWithCodec decodes one ordinary tool payload with
// its generated codec. A generated continuation action accepts only the empty
// object shown to the model; compilePlannerToolCallsForRun later replaces that
// object with a payload containing the saved cursor and validates it with the
// generated codec for the real continuation tool.
func validatePlannerToolPayloadWithCodec(
	ctx context.Context,
	r *Runtime,
	continuations []continuationAction,
	name tools.Ident,
	payload rawjson.Message,
) error {
	if _, ok := r.toolSpec(name); ok {
		if _, err := r.unmarshalToolValue(ctx, name, payload.RawMessage(), true); err != nil {
			return fmt.Errorf("tool %q payload does not satisfy its generated contract: %w", name, err)
		}
		return nil
	}
	for _, continuation := range continuations {
		if continuation.modelName != name {
			continue
		}
		if err := validateEmptyContinuationPayload(payload); err != nil {
			return fmt.Errorf("tool %q payload does not satisfy its continuation contract: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("tool %q has no payload codec", name)
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
	if req.ToolsetName == "" {
		return nil, errors.New("toolset name is required")
	}
	r.mu.RLock()
	reg, ok := r.toolsets[req.ToolsetName]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("toolset %q is not registered", req.ToolsetName)
	}

	// Rebuild the activity-local call from execution data only. The workflow
	// retains the model-authored call and owns any later correction evidence.
	raw := append(rawjson.Message(nil), req.Payload...)
	call := ToolCall{
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

	// For non DecodeInExecutor toolsets, validate payloads eagerly using the
	// generated codecs so we can surface structured correction contracts. Executors
	// still receive the execution payload and may decode again as needed.
	if !reg.DecodeInExecutor {
		_, ok := r.toolSpec(req.ToolName)
		if !ok {
			return nil, fmt.Errorf("tool %q has no registered ToolSpec", req.ToolName)
		}
		if _, decErr := r.unmarshalToolValue(ctx, req.ToolName, raw.RawMessage(), true); decErr != nil {
			return &ToolOutput{
				Failure: buildToolFailureFromPayloadError(decErr),
			}, nil
		}
	}

	meta := ToolCallMetaFromCall(call)
	start := time.Now()
	executorCall := cloneToolCall(call)
	execResult, err := reg.Execute(ctx, &executorCall)
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
	result, resultJSON, clarification, err := r.materializeActivityToolExecutionResult(ctx, call, execResult)
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
	if clarification != nil {
		out.Clarification = clarification
	}
	if err := validateToolActivityOutputBudget(out); err != nil {
		return nil, outputcontract.NewWithOrigin(err, outputcontract.OriginTool)
	}
	return out, nil
}

// validateToolActivityOutputBudget rejects a tool result before Temporal tries
// to encode it. Tools that can produce larger domain data must store that data
// durably and return a typed reference in their canonical result.
func validateToolActivityOutputBudget(output *ToolOutput) error {
	encoded, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("encode tool activity output for size validation: %w", err)
	}
	if len(encoded) > engine.MaxPayloadBytes {
		return fmt.Errorf(
			"tool activity output uses %d bytes, maximum is %d; store the result and return its typed reference",
			len(encoded),
			engine.MaxPayloadBytes,
		)
	}
	return nil
}

// buildToolFailureFromPayloadError classifies invalid executor input and
// preserves generated field issues. The workflow later attaches correction
// evidence from the retained model call and registered specification.
func buildToolFailureFromPayloadError(err error) *planner.ToolFailure {
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
			Action: planner.RecoveryCorrectCall,
			Issues: issues,
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
func buildToolFailureFromAgentToolRequestError(err error) *planner.ToolFailure {
	var issuer interface {
		Issues() []*tools.FieldIssue
	}
	if errors.As(err, &issuer) {
		return buildToolFailureFromPayloadError(err)
	}
	var payloadErr *agentToolPayloadError
	if errors.As(err, &payloadErr) {
		return buildToolFailureFromPayloadError(payloadErr.cause)
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
	advertisedSpecs []tools.ToolSpec,
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
		advertisedSpecs:     advertisedSpecs,
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
