package runtime

// continuation_contract.go validates the public continuation values and the
// private checkpoint returned from trusted storage. These checks run before a
// workflow starts or restores state, so execution code can trust exact request,
// planner-step, policy, timing, and registered-tool invariants.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
)

// ValidateContinuation verifies that this runtime can decode and execute a
// suspension without starting a workflow or consuming its pending response.
func (r *Runtime) ValidateContinuation(suspension *api.RunSuspension) error {
	_, err := r.decodeWorkflowCheckpoint(suspension)
	return err
}

// decodeWorkflowCheckpoint validates the public envelope before decoding the
// private state. Callers cannot select an older or alternate checkpoint format.
func (r *Runtime) decodeWorkflowCheckpoint(suspension *api.RunSuspension) (*workflowCheckpoint, error) {
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return nil, err
	}
	current := requiredCheckpointToolNames(checkpoint)
	if !reflect.DeepEqual(current, checkpoint.RequiredTools) {
		return nil, errors.New("run suspension required tools do not match saved state")
	}
	for _, name := range current {
		if _, ok := r.toolSpec(name); !ok {
			return nil, fmt.Errorf("run suspension requires unregistered tool %q", name)
		}
	}
	program, err := r.normalizeStep(checkpoint.Batch.Result)
	if err != nil {
		return nil, fmt.Errorf("validate suspended planner result: %w", err)
	}
	if program.kind != checkpoint.Batch.Kind {
		return nil, errors.New("run suspension step kind does not match saved planner result")
	}
	if !slices.EqualFunc(program.calls, checkpoint.Batch.Calls, func(a, b planner.ToolRequest) bool {
		return reflect.DeepEqual(a, b)
	}) {
		return nil, errors.New("run suspension tool calls do not match saved planner result")
	}
	if !slices.EqualFunc(program.awaitItems, checkpoint.Batch.AwaitItems, func(a, b planner.AwaitItem) bool {
		return reflect.DeepEqual(a, b)
	}) {
		return nil, errors.New("run suspension await items do not match saved planner result")
	}
	if err := r.validateCheckpointToolValues(checkpoint); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

// validateCheckpointToolValues proves that every concrete payload or result
// the continuation can execute or give back to planner code still satisfies
// the current registered codecs.
func (r *Runtime) validateCheckpointToolValues(checkpoint *workflowCheckpoint) error {
	ctx := context.Background()
	for _, output := range checkpoint.State.ToolOutputs {
		if err := r.validateCheckpointToolOutput(ctx, output); err != nil {
			return err
		}
	}
	for _, output := range checkpoint.State.PendingRecovery {
		if err := r.validateCheckpointToolOutput(ctx, output); err != nil {
			return err
		}
	}
	for _, event := range checkpoint.State.ToolEvents {
		if _, err := r.decodeCheckpointToolEvent(ctx, event); err != nil {
			return err
		}
	}
	for _, call := range checkpoint.Batch.Calls {
		if err := r.validateCheckpointToolRequest(ctx, call); err != nil {
			return err
		}
	}
	for _, record := range checkpoint.Batch.Records {
		if err := r.validateCheckpointToolRequest(ctx, record.Call); err != nil {
			return err
		}
		if record.ChildSuspension == nil {
			if _, err := r.decodeCheckpointToolEvent(ctx, record.Result); err != nil {
				return err
			}
		}
	}
	for _, pending := range checkpoint.Pending {
		if pending.Confirmation != nil {
			if err := r.validateCheckpointToolRequest(ctx, pending.Confirmation.Call); err != nil {
				return err
			}
			if _, err := r.unmarshalToolValue(ctx, pending.Confirmation.Call.Name, pending.Confirmation.DeniedResult.RawMessage(), false); err != nil {
				return fmt.Errorf("decode suspended denied result for %s: %w", pending.Confirmation.Call.Name, err)
			}
		}
		if pending.Await != nil {
			for _, call := range awaitToolRequests([]planner.AwaitItem{*pending.Await}) {
				if err := r.validateCheckpointToolRequest(ctx, call); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateCheckpointToolRequest decodes one saved executable payload through
// the current generated input codec without executing the tool.
func (r *Runtime) validateCheckpointToolRequest(ctx context.Context, call planner.ToolRequest) error {
	if _, err := r.unmarshalToolValue(ctx, call.Name, call.Payload.RawMessage(), true); err != nil {
		return fmt.Errorf("decode suspended tool payload for %s: %w", call.Name, err)
	}
	return nil
}

// validateCheckpointToolOutput decodes the canonical call and successful
// result bytes retained for planner resume.
func (r *Runtime) validateCheckpointToolOutput(ctx context.Context, output *planner.ToolOutput) error {
	if output == nil {
		return errors.New("run suspension contains nil tool output")
	}
	if _, err := r.unmarshalToolValue(ctx, output.Name, output.Payload.RawMessage(), true); err != nil {
		return fmt.Errorf("decode suspended tool payload for %s: %w", output.Name, err)
	}
	if output.Failure == nil && !output.ResultOmitted {
		if _, err := r.unmarshalToolValue(ctx, output.Name, output.Result.RawMessage(), false); err != nil {
			return fmt.Errorf("decode suspended tool result for %s: %w", output.Name, err)
		}
	}
	return nil
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
	if err := json.Unmarshal(suspension.Checkpoint, &checkpoint); err != nil {
		return nil, fmt.Errorf("decode run suspension checkpoint: %w", err)
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
	if checkpoint.BaseContext.RunID == "" {
		return errors.New("run suspension checkpoint requires predecessor run id")
	}
	if checkpoint.BaseContext.SessionID != checkpoint.SessionID {
		return errors.New("run suspension checkpoint session does not match saved run context")
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
	for i, event := range checkpoint.State.ToolEvents {
		if event == nil {
			return fmt.Errorf("run suspension checkpoint tool event %d is nil", i)
		}
	}
	caps := checkpoint.State.Caps
	if caps.MaxToolCalls < 0 || caps.RemainingToolCalls < 0 ||
		caps.MaxConsecutiveFailedToolCalls < 0 || caps.RemainingConsecutiveFailedToolCalls < 0 {
		return errors.New("run suspension checkpoint has negative policy caps")
	}
	if caps.RemainingToolCalls > caps.MaxToolCalls ||
		caps.RemainingConsecutiveFailedToolCalls > caps.MaxConsecutiveFailedToolCalls {
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
	if out.Suspension != nil {
		if out.Final != nil || out.FinalToolResult != nil {
			return errors.New("suspended workflow output cannot include a completed result")
		}
		return validatePublicRunSuspension(out.Suspension)
	}
	if out.Final != nil && out.FinalToolResult != nil {
		return errors.New("completed workflow output cannot include both final response and final tool result")
	}
	return nil
}

// validateWorkflowRunInput rejects mixed initial/continuation inputs before
// checkpoint decoding can overwrite caller-supplied state.
func validateWorkflowRunInput(input *RunInput) error {
	if input == nil {
		return errors.New("run input is required")
	}
	if input.Continuation == nil {
		return nil
	}
	if err := validatePendingInputResponse(input.Continuation.Response); err != nil {
		return err
	}
	if len(input.Messages) > 0 || len(input.Labels) > 0 || len(input.Metadata) > 0 ||
		input.Policy != nil || input.ParentRunID != "" || input.ParentAgentID != "" ||
		input.ParentToolCallID != "" || input.Tool != "" || len(input.ToolArgs) > 0 {
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
