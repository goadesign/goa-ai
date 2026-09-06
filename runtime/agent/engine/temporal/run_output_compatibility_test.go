package temporal

// These tests read retained workflow completions through the real Temporal
// client and replay a parent's saved child completion. The payload literals
// freeze the old and new wire formats; they are never encoded from today's
// RunOutput to manufacture an old result. The child history is synthetic, not
// a history captured from a deployed Temporal service.

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type retainedOutputService struct {
	workflowservice.UnimplementedWorkflowServiceServer
	payload *commonpb.Payload
}

const (
	// The legacy literal follows the previous unversioned RunOutput. Null fields
	// and both tool events are retained explicitly.
	retainedLegacyOutput    = `{"AgentID":"agent","RunID":"child","Final":null,"FinalToolResult":{"Name":"report.finish","ToolCallID":"finish-2","Result":{"ok":true},"Telemetry":{"TokensUsed":2,"DurationMs":3,"Model":"terminal"}},"ToolEvents":[{"Name":"records.page","ToolCallID":"page-1","Result":{"rows":[1]},"Telemetry":{"TokensUsed":4,"DurationMs":5,"Model":"first"}},{"Name":"report.finish","ToolCallID":"finish-2","Result":{"ok":true},"Telemetry":{"TokensUsed":2,"DurationMs":3,"Model":"terminal"}}],"Notes":null,"Usage":null,"Suspension":null}`
	retainedCurrentOutput   = `{"AgentID":"agent","RunID":"child","Final":null,"FinalToolResult":{"Name":"report.finish","ToolCallID":"finish-2","Result":{"ok":true},"Telemetry":{"TokensUsed":2,"DurationMs":3,"Model":"terminal"}},"ToolCount":2,"ToolTelemetry":{"TokensUsed":6,"DurationMs":8,"Model":""},"Notes":null,"Usage":null,"Suspension":null}`
	retainedOutputEncoding  = "json/goa-ai-run-output-v2"
	retainedChildWorkflowID = "child"
)

func TestRetainedRunOutputThroughTemporalHandleAndCompletionQuery(t *testing.T) {
	for _, test := range []struct{ name, encoding, body string }{
		{"legacy", converter.MetadataEncodingJSON, retainedLegacyOutput},
		{"current", retainedOutputEncoding, retainedCurrentOutput},
	} {
		t.Run(test.name, func(t *testing.T) {
			eng := retainedOutputEngine(t, &commonpb.Payload{
				Metadata: map[string][]byte{converter.MetadataEncoding: []byte(test.encoding)},
				Data:     []byte(test.body),
			})
			handle := &workflowHandle{run: eng.client.GetWorkflow(t.Context(), retainedChildWorkflowID, ""), client: eng.client}
			output, err := handle.Wait(t.Context())
			require.NoError(t, err)
			assertRetainedOutput(t, output)

			completion, err := eng.QueryRunCompletion(t.Context(), retainedChildWorkflowID)
			require.NoError(t, err)
			assert.Equal(t, engine.RunStatusCompleted, completion.Status)
			assert.Equal(t, time.Unix(100, 0).UTC(), completion.CompletedAt)
			require.NoError(t, completion.WorkflowError)
			assertRetainedOutput(t, completion.Output)
			assert.Equal(t, output, completion.Output)
		})
	}
}

func TestRetainedRunOutputReadersPreserveStrictDecodeErrors(t *testing.T) {
	for _, test := range []struct{ name, encoding, body string }{
		{"new body under old tag", converter.MetadataEncodingJSON, retainedCurrentOutput},
		{"old body under new tag", retainedOutputEncoding, retainedLegacyOutput},
		{"unknown tag", "json/goa-ai-run-output-v999", retainedCurrentOutput},
		{"unknown legacy field", converter.MetadataEncodingJSON, `{"Unexpected":true}`},
		{"unknown current field", retainedOutputEncoding, `{"Unexpected":true}`},
		{"legacy trailing value", converter.MetadataEncodingJSON, retainedLegacyOutput + ` {}`},
		{"current malformed value", retainedOutputEncoding, `{`},
		{"legacy invalid statistics", converter.MetadataEncodingJSON, `{"ToolEvents":[{"Telemetry":{"TokensUsed":-1}}]}`},
		{"current invalid statistics", retainedOutputEncoding, `{"ToolCount":-1}`},
		{"legacy oversized value", converter.MetadataEncodingJSON, `{"RunID":"` + strings.Repeat("x", engine.MaxPayloadBytes) + `"}`},
		{"current oversized value", retainedOutputEncoding, `{"RunID":"` + strings.Repeat("x", engine.MaxPayloadBytes) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := &commonpb.Payload{
				Metadata: map[string][]byte{converter.MetadataEncoding: []byte(test.encoding)},
				Data:     []byte(test.body),
			}
			var expected *api.RunOutput
			decodeErr := NewAgentDataConverter().FromPayloads(&commonpb.Payloads{Payloads: []*commonpb.Payload{payload}}, &expected)
			require.Error(t, decodeErr)
			eng := retainedOutputEngine(t, payload)
			handle := &workflowHandle{run: eng.client.GetWorkflow(t.Context(), retainedChildWorkflowID, ""), client: eng.client}
			output, err := handle.Wait(t.Context())
			assert.Nil(t, output)
			require.EqualError(t, err, decodeErr.Error())

			completion, err := eng.QueryRunCompletion(t.Context(), retainedChildWorkflowID)
			require.EqualError(t, err, decodeErr.Error())
			assert.Nil(t, completion.Output)
			assert.NoError(t, completion.WorkflowError, "decode errors must not become workflow execution failures")
		})
	}
}

func TestTemporalChildHandleReplaysFrozenLegacyCompletion(t *testing.T) {
	var output atomic.Pointer[api.RunOutput]
	parent := func(ctx workflow.Context) (int, error) {
		ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID: retainedChildWorkflowID, TaskQueue: "child.queue",
		})
		future := workflow.ExecuteChildWorkflow(ctx, "retained.child")
		handle := &temporalChildHandle{future: future, ctx: ctx}
		result, err := handle.Get(context.Background())
		if err != nil {
			return 0, err
		}
		output.Store(result)
		return result.ToolCount, nil
	}
	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
		DataConverter: NewAgentDataConverter(),
	})
	require.NoError(t, err)
	replayer.RegisterWorkflowWithOptions(parent, workflow.RegisterOptions{Name: "retained.parent"})
	require.NoError(t, replayer.ReplayWorkflowHistory(nil, retainedChildCompletionHistory()))
	assertRetainedOutput(t, output.Load())
}

// GetSystemInfo supplies the SDK handshake for this loopback-only service.
func (s *retainedOutputService) GetSystemInfo(context.Context, *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	return &workflowservice.GetSystemInfoResponse{}, nil
}

// DescribeWorkflowExecution identifies the exact completed fixture requested
// by QueryRunCompletion; unexpected workflow IDs are rejected by the service.
func (s *retainedOutputService) DescribeWorkflowExecution(_ context.Context, request *workflowservice.DescribeWorkflowExecutionRequest) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	if request.GetExecution().GetWorkflowId() != retainedChildWorkflowID {
		return nil, status.Error(codes.NotFound, "unexpected fixture workflow")
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: retainedChildWorkflowID, RunId: "child-execution"},
			Status:    enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
			CloseTime: timestamppb.New(time.Unix(100, 0)),
		},
	}, nil
}

// GetWorkflowExecutionHistory returns the unchanged saved completion payload.
// The SDK, not this service, owns decoding it into the caller's RunOutput.
func (s *retainedOutputService) GetWorkflowExecutionHistory(_ context.Context, request *workflowservice.GetWorkflowExecutionHistoryRequest) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
	if request.GetExecution().GetWorkflowId() != retainedChildWorkflowID {
		return nil, status.Error(codes.NotFound, "unexpected fixture workflow")
	}
	return &workflowservice.GetWorkflowExecutionHistoryResponse{
		History: &historypb.History{Events: []*historypb.HistoryEvent{{
			EventId:   11,
			EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED,
			Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{
				WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{
					Result: &commonpb.Payloads{Payloads: []*commonpb.Payload{s.payload}},
				},
			},
		}}},
	}, nil
}

// retainedOutputEngine installs the production adapter's converter on a real
// SDK client. A local gRPC service supplies only the retained history bytes.
func retainedOutputEngine(t *testing.T, payload *commonpb.Payload) *Engine {
	t.Helper()
	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	workflowservice.RegisterWorkflowServiceServer(server, &retainedOutputService{payload: payload})
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, <-served)
	})
	eng, err := NewClient(Options{ClientOptions: &client.Options{HostPort: listener.Addr().String()}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, eng.Close()) })
	return eng
}

// assertRetainedOutput checks the compact facts used by parents and keeps the
// terminal tool's own telemetry distinct from the combined tool telemetry.
func assertRetainedOutput(t *testing.T, output *api.RunOutput) {
	t.Helper()
	require.NotNil(t, output)
	assert.Equal(t, retainedChildWorkflowID, output.RunID)
	assert.Equal(t, 2, output.ToolCount)
	assert.Equal(t, &telemetry.ToolTelemetry{TokensUsed: 6, DurationMs: 8}, output.ToolTelemetry)
	require.NotNil(t, output.FinalToolResult)
	assert.Equal(t, "finish-2", output.FinalToolResult.ToolCallID)
	assert.Equal(t, &telemetry.ToolTelemetry{TokensUsed: 2, DurationMs: 3, Model: "terminal"}, output.FinalToolResult.Telemetry)
	assert.JSONEq(t, `{"ok":true}`, string(output.FinalToolResult.Result))
	assert.Nil(t, output.FinalToolResult.Failure)
}

// retainedChildCompletionHistory builds Temporal events around the frozen old
// payload. No current RunOutput encoder participates in this replay fixture.
func retainedChildCompletionHistory() *historypb.History {
	execution := &commonpb.WorkflowExecution{WorkflowId: retainedChildWorkflowID, RunId: "child-execution"}
	childType := &commonpb.WorkflowType{Name: "retained.child"}
	return &historypb.History{Events: []*historypb.HistoryEvent{
		workflowExecutionStartedEvent(1, "retained.parent", "parent.queue", nil),
		workflowTaskScheduledEvent(2),
		workflowTaskStartedEvent(3),
		workflowTaskCompletedEvent(4, 2, 3),
		{
			EventId:   5,
			EventType: enumspb.EVENT_TYPE_START_CHILD_WORKFLOW_EXECUTION_INITIATED,
			Attributes: &historypb.HistoryEvent_StartChildWorkflowExecutionInitiatedEventAttributes{
				StartChildWorkflowExecutionInitiatedEventAttributes: &historypb.StartChildWorkflowExecutionInitiatedEventAttributes{
					Namespace: "default", WorkflowId: retainedChildWorkflowID, WorkflowType: childType,
					TaskQueue: &taskqueuepb.TaskQueue{Name: "child.queue"}, WorkflowTaskCompletedEventId: 4,
				},
			},
		},
		{
			EventId:   6,
			EventType: enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_STARTED,
			Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionStartedEventAttributes{
				ChildWorkflowExecutionStartedEventAttributes: &historypb.ChildWorkflowExecutionStartedEventAttributes{
					Namespace: "default", InitiatedEventId: 5, WorkflowExecution: execution, WorkflowType: childType,
				},
			},
		},
		{
			EventId:   7,
			EventType: enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_COMPLETED,
			Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionCompletedEventAttributes{
				ChildWorkflowExecutionCompletedEventAttributes: &historypb.ChildWorkflowExecutionCompletedEventAttributes{
					Namespace: "default", InitiatedEventId: 5, StartedEventId: 6, WorkflowExecution: execution, WorkflowType: childType,
					Result: &commonpb.Payloads{Payloads: []*commonpb.Payload{{
						Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)},
						Data:     []byte(retainedLegacyOutput),
					}}},
				},
			},
		},
		workflowTaskScheduledEvent(8),
		workflowTaskStartedEvent(9),
		workflowTaskCompletedEvent(10, 8, 9),
		workflowExecutionCompletedEvent(11, 10),
	}}
}
