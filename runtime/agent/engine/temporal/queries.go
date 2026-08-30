// Package temporal keeps read-only workflow query helpers separate from the
// engine's write path. These methods translate Temporal-specific visibility and
// completion state into the smaller engine contract consumed by the runtime.
package temporal

import (
	"context"
	"errors"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/sdk/converter"
	temporalsdk "go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
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
	status := queryRunStatusFromInfo(desc.GetWorkflowExecutionInfo())
	if !isTerminalRunStatus(status) {
		return engine.RunCompletion{Status: status}, nil
	}
	completedAt := desc.GetWorkflowExecutionInfo().GetCloseTime().AsTime().UTC()
	var out *api.RunOutput
	if err := e.client.GetWorkflow(ctx, workflowID, "").Get(ctx, &out); err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			return engine.RunCompletion{}, engine.ErrWorkflowNotFound
		}
		var workflowErr *temporalsdk.WorkflowExecutionError
		if errors.As(err, &workflowErr) {
			return engine.RunCompletion{Status: status, CompletedAt: completedAt, WorkflowError: err}, nil
		}
		return engine.RunCompletion{}, err
	}
	return engine.RunCompletion{Status: status, CompletedAt: completedAt, Output: out}, nil
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
