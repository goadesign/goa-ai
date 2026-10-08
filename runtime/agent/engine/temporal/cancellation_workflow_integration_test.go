//go:build integration

// These checks use an explicitly supplied local Temporal server. A cancellation
// accepted without a worker must settle saved Tasks when a replacement starts.
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/run"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/session"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

func TestTemporalServerCancellationAfterWorkerReplacement(t *testing.T) {
	original, queue := localRequestEngine(t)
	store := storageinmem.New()
	spec := anyJSONToolSpec("remote.lookup")
	definition := testTemporalAgentDefinition("cancel.agent", "cancel.workflow", queue, []tools.ToolSpec{spec})
	plan := &mcpInputPlanner{}
	var creates, cancels atomic.Int32
	install := func(backend *Engine) *agentruntime.Runtime {
		runtime := agentruntime.New(store, agentruntime.WithEngine(backend))
		require.NoError(t, runtime.RegisterToolset(agentruntime.ToolsetRegistration{
			Name: "remote", Specs: []tools.ToolSpec{spec}, DecodeInExecutor: true,
			Execute: func(_ context.Context, call *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
				if operation := call.ExecutionContinuation; operation != nil {
					id, cancel := operation.AsTaskCancel()
					assert.True(t, cancel)
					assert.Equal(t, "accepted-task", id)
					cancels.Add(1)
					pending, err := tooloperation.NewPendingTaskWait(id, nil)
					if err != nil {
						return nil, err
					}
					return agentruntime.Unfinished(pending)
				}
				creates.Add(1)
				pending, err := tooloperation.NewPendingTaskInput("accepted-task", nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
					"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
				}})
				if err != nil {
					return nil, err
				}
				return agentruntime.Unfinished(pending)
			},
		}))
		require.NoError(t, runtime.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
			Definition: definition, Planner: plan, WorkflowHandler: runtime.ExecuteWorkflow,
			PlanActivityName: "cancel.plan", ResumeActivityName: "cancel.resume", ExecuteToolActivity: "cancel.execute",
		}))
		return runtime
	}
	ctx, stop := context.WithTimeout(t.Context(), time.Minute)
	defer stop()
	runtime := install(original)
	sessionID, runID := queue+"-session", queue+"-source"
	_, err := store.CreateSession(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	first, err := runtime.MustClient("cancel.agent").Run(ctx, sessionID, nil, agentruntime.WithRunID(runID))
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	original.stopWorkers()

	address := os.Getenv("GOA_AI_START_TEST_TEMPORAL_ADDRESS")
	remote, err := NewClient(Options{ClientOptions: &client.Options{HostPort: address, Namespace: "default"}})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, remote.Close()) })
	caller := agentruntime.New(store, agentruntime.WithEngine(remote))
	request := engine.CancellationRequest{RunID: runID, Reason: run.CancellationReasonUserRequested}
	callerContext, cancelCaller := context.WithCancel(ctx)
	require.NoError(t, caller.CancelRun(callerContext, request))
	cancelCaller()
	require.NoError(t, caller.CancelRun(ctx, request))
	before, err := store.LoadRun(ctx, runID)
	require.NoError(t, err)
	assert.Empty(t, before.SuccessorRunID)
	assert.Zero(t, cancels.Load())

	replacement, err := NewWorker(Options{
		ClientOptions:   &client.Options{HostPort: address, Namespace: "default"},
		WorkerOptions:   WorkerOptions{TaskQueue: queue},
		Instrumentation: InstrumentationOptions{DisableTracing: true, DisableMetrics: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, replacement.Close()) })
	resumed := install(replacement)
	require.NoError(t, resumed.Seal(ctx))
	jobID, _, err := startrecipe.CancellationRecipe("cancel.workflow.cancel", queue, request)
	require.NoError(t, err)
	require.NoError(t, replacement.client.GetWorkflow(ctx, jobID, "").Get(ctx, nil))
	previous, err := store.LoadRun(ctx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, previous.SuccessorRunID)
	cleanup, err := store.LoadRun(ctx, previous.SuccessorRunID)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, cleanup.Status)
	assert.Equal(t, request.Reason, cleanup.CancellationReason)
	assert.EqualValues(t, 1, creates.Load())
	assert.EqualValues(t, 1, cancels.Load())
	assert.EqualValues(t, 1, plan.starts.Load())
	assert.Zero(t, plan.resumes.Load())
	require.NoError(t, caller.CancelRun(ctx, request))
	assert.EqualValues(t, 1, cancels.Load())
}

// Temporal keeps the accepted job memo when history rolls over. Duplicate
// submission must still compare the original request after that rollover.
func TestTemporalServerCancellationKeepsRequestAfterHistoryRollover(t *testing.T) {
	backend, queue := localRequestEngine(t)
	name := "cancellation-rollover"
	request := engine.CancellationRequest{RunID: queue + "-source", Reason: run.CancellationReasonUserRequested}
	jobID, _, err := startrecipe.CancellationRecipe(name, queue, request)
	require.NoError(t, err)
	backend.workerForQueue(queue).registerWorkflow(name, func(ctx workflow.Context, got engine.CancellationRequest) error {
		if got != request {
			return errors.New("rollover changed the accepted cancellation request")
		}
		if workflow.GetInfo(ctx).ContinuedExecutionRunID == "" {
			return workflow.NewContinueAsNewErrorWithOptions(ctx, workflow.ContinueAsNewErrorOptions{BackoffStartInterval: time.Millisecond}, name, got)
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	require.NoError(t, backend.SealRegistration(ctx))
	require.NoError(t, backend.StartCancellationWorkflow(ctx, name, queue, request))
	require.NoError(t, backend.client.GetWorkflow(ctx, jobID, "").Get(ctx, nil))
	require.NoError(t, backend.StartCancellationWorkflow(ctx, name, queue, request))
	changed := request
	changed.Reason = run.CancellationReasonSessionEnded
	err = backend.StartCancellationWorkflow(ctx, name, queue, changed)
	var conflict *engine.CancellationConflictError
	require.ErrorAs(t, err, &conflict)
}
