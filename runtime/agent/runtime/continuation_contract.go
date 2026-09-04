package runtime

// continuation_contract.go validates the public continuation values and the
// private checkpoint returned from trusted storage. These checks run before a
// workflow starts or restores state, so execution code can trust exact request,
// planner-step, policy, timing, and registered-tool invariants.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// continuationContractError marks a validation result that proves no
// successor workflow was submitted for the rejected continuation.
func continuationContractError(err error) error {
	return fmt.Errorf("%w: %w", ErrContinuationRejected, err)
}

// prepareContinuation validates the complete saved state and the caller's one
// pending response against the current generated agent definition.
func prepareContinuation(input *RunInput, definition AgentDefinition) (*workflowCheckpoint, error) {
	if input == nil || input.Continuation == nil {
		return nil, errors.New("continuation input is required")
	}
	checkpoint, err := decodeWorkflowCheckpoint(input.Continuation.Suspension, definition)
	if err != nil {
		return nil, err
	}
	if err := validateContinuationAgainstCheckpoint(input, checkpoint, definition); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

// decodeWorkflowCheckpoint validates the public envelope before decoding the
// current private state.
func decodeWorkflowCheckpoint(suspension *api.RunSuspension, definition AgentDefinition) (*workflowCheckpoint, error) {
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return nil, err
	}
	current := requiredCheckpointToolNames(checkpoint)
	if !reflect.DeepEqual(current, checkpoint.RequiredTools) {
		return nil, errors.New("run suspension required tools do not match saved state")
	}
	for _, name := range current {
		if _, ok := definition.spec(name); !ok {
			return nil, fmt.Errorf("run suspension requires tool %q removed from the current agent definition", name)
		}
	}
	if checkpoint.Policy != nil &&
		(checkpoint.Policy.LimitTerminalPlans != nil || checkpoint.Policy.CompletionTool != "") {
		if definition.route.ID != agent.Ident(checkpoint.AgentID) {
			return nil, fmt.Errorf("run suspension agent %q does not match current definition %q", checkpoint.AgentID, definition.route.ID)
		}
		if err := validateCompletionToolPolicyForDefinition(definition, checkpoint.Policy); err != nil {
			return nil, fmt.Errorf("validate suspended completion tool: %w", err)
		}
		if err := validateLimitTerminalPlansForDefinition(definition, checkpoint.Policy.LimitTerminalPlans); err != nil {
			return nil, fmt.Errorf("validate suspended limit terminal plans: %w", err)
		}
	}
	if err := validateCompletionToolPlanResultWithSpecs(
		checkpoint.Batch.Result,
		completionToolFromPolicy(checkpoint.Policy),
		definition.spec,
	); err != nil {
		return nil, fmt.Errorf("validate suspended completion plan: %w", err)
	}
	if err := validatePlannerResultPayloadsWithSpecs(
		plannerResultValidationProjection(checkpoint.Batch.Result),
		checkpoint.Context.Tool,
		definition.spec,
	); err != nil {
		return nil, fmt.Errorf("validate suspended planner result: %w", err)
	}
	if err := validatePlanResultToolCallIDs(checkpoint.Batch.Result); err != nil {
		return nil, fmt.Errorf("validate suspended planner result: %w", err)
	}
	program, err := normalizeStepWithSpecs(checkpoint.Batch.Result, definition.spec)
	if err != nil {
		return nil, fmt.Errorf("validate suspended planner result: %w", err)
	}
	if program.kind != checkpoint.Batch.Kind {
		return nil, errors.New("run suspension step kind does not match saved planner result")
	}
	if !slices.EqualFunc(program.calls, checkpoint.Batch.Calls, func(a, b ToolCall) bool {
		return reflect.DeepEqual(a, b)
	}) {
		return nil, errors.New("run suspension tool calls do not match saved planner result")
	}
	if !slices.EqualFunc(program.awaitItems, checkpoint.Batch.AwaitItems, func(a, b planner.AwaitItem) bool {
		return reflect.DeepEqual(a, b)
	}) {
		return nil, errors.New("run suspension await items do not match saved planner result")
	}
	if err := validateCheckpointToolValues(checkpoint, definition); err != nil {
		return nil, err
	}
	if err := validateCheckpointChildren(checkpoint, definition); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

// validateCheckpointChildren checks every nested suspension with the generated
// definition of the child agent that owns its tool.
func validateCheckpointChildren(checkpoint *workflowCheckpoint, definition AgentDefinition) error {
	byCallID := make(map[string]checkpointToolRecord, len(checkpoint.Batch.Records))
	for _, record := range checkpoint.Batch.Records {
		if _, exists := byCallID[record.Call.ToolCallID]; exists {
			return fmt.Errorf("run suspension has duplicate saved tool call id %q", record.Call.ToolCallID)
		}
		byCallID[record.Call.ToolCallID] = record
		if record.ChildSuspension == nil {
			continue
		}
		if err := validateCheckpointChild(record.Call, record.ChildSuspension, definition); err != nil {
			return fmt.Errorf("validate suspended child for tool call %q: %w", record.Call.ToolCallID, err)
		}
	}
	pendingByCallID := make(map[string]struct{})
	for _, pending := range checkpoint.Pending {
		if pending.Child == nil {
			continue
		}
		record, ok := byCallID[pending.Child.ToolCallID]
		if !ok {
			return fmt.Errorf("pending child references unknown tool call %q", pending.Child.ToolCallID)
		}
		if record.ChildSuspension == nil || !reflect.DeepEqual(record.ChildSuspension, pending.Child.Suspension) {
			return fmt.Errorf("pending child does not match saved suspension for tool call %q", pending.Child.ToolCallID)
		}
		if _, exists := pendingByCallID[pending.Child.ToolCallID]; exists {
			return fmt.Errorf("run suspension has duplicate pending child for tool call %q", pending.Child.ToolCallID)
		}
		pendingByCallID[pending.Child.ToolCallID] = struct{}{}
		if err := validateCheckpointChild(record.Call, pending.Child.Suspension, definition); err != nil {
			return fmt.Errorf("validate pending child for tool call %q: %w", record.Call.ToolCallID, err)
		}
	}
	for _, record := range checkpoint.Batch.Records {
		if record.ChildSuspension == nil {
			continue
		}
		if _, ok := pendingByCallID[record.Call.ToolCallID]; !ok {
			return fmt.Errorf("saved child suspension for tool call %q has no pending response", record.Call.ToolCallID)
		}
	}
	return nil
}

func validateCheckpointChild(call ToolCall, suspension *api.RunSuspension, definition AgentDefinition) error {
	child, err := childDefinitionForCall(call, definition)
	if err != nil {
		return err
	}
	checkpoint, err := decodeWorkflowCheckpoint(suspension, child)
	if err != nil {
		return err
	}
	if checkpoint.AgentID != string(child.route.ID) {
		return fmt.Errorf("child suspension agent %q does not match tool agent %q", checkpoint.AgentID, child.route.ID)
	}
	return nil
}

// childDefinitionForCall returns the immutable generated definition selected
// by one agent-tool call.
func childDefinitionForCall(call ToolCall, definition AgentDefinition) (AgentDefinition, error) {
	spec, ok := definition.spec(call.Name)
	if !ok {
		return AgentDefinition{}, fmt.Errorf("child tool %q is not in the current agent definition", call.Name)
	}
	if !spec.IsAgentTool || spec.AgentID == "" {
		return AgentDefinition{}, fmt.Errorf("tool %q does not define a child agent", call.Name)
	}
	child, ok := definition.agents[agent.Ident(spec.AgentID)]
	if !ok {
		return AgentDefinition{}, fmt.Errorf("child agent %q is not in the current definition graph", spec.AgentID)
	}
	child.agents = definition.agents
	return child, nil
}

// validateCheckpointToolValues proves that every concrete payload or result
// the continuation can execute or give back to planner code still satisfies
// the current registered codecs.
func validateCheckpointToolValues(checkpoint *workflowCheckpoint, definition AgentDefinition) error {
	for _, output := range checkpoint.State.ToolOutputs {
		if err := validateCheckpointToolOutput(output, definition); err != nil {
			return err
		}
	}
	for _, output := range checkpoint.State.PendingRecovery {
		if err := validateCheckpointToolOutput(output, definition); err != nil {
			return err
		}
	}
	for _, event := range checkpoint.State.ToolEvents {
		if _, err := decodeCheckpointToolEventWithSpecs(event, definition.spec); err != nil {
			return err
		}
	}
	for _, call := range checkpoint.Batch.Calls {
		if err := validateCheckpointToolRequest(call, definition); err != nil {
			return err
		}
	}
	for _, record := range checkpoint.Batch.Records {
		if err := validateCheckpointToolRequest(record.Call, definition); err != nil {
			return err
		}
		if record.ChildSuspension == nil {
			if _, err := decodeCheckpointToolEventWithSpecs(record.Result, definition.spec); err != nil {
				return err
			}
		}
	}
	for _, pending := range checkpoint.Pending {
		if pending.Confirmation != nil {
			if err := validateCheckpointToolRequest(pending.Confirmation.Call, definition); err != nil {
				return err
			}
			if err := decodeToolValue(definition, pending.Confirmation.Call.Name, pending.Confirmation.DeniedResult.RawMessage(), false); err != nil {
				return fmt.Errorf("decode suspended denied result for %s: %w", pending.Confirmation.Call.Name, err)
			}
		}
		if pending.Await != nil {
			for _, call := range awaitToolRequests([]planner.AwaitItem{*pending.Await}) {
				if err := validateCheckpointToolRequest(call, definition); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateCheckpointToolRequest decodes one saved executable payload through
// the current generated input codec without executing the tool.
func validateCheckpointToolRequest(call ToolCall, definition AgentDefinition) error {
	if err := decodeToolValue(definition, call.Name, call.Payload.RawMessage(), true); err != nil {
		return fmt.Errorf("decode suspended tool payload for %s: %w", call.Name, err)
	}
	return nil
}

// validateCheckpointToolOutput decodes the canonical call and successful
// result bytes retained for planner resume.
func validateCheckpointToolOutput(output *planner.ToolOutput, definition AgentDefinition) error {
	if output == nil {
		return errors.New("run suspension contains nil tool output")
	}
	if err := decodeToolValue(definition, output.Name, output.Payload.RawMessage(), true); err != nil {
		return fmt.Errorf("decode suspended tool payload for %s: %w", output.Name, err)
	}
	var spec *tools.ToolSpec
	if output.Failure == nil {
		registered, ok := definition.spec(output.Name)
		if !ok {
			return fmt.Errorf("suspended tool result references unregistered tool %q", output.Name)
		}
		spec = &registered
	}
	if _, err := validatePersistedToolResult(
		spec,
		ToolCall{Name: output.Name, ToolCallID: output.ToolCallID},
		output.Result,
		output.ServerData,
		output.Bounds,
		output.Failure,
	); err != nil {
		return fmt.Errorf("decode suspended tool result for %s: %w", output.Name, err)
	}
	return nil
}

// decodeToolValue decodes one saved value with the current generated codec.
func decodeToolValue(definition AgentDefinition, name tools.Ident, data []byte, payload bool) error {
	spec, ok := definition.spec(name)
	if !ok {
		return fmt.Errorf("tool %q is not in the current agent definition", name)
	}
	codec := spec.Result.Codec
	if payload {
		codec = spec.Payload.Codec
	}
	if codec.FromJSON == nil {
		return fmt.Errorf("tool %q has no current generated codec", name)
	}
	_, err := codec.FromJSON(data)
	return err
}

// decodeWorkflowCheckpointState verifies and decodes runtime-owned state
// without consulting the local tool registry. Callers use it to restore run
// metadata before scheduling; the worker performs the additional executable
// tool-contract check in decodeWorkflowCheckpoint.
func decodeWorkflowCheckpointState(suspension *api.RunSuspension) (*workflowCheckpoint, error) {
	if err := validatePublicRunSuspension(suspension); err != nil {
		return nil, err
	}
	if suspension.Version != api.RunSuspensionVersion {
		return nil, fmt.Errorf("unsupported run suspension version %q", suspension.Version)
	}
	var checkpoint workflowCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(suspension.Checkpoint))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return nil, fmt.Errorf("decode run suspension checkpoint: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("run suspension checkpoint has trailing data")
	}
	if err := validateWorkflowCheckpoint(&checkpoint); err != nil {
		return nil, err
	}
	if checkpoint.Version != suspension.Version {
		return nil, fmt.Errorf("run suspension version mismatch: envelope=%q checkpoint=%q", suspension.Version, checkpoint.Version)
	}
	digest := sha256.Sum256(suspension.Checkpoint)
	if suspension.ID != hex.EncodeToString(digest[:16]) {
		return nil, errors.New("run suspension id does not match checkpoint")
	}
	publicPending, err := publicPendingInputs(checkpoint.Pending)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(suspension.Pending, publicPending) {
		return nil, errors.New("run suspension pending inputs do not match checkpoint")
	}
	if !reflect.DeepEqual(suspension.RequiredTools, checkpoint.RequiredTools) {
		return nil, errors.New("run suspension required tools do not match checkpoint")
	}
	return &checkpoint, nil
}

// validateWorkflowCheckpoint enforces the private state invariants established
// when a sessionful workflow suspends. Later restore code can then trust the
// checkpoint without presence-based behavior.
func validateWorkflowCheckpoint(checkpoint *workflowCheckpoint) error {
	if checkpoint.AgentID == "" || checkpoint.SessionID == "" {
		return errors.New("run suspension checkpoint requires agent and session ids")
	}
	if checkpoint.PreviousRunID == "" {
		return errors.New("run suspension checkpoint requires predecessor run id")
	}
	if !checkpoint.State.ResponseCommitted {
		return errors.New("run suspension checkpoint requires a committed planner response")
	}
	if checkpoint.Batch.Result == nil {
		return errors.New("run suspension checkpoint requires a planner result")
	}
	switch checkpoint.Batch.Kind {
	case stepKindAwait, stepKindTools, stepKindToolTerminal:
	case stepKindTerminal:
		return errors.New("terminal planner result cannot suspend")
	default:
		return fmt.Errorf("run suspension checkpoint has unknown step kind %d", checkpoint.Batch.Kind)
	}
	if checkpoint.Batch.Recorded < 0 || checkpoint.Batch.Recorded > len(checkpoint.Batch.Records) {
		return errors.New("run suspension checkpoint has invalid recorded result count")
	}
	if checkpoint.Batch.BudgetCost < 0 || checkpoint.Batch.Confirmations < 0 || checkpoint.Batch.AwaitCount < 0 {
		return errors.New("run suspension checkpoint has negative batch accounting")
	}
	if checkpoint.Batch.ResumePlannerAfterPending {
		if checkpoint.Batch.Kind != stepKindTools ||
			checkpoint.Batch.AwaitCount != 1 ||
			len(checkpoint.Pending) != 1 ||
			checkpoint.Pending[0].Await == nil ||
			checkpoint.Pending[0].Await.Kind != planner.AwaitItemKindClarification {
			return errors.New("run suspension planner-resume phase requires one generated clarification")
		}
	}
	if checkpoint.State.NextAttempt <= 0 {
		return errors.New("run suspension checkpoint requires a positive next planner attempt")
	}
	for i, output := range checkpoint.State.ToolOutputs {
		if output == nil {
			return fmt.Errorf("run suspension checkpoint tool output %d is nil", i)
		}
	}
	for i, output := range checkpoint.State.PendingRecovery {
		if output == nil {
			return fmt.Errorf("run suspension checkpoint recovery output %d is nil", i)
		}
	}
	if checkpoint.Batch.ResumePlannerAfterPending {
		if checkpoint.State.PendingRecoveryCatalog != nil {
			return errors.New("run suspension planner-resume phase cannot carry a recovery catalog")
		}
	} else {
		recoveryCatalog, err := checkpointRecoveryCatalog(checkpoint)
		if err != nil {
			return err
		}
		if err := validateRecoveryCatalog(
			checkpoint.State.PendingRecovery,
			recoveryCatalog,
			checkpoint.Batch.Result,
		); err != nil {
			return fmt.Errorf("run suspension checkpoint recovery state: %w", err)
		}
	}
	for i, event := range checkpoint.State.ToolEvents {
		if event == nil {
			return fmt.Errorf("run suspension checkpoint tool event %d is nil", i)
		}
	}
	caps := checkpoint.State.Caps
	if caps.MaxToolCalls < 0 || caps.RemainingToolCalls < 0 ||
		caps.MaxRecoveryTurns < 0 || caps.RemainingRecoveryTurns < 0 {
		return errors.New("run suspension checkpoint has negative policy caps")
	}
	if caps.MaxRecoveryTurns == 0 {
		return errors.New("run suspension checkpoint requires a positive recovery turn maximum")
	}
	if caps.RemainingToolCalls > caps.MaxToolCalls ||
		caps.RemainingRecoveryTurns > caps.MaxRecoveryTurns {
		return errors.New("run suspension checkpoint remaining policy cap exceeds its maximum")
	}
	if checkpoint.HasBudget != checkpoint.HasHard {
		return errors.New("run suspension checkpoint must carry both active deadlines or neither")
	}
	if checkpoint.BudgetLeft < 0 || checkpoint.HardLeft < 0 {
		return errors.New("run suspension checkpoint has negative remaining duration")
	}
	if !checkpoint.HasBudget && (checkpoint.BudgetLeft != 0 || checkpoint.HardLeft != 0) {
		return errors.New("run suspension checkpoint has durations without active deadlines")
	}
	if checkpoint.HasBudget && checkpoint.HardLeft < checkpoint.BudgetLeft {
		return errors.New("run suspension checkpoint hard deadline precedes budget deadline")
	}
	return nil
}

// checkpointRecoveryCatalog returns the one catalog representation permitted
// by typed pending failures. Exact correction tools are derived from the saved
// failures and reject a duplicate serialized catalog. Recovery actions whose
// visible tools cannot be derived still require their serialized catalog.
func checkpointRecoveryCatalog(checkpoint *workflowCheckpoint) (*RecoveryCatalog, error) {
	catalog := checkpoint.State.PendingRecoveryCatalog
	exactTools := correctCallCatalog(checkpoint.State.PendingRecovery)
	if len(exactTools) == 0 {
		return catalog, nil
	}
	if catalog != nil {
		return nil, errors.New("run suspension checkpoint correct-call recovery cannot carry a recovery catalog")
	}
	return &RecoveryCatalog{Tools: exactTools}, nil
}

// validatePublicRunSuspension checks the portion of a child suspension that a
// parent service is allowed to understand. The child service exclusively owns
// decoding and tool-schema validation for its private checkpoint.
func validatePublicRunSuspension(suspension *api.RunSuspension) error {
	if suspension == nil {
		return errors.New("run suspension is required")
	}
	if suspension.ID == "" || suspension.Version == "" || len(suspension.Checkpoint) == 0 {
		return errors.New("run suspension requires id, version, and checkpoint")
	}
	if len(suspension.Pending) == 0 {
		return errors.New("run suspension has no pending input")
	}
	for i, pending := range suspension.Pending {
		if err := validatePendingInput(pending); err != nil {
			return fmt.Errorf("run suspension pending input %d: %w", i, err)
		}
	}
	seenTools := make(map[string]struct{}, len(suspension.RequiredTools))
	for i, name := range suspension.RequiredTools {
		if name == "" {
			return fmt.Errorf("run suspension required tool %d requires a name", i)
		}
		key := string(name)
		if _, ok := seenTools[key]; ok {
			return fmt.Errorf("run suspension has duplicate required tool %q", name)
		}
		seenTools[key] = struct{}{}
	}
	return nil
}

// validateWorkflowOutput enforces identity and terminal shape when a successful
// result crosses an engine workflow boundary. It intentionally validates only
// the public shape of a suspension; the worker that owns the checkpoint
// performs private decoding and tool-schema validation.
func validateWorkflowOutput(out *RunOutput, expectedAgentID agent.Ident, expectedRunID string) error {
	if out == nil {
		return errors.New("workflow returned no output")
	}
	if out.AgentID != expectedAgentID {
		return fmt.Errorf("workflow output agent mismatch: got=%q want=%q", out.AgentID, expectedAgentID)
	}
	if out.RunID != expectedRunID {
		return fmt.Errorf("workflow output run mismatch: got=%q want=%q", out.RunID, expectedRunID)
	}
	terminalVariants := 0
	if out.Suspension != nil {
		terminalVariants++
	}
	if out.Final != nil {
		terminalVariants++
	}
	if out.FinalToolResult != nil {
		terminalVariants++
	}
	if terminalVariants != 1 {
		return fmt.Errorf("workflow output must contain exactly one terminal result, got %d", terminalVariants)
	}
	if out.Suspension != nil {
		return validatePublicRunSuspension(out.Suspension)
	}
	return nil
}

// validateWorkflowRunInput rejects mixed initial/continuation inputs before
// checkpoint decoding can overwrite caller-supplied state.
func validateWorkflowRunInput(input *RunInput) error {
	if input == nil {
		return errors.New("run input is required")
	}
	for index, rendered := range input.RenderedPrompts {
		if rendered.PromptID == "" || rendered.Version == "" {
			return fmt.Errorf("rendered prompt %d requires prompt id and version", index)
		}
		if rendered.Scope.SessionID != "" && rendered.Scope.SessionID != input.SessionID {
			return fmt.Errorf("rendered prompt %d scope session does not match run session", index)
		}
	}
	if input.Continuation == nil {
		if input.Policy != nil {
			return validateMaxRecoveryTurns(input.Policy.MaxRecoveryTurns)
		}
		return nil
	}
	if err := validatePendingInputResponse(input.Continuation.Response); err != nil {
		return err
	}
	if len(input.Messages) > 0 || len(input.Labels) > 0 || len(input.Metadata) > 0 ||
		input.Policy != nil || input.ParentRunID != "" || input.ParentAgentID != "" ||
		input.ParentToolCallID != "" || input.Tool != "" || len(input.ToolArgs) > 0 ||
		len(input.RenderedPrompts) > 0 {
		return errors.New("run continuation cannot include caller-supplied checkpoint state")
	}
	return nil
}

// validatePendingInput enforces the public request union before callers render
// it or select the corresponding response shape.
func validatePendingInput(input *api.PendingInput) error {
	if input == nil {
		return errors.New("pending input is nil")
	}
	switch input.Kind {
	case api.PendingInputKindConfirmation:
		if input.Confirmation == nil || input.Await != nil {
			return errors.New("confirmation pending input has an invalid payload")
		}
		if input.Confirmation.ID == "" || input.Confirmation.ToolName == "" || input.Confirmation.ToolCallID == "" {
			return errors.New("confirmation pending input requires id, tool, and tool_call_id")
		}
	case api.PendingInputKindClarification:
		if input.Confirmation != nil || input.Await == nil {
			return errors.New("clarification pending input has an invalid payload")
		}
		if input.Await.Kind != planner.AwaitItemKindClarification && input.Await.Kind != planner.AwaitItemKindToolClarification {
			return fmt.Errorf("clarification pending input has await kind %q", input.Await.Kind)
		}
		if err := validateAwaitItems([]planner.AwaitItem{*input.Await}); err != nil {
			return err
		}
	case api.PendingInputKindToolResults:
		if input.Confirmation != nil || input.Await == nil {
			return errors.New("tool-results pending input has an invalid payload")
		}
		if input.Await.Kind != planner.AwaitItemKindQuestions && input.Await.Kind != planner.AwaitItemKindExternalTools {
			return fmt.Errorf("tool-results pending input has await kind %q", input.Await.Kind)
		}
		if err := validateAwaitItems([]planner.AwaitItem{*input.Await}); err != nil {
			return err
		}
	default:
		return fmt.Errorf("pending input has unknown kind %q", input.Kind)
	}
	return nil
}

// validatePendingInputResponse enforces the response union independently of
// the pending request; matching the specific request happens during resume.
func validatePendingInputResponse(response *api.PendingInputResponse) error {
	if response == nil {
		return errors.New("pending input response is required")
	}
	variants := 0
	if response.Clarification != nil {
		variants++
	}
	if response.Confirmation != nil {
		variants++
	}
	if response.ToolResults != nil {
		variants++
	}
	if variants != 1 {
		return fmt.Errorf("pending input response has %d variants", variants)
	}
	return nil
}

// validatePendingInputResponseFor checks that one response completes the
// first visible request stored in a suspension.
func validatePendingInputResponseFor(pending *api.PendingInput, response *api.PendingInputResponse) error {
	if err := validatePendingInput(pending); err != nil {
		return err
	}
	if err := validatePendingInputResponse(response); err != nil {
		return err
	}
	switch pending.Kind {
	case api.PendingInputKindConfirmation:
		if response.Confirmation == nil {
			return errors.New("run continuation requires a confirmation response")
		}
		if response.Confirmation.ID != pending.Confirmation.ID {
			return fmt.Errorf(
				"confirmation response id %q does not match pending id %q",
				response.Confirmation.ID,
				pending.Confirmation.ID,
			)
		}
	case api.PendingInputKindClarification:
		if response.Clarification == nil {
			return errors.New("run continuation requires a clarification response")
		}
		var expectedID string
		switch pending.Await.Kind {
		case planner.AwaitItemKindClarification:
			expectedID = pending.Await.Clarification.ID
		case planner.AwaitItemKindToolClarification:
			expectedID = pending.Await.ToolClarification.ID
		case planner.AwaitItemKindQuestions, planner.AwaitItemKindExternalTools:
			return fmt.Errorf("clarification pending input has await kind %q", pending.Await.Kind)
		}
		if response.Clarification.ID != expectedID {
			return fmt.Errorf(
				"clarification response id %q does not match pending id %q",
				response.Clarification.ID,
				expectedID,
			)
		}
	case api.PendingInputKindToolResults:
		if response.ToolResults == nil {
			return errors.New("run continuation requires a tool-results response")
		}
		var expectedID string
		switch pending.Await.Kind {
		case planner.AwaitItemKindQuestions:
			expectedID = pending.Await.Questions.ID
		case planner.AwaitItemKindExternalTools:
			expectedID = pending.Await.ExternalTools.ID
		case planner.AwaitItemKindClarification, planner.AwaitItemKindToolClarification:
			return fmt.Errorf("tool-results pending input has await kind %q", pending.Await.Kind)
		}
		if response.ToolResults.ID != expectedID {
			return fmt.Errorf(
				"tool-results response id %q does not match pending id %q",
				response.ToolResults.ID,
				expectedID,
			)
		}
	}
	return nil
}

// validateContinuationAgainstCheckpoint checks the new run identity and the
// one response accepted by the first request saved in the checkpoint.
func validateContinuationAgainstCheckpoint(input *RunInput, checkpoint *workflowCheckpoint, definition AgentDefinition) error {
	if err := validateContinuationIdentity(input, checkpoint); err != nil {
		return err
	}
	publicPending, err := publicPendingInputs(checkpoint.Pending)
	if err != nil {
		return err
	}
	if err := validatePendingInputResponseFor(publicPending[0], input.Continuation.Response); err != nil {
		return err
	}
	return validateContinuationResponse(checkpoint, input.Continuation.Response, definition)
}

// validateContinuationResponse checks caller-supplied tool results with the
// generated definition that owns the first pending request. Nested child
// requests use the child's definition instead of the root agent's tools.
func validateContinuationResponse(
	checkpoint *workflowCheckpoint,
	response *api.PendingInputResponse,
	definition AgentDefinition,
) error {
	pending := checkpoint.Pending[0]
	if pending.Child != nil {
		record, ok := checkpointRecordByCallID(checkpoint.Batch.Records, pending.Child.ToolCallID)
		if !ok {
			return fmt.Errorf("pending child references unknown tool call %q", pending.Child.ToolCallID)
		}
		child, err := childDefinitionForCall(record.Call, definition)
		if err != nil {
			return err
		}
		childCheckpoint, err := decodeWorkflowCheckpoint(pending.Child.Suspension, child)
		if err != nil {
			return err
		}
		return validateContinuationResponse(childCheckpoint, response, child)
	}
	if pending.Await == nil || response.ToolResults == nil {
		return nil
	}
	return validateProvidedToolResults(*pending.Await, response.ToolResults, definition)
}

// validateProvidedToolResults proves that one external result exists for each
// awaited call and that successful bytes satisfy the current generated codec.
func validateProvidedToolResults(
	await planner.AwaitItem,
	results *api.ToolResultsSet,
	definition AgentDefinition,
) error {
	var calls []ToolCall
	switch await.Kind {
	case planner.AwaitItemKindQuestions:
		question := await.Questions
		calls = []ToolCall{{
			Name: question.ToolName, ToolCallID: question.ToolCallID,
			ModelToolCallID: question.ModelToolCallID, Payload: question.Payload,
		}}
	case planner.AwaitItemKindExternalTools:
		calls = awaitToolRequests([]planner.AwaitItem{await})
	case planner.AwaitItemKindClarification, planner.AwaitItemKindToolClarification:
		return nil
	default:
		return fmt.Errorf("pending tool results have unknown await kind %q", await.Kind)
	}
	if len(results.Results) != len(calls) {
		return fmt.Errorf("tool-results response has %d results, want %d", len(results.Results), len(calls))
	}
	byID := make(map[string]*api.ProvidedToolResult, len(results.Results))
	for _, result := range results.Results {
		if result == nil {
			return errors.New("tool-results response contains a nil result")
		}
		if result.ToolCallID == "" {
			return fmt.Errorf("tool result for %q requires tool_call_id", result.Name)
		}
		if _, exists := byID[result.ToolCallID]; exists {
			return fmt.Errorf("tool-results response repeats tool_call_id %q", result.ToolCallID)
		}
		byID[result.ToolCallID] = result
	}
	for _, call := range calls {
		result, ok := byID[call.ToolCallID]
		if !ok {
			return fmt.Errorf("tool-results response is missing tool_call_id %q", call.ToolCallID)
		}
		if result.Name != call.Name {
			return fmt.Errorf("tool result %q names tool %q, want %q", call.ToolCallID, result.Name, call.Name)
		}
		if (result.Success == nil) == (result.Failure == nil) {
			return fmt.Errorf("tool result %q must contain exactly one success or failure", call.ToolCallID)
		}
		spec, ok := definition.spec(call.Name)
		if !ok {
			return fmt.Errorf("tool result %q references tool %q removed from the current agent definition", call.ToolCallID, call.Name)
		}
		if result.Success != nil {
			if _, err := decodeSuccessfulToolResult(spec, result.Success.Result); err != nil {
				return fmt.Errorf("decode tool result %q: %w", call.ToolCallID, err)
			}
			if err := validateToolBoundsContract(spec, call, false, result.Success.Bounds); err != nil {
				return err
			}
			continue
		}
		if err := planner.ValidateToolFailure(canonicalProvidedToolFailure(result.Failure)); err != nil {
			return fmt.Errorf("validate tool failure %q: %w", call.ToolCallID, err)
		}
	}
	return nil
}

// checkpointRecordByCallID returns the single record already proved unique by
// checkpoint validation.
func checkpointRecordByCallID(records []checkpointToolRecord, toolCallID string) (checkpointToolRecord, bool) {
	for _, record := range records {
		if record.Call.ToolCallID == toolCallID {
			return record, true
		}
	}
	return checkpointToolRecord{}, false
}
