// Package temporal verifies that completion and accepted-run queries pin
// execution identity and reject attempts superseded by retries.
package temporal

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
	"google.golang.org/protobuf/types/known/timestamppb"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

type (
	completionIdentityClient struct {
		client.Client
		info             *workflowpb.WorkflowExecutionInfo
		started          *historypb.HistoryEvent
		closed           *historypb.HistoryEvent
		fetched          string
		historyExecution string
	}
	completionIdentityHistory struct{ event *historypb.HistoryEvent }
	completionIdentityRun     struct{ client.WorkflowRun }
)

func TestQueryRunCompletionPinsAcceptedExecution(t *testing.T) {
	digest := [32]byte{1}
	payload, err := NewAgentDataConverter().ToPayload(digest[:])
	require.NoError(t, err)
	for _, test := range []struct {
		name      string
		memo      *commonpb.Memo
		successor bool
		wantErr   string
	}{
		{name: "accepted memo", memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{startrecipe.MemoKey: payload}}},
		{name: "missing memo", wantErr: "accepted request digest is missing"},
		{name: "retried close", memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{startrecipe.MemoKey: payload}}, successor: true, wantErr: "has a successor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &completionIdentityClient{info: &workflowpb.WorkflowExecutionInfo{
				Execution: &commonpb.WorkflowExecution{WorkflowId: "workflow", RunId: "accepted-execution"},
				Memo:      test.memo, Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, CloseTime: timestamppb.New(time.Unix(10, 0)),
			}, closed: &historypb.HistoryEvent{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED, Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{}}}}
			if test.successor {
				backend.closed.GetWorkflowExecutionCompletedEventAttributes().NewExecutionRunId = "retry"
			}
			got, err := (&Engine{client: backend}).QueryRunCompletion(t.Context(), "workflow")
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				assert.Empty(t, backend.fetched)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, digest, got.StartRequestDigest)
			assert.Equal(t, engine.RunStatusCompleted, got.Status)
			assert.Equal(t, "accepted-execution", backend.fetched)
			assert.Equal(t, "accepted-execution", backend.historyExecution)
		})
	}
}

func TestQueryAcceptedRunPinsAcceptedExecution(t *testing.T) {
	digest := [32]byte{1}
	payload, err := NewAgentDataConverter().ToPayload(digest[:])
	require.NoError(t, err)
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}
	inputPayload, err := NewAgentDataConverter().ToPayloads(&api.RunInput{
		RunID:     "workflow",
		Subject:   subject,
		SessionID: "session-1",
		TurnID:    "turn-1",
		SeedEndID: "seed-1",
		Labels:    map[string]string{"TenantID": "t-1"},
	})
	require.NoError(t, err)
	backend := &completionIdentityClient{
		info: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: "workflow", RunId: "accepted-execution"},
			Memo:      &commonpb.Memo{Fields: map[string]*commonpb.Payload{startrecipe.MemoKey: payload}},
			Status:    enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
		started: &historypb.HistoryEvent{
			EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
				WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{Input: inputPayload},
			},
		},
	}
	got, err := (&Engine{client: backend}).QueryAcceptedRun(t.Context(), "workflow")
	require.NoError(t, err)
	assert.Equal(t, subject, got.Subject)
	assert.Equal(t, "workflow", got.RunID)
	assert.Equal(t, "session-1", got.SessionID)
	assert.Equal(t, "turn-1", got.TurnID)
	assert.Equal(t, "seed-1", got.SeedEndID)
	assert.Equal(t, map[string]string{"TenantID": "t-1"}, got.Labels)
	assert.Equal(t, engine.RunStatusRunning, got.Status)
	assert.Equal(t, "accepted-execution", backend.historyExecution)
}

func (c *completionIdentityClient) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: c.info}, nil
}
func (c *completionIdentityClient) GetWorkflowHistory(_ context.Context, _, execution string, _ bool, filter enumspb.HistoryEventFilterType) client.HistoryEventIterator {
	c.historyExecution = execution
	if filter == enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT {
		return &completionIdentityHistory{event: c.started}
	}
	return &completionIdentityHistory{event: c.closed}
}
func (c *completionIdentityClient) GetWorkflow(_ context.Context, _, execution string) client.WorkflowRun {
	c.fetched = execution
	return completionIdentityRun{}
}
func (h *completionIdentityHistory) HasNext() bool { return h.event != nil }
func (h *completionIdentityHistory) Next() (*historypb.HistoryEvent, error) {
	event := h.event
	h.event = nil
	return event, nil
}
func (completionIdentityRun) GetWithOptions(_ context.Context, valuePtr any, opts client.WorkflowRunGetOptions) error {
	if !opts.DisableFollowingRuns {
		panic("completion query must not follow successor runs")
	}
	*(valuePtr.(**api.RunOutput)) = &api.RunOutput{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")},
		RunID:   "workflow",
	}
	return nil
}
