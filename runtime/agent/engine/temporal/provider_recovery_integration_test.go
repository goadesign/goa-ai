//go:build integration

package temporal

// These tests use a dedicated loopback Temporal server. Real child start,
// signal, retry, and replay history supplies the execution identities; the SDK
// test environment cannot supply those facts for later native attempts.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestTemporalServerProviderRecoveryRelayAndReplay(t *testing.T) {
	eng, queue := localRequestEngine(t)
	leaf := recoveryTestLeaf(eng)
	wrapper := func(ctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
		w, err := NewWorkflowContext(eng, ctx)
		if err != nil {
			return nil, err
		}
		child, err := w.StartChildWorkflow(w.Context(), engine.ChildWorkflowRequest{
			ID: input.RunID + "-leaf", Workflow: "recovery-leaf", TaskQueue: queue,
			Input: &api.RunInput{RunID: input.RunID + "-leaf"},
		})
		if err != nil {
			return nil, err
		}
		if _, err := child.Get(w.Context()); err != nil {
			return nil, err
		}
		control := w.(*temporalWorkflowContext).control
		if len(control.pauses) != 1 {
			return nil, fmt.Errorf("wrapper retained %d child pauses", len(control.pauses))
		}
		return &api.RunOutput{RunID: input.RunID}, nil
	}
	root := eng.temporalWorkflowHandler(func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		var accepted, transitions, pauses int
		if err := w.ProviderRecovery().RegisterPauseHandler(func(engine.WorkflowContext, string, engine.ProviderRecoveryPaused) error {
			pauses++
			return nil
		}); err != nil {
			return nil, err
		}
		if err := w.ProviderRecovery().Register(func(
			ctx engine.WorkflowContext, childID string, failure engine.ProviderRecoveryFailure, control engine.ProviderRecovery,
		) (engine.ProviderRecoveryReceive, error) {
			if childID != input.RunID+"-wrapper" || failure.Err.RequestID() != "synthetic-request" ||
				!errors.Is(failure.Err, context.DeadlineExceeded) {
				return nil, errors.New("relayed failure lost child identity or provider cause")
			}
			accepted++
			return func(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
				transitions++
				switch message.(type) {
				case engine.ProviderWaitRequest:
					_, err := control.Send(ctx, engine.ProviderRecoveryPermission{ExpiresAt: ctx.Now().Add(time.Minute)})
					return err
				case engine.ProviderAttemptSucceeded:
					_, err := control.Send(ctx, engine.ProviderRecoverySettled{})
					return err
				default:
					return fmt.Errorf("unexpected transition %T", message)
				}
			}, nil
		}); err != nil {
			return nil, err
		}
		child, err := w.StartChildWorkflow(w.Context(), engine.ChildWorkflowRequest{
			ID: input.RunID + "-wrapper", Workflow: "recovery-wrapper", TaskQueue: queue,
			Input: &api.RunInput{RunID: input.RunID + "-wrapper"},
		})
		if err != nil {
			return nil, err
		}
		if _, err := child.Get(w.Context()); err != nil {
			return nil, err
		}
		if accepted != 1 || transitions != 2 || pauses != 0 {
			return nil, fmt.Errorf("root accepted=%d transitions=%d descendant pauses=%d", accepted, transitions, pauses)
		}
		return &api.RunOutput{RunID: input.RunID}, nil
	})
	bundle := eng.workerForQueue(queue)
	bundle.registerWorkflow("recovery-root", root)
	bundle.registerWorkflow("recovery-wrapper", wrapper)
	bundle.registerWorkflow("recovery-leaf", leaf)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, eng.SealRegistration(ctx))
	id := queue + "-root"
	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID: id, Workflow: "recovery-root", TaskQueue: queue, Input: &api.RunInput{RunID: id},
	})
	require.NoError(t, err)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
	for _, test := range []struct {
		id   string
		name string
		fn   any
	}{
		{id, "recovery-root", root},
		{id + "-wrapper", "recovery-wrapper", wrapper},
		{id + "-wrapper-leaf", "recovery-leaf", leaf},
	} {
		history := recoveryTestHistory(t, ctx, eng, test.id, "")
		replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
			DataConverter: NewAgentDataConverter(), Interceptors: eng.workerOpts.Interceptors,
		})
		require.NoError(t, err)
		replayer.RegisterWorkflowWithOptions(test.fn, workflow.RegisterOptions{Name: test.name})
		runID := history.Events[0].GetWorkflowExecutionStartedEventAttributes().OriginalExecutionRunId
		require.NoError(t, replayer.ReplayWorkflowExecution(ctx, eng.client.WorkflowService(), nil,
			"default", workflow.Execution{ID: test.id, RunID: runID}), test.name)
	}
}

// recoveryTestLeaf waits for actual owner permission and settlement. It records
// its own measured pause separately from the unfinished request's transitions.
func recoveryTestLeaf(eng *Engine) func(workflow.Context, *api.RunInput) (*api.RunOutput, error) {
	return func(ctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
		w, err := NewWorkflowContext(eng, ctx)
		if err != nil {
			return nil, err
		}
		var permitted, settled bool
		failure := engine.ProviderRecoveryFailure{
			PublicationBatchID: "synthetic-publication", StartedAt: w.Now(), EndedAt: w.Now(),
			Err: model.NewProviderError("synthetic", "complete", 503, model.ProviderErrorKindUnavailable,
				"capacity", "wait", "synthetic-request", true, context.DeadlineExceeded),
		}
		control, err := w.ProviderRecovery().Open(w, failure, func(_ engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
			switch value := message.(type) {
			case engine.ProviderRecoveryPermission:
				permitted = true
			case engine.ProviderRecoverySettled:
				settled = true
			case engine.ProviderRecoveryStopped:
				return value.Err
			default:
				return fmt.Errorf("unexpected reply %T", message)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if _, err := control.Send(w, engine.ProviderWaitRequest{Delay: time.Millisecond}); err != nil {
			return nil, err
		}
		if err := w.Await(func() bool { return permitted }); err != nil {
			return nil, err
		}
		start := w.Now()
		timer, err := w.NewTimer(w.Context(), time.Millisecond)
		if err != nil {
			return nil, err
		}
		if _, err := timer.Get(w.Context()); err != nil {
			return nil, err
		}
		if _, err := w.ProviderRecovery().ReportPause(w, engine.ProviderRecoveryPaused{StartedAt: start, Through: w.Now()}); err != nil {
			return nil, err
		}
		if _, err := control.Send(w, engine.ProviderAttemptSucceeded{}); err != nil {
			return nil, err
		}
		if err := w.Await(func() bool { return settled }); err != nil {
			return nil, err
		}
		return &api.RunOutput{RunID: input.RunID}, nil
	}
}

func recoveryTestHistory(t *testing.T, ctx context.Context, eng *Engine, id, runID string) *historypb.History {
	t.Helper()
	result := &historypb.History{}
	history := eng.client.GetWorkflowHistory(ctx, id, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for history.HasNext() {
		event, err := history.Next()
		require.NoError(t, err)
		result.Events = append(result.Events, event)
		require.Less(t, len(result.Events), 500, "synthetic test history unexpectedly large")
	}
	return result
}

func TestTemporalServerProviderRecoveryAfterUnpolledAttempts(t *testing.T) {
	eng, queue := localRequestEngine(t)
	address := os.Getenv("GOA_AI_START_TEST_TEMPORAL_ADDRESS")
	childEngine, err := NewWorker(Options{
		ClientOptions:   &client.Options{HostPort: address, Namespace: "default"},
		WorkerOptions:   WorkerOptions{TaskQueue: queue + "-late"},
		Instrumentation: InstrumentationOptions{DisableTracing: true, DisableMetrics: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, childEngine.Close()) })
	childEngine.workerForQueue(queue+"-late").registerWorkflow("late-child", recoveryTestLeaf(childEngine))
	root := eng.temporalWorkflowHandler(func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		var opened, pauses int
		if err := w.ProviderRecovery().RegisterPauseHandler(func(engine.WorkflowContext, string, engine.ProviderRecoveryPaused) error {
			pauses++
			return nil
		}); err != nil {
			return nil, err
		}
		if err := w.ProviderRecovery().Register(func(
			_ engine.WorkflowContext, _ string, _ engine.ProviderRecoveryFailure, control engine.ProviderRecovery,
		) (engine.ProviderRecoveryReceive, error) {
			opened++
			return func(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
				if _, ok := message.(engine.ProviderWaitRequest); ok {
					_, err := control.Send(ctx, engine.ProviderRecoveryPermission{ExpiresAt: ctx.Now().Add(time.Minute)})
					return err
				}
				_, err := control.Send(ctx, engine.ProviderRecoverySettled{})
				return err
			}, nil
		}); err != nil {
			return nil, err
		}
		child, err := w.StartChildWorkflow(w.Context(), engine.ChildWorkflowRequest{
			ID: input.RunID + "-child", Workflow: "late-child", TaskQueue: queue + "-late",
			Input:      &api.RunInput{RunID: input.RunID + "-child"},
			RunTimeout: 3 * time.Second, RetryPolicy: engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Millisecond},
		})
		if err != nil {
			return nil, err
		}
		if _, err := child.Get(w.Context()); err != nil {
			return nil, err
		}
		if opened != 1 || pauses != 1 {
			return nil, fmt.Errorf("unpolled attempts created recovery state: opened=%d pauses=%d", opened, pauses)
		}
		return &api.RunOutput{RunID: input.RunID}, nil
	})
	eng.workerForQueue(queue).registerWorkflow("late-parent", root)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, eng.SealRegistration(ctx))
	id := queue + "-late-parent"
	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID: id, Workflow: "late-parent", TaskQueue: queue, Input: &api.RunInput{RunID: id},
	})
	require.NoError(t, err)
	var current *historypb.WorkflowExecutionStartedEventAttributes
	require.Eventually(t, func() bool {
		history := eng.client.GetWorkflowHistory(ctx, id+"-child", "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		if !history.HasNext() {
			return false
		}
		event, err := history.Next()
		if err != nil {
			return false
		}
		current = event.GetWorkflowExecutionStartedEventAttributes()
		return current != nil && current.Attempt == 3
	}, 12*time.Second, 20*time.Millisecond)
	require.NoError(t, childEngine.SealRegistration(ctx))
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
	history := recoveryTestHistory(t, ctx, eng, id+"-child", "")
	started := history.Events[0].GetWorkflowExecutionStartedEventAttributes()
	assert.EqualValues(t, 3, started.Attempt)
	assert.NotEqual(t, started.FirstExecutionRunId, started.OriginalExecutionRunId)
	assert.NotNil(t, started.Header.Fields[recoveryHeaderName])
	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
		DataConverter: NewAgentDataConverter(), Interceptors: childEngine.workerOpts.Interceptors,
	})
	require.NoError(t, err)
	replayer.RegisterWorkflowWithOptions(recoveryTestLeaf(childEngine), workflow.RegisterOptions{Name: "late-child"})
	require.NoError(t, replayer.ReplayWorkflowExecution(ctx, eng.client.WorkflowService(), nil,
		"default", workflow.Execution{ID: id + "-child", RunID: started.OriginalExecutionRunId}))
}

func TestTemporalServerProviderRecoveryContinuationAndDelayedDuplicate(t *testing.T) {
	eng, queue := localRequestEngine(t)
	leaf := func(ctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
		output, err := recoveryTestLeaf(eng)(ctx, input)
		if err != nil {
			return nil, err
		}
		if workflow.GetInfo(ctx).ContinuedExecutionRunID == "" {
			return nil, workflow.NewContinueAsNewError(ctx, "continuing-child", input)
		}
		var finish bool
		workflow.GetSignalChannel(ctx, "finish").Receive(ctx, &finish)
		return output, nil
	}
	root := eng.temporalWorkflowHandler(func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		var opened, pauses int
		expires := w.Now().Add(time.Minute)
		if err := w.ProviderRecovery().RegisterPauseHandler(func(engine.WorkflowContext, string, engine.ProviderRecoveryPaused) error {
			pauses++
			return nil
		}); err != nil {
			return nil, err
		}
		if err := w.ProviderRecovery().Register(func(
			_ engine.WorkflowContext, _ string, _ engine.ProviderRecoveryFailure, control engine.ProviderRecovery,
		) (engine.ProviderRecoveryReceive, error) {
			opened++
			return func(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
				if _, ok := message.(engine.ProviderWaitRequest); ok {
					_, err := control.Send(ctx, engine.ProviderRecoveryPermission{ExpiresAt: expires})
					return err
				}
				_, err := control.Send(ctx, engine.ProviderRecoverySettled{})
				return err
			}, nil
		}); err != nil {
			return nil, err
		}
		native := w.(*temporalWorkflowContext)
		if err := workflow.SetQueryHandler(native.ctx, "counts", func() ([]int, error) {
			return []int{opened, pauses}, nil
		}); err != nil {
			return nil, err
		}
		child, err := w.StartChildWorkflow(w.Context(), engine.ChildWorkflowRequest{
			ID: input.RunID + "-child", Workflow: "continuing-child", TaskQueue: queue,
			Input: &api.RunInput{RunID: input.RunID + "-child"},
		})
		if err != nil {
			return nil, err
		}
		if _, err := child.Get(w.Context()); err != nil {
			return nil, err
		}
		if opened != 2 || pauses != 2 || len(native.control.endpoints) != 2 {
			return nil, fmt.Errorf("continuation or duplicate replaced original evidence: opened=%d pauses=%d requests=%d",
				opened, pauses, len(native.control.endpoints))
		}
		return &api.RunOutput{RunID: input.RunID}, nil
	})
	bundle := eng.workerForQueue(queue)
	bundle.registerWorkflow("continuing-root", root)
	bundle.registerWorkflow("continuing-child", leaf)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	require.NoError(t, eng.SealRegistration(ctx))
	id := queue + "-continuing"
	handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID: id, Workflow: "continuing-root", TaskQueue: queue, Input: &api.RunInput{RunID: id},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		value, err := eng.client.QueryWorkflow(ctx, id, "", "counts")
		if err != nil {
			return false
		}
		var counts []int
		return value.Get(&counts) == nil && len(counts) == 2 && counts[0] == 2 && counts[1] == 2
	}, 10*time.Second, 20*time.Millisecond)
	rootHistory := recoveryTestHistory(t, ctx, eng, id, "")
	var original recoveryFrame
	for _, event := range rootHistory.Events {
		signal := event.GetWorkflowExecutionSignaledEventAttributes()
		if signal == nil || signal.SignalName != recoverySignalName {
			continue
		}
		var frame recoveryFrame
		require.NoError(t, NewAgentDataConverter().FromPayloads(signal.Input, &frame))
		if frame.Pause != nil {
			original = frame
			break
		}
	}
	require.NotNil(t, original.Pause)
	continued := recoveryTestHistory(t, ctx, eng, id+"-child", "")
	start := continued.Events[0].GetWorkflowExecutionStartedEventAttributes()
	require.Equal(t, original.Source.RunID, start.ContinuedExecutionRunId)
	require.Equal(t, original.FirstRunID, start.FirstExecutionRunId)
	require.NotEqual(t, original.Source.RunID, start.OriginalExecutionRunId)
	require.NotNil(t, start.Header.Fields[recoveryHeaderName])
	require.NoError(t, eng.client.SignalWorkflow(ctx, id, "", recoverySignalName, original))
	require.NoError(t, eng.client.SignalWorkflow(ctx, id+"-child", start.OriginalExecutionRunId, "finish", true))
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
	for _, execution := range []struct {
		id, run, name string
		fn            any
	}{
		{id, "", "continuing-root", root},
		{id + "-child", original.Source.RunID, "continuing-child", leaf},
		{id + "-child", start.OriginalExecutionRunId, "continuing-child", leaf},
	} {
		replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
			DataConverter: NewAgentDataConverter(), Interceptors: eng.workerOpts.Interceptors,
		})
		require.NoError(t, err)
		replayer.RegisterWorkflowWithOptions(execution.fn, workflow.RegisterOptions{Name: execution.name})
		require.NoError(t, replayer.ReplayWorkflowExecution(ctx, eng.client.WorkflowService(), nil,
			"default", workflow.Execution{ID: execution.id, RunID: execution.run}))
	}
}
