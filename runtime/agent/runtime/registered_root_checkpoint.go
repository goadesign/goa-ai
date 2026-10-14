// Package runtime saves one original service invocation and its next provider
// operation in each checkpoint. It saves no planner state, transcript, or
// client-owned cursor.
package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	registeredRootState struct {
		queue        string
		call         api.ToolInput
		pending      *api.PendingExecution
		task         *taskExecution
		confirmation *api.PendingConfirmation
		denied       rawjson.Message
		decision     *api.ConfirmationDecision
		predecessor  string
	}
	registeredRootCheckpoint struct {
		Queue        string
		Input        api.RunInput
		Call         api.ToolInput
		Pending      *api.PendingExecution
		Confirmation *api.PendingConfirmation
		Denied       rawjson.Message
		Task         *taskExecution
	}
)

const registeredRootCheckpointVersion = "service-tool-v1"

// advance retains the invocation and original arguments while selecting exactly
// one new provider operation. Replayed activities reuse the saved sequence.
func (state *registeredRootState) advance(next *api.ExecutionContinuation) error {
	if state.call.ExecutionSequence == ^uint64(0) {
		return errors.New("registered execution sequence exhausted")
	}
	state.call.ExecutionSequence++
	state.call.ExecutionContinuation = next
	return nil
}

// restoreRegisteredRoot validates fresh input or restores one exact suspended
// service invocation. A continuation supplies only its answer or cancellation.
func restoreRegisteredRoot(submitted *api.RunInput) (*api.RunInput, *registeredRootState, error) {
	if submitted == nil {
		return nil, nil, errors.New("registered workflow input is required")
	}
	if submitted.Continuation == nil {
		if _, err := validateRegisteredRootInput(submitted); err != nil {
			return nil, nil, err
		}
		root, _ := submitted.RegisteredInvocation.Invocation.Invocation.AsRoot()
		if submitted.RunID != root.RunID || submitted.SeedEndID != "" {
			return nil, nil, errors.New("fresh registered root changed its original identity or supplied history")
		}
		input := *submitted
		subject, _ := input.Subject.Executor.AsServiceTool()
		state := &registeredRootState{call: api.ToolInput{Invocation: *input.RegisteredInvocation.Invocation, OriginalArguments: input.RegisteredInvocation.Arguments, Registry: input.ToolRegistry.Clone(), RunID: root.RunID, Subject: input.Subject, ToolName: tools.Ident(subject.Tool), ToolCallID: root.RunID, Payload: input.RegisteredInvocation.Arguments, SessionID: input.SessionID, Labels: cloneLabels(input.Labels)}}
		return &input, state, nil
	}
	if submitted.TurnID != "" || submitted.RegisteredInvocation != nil || submitted.ToolRegistry != nil || submitted.Operation != nil || submitted.Policy != nil || submitted.Tool != "" || len(submitted.ToolArgs) != 0 || len(submitted.Labels) != 0 || len(submitted.Metadata) != 0 || submitted.SeedEndID != storage.EmptySeedEndID || submitted.ParentRunID != "" || submitted.ParentAgentID != "" || submitted.ParentToolCallID != "" {
		return nil, nil, errors.New("registered continuation cannot supply saved execution state")
	}
	continuation := submitted.Continuation
	if err := validateRunContinuationOperation(continuation); err != nil {
		return nil, nil, err
	}
	checkpoint, err := decodeRegisteredRootCheckpoint(continuation.Suspension)
	if err != nil {
		return nil, nil, err
	}
	if !reflect.DeepEqual(submitted.Subject, checkpoint.Input.Subject) || submitted.SessionID != checkpoint.Input.SessionID || submitted.RunID == "" || submitted.RunID == checkpoint.Input.RunID {
		return nil, nil, errors.New("registered continuation changed owner or reused predecessor identity")
	}
	input := checkpoint.Input
	state := &registeredRootState{queue: checkpoint.Queue, call: checkpoint.Call, pending: checkpoint.Pending, confirmation: checkpoint.Confirmation, denied: checkpoint.Denied, task: cloneTaskExecution(checkpoint.Task), predecessor: input.RunID}
	input.RunID = submitted.RunID
	input.SeedEndID = submitted.SeedEndID
	if continuation.Cancellation != nil {
		if continuation.Cancellation.RunID != state.predecessor {
			return nil, nil, errors.New("service cancellation does not target its saved predecessor")
		}
		return &input, state, nil
	}
	if state.confirmation != nil {
		decision := continuation.Response.Confirmation
		if decision == nil || decision.ID != state.confirmation.ID {
			return nil, nil, errors.New("confirmation response does not match saved request")
		}
		state.decision = decision
		state.confirmation = nil
		if decision.Approved {
			state.denied = nil
		}
		return &input, state, nil
	}
	response := continuation.Response.MCP
	if response == nil || response.ToolCallID != state.call.ToolCallID {
		return nil, nil, errors.New("registered continuation answer does not match pending invocation")
	}
	hostInput := pendingHostInput(state.pending)
	if err := hostInput.ValidateResponses(response.Responses); err != nil {
		return nil, nil, err
	}
	var next *api.ExecutionContinuation
	if id, _, _, task := state.pending.AsTaskInput(); task {
		next, err = tooloperation.NewTaskUpdate(id, response.Responses)
	} else {
		next, err = tooloperation.NewInput(&mcp.CallContinuation{RequestState: hostInput.RequestState, InputResponses: response.Responses})
	}
	if err != nil {
		return nil, nil, err
	}
	if err := state.advance(next); err != nil {
		return nil, nil, err
	}
	return &input, state, nil
}

// registeredRootSuspension saves provider state privately and returns only its
// input questions through the existing public pending-input representation.
func registeredRootSuspension(input *api.RunInput, state *registeredRootState) (*api.RunSuspension, error) {
	checkpoint := registeredRootCheckpoint{Queue: state.queue, Input: *input, Call: state.call, Pending: state.pending, Confirmation: state.confirmation, Denied: state.denied, Task: cloneTaskExecution(state.task)}
	checkpoint.Input.SeedEndID = ""
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	var pending *api.PendingInput
	if state.confirmation != nil {
		pending = &api.PendingInput{Kind: api.PendingInputKindConfirmation, Confirmation: state.confirmation}
	} else {
		pending = &api.PendingInput{Kind: api.PendingInputKindMCP, MCP: &api.PendingMCPInput{ToolName: state.call.ToolName, ToolCallID: state.call.ToolCallID, Requests: pendingHostInput(state.pending).Requests}}
	}
	return &api.RunSuspension{ID: hex.EncodeToString(digest[:]), Version: registeredRootCheckpointVersion, Checkpoint: data, Pending: []*api.PendingInput{pending}, RequiredTools: []tools.Ident{state.call.ToolName}}, nil
}

// decodeRegisteredRootCheckpoint verifies the complete saved identity and the
// public questions against private provider state before accepting an answer.
func decodeRegisteredRootCheckpoint(suspension *api.RunSuspension) (*registeredRootCheckpoint, error) {
	if suspension == nil || suspension.Version != registeredRootCheckpointVersion {
		return nil, errors.New("registered service checkpoint version is required")
	}
	digest := sha256.Sum256(suspension.Checkpoint)
	if suspension.ID != hex.EncodeToString(digest[:]) {
		return nil, errors.New("registered checkpoint identity differs")
	}
	var checkpoint registeredRootCheckpoint
	if err := workflowcodec.DecodeJSON(suspension.Checkpoint, &checkpoint); err != nil {
		return nil, err
	}
	resolution, err := validateRegisteredRootInput(&checkpoint.Input)
	if err != nil {
		return nil, err
	}
	if checkpoint.Queue == "" {
		return nil, errors.New("registered checkpoint requires its worker queue")
	}
	if (checkpoint.Pending == nil) == (checkpoint.Confirmation == nil) {
		return nil, errors.New("registered checkpoint requires one unfinished state")
	}
	if checkpoint.Pending != nil {
		if len(checkpoint.Denied) != 0 {
			return nil, errors.New("provider input cannot include confirmation result")
		}
		if err := checkpoint.Pending.Validate(); err != nil {
			return nil, err
		}
		if err := validateTaskInputState(checkpoint.Pending, checkpoint.Task); err != nil {
			return nil, err
		}
		if _, _, waiting := checkpoint.Pending.AsTaskWait(); waiting {
			return nil, errors.New("working provider Task cannot suspend for input")
		}
	} else {
		if checkpoint.Task != nil || checkpoint.Call.ExecutionSequence != 0 || checkpoint.Call.ExecutionContinuation != nil {
			return nil, errors.New("confirmation cannot contain provider execution state")
		}
		if err := validatePendingInput(&api.PendingInput{Kind: api.PendingInputKindConfirmation, Confirmation: checkpoint.Confirmation}); err != nil {
			return nil, err
		}
		confirmation := checkpoint.Confirmation
		if confirmation.ID != checkpoint.Call.ToolCallID+"/confirmation" || confirmation.ToolName != checkpoint.Call.ToolName || confirmation.ToolCallID != checkpoint.Call.ToolCallID || !bytes.Equal(confirmation.Payload, checkpoint.Call.Payload) {
			return nil, errors.New("registered confirmation differs from saved invocation")
		}
		spec, exists := resolution.Specs[checkpoint.Call.ToolName]
		if !exists {
			return nil, errors.New("registered confirmation has no retained result schema")
		}
		if _, err := spec.Result.Codec.FromJSON(checkpoint.Denied); err != nil {
			return nil, err
		}
	}
	root, _ := checkpoint.Input.RegisteredInvocation.Invocation.Invocation.AsRoot()
	subject, _ := checkpoint.Input.Subject.Executor.AsServiceTool()
	call := checkpoint.Call
	if call.RunID != root.RunID || call.ToolCallID != root.RunID || call.ToolName.String() != subject.Tool || call.SessionID != checkpoint.Input.SessionID || !reflect.DeepEqual(call.Subject, checkpoint.Input.Subject) || !reflect.DeepEqual(call.Invocation, *checkpoint.Input.RegisteredInvocation.Invocation) || !reflect.DeepEqual(call.Registry, checkpoint.Input.ToolRegistry) || !reflect.DeepEqual(call.Labels, checkpoint.Input.Labels) || !bytes.Equal(call.OriginalArguments, checkpoint.Input.RegisteredInvocation.Arguments) || !bytes.Equal(call.Payload, checkpoint.Input.RegisteredInvocation.Arguments) {
		return nil, errors.New("registered checkpoint call differs from original request")
	}
	expected, err := registeredRootSuspension(&checkpoint.Input, &registeredRootState{queue: checkpoint.Queue, call: call, pending: checkpoint.Pending, confirmation: checkpoint.Confirmation, denied: checkpoint.Denied, task: checkpoint.Task})
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(expected.Pending, suspension.Pending) || !reflect.DeepEqual(expected.RequiredTools, suspension.RequiredTools) {
		return nil, errors.New("registered checkpoint input questions differ from saved provider state")
	}
	return &checkpoint, nil
}
