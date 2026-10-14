package temporal

// Completion reads must return evidence from one exact accepted execution and
// reject a closed attempt whose retry or continuation still owns the operation.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type (
	completionIdentityClient struct {
		client.Client
		info              *workflowpb.WorkflowExecutionInfo
		closed            *historypb.HistoryEvent
		fetched           string
		historyExecution  string
		followingDisabled bool
	}
	completionIdentityHistory struct{ event *historypb.HistoryEvent }
	completionIdentityRun     struct {
		client.WorkflowRun
		owner *completionIdentityClient
	}
)

func TestQueryRunCompletionPinsAcceptedExecution(t *testing.T) {
	digest := [32]byte{1}
	payload, err := NewAgentDataConverter().ToPayload(digest[:])
	require.NoError(t, err)
	for _, test := range []struct {
		name      string
		memo      *commonpb.Memo
		successor bool
		wantError string
	}{
		{name: "exact", memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{startrecipe.MemoKey: payload}}},
		{name: "missing identity", wantError: "digest is missing"},
		{name: "retry pending", memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{startrecipe.MemoKey: payload}}, successor: true, wantError: "has a successor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &completionIdentityClient{info: &workflowpb.WorkflowExecutionInfo{
				Execution: &commonpb.WorkflowExecution{WorkflowId: "workflow", RunId: "accepted-execution"},
				Memo:      test.memo, Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, CloseTime: timestamppb.New(time.Unix(10, 0)),
			}, closed: &historypb.HistoryEvent{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED, Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{}}}}
			if test.successor {
				backend.closed.GetWorkflowExecutionCompletedEventAttributes().NewExecutionRunId = "retry"
			}
			e := &Engine{client: backend}
			got, err := e.QueryRunCompletion(t.Context(), "workflow")
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				assert.Empty(t, backend.fetched)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, digest, got.StartRequestDigest)
			assert.Equal(t, engine.RunStatusCompleted, got.Status)
			assert.Equal(t, "accepted-execution", backend.fetched)
			assert.Equal(t, "accepted-execution", backend.historyExecution)
			assert.True(t, backend.followingDisabled)
		})
	}
}

func (c *completionIdentityClient) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: c.info}, nil
}
func (c *completionIdentityClient) GetWorkflowHistory(_ context.Context, _, execution string, _ bool, _ enumspb.HistoryEventFilterType) client.HistoryEventIterator {
	c.historyExecution = execution
	return &completionIdentityHistory{event: c.closed}
}
func (c *completionIdentityClient) GetWorkflow(_ context.Context, _, execution string) client.WorkflowRun {
	c.fetched = execution
	return &completionIdentityRun{owner: c}
}
func (h *completionIdentityHistory) HasNext() bool { return h.event != nil }
func (h *completionIdentityHistory) Next() (*historypb.HistoryEvent, error) {
	event := h.event
	h.event = nil
	return event, nil
}
func (r *completionIdentityRun) GetWithOptions(_ context.Context, destination any, options client.WorkflowRunGetOptions) error {
	r.owner.followingDisabled = options.DisableFollowingRuns
	*(destination.(**api.RunOutput)) = &api.RunOutput{RunID: "run"}
	return nil
}
