// Package temporal isolates workflow-handle and cancellation helpers so the
// engine's main file can focus on registration and workflow start semantics.
package temporal

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

const (
	cancellationUpdateName         = "goa_ai_request_cancellation"
	cancellationUpdateID           = "goa_ai_cancellation"
	cancellationConflictErrorType  = "goa_ai_cancellation_conflict"
	cancellationCompletedErrorType = "goa_ai_workflow_completed"
)

type workflowHandle struct {
	run    client.WorkflowRun
	client client.Client
}

func (h *workflowHandle) Wait(ctx context.Context) (*api.RunOutput, error) {
	var out *api.RunOutput
	if err := h.run.Get(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *workflowHandle) Cancel(ctx context.Context) error {
	return h.client.CancelWorkflow(ctx, h.run.GetID(), "")
}

// RequestCancellation waits for workflow code to record the reason and cancel
// its execution scope in the same workflow update.
func (e *Engine) RequestCancellation(ctx context.Context, request engine.CancellationRequest) error {
	if request.RunID == "" {
		return errors.New("run id is required")
	}
	if request.Reason == "" {
		return errors.New("cancellation reason is required")
	}
	acceptedReason, err := e.requestCancellationUpdate(ctx, request)
	if err != nil {
		return err
	}
	if acceptedReason != request.Reason {
		return &engine.CancellationConflictError{RunID: request.RunID, Reason: request.Reason}
	}
	return nil
}

// requestCancellationUpdate returns the reason saved by the workflow update.
// A closed workflow can still answer an exact retry from its completed update.
func (e *Engine) requestCancellationUpdate(ctx context.Context, request engine.CancellationRequest) (string, error) {
	options := client.UpdateWorkflowOptions{
		UpdateID:     cancellationUpdateID,
		WorkflowID:   request.RunID,
		UpdateName:   cancellationUpdateName,
		Args:         []any{request},
		WaitForStage: client.WorkflowUpdateStageCompleted,
	}
	handle, err := e.client.UpdateWorkflow(ctx, options)
	workflowCompleted := false
	if err != nil {
		mapped := mapWorkflowMutationError(err)
		if !errors.Is(mapped, engine.ErrWorkflowCompleted) {
			return "", mapped
		}
		workflowCompleted = true
		handle = e.client.GetWorkflowUpdateHandle(client.GetWorkflowUpdateHandleOptions{
			WorkflowID: request.RunID,
			UpdateID:   cancellationUpdateID,
		})
	}
	var acceptedReason string
	if err := handle.Get(ctx, &acceptedReason); err != nil {
		mapped := mapCancellationUpdateError(err)
		if workflowCompleted && errors.Is(mapped, engine.ErrWorkflowNotFound) {
			return "", engine.ErrWorkflowCompleted
		}
		return "", mapped
	}
	return acceptedReason, nil
}

// mapCancellationUpdateError restores the engine conflict returned by workflow
// code and preserves every other Temporal error.
func mapCancellationUpdateError(err error) error {
	var applicationErr *temporal.ApplicationError
	if !errors.As(err, &applicationErr) {
		return mapWorkflowMutationError(err)
	}
	if applicationErr.Type() == cancellationCompletedErrorType {
		return engine.ErrWorkflowCompleted
	}
	if applicationErr.Type() != cancellationConflictErrorType {
		return mapWorkflowMutationError(err)
	}
	var request engine.CancellationRequest
	if detailsErr := applicationErr.Details(&request); detailsErr != nil {
		return fmt.Errorf("decode cancellation conflict: %w", detailsErr)
	}
	return &engine.CancellationConflictError{RunID: request.RunID, Reason: request.Reason}
}

var (
	_ engine.WorkflowHandle        = (*workflowHandle)(nil)
	_ engine.CancellationRequester = (*Engine)(nil)
)
