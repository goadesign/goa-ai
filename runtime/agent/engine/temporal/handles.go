// Package temporal isolates workflow-handle and signal/cancel helpers so the
// engine's main file can focus on registration and workflow start semantics.
package temporal

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/client"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
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

func (h *workflowHandle) Signal(ctx context.Context, name string, payload any) error {
	return mapSignalError(h.client.SignalWorkflow(ctx, h.run.GetID(), h.run.GetRunID(), name, payload))
}

func (h *workflowHandle) Cancel(ctx context.Context) error {
	return h.client.CancelWorkflow(ctx, h.run.GetID(), h.run.GetRunID())
}

// SignalByID sends a signal to a workflow by workflow ID and optional run ID.
func (e *Engine) SignalByID(ctx context.Context, workflowID, runID, name string, payload any) error {
	if workflowID == "" {
		return fmt.Errorf("workflow id is required")
	}
	return mapSignalError(e.client.SignalWorkflow(ctx, workflowID, runID, name, payload))
}

// CancelByID requests cancellation of a workflow by its durable workflow ID.
func (e *Engine) CancelByID(ctx context.Context, workflowID string) error {
	if workflowID == "" {
		return fmt.Errorf("workflow id is required")
	}
	if err := e.client.CancelWorkflow(ctx, workflowID, ""); err != nil {
		return mapSignalError(err)
	}
	return nil
}

var (
	_ engine.WorkflowHandle = (*workflowHandle)(nil)
)
