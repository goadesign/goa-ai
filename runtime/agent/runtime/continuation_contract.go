package runtime

// continuation_contract.go validates the public continuation values and the
// private checkpoint returned from trusted storage. These checks run before a
// workflow starts or restores state, so execution code can trust exact request,
// planner-step, policy, timing, and registered-tool invariants.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
	"unicode/utf8"

	"goa.design/goa-ai/internal/registrycontract"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
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
	if err := validateCheckpointMCPInputs(checkpoint); err != nil {
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
	parentSpec, err := selectedRunTool(run.Context{Tool: checkpoint.Context.Tool, ToolRegistry: checkpoint.Context.ToolRegistry, Operation: checkpoint.Context.Operation}, definition.route.ID, definition.spec)
	if err != nil {
		return nil, err
	}
	if err := validatePlannerResultPayloads(
		plannerResultValidationProjection(checkpoint.Batch.Result),
		checkpoint.Context.Tool,
		parentSpec,
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
	return validateCheckpointChildRecords(checkpoint, func(call ToolCall, child *api.RunSuspension) error {
		return validateCheckpointChild(call, child, definition)
	})
}

// validateCheckpointChildRecords checks saved child identities and their exact
// pending responses before the supplied owner check validates each child.
func validateCheckpointChildRecords(checkpoint *workflowCheckpoint, validate func(ToolCall, *api.RunSuspension) error) error {
	byCallID := make(map[string]checkpointToolRecord, len(checkpoint.Batch.Records))
	for _, record := range checkpoint.Batch.Records {
		if _, exists := byCallID[record.Call.ToolCallID]; exists {
			return fmt.Errorf("run suspension has duplicate saved tool call id %q", record.Call.ToolCallID)
		}
		byCallID[record.Call.ToolCallID] = record
		if record.ChildSuspension == nil {
			continue
		}
		if err := validate(record.Call, record.ChildSuspension); err != nil {
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
	var checkpoint *workflowCheckpoint
	if call.Registry != nil {
		// The parent checks child identity below. The provider validates its
		// private tools and saved planner state when the child resumes.
		checkpoint, err = decodeProviderChildCheckpoint(call, suspension)
	} else {
		checkpoint, err = decodeWorkflowCheckpoint(suspension, child)
	}
	if err != nil {
		return err
	}
	if err := validateCheckpointChildIdentity(call, checkpoint); err != nil {
		return err
	}
	if checkpoint.AgentID != string(child.route.ID) {
		return fmt.Errorf("child suspension agent %q does not match tool agent %q", checkpoint.AgentID, child.route.ID)
	}
	return nil
}

// validateCheckpointChildIdentity checks the saved parent call and retained
// operation contract. The child provider separately validates private tools.
func validateCheckpointChildIdentity(call ToolCall, checkpoint *workflowCheckpoint) error {
	parentAgent, ok := call.Subject.Executor.AsAgent()
	if !ok {
		return errors.New("agent child checkpoint requires an Agent parent subject")
	}
	if checkpoint.SessionID != call.SessionID || checkpoint.Context.ParentRunID == "" ||
		checkpoint.Context.ParentAgentID != agent.Ident(parentAgent) || checkpoint.Context.ParentToolCallID != call.ToolCallID ||
		checkpoint.Context.Tool != call.Name || !bytes.Equal(checkpoint.Context.ToolArgs, call.Payload) {
		return fmt.Errorf("child suspension does not match parent tool call %q", call.ToolCallID)
	}
	if (call.Registry == nil) != (checkpoint.Context.ToolRegistry == nil) {
		return fmt.Errorf("child suspension changed the selected contract for %q", call.Name)
	}
	if call.Registry != nil {
		selected, err := registrycontract.Read(call.Registry)
		if err != nil {
			return err
		}
		saved, err := registrycontract.Read(checkpoint.Context.ToolRegistry)
		if err != nil {
			return err
		}
		if checkpoint.Context.Tool != call.Name || call.Registry.Registry != checkpoint.Context.ToolRegistry.Registry ||
			!reflect.DeepEqual(selected.Registered, saved.Registered) {
			return fmt.Errorf("child suspension changed the selected contract for %q", call.Name)
		}
	}
	return nil
}

// childDefinitionForCall returns the immutable generated definition selected
// by one agent-tool call.
func childDefinitionForCall(call ToolCall, definition AgentDefinition) (AgentDefinition, error) {
	if call.Registry != nil {
		return registeredChildDefinition(call, definition)
	}
	spec, ok, err := lookupOwnedCallSpec(call, definition.spec)
	if err != nil {
		return AgentDefinition{}, err
	}
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

// validateCheckpointToolValues checks current result consumers before deciding
// which saved inputs still have an executable or typed consumer.
func validateCheckpointToolValues(checkpoint *workflowCheckpoint, definition AgentDefinition) error {
	calls, err := recordedToolCalls(checkpoint.State.ToolOutputs)
	if err != nil {
		return err
	}
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
		call, err := recordedResultCall(event.Name, event.ToolCallID, calls)
		if err != nil {
			return err
		}
		if _, err := decodeCheckpointToolEvent(event, call, definition.spec); err != nil {
			return err
		}
	}
	for i, record := range checkpoint.Batch.Records {
		if record.ChildSuspension != nil || record.MCPPending != nil {
			if record.ResultRecord != nil {
				return fmt.Errorf("unfinished child %q has a materialized result record", record.Call.ToolCallID)
			}
			continue
		}
		if record.ResultRecord != nil {
			if err := validateCheckpointResultRecord(record, checkpoint, definition); err != nil {
				return err
			}
			continue
		}
		if _, err := decodeCheckpointToolEvent(record.Result, record.Call, definition.spec); err != nil {
			return err
		}
		if record.ResultPublished && i >= checkpoint.Batch.Recorded {
			return fmt.Errorf("published result for call %q has no materialized result record", record.Call.ToolCallID)
		}
	}
	for _, pending := range checkpoint.Pending {
		if pending.Confirmation != nil {
			if err := decodeToolValue(definition, pending.Confirmation.Call, pending.Confirmation.DeniedResult.RawMessage(), false); err != nil {
				return fmt.Errorf("decode suspended denied result for %s: %w", pending.Confirmation.Call.Name, err)
			}
		}
	}
	return validateCheckpointInputs(checkpoint, definition)
}

// validateCheckpointToolRequest decodes one saved executable payload through
// the current generated input codec without executing the tool.
func validateCheckpointToolRequest(call ToolCall, definition AgentDefinition) error {
	if call.Registry != nil {
		if _, err := validateRegistrySource(definition, call); err != nil {
			return err
		}
	}
	if err := decodeToolValue(definition, call, call.Payload.RawMessage(), true); err != nil {
		return fmt.Errorf("decode suspended tool payload for %s: %w", call.Name, err)
	}
	return nil
}

// validateCheckpointToolOutput validates result bytes retained for planner
// resume. Input consumers are checked across the complete checkpoint separately.
func validateCheckpointToolOutput(output *planner.ToolOutput, definition AgentDefinition) error {
	if output == nil {
		return errors.New("run suspension contains nil tool output")
	}
	call := ToolCall{Name: output.Name, ToolCallID: output.ToolCallID, Payload: output.Payload, Registry: output.Registry}
	var spec *tools.ToolSpec
	if output.Failure == nil {
		registered, ok, err := lookupOwnedCallSpec(call, definition.spec)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("suspended tool result references unregistered tool %q", output.Name)
		}
		spec = &registered
	}
	if _, err := validatePersistedToolResult(
		spec,
		call,
		output.Result,
		output.ServerData,
		output.Blocks,
		output.Bounds,
		output.Failure,
	); err != nil {
		return fmt.Errorf("decode suspended tool result for %s: %w", output.Name, err)
	}
	return nil
}

// decodeToolValue validates a saved value against the selected contract.
func decodeToolValue(definition AgentDefinition, call ToolCall, data []byte, payload bool) error {
	spec, ok, err := lookupOwnedCallSpec(call, definition.spec)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("tool %q is not in the current agent definition", call.Name)
	}
	codec := spec.Result.Codec
	if payload {
		codec = spec.ExecutionPayloadCodec
	}
	if codec.FromJSON == nil {
		return fmt.Errorf("tool %q has no current generated codec", call.Name)
	}
	_, err = codec.FromJSON(data)
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
	if err := workflowcodec.DecodeJSON(suspension.Checkpoint, &checkpoint); err != nil {
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
// when an Agent workflow suspends. Later restore code can then trust the
// checkpoint without presence-based behavior.
func validateWorkflowCheckpoint(checkpoint *workflowCheckpoint) error {
	if checkpoint.AgentID == "" {
		return errors.New("run suspension checkpoint requires agent id")
	}
	if checkpoint.PreviousRunID == "" {
		return errors.New("run suspension checkpoint requires predecessor run id")
	}
	if checkpoint.HistoryEndID == "" {
		return errors.New("run suspension checkpoint requires an exact saved history position")
	}
	if !checkpoint.State.ResponseCommitted {
		return errors.New("run suspension checkpoint requires a committed planner response")
	}
	if checkpoint.Batch.Result == nil {
		return errors.New("run suspension checkpoint requires a planner result")
	}
	if err := validateCheckpointSubjects(checkpoint); err != nil {
		return err
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
		if err := validateRecoveryCatalog(
			checkpoint.State.PendingRecovery,
			checkpoint.State.PendingRecoveryCatalog,
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
	var providerBudget time.Duration
	if checkpoint.Policy != nil {
		providerBudget = checkpoint.Policy.ProviderRetryBudget
	}
	if providerBudget < 0 {
		return errors.New("run suspension checkpoint has negative provider recovery budget")
	}
	if (providerBudget > 0) != (checkpoint.ProviderRecovery != nil) {
		return errors.New("run suspension checkpoint provider recovery state must match its policy")
	}
	if recovery := checkpoint.ProviderRecovery; recovery != nil &&
		(recovery.Remaining < 0 || recovery.Remaining > providerBudget) {
		return errors.New("run suspension checkpoint has invalid remaining provider recovery budget")
	}
	return nil
}

// validateCheckpointSubjects checks identities physically saved in an Agent
// checkpoint before restore code can replace call metadata. Calls issued by an
// earlier run remain valid when the same Agent owns them. Planner outputs that
// contain only tool intent have no saved Subject and are checked separately.
func validateCheckpointSubjects(checkpoint *workflowCheckpoint) error {
	owner := genrunidentity.RunSubject{
		Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(checkpoint.AgentID)),
	}
	for i, call := range checkpoint.Batch.Result.ToolCalls {
		if !reflect.DeepEqual(call.Subject, owner) {
			return fmt.Errorf("run suspension planner call %d subject does not match checkpoint owner", i)
		}
	}
	for i, call := range checkpoint.Batch.Calls {
		if !reflect.DeepEqual(call.Subject, owner) {
			return fmt.Errorf("run suspension batch call %d subject does not match checkpoint owner", i)
		}
	}
	for i, record := range checkpoint.Batch.Records {
		if !reflect.DeepEqual(record.Call.Subject, owner) {
			return fmt.Errorf("run suspension recorded call %d subject does not match checkpoint owner", i)
		}
		if record.ResultRecord != nil && !reflect.DeepEqual(record.ResultRecord.Subject, owner) {
			return fmt.Errorf("run suspension result record %d subject does not match checkpoint owner", i)
		}
	}
	for i, pending := range checkpoint.Pending {
		if pending.Confirmation != nil && !reflect.DeepEqual(pending.Confirmation.Call.Subject, owner) {
			return fmt.Errorf("run suspension confirmation call %d subject does not match checkpoint owner", i)
		}
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
func validateWorkflowOutput(out *RunOutput, expectedSubject genrunidentity.RunSubject, expectedRunID string) error {
	if out == nil {
		return errors.New("workflow returned no output")
	}
	if _, err := genrunidentity.EncodeRunSubject(&out.Subject); err != nil {
		return fmt.Errorf("workflow output subject: %w", err)
	}
	if !reflect.DeepEqual(out.Subject, expectedSubject) {
		return errors.New("workflow output subject mismatch")
	}
	if out.RunID != expectedRunID {
		return fmt.Errorf("workflow output run mismatch: got=%q want=%q", out.RunID, expectedRunID)
	}
	terminalVariants := 0
	if out.Suspension != nil {
		terminalVariants++
	}
	// A typed result may carry its canonical observer message in Final. They
	// describe one completed invocation; suspension still excludes both.
	if out.FinalToolResult != nil || out.Final != nil {
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
	if _, err := genrunidentity.EncodeRunSubject(&input.Subject); err != nil {
		return fmt.Errorf("run input subject: %w", err)
	}
	if _, isAgent := input.Subject.Executor.AsAgent(); !isAgent {
		return errors.New("agent workflow requires an Agent run subject")
	}
	if input.Continuation == nil {
		if input.Policy != nil {
			if input.Policy.ProviderRetryBudget < 0 {
				return errors.New("provider recovery budget must not be negative")
			}
			return validateMaxRecoveryTurns(input.Policy.MaxRecoveryTurns)
		}
		return nil
	}
	if err := validateRunContinuationOperation(input.Continuation); err != nil {
		return err
	}
	if len(input.Labels) > 0 || len(input.Metadata) > 0 ||
		input.Policy != nil || input.ParentAgentID != "" ||
		input.ParentToolCallID != "" || input.Tool != "" || len(input.ToolArgs) > 0 ||
		input.ToolRegistry != nil || input.Operation != nil {
		return errors.New("run continuation cannot include caller-supplied checkpoint state")
	}
	return nil
}

// validateRunContinuationOperation rejects missing or mixed operations before
// a worker restores saved values or schedules any external work.
func validateRunContinuationOperation(input *api.RunContinuationInput) error {
	if (input.Response == nil) == (input.Cancellation == nil) {
		return errors.New("run continuation requires exactly one answer or cancellation")
	}
	if input.Response != nil {
		return validatePendingInputResponse(input.Response)
	}
	request := input.Cancellation
	if request.RunID == "" || request.Reason == "" {
		return errors.New("run continuation cancellation requires run id and reason")
	}
	if !utf8.ValidString(request.RunID) || !utf8.ValidString(request.Reason) {
		return errors.New("run continuation cancellation contains invalid UTF-8")
	}
	return nil
}

// validatePendingInput enforces the public request union before callers render
// it or select the corresponding response shape.
func validatePendingInput(input *api.PendingInput) error {
	if input == nil {
		return errors.New("pending input is nil")
	}
	if input.Kind != api.PendingInputKindMCP && input.MCP != nil {
		return errors.New("MCP input cannot accompany another pending request")
	}
	switch input.Kind {
	case api.PendingInputKindMCP:
		if input.MCP == nil || input.Confirmation != nil || input.Await != nil || input.MCP.ToolName == "" || input.MCP.ToolCallID == "" {
			return errors.New("MCP pending input requires an exact invocation")
		}
		if input.MCP.Requests != nil {
			return (&mcp.InputRequired{Requests: input.MCP.Requests}).Validate(mcp.InputSupport{Form: true, URL: true})
		}
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
	if response.MCP != nil {
		variants++
	}
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
	if response.MCP != nil && pending.Kind != api.PendingInputKindMCP {
		return errors.New("MCP response cannot complete another pending input")
	}
	switch pending.Kind {
	case api.PendingInputKindMCP:
		if response.MCP == nil || response.MCP.ToolCallID != pending.MCP.ToolCallID {
			return errors.New("MCP response does not match the pending tool call")
		}
		return (&mcp.InputRequired{Requests: pending.MCP.Requests}).ValidateResponses(response.MCP.Responses)
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
	if err := validateRunContinuationOperation(input.Continuation); err != nil {
		return err
	}
	if request := input.Continuation.Cancellation; request != nil {
		found, err := checkpointContainsRun(checkpoint, definition, request.RunID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("cancellation run %q is not saved in the selected checkpoint", request.RunID)
		}
		return nil
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

// checkpointContainsRun follows only the child calls proved by the selected
// checkpoint and generated definitions. Another run in the same session cannot
// be canceled through this continuation.
func checkpointContainsRun(checkpoint *workflowCheckpoint, definition AgentDefinition, runID string) (bool, error) {
	if checkpoint.PreviousRunID == runID {
		return true, nil
	}
	for _, record := range checkpoint.Batch.Records {
		if record.ChildSuspension == nil {
			continue
		}
		found, err := checkpointChildContainsRun(record.Call, record.ChildSuspension, definition, runID)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

// checkpointChildContainsRun checks one saved child and its descendants with
// the generated definition that owns that call. Current and restored batches
// use the same check before accepting cancellation for a child.
func checkpointChildContainsRun(call ToolCall, suspension *api.RunSuspension, definition AgentDefinition, runID string) (bool, error) {
	if call.Registry != nil {
		child, err := decodeProviderChildCheckpoint(call, suspension)
		if err != nil {
			return false, err
		}
		return providerCheckpointContainsRun(child, runID)
	}
	childDefinition, err := childDefinitionForCall(call, definition)
	if err != nil {
		return false, err
	}
	child, err := decodeWorkflowCheckpoint(suspension, childDefinition)
	if err != nil {
		return false, err
	}
	return checkpointContainsRun(child, childDefinition, runID)
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
	if pending.MCP != nil {
		return nil
	}
	if pending.Child != nil {
		record, ok := checkpointRecordByCallID(checkpoint.Batch.Records, pending.Child.ToolCallID)
		if !ok {
			return fmt.Errorf("pending child references unknown tool call %q", pending.Child.ToolCallID)
		}
		if record.Call.Registry != nil {
			child, err := decodeProviderChildCheckpoint(record.Call, pending.Child.Suspension)
			if err != nil {
				return err
			}
			return validateProviderContinuationResponse(child, response)
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
	return validateProvidedToolResults(*pending.Await, response.ToolResults, definition.spec)
}

// validateProvidedToolResults proves that one external result exists for each
// awaited call and that successful bytes satisfy the current generated codec.
func validateProvidedToolResults(
	await planner.AwaitItem,
	results *api.ToolResultsSet,
	lookup toolSpecLookup,
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
		if err := result.Blocks.Validate(); err != nil {
			return fmt.Errorf("tool result %q content: %w", result.ToolCallID, err)
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
		spec, ok := lookup(call.Name)
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
