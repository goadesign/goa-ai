// Package temporal keeps read-only workflow query helpers separate from the
// engine's write path. These methods translate Temporal-specific visibility and
// completion state into the smaller engine contract consumed by the runtime.
package temporal

import (
	"context"
	"errors"
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	temporalsdk "go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

// QueryWorkflow exposes Temporal query execution through the engine's durable
// workflow identity. It exists only for read-only integrations that need a
// narrow query capability instead of the full Temporal client surface.
func (e *Engine) QueryWorkflow(ctx context.Context, workflowID, queryType string, args ...any) (converter.EncodedValue, error) {
	if workflowID == "" {
		return nil, fmt.Errorf("workflow id is required")
	}
	if queryType == "" {
		return nil, fmt.Errorf("query type is required")
	}
	return e.client.QueryWorkflow(ctx, workflowID, "", queryType, args...)
}

// QueryRunCompletion returns Temporal's current workflow status and retrieves
// the final output only after Temporal reports that the workflow has closed.
func (e *Engine) QueryRunCompletion(ctx context.Context, workflowID string) (engine.RunCompletion, error) {
	if workflowID == "" {
		return engine.RunCompletion{}, fmt.Errorf("workflow id is required")
	}
	desc, err := e.client.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		return engine.RunCompletion{}, mapDescribeWorkflowExecutionError(err)
	}
	info := desc.GetWorkflowExecutionInfo()
	executionID := info.GetExecution().GetRunId()
	if executionID == "" {
		return engine.RunCompletion{}, errors.New("workflow execution identity is missing")
	}
	digest, err := acceptedRequestDigest(info.GetMemo())
	if err != nil {
		return engine.RunCompletion{}, err
	}
	input, err := e.acceptedInputFromHistory(ctx, workflowID, executionID)
	if err != nil {
		return engine.RunCompletion{}, err
	}
	status := queryRunStatusFromInfo(info)
	completion := engine.RunCompletion{Status: status, StartRequestDigest: digest, Input: input}
	if !isTerminalRunStatus(status) {
		return completion, nil
	}
	// A closed attempt with a successor has not completed the whole execution.
	// Inspect its exact close event before allowing callers to persist an outcome.
	history := e.client.GetWorkflowHistory(ctx, workflowID, executionID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT)
	if !history.HasNext() {
		return engine.RunCompletion{}, errors.New("closed workflow has no close event")
	}
	closed, err := history.Next()
	if err != nil {
		return engine.RunCompletion{}, mapDescribeWorkflowExecutionError(err)
	}
	if closed.GetWorkflowExecutionCompletedEventAttributes().GetNewExecutionRunId() != "" ||
		closed.GetWorkflowExecutionFailedEventAttributes().GetNewExecutionRunId() != "" ||
		closed.GetWorkflowExecutionTimedOutEventAttributes().GetNewExecutionRunId() != "" ||
		closed.GetWorkflowExecutionContinuedAsNewEventAttributes() != nil {
		return engine.RunCompletion{}, errors.New("selected workflow attempt has a successor; completion requires the current execution")
	}
	completion.CompletedAt = info.GetCloseTime().AsTime().UTC()
	var out *api.RunOutput
	if err := e.client.GetWorkflow(ctx, workflowID, executionID).GetWithOptions(ctx, &out, client.WorkflowRunGetOptions{DisableFollowingRuns: true}); err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			return engine.RunCompletion{}, engine.ErrWorkflowNotFound
		}
		var workflowErr *temporalsdk.WorkflowExecutionError
		if errors.As(err, &workflowErr) {
			completion.WorkflowError = err
			return completion, nil
		}
		return engine.RunCompletion{}, err
	}
	completion.Output = out
	return completion, nil
}

// isTerminalRunStatus reports whether Temporal has a final workflow result.
func isTerminalRunStatus(status engine.RunStatus) bool {
	switch status {
	case engine.RunStatusCompleted, engine.RunStatusTimedOut,
		engine.RunStatusFailed, engine.RunStatusCanceled:
		return true
	case engine.RunStatusPending, engine.RunStatusRunning, engine.RunStatusPaused:
		return false
	default:
		panic("temporal engine: unsupported run status: " + string(status))
	}
}

// queryRunStatusFromInfo maps Temporal execution info into the engine's coarse
// lifecycle contract. Closed executions retain Temporal's terminal outcome so
// cross-process repair can recover the exact completed or suspended result.
func queryRunStatusFromInfo(info *workflowpb.WorkflowExecutionInfo) engine.RunStatus {
	if info == nil {
		return engine.RunStatusPending
	}
	if info.GetCloseTime() == nil {
		if info.GetStatus() == enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED {
			return engine.RunStatusPaused
		}
		return engine.RunStatusRunning
	}
	switch info.GetStatus() {
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return engine.RunStatusCompleted
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return engine.RunStatusCanceled
	case enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return engine.RunStatusTimedOut
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return engine.RunStatusFailed
	case enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED,
		enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED:
		panic(fmt.Sprintf("temporal engine: closed workflow has non-terminal status %s", info.GetStatus()))
	default:
		panic(fmt.Sprintf("temporal engine: closed workflow has unsupported status %s", info.GetStatus()))
	}
}

func mapDescribeWorkflowExecutionError(err error) error {
	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return engine.ErrWorkflowNotFound
	}
	return err
}

// mapWorkflowMutationError normalizes Temporal workflow mutation failures into
// engine-level contract errors consumed by runtime callers.
func mapWorkflowMutationError(err error) error {
	if err == nil {
		return nil
	}

	var notFound *serviceerror.NotFound
	if errors.As(err, &notFound) {
		return engine.ErrWorkflowNotFound
	}
	var failedPrecondition *serviceerror.FailedPrecondition
	if errors.As(err, &failedPrecondition) {
		return engine.ErrWorkflowCompleted
	}

	return err
}

// acceptedRequestDigest decodes the engine-owned request identity from one
// execution's saved metadata. Missing or malformed metadata cannot authorize repair.
func acceptedRequestDigest(memo *commonpb.Memo) ([32]byte, error) {
	payload := memo.GetFields()[startrecipe.MemoKey]
	if payload == nil {
		return [32]byte{}, errors.New("workflow accepted request digest is missing")
	}
	var value []byte
	if err := NewAgentDataConverter().FromPayload(payload, &value); err != nil {
		return [32]byte{}, fmt.Errorf("decode accepted request digest: %w", err)
	}
	if len(value) != 32 {
		return [32]byte{}, errors.New("workflow accepted request digest must contain 32 bytes")
	}
	digest := [32]byte(value)
	if digest == ([32]byte{}) {
		return [32]byte{}, errors.New("workflow accepted request digest is missing")
	}
	return digest, nil
}

// acceptedInputFromHistory decodes the workflow's started event input for this
// execution.
func (e *Engine) acceptedInputFromHistory(ctx context.Context, workflowID, executionID string) (*api.RunInput, error) {
	history := e.client.GetWorkflowHistory(ctx, workflowID, executionID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	if !history.HasNext() {
		return nil, errors.New("workflow has no start event")
	}
	startedEvent, err := history.Next()
	if err != nil {
		return nil, mapDescribeWorkflowExecutionError(err)
	}
	if startedEvent.GetEventType() != enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED {
		return nil, fmt.Errorf("workflow history first event is %s, want %s", startedEvent.GetEventType(), enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED)
	}
	started := startedEvent.GetWorkflowExecutionStartedEventAttributes()
	if started == nil || len(started.GetInput().GetPayloads()) == 0 {
		return nil, errors.New("workflow started event has no input")
	}
	var input *api.RunInput
	if err := NewAgentDataConverter().FromPayloads(started.GetInput(), &input); err != nil {
		return nil, fmt.Errorf("decode workflow start input: %w", err)
	}
	if input == nil || input.RunID != workflowID {
		return nil, errors.New("workflow started input run id does not match workflow id")
	}
	return input, nil
}
