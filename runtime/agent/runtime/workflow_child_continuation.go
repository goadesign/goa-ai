// Package runtime restores a saved agent-tool call on a new child workflow.
// Answer and cancellation continuations share child identity, published history,
// generated routing, and admission proof; the parent waits for the child's result.
package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage"
)

// applyChildContinuation answers the saved child's first pending request. A
// further suspension replaces that request; a complete result closes the call.
func (l *workflowLoop) applyChildContinuation(batch *stepBatch, pending *checkpointChildContinuation, response *api.PendingInputResponse) ([]checkpointPendingInput, error) {
	info, index, err := l.startChildContinuation(batch, pending, &api.RunContinuationInput{Response: response})
	if err != nil {
		return nil, err
	}
	record := &batch.records[index]
	// The accepted child handle owns cancellation until its wait finishes.
	// Only a new suspension returns ownership to the saved parent record.
	record.childSuspension = nil
	out, err := awaitAgentChild(l.wfCtx, info.handle, l.wfCtx.Context())
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("child continuation returned no output")
	}
	if err := validateWorkflowOutput(out, info.cfg.Definition.route.ID, info.nestedRun.RunID); err != nil {
		return nil, err
	}
	if out.Suspension != nil {
		record.childSuspension = out.Suspension
		return []checkpointPendingInput{{Child: &checkpointChildContinuation{
			ToolCallID: record.call.ToolCallID,
			Suspension: out.Suspension,
		}}}, nil
	}
	result, err := l.r.adaptAgentChildOutput(info.cfg, &info.call, info.nestedRun, out)
	if err != nil {
		return nil, err
	}
	record.result = result
	record.childSuspension = nil
	record.requiresResume = true
	return nil, nil
}

// startChildContinuation publishes the exact saved call and operation before
// starting its generated child route. Both answer and cancellation use this path.
func (l *workflowLoop) startChildContinuation(batch *stepBatch, pending *checkpointChildContinuation, operation *api.RunContinuationInput) (agentChildFutureInfo, int, error) {
	recordIndex := -1
	for i := range batch.records {
		if batch.records[i].call.ToolCallID == pending.ToolCallID {
			if recordIndex >= 0 {
				return agentChildFutureInfo{}, -1, fmt.Errorf("child continuation has duplicate tool_call_id %s", pending.ToolCallID)
			}
			recordIndex = i
		}
	}
	if recordIndex < 0 {
		return agentChildFutureInfo{}, -1, fmt.Errorf("child continuation references unknown tool_call_id %s", pending.ToolCallID)
	}
	record := &batch.records[recordIndex]
	if record.childSuspension == nil || !reflect.DeepEqual(record.childSuspension, pending.Suspension) {
		return agentChildFutureInfo{}, -1, fmt.Errorf("child continuation does not match tool_call_id %s", pending.ToolCallID)
	}

	cfg, err := l.r.selectedAgentToolConfig(record.call)
	if err != nil {
		return agentChildFutureInfo{}, -1, err
	}

	if err := validateCheckpointChild(record.call, pending.Suspension, l.reg.Definition); err != nil {
		return agentChildFutureInfo{}, -1, err
	}

	currentCall := retargetToolRequest(record.call, l.input, &l.base.RunContext)
	nested := run.Context{
		Tool:             currentCall.Name,
		RunID:            childContinuationRunID(currentCall, pending.Suspension),
		SessionID:        currentCall.SessionID,
		TurnID:           currentCall.TurnID,
		ParentToolCallID: currentCall.ToolCallID,
		ParentRunID:      currentCall.RunID,
		ParentAgentID:    currentCall.AgentID,
		ToolArgs:         append(rawjson.Message(nil), currentCall.Payload...),
		ToolRegistry:     currentCall.Registry.Clone(),
		Labels:           cloneLabels(currentCall.Labels),
	}
	childInput := &RunInput{
		AgentID: cfg.Definition.route.ID, ParentRunID: nested.ParentRunID,
		RunID: nested.RunID, SessionID: nested.SessionID, TurnID: nested.TurnID,
		Continuation: &api.RunContinuationInput{Suspension: pending.Suspension, Response: operation.Response, Cancellation: operation.Cancellation},
	}
	checkpoint, err := prepareContinuation(childInput, cfg.Definition)
	if err != nil {
		return agentChildFutureInfo{}, -1, err
	}
	seedStore := workflowSeedWriter{r: l.r}
	seed, err := seedStore.BeginRunSeed(l.wfCtx.Context(), storage.SeedDeclaration{
		AgentID: string(childInput.AgentID), RunID: childInput.RunID, SessionID: childInput.SessionID,
		CommandID: childInput.RunID, AttemptID: childInput.RunID,
		Kind: storage.SeedContinuation, SourceRunID: checkpoint.PreviousRunID, SourceEndID: checkpoint.HistoryEndID,
	})
	if err != nil {
		return agentChildFutureInfo{}, -1, err
	}
	writer := initialHistoryWriter{store: seedStore, runID: childInput.RunID, attemptID: childInput.RunID, endID: storage.EmptySeedEndID}
	if err := writer.appendPrefix(l.wfCtx.Context(), seed.Source); err != nil {
		return agentChildFutureInfo{}, -1, err
	}
	childInput.SeedEndID = writer.endID
	compiled, err := json.Marshal(childInput)
	if err != nil {
		return agentChildFutureInfo{}, -1, err
	}
	if err := writer.publish(l.wfCtx.Context(), compiled); err != nil {
		return agentChildFutureInfo{}, -1, err
	}

	route := cfg.Definition.route
	handle, err := l.wfCtx.StartChildWorkflow(l.wfCtx.Context(), engine.ChildWorkflowRequest{
		ID:        childInput.RunID,
		Workflow:  route.WorkflowName,
		TaskQueue: route.DefaultTaskQueue,
		Input:     childInput,
	})
	if err != nil {
		return agentChildFutureInfo{}, -1, err
	}
	return agentChildFutureInfo{
		handle: handle, call: currentCall, cfg: cfg, nestedRun: nested, startTime: l.wfCtx.Now(),
	}, recordIndex, nil
}

// childContinuationRunID binds this child to one parent call and one proved
// suspension. A new question and its cancellation cannot reuse a child ID;
// replay of the same source retains the exact ID without depending on timing.
func childContinuationRunID(call ToolCall, suspension *api.RunSuspension) string {
	identity := []byte("goa-ai-child-continuation-v1")
	identity = appendLengthDelimited(identity, call.RunID)
	identity = appendLengthDelimited(identity, string(call.Name))
	identity = appendLengthDelimited(identity, call.ToolCallID)
	identity = appendLengthDelimited(identity, suspension.ID)
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:])
}
