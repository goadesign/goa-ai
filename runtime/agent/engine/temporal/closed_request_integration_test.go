//go:build integration

package temporal

// These tests use a dedicated local Temporal server and real worker activities.
// Runtime storage is in-memory; durable host-store adoption needs its own proof.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type (
	requestCompletionPlanner struct {
		calls   atomic.Int32
		suspend bool
	}

	// submittedRequestEngine records an owned copy of the exact request sent
	// through StartPrepared, while the real engine owns submission and execution.
	submittedRequestEngine struct {
		*Engine
		request engine.WorkflowStartRequest
	}
)

func TestTemporalServerChildAcceptedRequest(t *testing.T) {
	eng, queue := localRequestEngine(t)
	request := engine.ChildWorkflowRequest{
		ID: queue + "-child", Workflow: "digest-child", TaskQueue: queue,
		Input: &api.RunInput{RunID: queue + "-child", Metadata: map[string]any{"value": "accepted"}},
	}
	snapshot, err := startrecipe.SnapshotChildRequest(request)
	require.NoError(t, err)
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: request.Workflow,
		Handler: func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			input.Metadata["value"] = "working input"
			digest, err := w.StartRequestDigest()
			if err != nil {
				return nil, err
			}
			if digest != snapshot.Digest {
				return nil, fmt.Errorf("child digest differs from the accepted request")
			}
			canceled, cancel := w.WithCancel()
			cancel()
			detached, err := canceled.Detached().StartRequestDigest()
			if err != nil {
				return nil, err
			}
			if detached != digest {
				return nil, fmt.Errorf("detached child lost accepted request")
			}
			return &api.RunOutput{RunID: input.RunID}, nil
		},
	}))
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "digest-parent",
		Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			h, err := w.StartChildWorkflow(w.Context(), request)
			if err != nil {
				return nil, err
			}
			return h.Get(w.Context())
		},
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, eng.SealRegistration(ctx))
	id := queue + "-parent"
	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID: id, Workflow: "digest-parent", TaskQueue: queue, Input: &api.RunInput{RunID: id},
	})
	require.NoError(t, err)
	output, err := handle.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, request.ID, output.RunID)
}

// TestTemporalServerStartConflictStopsRetries checks real activity and workflow
// retries. The SDK's virtual child-retry clock cannot provide race-safe proof here.
func TestTemporalServerStartConflictStopsRetries(t *testing.T) {
	eng, queue := localRequestEngine(t)
	id := queue + "-conflict"
	var starts, writes atomic.Int32
	require.NoError(t, eng.RegisterStorageActivity(t.Context(), "start", engine.ActivityOptions{},
		func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
			writes.Add(1)
			return nil, engine.MarkActivityErrorNonRetryable(&engine.WorkflowStartConflictError{ID: id})
		}))
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "conflict",
		Handler: func(w engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			starts.Add(1)
			_, err := w.ExecuteStorageActivity(engine.StorageActivityCall{
				Name: "start", Command: &api.StorageActivityCommand{RootStart: &api.RootRunStartCommand{}},
				Options: engine.ActivityOptions{StartToCloseTimeout: 10 * time.Second, RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}},
			})
			return nil, err
		},
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, eng.SealRegistration(ctx))
	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID: id, Workflow: "conflict", TaskQueue: queue, Input: &api.RunInput{RunID: id},
		RetryPolicy: engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Millisecond},
	})
	require.NoError(t, err)
	_, err = handle.Wait(ctx)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
	var raw *api.RunOutput
	err = eng.client.GetWorkflow(ctx, id, handle.(*workflowHandle).run.GetRunID()).Get(ctx, &raw)
	var failure *temporal.ApplicationError
	require.ErrorAs(t, err, &failure)
	assert.True(t, failure.NonRetryable())
	assert.Equal(t, startConflictErrorType, failure.Type())
	assert.EqualValues(t, 1, starts.Load())
	assert.EqualValues(t, 1, writes.Load())
}

func TestTemporalServerClosedRequestSelectsOriginalStart(t *testing.T) {
	eng, queue := localRequestEngine(t)
	store := storageinmem.New()
	submitted := &submittedRequestEngine{Engine: eng}
	runtime := agentruntime.New(store, agentruntime.WithEngine(submitted))
	plan := &requestCompletionPlanner{}
	require.NoError(t, runtime.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
		Definition: testTemporalAgentDefinition("request.agent", "request-workflow", queue, nil),
		Planner:    plan, WorkflowHandler: runtime.ExecuteWorkflow,
		PlanActivityName: "request-plan", ResumeActivityName: "request-resume", ExecuteToolActivity: "request-tool",
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	require.NoError(t, runtime.Seal(ctx))
	for _, sessionless := range []bool{false, true} {
		t.Run(fmt.Sprintf("sessionless=%t", sessionless), func(t *testing.T) {
			runID := fmt.Sprintf("%s-run-%t", queue, sessionless)
			sessionID := ""
			if !sessionless {
				sessionID = queue + "-session"
				_, err := store.CreateSession(ctx, sessionID, time.Now().UTC())
				require.NoError(t, err)
			}
			agentClient := runtime.MustClient("request.agent")
			messages := []*model.Message{{
				Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "exact original request"}},
			}}
			options := []agentruntime.RunOption{agentruntime.WithRunID(runID), agentruntime.WithTurnID(runID)}
			var prepared *agentruntime.PreparedRun
			var err error
			if sessionless {
				prepared, err = agentClient.PrepareOneShot(ctx, messages, options...)
			} else {
				prepared, err = agentClient.Prepare(ctx, sessionID, messages, options...)
			}
			require.NoError(t, err)
			originalBytes, err := prepared.MarshalBinary()
			require.NoError(t, err)
			assert.Contains(t, string(originalBytes), `"goa-ai-prepared-run-v3"`)
			prepared, err = agentruntime.ParsePreparedRun(originalBytes)
			require.NoError(t, err)
			first, err := agentClient.StartPrepared(ctx, prepared)
			require.NoError(t, err)
			request := submitted.request
			_, err = first.Wait(ctx)
			require.NoError(t, err)
			meta, err := store.LoadRun(ctx, runID)
			require.NoError(t, err)
			records, err := store.ListRunRecords(ctx, runID, "", 100)
			require.NoError(t, err)
			require.Empty(t, records.NextCursor)
			calls := plan.calls.Load()
			firstExecution := first.(*workflowHandle).run.GetRunID()
			deleteRequestHistory(t, ctx, eng, runID, firstExecution)

			retry, err := agentClient.StartPrepared(ctx, prepared)
			require.NoError(t, err)
			_, err = retry.Wait(ctx)
			require.Error(t, err)
			var failure *temporal.ApplicationError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, cancellationCompletedErrorType, failure.Type())
			assert.True(t, failure.NonRetryable())
			assert.NotEqual(t, firstExecution, retry.(*workflowHandle).run.GetRunID())
			assert.Equal(t, calls, plan.calls.Load())
			after, err := store.LoadRun(ctx, runID)
			require.NoError(t, err)
			afterRecords, err := store.ListRunRecords(ctx, runID, "", 100)
			require.NoError(t, err)
			assert.Equal(t, meta, after)
			assert.Equal(t, records, afterRecords)

			retainedBytes, err := prepared.MarshalBinary()
			require.NoError(t, err)
			assert.Equal(t, originalBytes, retainedBytes)
			// Different history must fail at its immutable publication. It cannot
			// be smuggled into a retry of the accepted engine request.
			changed := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "different request"}}}}
			if sessionless {
				_, err = agentClient.PrepareOneShot(ctx, changed, options...)
			} else {
				_, err = agentClient.Prepare(ctx, sessionID, changed, options...)
			}
			require.ErrorIs(t, err, storage.ErrSeedConflict)
			// A different launch control is a separately accepted engine request,
			// even with the same published history and visible Run identity.
			deleteRequestHistory(t, ctx, eng, runID, retry.(*workflowHandle).run.GetRunID())
			request.RunTimeout += time.Second
			conflict, err := eng.StartWorkflow(ctx, request)
			require.NoError(t, err)
			_, err = conflict.Wait(ctx)
			require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
			var raw *api.RunOutput
			err = eng.client.GetWorkflow(ctx, runID, conflict.(*workflowHandle).run.GetRunID()).Get(ctx, &raw)
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, startConflictErrorType, failure.Type())
			assert.True(t, failure.NonRetryable())
			assert.Equal(t, calls, plan.calls.Load())
			final, err := store.ListRunRecords(ctx, runID, "", 100)
			require.NoError(t, err)
			assert.Equal(t, records, final)
		})
	}
}

func TestTemporalServerClosedContinuationSelectsOriginalStart(t *testing.T) {
	eng, queue := localRequestEngine(t)
	store := storageinmem.New()
	runtime := agentruntime.New(store, agentruntime.WithEngine(eng))
	plan := &requestCompletionPlanner{suspend: true}
	require.NoError(t, runtime.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
		Definition: testTemporalAgentDefinition("request.agent", "request-workflow", queue, nil),
		Planner:    plan, WorkflowHandler: runtime.ExecuteWorkflow,
		PlanActivityName: "request-plan", ResumeActivityName: "request-resume", ExecuteToolActivity: "request-tool",
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	require.NoError(t, runtime.Seal(ctx))
	sessionID, predecessorID, successorID := queue+"-session", queue+"-first", queue+"-next"
	_, err := store.CreateSession(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	agentClient := runtime.MustClient("request.agent")
	initial, err := agentClient.Prepare(ctx, sessionID, nil,
		agentruntime.WithRunID(predecessorID), agentruntime.WithTurnID(predecessorID))
	require.NoError(t, err)
	first, err := agentClient.StartPrepared(ctx, initial)
	require.NoError(t, err)
	output, err := first.Wait(ctx)
	require.NoError(t, err)
	require.NotNil(t, output.Suspension)
	assert.Equal(t, "goa-ai.run-suspension.v9", output.Suspension.Version)
	response := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
		ID: "question", Answer: "exact accepted answer",
	}}
	prepared, err := agentClient.PrepareContinuation(
		ctx, sessionID, predecessorID, successorID, successorID, response, agentruntime.WorkflowOptions{},
	)
	require.NoError(t, err)
	originalBytes, err := prepared.MarshalBinary()
	require.NoError(t, err)
	assert.Contains(t, string(originalBytes), `"goa-ai-prepared-run-v3"`)
	continuation, err := agentClient.StartPrepared(ctx, prepared)
	require.NoError(t, err)
	_, err = continuation.Wait(ctx)
	require.NoError(t, err)
	meta, err := store.LoadRun(ctx, successorID)
	require.NoError(t, err)
	records, err := store.ListRunRecords(ctx, successorID, "", 100)
	require.NoError(t, err)
	require.Empty(t, records.NextCursor)
	calls := plan.calls.Load()
	deleteRequestHistory(t, ctx, eng, successorID, continuation.(*workflowHandle).run.GetRunID())
	retry, err := agentClient.StartPrepared(ctx, prepared)
	require.NoError(t, err)
	_, err = retry.Wait(ctx)
	require.Error(t, err)
	var failure *temporal.ApplicationError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, cancellationCompletedErrorType, failure.Type())
	assert.True(t, failure.NonRetryable())
	assert.NotEqual(t, continuation.(*workflowHandle).run.GetRunID(), retry.(*workflowHandle).run.GetRunID())
	assert.Equal(t, calls, plan.calls.Load())
	after, err := store.LoadRun(ctx, successorID)
	require.NoError(t, err)
	afterRecords, err := store.ListRunRecords(ctx, successorID, "", 100)
	require.NoError(t, err)
	assert.Equal(t, meta, after)
	assert.Equal(t, records, afterRecords)
	retainedBytes, err := prepared.MarshalBinary()
	require.NoError(t, err)
	assert.Equal(t, originalBytes, retainedBytes)
}

func (p *requestCompletionPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	p.calls.Add(1)
	if p.suspend {
		return &planner.PlanResult{Await: planner.NewAwait(
			planner.AwaitClarificationItem(&planner.AwaitClarification{
				ID: "question", Question: "Which value?", MissingFields: []string{"value"},
			}),
		)}, nil
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{
		Message: &model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "completed"}},
		},
	}}, nil
}

func (p *requestCompletionPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.calls.Add(1)
	if !p.suspend {
		return nil, fmt.Errorf("unexpected planner resume")
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{
		Message: &model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "completed continuation"}},
		},
	}}, nil
}

// localRequestEngine requires an explicitly supplied loopback server. It cannot
// connect to a configured development or production Temporal endpoint.
func localRequestEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	address := os.Getenv("GOA_AI_START_TEST_TEMPORAL_ADDRESS")
	if address == "" {
		t.Skip("dedicated local Temporal server address is required")
	}
	host, _, err := net.SplitHostPort(address)
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.NotNil(t, ip)
	require.True(t, ip.IsLoopback(), "test server must be loopback")
	queue := fmt.Sprintf("closed-request-%d", time.Now().UnixNano())
	eng, err := NewWorker(Options{
		ClientOptions:   &client.Options{HostPort: address, Namespace: "default"},
		WorkerOptions:   WorkerOptions{TaskQueue: queue},
		Instrumentation: InstrumentationOptions{DisableTracing: true, DisableMetrics: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, eng.Close()) })
	return eng, queue
}

// deleteRequestHistory removes one exact closed synthetic execution so a later
// request must pass through a genuinely new workflow execution.
func deleteRequestHistory(t *testing.T, ctx context.Context, eng *Engine, id, execution string) {
	t.Helper()
	_, err := eng.client.WorkflowService().DeleteWorkflowExecution(ctx, &workflowservice.DeleteWorkflowExecutionRequest{
		Namespace: "default", WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: id, RunId: execution},
	})
	require.NoError(t, err)
	deadline, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		_, err := eng.client.DescribeWorkflowExecution(deadline, id, execution)
		var absent *serviceerror.NotFound
		if errors.As(err, &absent) {
			return
		}
		require.NoError(t, err)
		select {
		case <-deadline.Done():
			t.Fatal("deleted synthetic workflow remained queryable", deadline.Err())
		case <-time.After(time.Second):
		}
	}
}

// StartWorkflow saves the immutable submitted request only after real engine
// acceptance. Tests can then change a launch control without rebuilding history.
func (e *submittedRequestEngine) StartWorkflow(ctx context.Context, request engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	snapshot, err := startrecipe.SnapshotRequest(request)
	if err != nil {
		return nil, err
	}
	handle, err := e.Engine.StartWorkflow(ctx, request)
	if err == nil {
		e.request = snapshot.Request
	}
	return handle, err
}
