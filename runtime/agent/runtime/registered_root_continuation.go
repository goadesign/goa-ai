// Package runtime stores the complete successor request before submitting a
// direct or registered continuation to the engine. Durable input and cancellation
// workers recover the same request after a lost reply; clients do not retry completion.
package runtime

import (
	"context"
	"reflect"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
)

// StartToolContinuation submits an answer and the successor workflow Memo
// already durably owned by the calling service. The saved predecessor supplies
// the original operation and identity; Memo is chosen separately for each successor.
// Passing nil stores empty Memo. A changed answer or Memo is rejected on recovery.
func (r *Runtime) StartToolContinuation(ctx context.Context, predecessorRunID, runID string, response *api.PendingInputResponse, memo map[string]any) (engine.WorkflowHandle, error) {
	request, err := r.prepareOperationContinuationRequest(ctx, predecessorRunID, runID, &api.RunContinuationInput{Response: response}, memo)
	if err != nil {
		return nil, err
	}
	return r.startWorkflow(ctx, request)
}

// EnsureToolContinuationCompletion delivers the engine outcome for the exact
// saved successor, including its caller-supplied Memo. Durable workers call it
// until storage owns the terminal state. A changed answer or Memo is rejected.
func (r *Runtime) EnsureToolContinuationCompletion(ctx context.Context, predecessorRunID, runID string, response *api.PendingInputResponse, memo map[string]any) error {
	request, err := r.prepareOperationContinuationRequest(ctx, predecessorRunID, runID, &api.RunContinuationInput{Response: response}, memo)
	if err != nil {
		return err
	}
	return r.ensureOperationCompletion(ctx, request)
}

// prepareOperationContinuationRequest recovers published bytes before reading
// the suspended provider state, so retries cannot select new execution values.
func (r *Runtime) prepareOperationContinuationRequest(ctx context.Context, predecessorRunID, runID string, continuation *api.RunContinuationInput, memo map[string]any) (engine.WorkflowStartRequest, error) {
	launch, err := encodeWorkflowLaunch(WorkflowOptions{Memo: memo})
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	owner, err := r.Store.LoadRun(ctx, predecessorRunID)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	operation := storage.PreparationOperation{Subject: owner.Subject, SessionID: owner.SessionID, RunID: runID, CommandID: runID}
	accepted, found, err := r.Store.FindRunPreparation(ctx, operation)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	if found {
		data, err := readPreparation(ctx, r.Store, accepted)
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		prepared, err := startrecipe.ParsePreparedRequest(data)
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		input := prepared.Request.Input
		if accepted.Seed.Declaration.PredecessorRunID != predecessorRunID || input.RunID != runID || !reflect.DeepEqual(input.Subject, owner.Subject) || input.SessionID != owner.SessionID || input.SeedEndID != accepted.Seed.EndID || input.Continuation == nil {
			return engine.WorkflowStartRequest{}, storage.ErrSeedConflict
		}
		// Compare the supplied answer and Memo with the saved engine request.
		// Encoding keeps equivalent empty content lists from causing conflicts.
		candidate := prepared.Request
		candidateInput := *input
		candidateContinuation := *input.Continuation
		candidateContinuation.Response = continuation.Response
		candidateContinuation.Cancellation = continuation.Cancellation
		candidateInput.Continuation = &candidateContinuation
		candidate.Input = &candidateInput
		candidate.Memo = launch.memo
		snapshot, err := startrecipe.SnapshotRequest(candidate)
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		if snapshot.Digest != prepared.Digest {
			return engine.WorkflowStartRequest{}, storage.ErrSeedConflict
		}
		if _, service := owner.Subject.Executor.AsServiceTool(); service {
			if _, _, err := restoreRegisteredRoot(input); err != nil {
				return engine.WorkflowStartRequest{}, err
			}
		} else {
			checkpoint, err := decodeProviderCheckpoint(input.Continuation.Suspension)
			if err != nil {
				return engine.WorkflowStartRequest{}, err
			}
			if _, err := operationCheckpointDeclaration(checkpoint); err != nil {
				return engine.WorkflowStartRequest{}, err
			}
		}
		return prepared.Request, nil
	}
	turnID, err := lifecycle.RegisteredRunTurnID(owner.Subject, runID, predecessorRunID)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	suspension, err := r.LoadRunSuspension(ctx, predecessorRunID)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	if _, native := owner.Subject.Executor.AsAgent(); native {
		checkpoint, err := decodeProviderCheckpoint(suspension)
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		declaration, err := operationCheckpointDeclaration(checkpoint)
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		route := declaration.ConsumerContract.Agent
		definition := NewAgentDefinition(AgentRoute{ID: agent.Ident(route.Executor), WorkflowName: route.Workflow, DefaultTaskQueue: route.Queue}, nil, nil, nil, nil, nil, nil)
		input, writer, err := r.buildStoredContinuationRunInput(ctx, definition, owner.SessionID, predecessorRunID, runID, turnID, continuation, runID, runID)
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		request, err := prepareRunWithDefinition(input, launch, definition, owner.SessionID != "")
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		snapshot, err := startrecipe.SnapshotRequest(request)
		if err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		request = snapshot.Request
		if _, err := publishPreparedRun(ctx, writer, definition.route.ID, request, "", runID); err != nil {
			return engine.WorkflowStartRequest{}, err
		}
		return request, nil
	}
	checkpoint, err := decodeRegisteredRootCheckpoint(suspension)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	selected := *continuation
	selected.Suspension = suspension
	input := &api.RunInput{Subject: owner.Subject, RunID: runID, SessionID: owner.SessionID, TurnID: turnID, SeedEndID: storage.EmptySeedEndID, Continuation: &selected}
	if _, _, err := restoreRegisteredRoot(input); err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	request := engine.WorkflowStartRequest{ID: runID, Workflow: registeredRootWorkflow, TaskQueue: checkpoint.Queue, Input: input, Memo: launch.memo}
	prepared, err := startrecipe.NewPreparedRequest("", request, "")
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	data, err := prepared.MarshalBinary()
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	declaration := storage.SeedDeclaration{Subject: owner.Subject, SessionID: owner.SessionID, RunID: runID, CommandID: runID, AttemptID: runID, Kind: storage.SeedLiteral, PredecessorRunID: predecessorRunID}
	if _, err := bindHistoryOperation(ctx, r.Store, declaration, owner.Origin); err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	writer, err := stageLiteralHistory(ctx, r.Store, declaration, nil)
	if err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	if err := writer.publish(ctx, data); err != nil {
		return engine.WorkflowStartRequest{}, err
	}
	return prepared.Request, nil
}
