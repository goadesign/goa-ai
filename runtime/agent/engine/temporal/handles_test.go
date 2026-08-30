// Package temporal tests client-side workflow handles and durable cancellation
// requests without requiring a Temporal server.
package temporal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	cancellationClient struct {
		client.Client

		updateOptions     client.UpdateWorkflowOptions
		updateHandle      client.WorkflowUpdateHandle
		updateErr         error
		priorHandle       client.WorkflowUpdateHandle
		priorHandleLookup client.GetWorkflowUpdateHandleOptions
	}

	cancellationUpdateHandle struct {
		reason string
		err    error
	}
)

func (c *cancellationClient) UpdateWorkflow(_ context.Context, options client.UpdateWorkflowOptions) (client.WorkflowUpdateHandle, error) {
	c.updateOptions = options
	return c.updateHandle, c.updateErr
}

func (c *cancellationClient) GetWorkflowUpdateHandle(options client.GetWorkflowUpdateHandleOptions) client.WorkflowUpdateHandle {
	c.priorHandleLookup = options
	return c.priorHandle
}

func (h *cancellationUpdateHandle) WorkflowID() string {
	return "run"
}

func (h *cancellationUpdateHandle) RunID() string {
	return "temporal-run"
}

func (h *cancellationUpdateHandle) UpdateID() string {
	return cancellationUpdateID
}

func (h *cancellationUpdateHandle) Get(_ context.Context, value any) error {
	if h.err != nil {
		return h.err
	}
	reason, ok := value.(*string)
	if !ok {
		return errors.New("cancellation update result must be a string pointer")
	}
	*reason = h.reason
	return nil
}

func TestRequestCancellationCompletesFromWorkflowUpdate(t *testing.T) {
	request := engine.CancellationRequest{RunID: "run", Reason: "user_requested"}
	fakeClient := &cancellationClient{
		updateHandle: &cancellationUpdateHandle{reason: request.Reason},
	}
	implementation := &Engine{client: fakeClient}

	require.NoError(t, implementation.RequestCancellation(t.Context(), request))
	require.Equal(t, cancellationUpdateID, fakeClient.updateOptions.UpdateID)
	require.Equal(t, request.RunID, fakeClient.updateOptions.WorkflowID)
	require.Equal(t, cancellationUpdateName, fakeClient.updateOptions.UpdateName)
	require.Equal(t, client.WorkflowUpdateStageCompleted, fakeClient.updateOptions.WaitForStage)
	require.Equal(t, []any{request}, fakeClient.updateOptions.Args)
}

func TestRequestCancellationRejectsDifferentReasonFromExactUpdate(t *testing.T) {
	fakeClient := &cancellationClient{
		updateHandle: &cancellationUpdateHandle{reason: "user_requested"},
	}
	implementation := &Engine{client: fakeClient}

	err := implementation.RequestCancellation(t.Context(), engine.CancellationRequest{
		RunID: "run", Reason: "session_ended",
	})
	var conflict *engine.CancellationConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "run", conflict.RunID)
	require.Equal(t, "session_ended", conflict.Reason)
}

func TestRequestCancellationRetriesCompletedUpdate(t *testing.T) {
	request := engine.CancellationRequest{RunID: "run", Reason: "user_requested"}
	fakeClient := &cancellationClient{
		updateErr:   serviceerror.NewFailedPrecondition("workflow completed"),
		priorHandle: &cancellationUpdateHandle{reason: request.Reason},
	}
	implementation := &Engine{client: fakeClient}

	require.NoError(t, implementation.RequestCancellation(t.Context(), request))
	require.Equal(t, request.RunID, fakeClient.priorHandleLookup.WorkflowID)
	require.Equal(t, cancellationUpdateID, fakeClient.priorHandleLookup.UpdateID)
}
