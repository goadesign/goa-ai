package temporal

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"google.golang.org/grpc"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

// These tests lock the Temporal adapter contract around duplicate registration
// handling and workflow start option propagation.

func TestRegisterWorkflowRejectsDuplicateBeforeCreatingWorkerForNewQueue(t *testing.T) {
	t.Parallel()

	eng := newTestEngine(t)
	handler := func(ctx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		return &api.RunOutput{}, nil
	}

	err := eng.RegisterWorkflow(context.Background(), engine.WorkflowDefinition{
		Name:      "agent.workflow",
		TaskQueue: "queue.alpha",
		Handler:   handler,
	})
	require.NoError(t, err)

	err = eng.RegisterWorkflow(context.Background(), engine.WorkflowDefinition{
		Name:      "agent.workflow",
		TaskQueue: "queue.beta",
		Handler:   handler,
	})
	require.ErrorContains(t, err, `workflow "agent.workflow" already registered`)
	assert.Len(t, eng.workers, 1)
	_, exists := eng.workers["queue.beta"]
	assert.False(t, exists)
}

func TestRegisterPlannerActivityRejectsDuplicateNameAcrossQueues(t *testing.T) {
	t.Parallel()

	eng := newTestEngine(t)
	handler := func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{}, nil
	}

	err := eng.RegisterPlannerActivity(context.Background(), "planner.activity", engine.ActivityOptions{
		Queue:               "queue.alpha",
		StartToCloseTimeout: time.Minute,
	}, handler)
	require.NoError(t, err)

	err = eng.RegisterPlannerActivity(context.Background(), "planner.activity", engine.ActivityOptions{
		Queue:               "queue.beta",
		StartToCloseTimeout: 2 * time.Minute,
	}, handler)
	require.ErrorContains(t, err, `activity "planner.activity" already registered`)

	wfCtx := &temporalWorkflowContext{engine: eng}
	opts := wfCtx.activityOptionsFor("planner.activity", engine.ActivityOptions{})
	assert.Equal(t, "queue.alpha", opts.TaskQueue)
	assert.Equal(t, time.Minute, opts.StartToCloseTimeout)
	assert.Len(t, eng.workers, 1)
	_, exists := eng.workers["queue.beta"]
	assert.False(t, exists)
}

func TestStartWorkflowPropagatesMemoAndSearchAttributes(t *testing.T) {
	t.Parallel()

	service := &testWorkflowService{}
	eng, err := New(Options{
		Client: newWorkflowServiceClient(t, service),
		WorkerOptions: WorkerOptions{
			TaskQueue: "default.queue",
		},
	})
	require.NoError(t, err)

	handler := func(ctx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		return &api.RunOutput{}, nil
	}
	err = eng.RegisterWorkflow(context.Background(), engine.WorkflowDefinition{
		Name:    "agent.workflow",
		Handler: handler,
	})
	require.NoError(t, err)

	input := &api.RunInput{RunID: "run-123"}
	occurredAt := time.Date(2026, time.March, 14, 15, 9, 26, 0, time.UTC)
	req := engine.WorkflowStartRequest{
		ID:       "run-123",
		Workflow: "agent.workflow",
		Input:    input,
		Memo:     map[string]any{"memo_key": "memo-value"},
		SearchAttributes: map[string]any{
			"SessionID":  "session-123",
			"Approved":   true,
			"Attempt":    7,
			"Score":      12.5,
			"OccurredAt": occurredAt,
			"Labels":     []string{"alpha", "beta"},
		},
	}

	_, err = eng.StartWorkflow(context.Background(), req)
	require.NoError(t, err)

	startReq := service.startRequest()
	require.NotNil(t, startReq)
	require.Equal(t, req.ID, startReq.WorkflowId)
	require.Equal(t, eng.defaultQueue, startReq.TaskQueue.GetName())
	require.Equal(t, "memo-value", decodePayload[string](t, startReq.GetMemo().GetFields()["memo_key"]))

	fields := startReq.GetSearchAttributes().GetIndexedFields()
	requireSearchAttribute(t, fields, "SessionID", enumspb.INDEXED_VALUE_TYPE_KEYWORD, "session-123")
	requireSearchAttribute(t, fields, "Approved", enumspb.INDEXED_VALUE_TYPE_BOOL, true)
	requireSearchAttribute(t, fields, "Attempt", enumspb.INDEXED_VALUE_TYPE_INT, int64(7))
	requireSearchAttribute(t, fields, "Score", enumspb.INDEXED_VALUE_TYPE_DOUBLE, 12.5)
	requireSearchAttribute(t, fields, "OccurredAt", enumspb.INDEXED_VALUE_TYPE_DATETIME, occurredAt)
	requireSearchAttribute(t, fields, "Labels", enumspb.INDEXED_VALUE_TYPE_KEYWORD_LIST, []string{"alpha", "beta"})
}

func TestStartWorkflowRejectsUnsupportedSearchAttributeType(t *testing.T) {
	t.Parallel()

	eng := newTestEngine(t)
	err := eng.RegisterWorkflow(context.Background(), engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(ctx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			return &api.RunOutput{}, nil
		},
	})
	require.NoError(t, err)

	_, err = eng.StartWorkflow(context.Background(), engine.WorkflowStartRequest{
		ID:       "run-123",
		Workflow: "agent.workflow",
		Input:    &api.RunInput{RunID: "run-123"},
		SearchAttributes: map[string]any{
			"Unsupported": []int{1, 2, 3},
		},
	})
	require.ErrorContains(t, err, `search attribute "Unsupported" has unsupported type []int`)
}

// newTestEngine returns a Temporal engine backed by a lazy Temporal client so tests can
// exercise registration logic without contacting a Temporal server.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()

	eng, err := New(Options{
		Client: newLazyTestClient(t),
		WorkerOptions: WorkerOptions{
			TaskQueue: "default.queue",
		},
	})
	require.NoError(t, err)
	return eng
}

// newLazyTestClient returns a Temporal client that satisfies worker.New without
// eagerly dialing a server.
func newLazyTestClient(t *testing.T) client.Client {
	t.Helper()

	cli, err := client.NewLazyClient(client.Options{})
	require.NoError(t, err)
	t.Cleanup(cli.Close)
	return cli
}

// newWorkflowServiceClient returns a client wired to a local gRPC server so the
// test can observe the exact Temporal start request emitted by the adapter.
func newWorkflowServiceClient(t *testing.T, service workflowservice.WorkflowServiceServer) client.Client {
	t.Helper()

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := grpc.NewServer()
	workflowservice.RegisterWorkflowServiceServer(server, service)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)
	t.Cleanup(func() {
		_ = listener.Close()
	})

	cli, err := client.NewLazyClient(client.Options{HostPort: listener.Addr().String()})
	require.NoError(t, err)
	t.Cleanup(cli.Close)
	return cli
}

func decodePayload[T any](t *testing.T, payload any) T {
	t.Helper()

	temporalPayload, ok := payload.(*commonpb.Payload)
	require.True(t, ok)
	var decoded T
	err := converter.GetDefaultDataConverter().FromPayload(temporalPayload, &decoded)
	require.NoError(t, err)
	return decoded
}

func requireSearchAttribute[T any](t *testing.T, fields map[string]*commonpb.Payload, name string, valueType enumspb.IndexedValueType, expected T) {
	t.Helper()

	payload := fields[name]
	require.NotNil(t, payload)
	require.Equal(t, valueType.String(), string(payload.GetMetadata()["type"]))
	require.Equal(t, expected, decodePayload[T](t, payload))
}

type testWorkflowService struct {
	workflowservice.UnimplementedWorkflowServiceServer

	mu       sync.Mutex
	startReq *workflowservice.StartWorkflowExecutionRequest
}

func (s *testWorkflowService) GetSystemInfo(context.Context, *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	return &workflowservice.GetSystemInfoResponse{
		Capabilities: &workflowservice.GetSystemInfoResponse_Capabilities{
			SdkMetadata: true,
		},
	}, nil
}

func (s *testWorkflowService) StartWorkflowExecution(ctx context.Context, req *workflowservice.StartWorkflowExecutionRequest) (*workflowservice.StartWorkflowExecutionResponse, error) {
	s.mu.Lock()
	s.startReq = req
	s.mu.Unlock()
	return &workflowservice.StartWorkflowExecutionResponse{RunId: "temporal-run-123"}, nil
}

// startRequest returns the most recent workflow start request observed by the
// local test server.
func (s *testWorkflowService) startRequest() *workflowservice.StartWorkflowExecutionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startReq
}
