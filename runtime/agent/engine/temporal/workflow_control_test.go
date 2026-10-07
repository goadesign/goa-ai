package temporal

// These tests run native workflows through the same worker interceptor used by
// engine-created workers. They distinguish accepted delivery completion from
// function return and prove cancellation does not wait for an unreachable peer.

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestWorkflowControlInstalledWithoutTracing(t *testing.T) {
	eng, err := NewWorker(Options{
		ClientOptions:   &client.Options{},
		WorkerOptions:   WorkerOptions{TaskQueue: "control-test"},
		Instrumentation: InstrumentationOptions{DisableTracing: true, DisableMetrics: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, eng.Close()) })
	require.Len(t, eng.workerOpts.Interceptors, 1)
	control, ok := eng.workerOpts.Interceptors[0].(*workflowControlInterceptor)
	require.True(t, ok)
	assert.Same(t, eng, control.engine)
}

func TestNewWorkflowContextRejectsMissingOrMismatchedHook(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "mismatched"}[mismatch], func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			eng := &Engine{}
			if mismatch {
				env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
					&workflowControlInterceptor{engine: &Engine{}},
				}})
			}
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				w, err := NewWorkflowContext(eng, ctx)
				if w != nil {
					return errors.New("construction returned a context without its owning worker")
				}
				return err
			})
			require.Error(t, env.GetWorkflowError())
			if mismatch {
				require.ErrorContains(t, env.GetWorkflowError(), "belongs to another engine")
			} else {
				require.ErrorContains(t, env.GetWorkflowError(), "interceptor is missing")
			}
			var panicErr *temporal.PanicError
			assert.NotErrorAs(t, env.GetWorkflowError(), &panicErr)
		})
	}
}

func TestWorkflowControlDrainsAcceptedAndOnwardObligations(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	eng := &Engine{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&workflowControlInterceptor{engine: eng},
	}})
	var returned, onwardAccepted bool
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		w, err := NewWorkflowContext(eng, ctx)
		if err != nil {
			return err
		}
		control, err := workflowControlFromContext(eng, ctx)
		if err != nil {
			return err
		}
		first, acceptFirst := workflow.NewFuture(ctx)
		control.obligations = append(control.obligations, first)
		workflow.Go(ctx, func(ctx workflow.Context) {
			if err := workflow.Sleep(ctx, time.Second); err != nil {
				acceptFirst.SetError(err)
				return
			}
			if !returned {
				acceptFirst.SetError(errors.New("native function did not return before delivery"))
				return
			}
			next, acceptNext := workflow.NewFuture(ctx)
			control.obligations = append(control.obligations, next)
			acceptFirst.SetValue(struct{}{})
			if err := workflow.Sleep(ctx, time.Second); err != nil {
				acceptNext.SetError(err)
				return
			}
			onwardAccepted = true
			acceptNext.SetValue(struct{}{})
		})
		derived, cancel := w.WithCancel()
		cancel()
		assert.Same(t, control, derived.(*temporalWorkflowContext).control)
		assert.Same(t, control, derived.Detached().(*temporalWorkflowContext).control)
		require.NoError(t, control.workflow.ctx.Err())
		returned = true
		return nil
	})
	require.NoError(t, env.GetWorkflowError())
	assert.True(t, returned)
	assert.True(t, onwardAccepted)
}

func TestWorkflowControlCancellationStopsUnacceptedDelivery(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	eng := &Engine{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&workflowControlInterceptor{engine: eng},
	}})
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		if _, err := NewWorkflowContext(eng, ctx); err != nil {
			return err
		}
		control, err := workflowControlFromContext(eng, ctx)
		if err != nil {
			return err
		}
		unaccepted, _ := workflow.NewFuture(ctx)
		control.obligations = append(control.obligations, unaccepted)
		return nil
	})
	require.Error(t, env.GetWorkflowError())
	// The cancellation must finish without the test environment's deadlock
	// timeout even though no peer ever accepts the recorded delivery.
	assert.True(t, temporal.IsCanceledError(env.GetWorkflowError()))
}

func TestWorkflowControlReturnedCancellationKeepsFirstTerminal(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Second)
	eng := &Engine{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&workflowControlInterceptor{engine: eng},
	}})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		w, err := NewWorkflowContext(eng, ctx)
		if err != nil {
			return err
		}
		control := w.(*temporalWorkflowContext).control
		unaccepted, _ := workflow.NewFuture(ctx)
		control.obligations = append(control.obligations, unaccepted)
		canceled, cancel := w.WithCancel()
		cancel()
		require.Error(t, canceled.Await(func() bool { return true }), "readiness cannot clear cancellation")
		return temporal.NewCanceledError("first terminal")
	})
	require.Error(t, env.GetWorkflowError())
	assert.True(t, temporal.IsCanceledError(env.GetWorkflowError()))
	var canceled *temporal.CanceledError
	require.ErrorAs(t, env.GetWorkflowError(), &canceled)
	var detail string
	require.NoError(t, canceled.Details(&detail))
	assert.Equal(t, "first terminal", detail)
}
