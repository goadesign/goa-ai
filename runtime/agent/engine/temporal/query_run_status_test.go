package temporal

import (
	"errors"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"goa.design/goa-ai/runtime/agent/engine"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/stretchr/testify/require"
)

func TestQueryRunStatusFromInfoMapsTerminalStates(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		status enumspb.WorkflowExecutionStatus
		want   engine.RunStatus
	}{
		{
			name:   "completed",
			status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
			want:   engine.RunStatusCompleted,
		},
		{
			name:   "failed",
			status: enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
			want:   engine.RunStatusFailed,
		},
		{
			name:   "timed out",
			status: enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
			want:   engine.RunStatusTimedOut,
		},
		{
			name:   "terminated",
			status: enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
			want:   engine.RunStatusFailed,
		},
		{
			name:   "canceled",
			status: enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED,
			want:   engine.RunStatusCanceled,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			info := &workflowpb.WorkflowExecutionInfo{
				Status:    tc.status,
				CloseTime: timestamppb.Now(),
			}
			require.Equal(t, tc.want, queryRunStatusFromInfo(info))
		})
	}
}

func TestQueryRunStatusFromInfoTreatsOpenExecutionAsRunning(t *testing.T) {
	t.Parallel()

	info := &workflowpb.WorkflowExecutionInfo{
		Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
	}

	require.Equal(t, engine.RunStatusRunning, queryRunStatusFromInfo(info))
}

func TestQueryRunStatusFromInfoTreatsOpenPausedExecutionAsPaused(t *testing.T) {
	t.Parallel()

	info := &workflowpb.WorkflowExecutionInfo{
		Status: enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED,
	}

	require.Equal(t, engine.RunStatusPaused, queryRunStatusFromInfo(info))
}

func TestQueryRunStatusFromInfoPanicsOnClosedNonTerminalStatus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		status enumspb.WorkflowExecutionStatus
	}{
		{
			name:   "unspecified",
			status: enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED,
		},
		{
			name:   "running",
			status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
		{
			name:   "paused",
			status: enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			info := &workflowpb.WorkflowExecutionInfo{
				Status:    tc.status,
				CloseTime: timestamppb.Now(),
			}

			require.Panics(t, func() {
				queryRunStatusFromInfo(info)
			})
		})
	}
}

func TestMapDescribeWorkflowExecutionErrorPreservesNonNotFoundFailures(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, mapDescribeWorkflowExecutionError(serviceerror.NewNotFound("missing")), engine.ErrWorkflowNotFound)

	backendErr := errors.New("temporal unavailable")
	require.ErrorIs(t, mapDescribeWorkflowExecutionError(backendErr), backendErr)
}
