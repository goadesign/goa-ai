package temporal

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/grpc"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
)

// These tests lock the Temporal adapter contract around duplicate registration
// handling and workflow start option propagation.

func TestNewRejectsCustomDataConverter(t *testing.T) {
	_, err := NewClient(Options{
		ClientOptions: &client.Options{
			DataConverter: converter.GetDefaultDataConverter(),
		},
	})

	require.EqualError(t, err, "temporal engine: custom data converter is not supported")
}

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
	require.False(t, eng.workers["queue.alpha"].isStarted())

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

func TestTemporalWorkflowHandlerPreservesCancellationStatus(t *testing.T) {
	for _, test := range []struct {
		name         string
		err          error
		wantCanceled bool
	}{
		{name: "context cancellation", err: context.Canceled, wantCanceled: true},
		{name: "wrapped context cancellation", err: fmt.Errorf("runtime: %w", context.Canceled), wantCanceled: true},
		{name: "Temporal cancellation", err: temporal.NewCanceledError("superseded"), wantCanceled: true},
		{
			name:         "joined cancellations",
			err:          errors.Join(context.Canceled, fmt.Errorf("tool: %w", context.Canceled)),
			wantCanceled: true,
		},
		{
			name: "cancellation joined with failure",
			err:  errors.Join(context.Canceled, errors.New("persist hook failed")),
		},
		{
			name: "wrapped cancellation joined with failure",
			err: fmt.Errorf(
				"runtime: %w",
				errors.Join(context.Canceled, errors.New("persist hook failed")),
			),
		},
		{name: "ordinary failure", err: errors.New("planner failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			eng := newTestEngine(t)
			handler := eng.temporalWorkflowHandler(
				func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
					return nil, test.err
				},
			)
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()

			env.ExecuteWorkflow(handler, &api.RunInput{RunID: "run-1"})

			err := env.GetWorkflowError()
			require.Error(t, err)
			assert.Equal(t, test.wantCanceled, temporal.IsCanceledError(err))
		})
	}
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
	require.False(t, eng.workers["queue.alpha"].isStarted())

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

func TestClientStartWorkflowPropagatesRequestedValuesWithoutRegistration(t *testing.T) {
	t.Parallel()

	service := &testWorkflowService{}
	eng, err := NewClient(Options{
		ClientOptions: &client.Options{},
	})
	require.NoError(t, err)
	eng.client.Close()
	eng.client = newWorkflowServiceClient(t, service)
	t.Cleanup(func() {
		require.NoError(t, eng.Close())
	})

	input := &api.RunInput{RunID: "run-123"}
	occurredAt := time.Date(2026, time.March, 14, 15, 9, 26, 0, time.UTC)
	req := engine.WorkflowStartRequest{
		ID:        "run-123",
		Workflow:  "agent.workflow",
		TaskQueue: "agent.queue",
		Input:     input,
		Memo: map[string]engine.EncodedValue{
			"memo_key": {
				Metadata: map[string][]byte{"encoding": []byte("json/plain")},
				Data:     []byte(`"memo-value"`),
			},
		},
		SearchAttributes: map[string]any{
			"SessionID":  "session-123",
			"Approved":   true,
			"Attempt":    7,
			"Score":      12.5,
			"OccurredAt": occurredAt,
			"Labels":     []string{"alpha", "beta"},
		},
	}

	handle, err := eng.StartWorkflow(context.Background(), req)
	require.NoError(t, err)

	startReq := service.startRequest()
	require.NotNil(t, startReq)
	require.Equal(t, req.ID, startReq.WorkflowId)
	require.Equal(t, req.Workflow, startReq.GetWorkflowType().GetName())
	require.Equal(t, req.TaskQueue, startReq.TaskQueue.GetName())
	require.Equal(t, enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, startReq.WorkflowIdReusePolicy)
	require.Nil(t, startReq.RetryPolicy)
	require.Equal(t, req.Memo["memo_key"].Metadata, startReq.GetMemo().GetFields()["memo_key"].Metadata)
	require.Equal(t, req.Memo["memo_key"].Data, startReq.GetMemo().GetFields()["memo_key"].Data)
	require.Equal(t, "memo-value", decodePayload[string](t, startReq.GetMemo().GetFields()["memo_key"]))
	require.Len(t, decodePayload[[]byte](t, startReq.GetMemo().GetFields()[workflowStartRecipeMemoKey]), 32)

	fields := startReq.GetSearchAttributes().GetIndexedFields()
	requireSearchAttribute(t, fields, "SessionID", enumspb.INDEXED_VALUE_TYPE_KEYWORD, "session-123")
	requireSearchAttribute(t, fields, "Approved", enumspb.INDEXED_VALUE_TYPE_BOOL, true)
	requireSearchAttribute(t, fields, "Attempt", enumspb.INDEXED_VALUE_TYPE_INT, int64(7))
	requireSearchAttribute(t, fields, "Score", enumspb.INDEXED_VALUE_TYPE_DOUBLE, 12.5)
	requireSearchAttribute(t, fields, "OccurredAt", enumspb.INDEXED_VALUE_TYPE_DATETIME, occurredAt)
	requireSearchAttribute(t, fields, "Labels", enumspb.INDEXED_VALUE_TYPE_KEYWORD_LIST, []string{"alpha", "beta"})

	require.NoError(t, handle.Cancel(context.Background()))
	cancelReq := service.cancelRequest()
	require.Equal(t, req.ID, cancelReq.GetWorkflowExecution().GetWorkflowId())
	require.Empty(t, cancelReq.GetWorkflowExecution().GetRunId())
}

func TestClientStartWorkflowEnforcesExactPayloadLimitBeforeSubmission(t *testing.T) {
	t.Parallel()

	service := &testWorkflowService{}
	eng, err := NewClient(Options{ClientOptions: &client.Options{}})
	require.NoError(t, err)
	eng.client.Close()
	eng.client = newWorkflowServiceClient(t, service)
	t.Cleanup(func() {
		require.NoError(t, eng.Close())
	})

	dataConverter := workflowcodec.NewDataConverter()
	recipePayload, err := dataConverter.ToPayload(make([]byte, sha256.Size))
	require.NoError(t, err)
	reserved := len(startrecipe.MemoKey) + temporalPayloadSize(recipePayload)
	exact := temporalInputAtWorkflowBudget(t, "root-1", "agent.workflow", "agent.queue", reserved, 0)
	_, err = eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "root-1", Workflow: "agent.workflow", TaskQueue: "agent.queue", Input: exact,
	})
	require.NoError(t, err)

	oversized := temporalInputAtWorkflowBudget(t, "root-2", "agent.workflow", "agent.queue", reserved, 1)
	_, err = eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "root-2", Workflow: "agent.workflow", TaskQueue: "agent.queue", Input: oversized,
	})
	require.ErrorContains(t, err, "payloads exceed maximum aggregate size")
	require.Equal(t, "root-1", service.startRequest().GetWorkflowId())
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
		ID:        "run-123",
		Workflow:  "agent.workflow",
		TaskQueue: "agent.queue",
		Input:     &api.RunInput{RunID: "run-123"},
		SearchAttributes: map[string]any{
			"Unsupported": []int{1, 2, 3},
		},
	})
	require.ErrorContains(t, err, `search attribute "Unsupported" has unsupported type []int`)
}

func TestStartWorkflowRejectsReservedRecipeMemoKey(t *testing.T) {
	eng := newTestEngine(t)
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
			return &api.RunOutput{}, nil
		},
	}))

	_, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{RunID: "run"},
		Memo:  map[string]engine.EncodedValue{workflowStartRecipeMemoKey: {Data: []byte("caller")}},
	})
	require.ErrorContains(t, err, "reserved")
}

func TestStartWorkflowValidatesDuplicateRecipe(t *testing.T) {
	t.Parallel()

	service := &testWorkflowService{}
	eng, err := NewWorker(Options{
		ClientOptions: &client.Options{},
		WorkerOptions: WorkerOptions{
			TaskQueue: "default.queue",
		},
	})
	require.NoError(t, err)
	eng.client.Close()
	eng.client = newWorkflowServiceClient(t, service)
	t.Cleanup(func() {
		require.NoError(t, eng.Close())
	})
	err = eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
			return &api.RunOutput{}, nil
		},
	})
	require.NoError(t, err)
	request := engine.WorkflowStartRequest{
		ID: "run-123", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		Input: &api.RunInput{RunID: "run-123"},
		Memo: map[string]engine.EncodedValue{
			"kind": {Metadata: map[string][]byte{"encoding": []byte("binary/plain")}, Data: []byte("value")},
		},
	}

	_, err = eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	_, err = eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)

	tests := []struct {
		name  string
		value engine.EncodedValue
	}{
		{
			name: "metadata",
			value: engine.EncodedValue{
				Metadata: map[string][]byte{"encoding": []byte("json/plain")},
				Data:     []byte("value"),
			},
		},
		{
			name: "data",
			value: engine.EncodedValue{
				Metadata: map[string][]byte{"encoding": []byte("binary/plain")},
				Data:     []byte("changed"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			changed.Memo = map[string]engine.EncodedValue{"kind": test.value}
			_, err := eng.StartWorkflow(t.Context(), changed)
			require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
		})
	}

	service.removeRecipeMemo()
	_, err = eng.StartWorkflow(t.Context(), request)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}

func TestStartWorkflowRejectsIncompleteRequest(t *testing.T) {
	t.Parallel()

	eng, err := NewClient(Options{ClientOptions: &client.Options{}})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, eng.Close())
	})
	valid := engine.WorkflowStartRequest{
		ID:        "run-123",
		Workflow:  "agent.workflow",
		TaskQueue: "agent.queue",
		Input:     &api.RunInput{RunID: "run-123"},
	}
	tests := []struct {
		name    string
		wantErr string
		change  func(*engine.WorkflowStartRequest)
	}{
		{name: "missing id", wantErr: "workflow id is required", change: func(req *engine.WorkflowStartRequest) {
			req.ID = ""
		}},
		{name: "missing workflow", wantErr: "workflow name is required", change: func(req *engine.WorkflowStartRequest) {
			req.Workflow = ""
		}},
		{name: "missing task queue", wantErr: "workflow task queue is required", change: func(req *engine.WorkflowStartRequest) {
			req.TaskQueue = ""
		}},
		{name: "missing input", wantErr: "workflow input is required", change: func(req *engine.WorkflowStartRequest) {
			req.Input = nil
		}},
		{name: "mismatched run id", wantErr: "workflow id must match input run id", change: func(req *engine.WorkflowStartRequest) {
			req.Input = &api.RunInput{RunID: "other-run"}
		}},
		{name: "retry interval without attempts", wantErr: "workflow retry timing requires max attempts or unlimited attempts", change: func(req *engine.WorkflowStartRequest) {
			req.RetryPolicy.InitialInterval = time.Second
		}},
		{name: "retry backoff without attempts", wantErr: "workflow retry timing requires max attempts or unlimited attempts", change: func(req *engine.WorkflowStartRequest) {
			req.RetryPolicy.BackoffCoefficient = 2
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := valid
			test.change(&req)
			_, err := eng.StartWorkflow(t.Context(), req)
			require.EqualError(t, err, test.wantErr)
		})
	}
}

func TestNewClientRejectsRegistration(t *testing.T) {
	t.Parallel()

	eng, err := NewClient(Options{
		ClientOptions: &client.Options{},
	})
	require.NoError(t, err)

	err = eng.RegisterWorkflow(context.Background(), engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(ctx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			return &api.RunOutput{}, nil
		},
	})
	require.ErrorContains(t, err, "client mode cannot register workflows")
}

// newTestEngine returns a Temporal engine backed by a lazy Temporal client so tests can
// exercise registration logic without contacting a Temporal server.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()

	eng, err := NewWorker(Options{
		ClientOptions: &client.Options{},
		WorkerOptions: WorkerOptions{
			TaskQueue: "default.queue",
		},
	})
	require.NoError(t, err)
	return eng
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

	mu        sync.Mutex
	startReq  *workflowservice.StartWorkflowExecutionRequest
	cancelReq *workflowservice.RequestCancelWorkflowExecutionRequest
}

func (s *testWorkflowService) RequestCancelWorkflowExecution(
	_ context.Context,
	req *workflowservice.RequestCancelWorkflowExecutionRequest,
) (*workflowservice.RequestCancelWorkflowExecutionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelReq = req
	return &workflowservice.RequestCancelWorkflowExecutionResponse{}, nil
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
	defer s.mu.Unlock()
	if s.startReq != nil {
		alreadyStarted := &serviceerror.WorkflowExecutionAlreadyStarted{
			Message:        "workflow already started",
			StartRequestId: s.startReq.RequestId,
			RunId:          "temporal-run-123",
		}
		return nil, alreadyStarted.Status().Err()
	}
	s.startReq = req
	return &workflowservice.StartWorkflowExecutionResponse{RunId: "temporal-run-123"}, nil
}

func (s *testWorkflowService) DescribeWorkflowExecution(context.Context, *workflowservice.DescribeWorkflowExecutionRequest) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{
				WorkflowId: s.startReq.WorkflowId,
				RunId:      "temporal-run-123",
			},
			Memo: s.startReq.Memo,
		},
	}, nil
}

// startRequest returns the most recent workflow start request observed by the
// local test server.
func (s *testWorkflowService) startRequest() *workflowservice.StartWorkflowExecutionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startReq
}

// cancelRequest returns the cancellation observed by the local gRPC server.
func (s *testWorkflowService) cancelRequest() *workflowservice.RequestCancelWorkflowExecutionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelReq
}

// removeRecipeMemo simulates a queryable workflow started before recipe
// fingerprints were deployed.
func (s *testWorkflowService) removeRecipeMemo() {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.startReq.Memo.Fields, workflowStartRecipeMemoKey)
}

// temporalInputAtWorkflowBudget builds input whose encoded bytes and start
// names are exactly at the shared limit, plus extra bytes for rejection tests.
func temporalInputAtWorkflowBudget(
	t *testing.T,
	id, workflow, queue string,
	reserved, extra int,
) *api.RunInput {
	t.Helper()
	dataConverter := workflowcodec.NewDataConverter()
	base := &api.RunInput{RunID: id, Metadata: map[string]any{"payload": ""}}
	payload, err := dataConverter.ToPayload(base)
	require.NoError(t, err)
	padding := engine.MaxPayloadBytes - temporalPayloadSize(payload) - len(id) - len(workflow) - len(queue) - reserved + extra
	require.Positive(t, padding)
	input := &api.RunInput{RunID: id, Metadata: map[string]any{"payload": strings.Repeat("x", padding)}}
	payload, err = dataConverter.ToPayload(input)
	require.NoError(t, err)
	require.Equal(t, engine.MaxPayloadBytes+extra, temporalPayloadSize(payload)+len(id)+len(workflow)+len(queue)+reserved)
	return input
}

// temporalPayloadSize returns the encoded data and metadata bytes charged to
// one workflow start.
func temporalPayloadSize(payload *commonpb.Payload) int {
	size := len(payload.Data)
	for key, value := range payload.Metadata {
		size += len(key) + len(value)
	}
	return size
}
