package temporal

// These native SDK workflows adapt canceled scopes and contexts supplied by
// workflow.Go. Their engine calls must use the caller's current coroutine and
// cancellation scope, while sharing execution-owned control and completion.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestNewWorkflowContextPreservesNativeCanceledScope(t *testing.T) {
	for _, cancelBefore := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_construction", false: "after_construction"}[cancelBefore], func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.SetTestTimeout(time.Second)
			eng := &Engine{}
			env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
				&workflowControlInterceptor{engine: eng},
			}})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				root, err := NewWorkflowContext(eng, ctx)
				if err != nil {
					return err
				}
				native, cancel := workflow.WithCancel(ctx)
				if cancelBefore {
					cancel()
				}
				adapted, err := NewWorkflowContext(eng, native)
				if err != nil {
					return err
				}
				if !cancelBefore {
					cancel()
				}
				require.ErrorIs(t, adapted.Context().Err(), context.Canceled)
				assert.True(t, temporal.IsCanceledError(adapted.Await(func() bool { return true })))
				timer, err := adapted.NewTimer(adapted.Context(), time.Millisecond)
				if err != nil {
					return err
				}
				_, err = timer.Get(adapted.Context())
				require.ErrorIs(t, err, context.Canceled)
				require.NoError(t, root.Context().Err(), "native child cancellation must leave its parent active")
				assert.Same(t, root.(*temporalWorkflowContext).control, adapted.(*temporalWorkflowContext).control)
				assert.EqualValues(t, 1, adapted.NextSequence())
				assert.EqualValues(t, 2, root.NextSequence(), "adapters must share the execution's sequence")

				detached, cancelCleanup := workflow.NewDisconnectedContext(native)
				defer cancelCleanup()
				cleanup, err := NewWorkflowContext(eng, detached)
				if err != nil {
					return err
				}
				require.NoError(t, cleanup.Context().Err())
				require.NoError(t, cleanup.Await(func() bool { return true }))
				cleanupTimer, err := cleanup.NewTimer(cleanup.Context(), time.Millisecond)
				if err != nil {
					return err
				}
				if _, err := cleanupTimer.Get(cleanup.Context()); err != nil {
					return err
				}
				require.ErrorIs(t, adapted.Context().Err(), context.Canceled, "cleanup must not revive the canceled scope")
				assert.Same(t, root.(*temporalWorkflowContext).control, cleanup.(*temporalWorkflowContext).control)
				return nil
			})
			require.NoError(t, env.GetWorkflowError())
		})
	}
}

func TestNewWorkflowContextPreservesNativeCoroutine(t *testing.T) {
	for _, operation := range []string{"timer", "await", "storage", "planner", "tool", "child_workflow", "recovery"} {
		t.Run(operation, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.SetTestTimeout(time.Second)
			eng := &Engine{defaultQueue: "native-context"}
			capture := &recoverySignalCapture{}
			installFirstAttemptRecoveryControl(t, env, eng, worker.Options{
				Interceptors: []interceptor.WorkerInterceptor{capture},
			})
			env.RegisterActivityWithOptions(
				func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
					return &api.StorageActivityResult{}, nil
				}, activity.RegisterOptions{Name: "native.storage"})
			env.RegisterActivityWithOptions(
				func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
					return &api.PlanActivityOutput{PublicationBatchID: "native-publication"}, nil
				}, activity.RegisterOptions{Name: "native.planner"})
			env.RegisterActivityWithOptions(
				func(context.Context, *api.ToolInput) (*api.ToolOutput, error) {
					return &api.ToolOutput{}, nil
				}, activity.RegisterOptions{Name: "native.tool"})
			env.RegisterWorkflowWithOptions(
				func(ctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
					if err := workflow.Sleep(ctx, time.Millisecond); err != nil {
						return nil, err
					}
					return &api.RunOutput{RunID: input.RunID}, nil
				}, workflow.RegisterOptions{Name: "native.child"})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				root, err := NewWorkflowContext(eng, ctx)
				if err != nil {
					return err
				}
				result, complete := workflow.NewFuture(ctx)
				workflow.Go(ctx, func(native workflow.Context) {
					adapted, err := NewWorkflowContext(eng, native)
					if err != nil {
						complete.SetError(err)
						return
					}
					assert.Same(t, root.(*temporalWorkflowContext).control, adapted.(*temporalWorkflowContext).control)
					complete.Set(nil, runNativeContextOperation(t, operation, adapted, native, capture))
				})
				return result.Get(ctx, nil)
			})
			require.NoError(t, env.GetWorkflowError())
		})
	}
}

func TestNewWorkflowContextSharesExecutionCancellation(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Second)
	eng := &Engine{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&workflowControlInterceptor{engine: eng},
	}})
	request := engine.CancellationRequest{RunID: "native-cancellation", Reason: "user_requested"}
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: request.RunID})
	var updateError error
	var updateResult any
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(cancellationUpdateName, cancellationUpdateID, &testsuite.TestUpdateCallback{
			OnReject: func(err error) {
				updateError = err
			},
			OnComplete: func(value any, err error) {
				updateResult, updateError = value, err
			},
		}, request)
	}, time.Millisecond)
	var handled, coroutineCanceled, cleanupCompleted bool
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		root, err := NewWorkflowContext(eng, ctx)
		if err != nil {
			return err
		}
		native, cancel := workflow.WithCancel(ctx)
		defer cancel()
		adapted, err := NewWorkflowContext(eng, native)
		if err != nil {
			return err
		}
		handler := func(handlerCtx engine.WorkflowContext, got engine.CancellationRequest) error {
			assert.Equal(t, request, got)
			assert.Same(t, root.(*temporalWorkflowContext).control, handlerCtx.(*temporalWorkflowContext).control)
			timer, err := handlerCtx.NewTimer(handlerCtx.Context(), time.Millisecond)
			if err != nil {
				return err
			}
			if _, err := timer.Get(handlerCtx.Context()); err != nil {
				return err
			}
			handled = true
			return nil
		}
		if err := adapted.SetCancellationHandler(handler); err != nil {
			return err
		}
		phase, cancelPhase := root.WithCancel()
		defer cancelPhase()
		require.ErrorContains(t, root.SetCancellationHandler(handler), "already registered")
		require.ErrorContains(t, phase.SetCancellationHandler(handler), "already registered")
		require.ErrorContains(t, phase.Detached().SetCancellationHandler(handler), "already registered")
		done, complete := workflow.NewFuture(ctx)
		workflow.Go(native, func(current workflow.Context) {
			view, err := NewWorkflowContext(eng, current)
			if err != nil {
				complete.SetError(err)
				return
			}
			err = view.Await(func() bool { return false })
			coroutineCanceled = temporal.IsCanceledError(err)
			assert.True(t, coroutineCanceled)
			complete.SetValue(struct{}{})
		})
		err = root.Await(func() bool { return false })
		assert.True(t, temporal.IsCanceledError(err))
		assert.True(t, handled)
		require.ErrorIs(t, adapted.Context().Err(), context.Canceled)
		require.ErrorIs(t, phase.Context().Err(), context.Canceled)
		assert.True(t, temporal.IsCanceledError(ctx.Err()), "native workflow scope inherits execution cancellation")

		detached, cancelCleanup := workflow.NewDisconnectedContext(ctx)
		defer cancelCleanup()
		cleanup, cleanupErr := NewWorkflowContext(eng, detached)
		if cleanupErr != nil {
			return cleanupErr
		}
		if cleanupErr := done.Get(detached, nil); cleanupErr != nil {
			return cleanupErr
		}
		timer, cleanupErr := cleanup.NewTimer(cleanup.Context(), time.Millisecond)
		if cleanupErr != nil {
			return cleanupErr
		}
		if _, cleanupErr := timer.Get(cleanup.Context()); cleanupErr != nil {
			return cleanupErr
		}
		cleanupCompleted = true
		require.ErrorIs(t, root.Context().Err(), context.Canceled)
		return err
	})
	require.True(t, temporal.IsCanceledError(env.GetWorkflowError()))
	require.NoError(t, updateError)
	assert.Equal(t, request, updateResult)
	assert.True(t, handled)
	assert.True(t, coroutineCanceled)
	assert.True(t, cleanupCompleted)
	_, retained := eng.workflowContexts.Load("default-test-run-id")
	assert.False(t, retained, "completion releases the execution owner")
}

func TestNewWorkflowContextNativeWrapperDrainsRelay(t *testing.T) {
	const leafID, leafRunID, leafCapability = "native-wrapper-leaf", "native-wrapper-leaf-run", "native-wrapper-leaf-issued"
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Second)
	eng := &Engine{}
	capture := &recoverySignalCapture{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&workflowControlInterceptor{engine: eng}, capture,
	}})
	var returned, accepted bool
	var control *workflowControl
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		w, err := NewWorkflowContext(eng, ctx)
		if err != nil {
			return err
		}
		control = w.(*temporalWorkflowContext).control
		info := workflow.GetInfo(ctx)
		parent := recoveryAddress{Namespace: info.Namespace, WorkflowID: "root", RunID: "root-run"}
		control.parent = &recoveryBinding{Capability: "wrapper-issued", Parent: parent, Inherited: true}
		// This SDK fixture supplies an already accepted child binding. Separate
		// server tests prove its real origin; here the receiver and completion
		// interceptor must retain the relay made from the supplied coroutine.
		control.children[leafCapability] = &recoveryChild{
			id: leafID, bound: true, execution: workflow.Execution{ID: leafID, RunID: leafRunID},
			binding: recoveryBinding{
				Capability: leafCapability, Inherited: true,
				Parent: recoveryAddress{
					Namespace: info.Namespace, WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID,
				},
			},
		}
		workflow.Go(ctx, func(current workflow.Context) {
			if err := workflow.Await(current, func() bool {
				return returned && len(capture.frames) == 3
			}); err != nil {
				return
			}
			for _, frame := range capture.frames {
				if frame.ToParent && !frame.Ack {
					assert.NotNil(t, frame.Open)
					assert.Nil(t, frame.Pause, "a wrapper cannot report its descendant's pause as its own")
					require.NoError(t, control.receiveFrame(current, recoveryFrame{
						Capability: frame.Capability, Source: parent, Sequence: frame.Sequence, Ack: true, ToParent: true,
					}))
					accepted = true
					return
				}
			}
		})
		done, complete := workflow.NewFuture(ctx)
		workflow.Go(ctx, func(current workflow.Context) {
			view, err := NewWorkflowContext(eng, current)
			if err != nil {
				complete.SetError(err)
				return
			}
			failure, err := encodeRecoveryFailure(engine.ProviderRecoveryFailure{
				PublicationBatchID: "leaf-publication", StartedAt: view.Now(), EndedAt: view.Now(),
				Err: model.NewProviderError("synthetic", "complete", 503,
					model.ProviderErrorKindUnavailable, "capacity", "wait", "", true, nil),
			})
			if err != nil {
				complete.SetError(err)
				return
			}
			frame := recoveryFrame{
				Capability: leafCapability, FirstRunID: leafRunID, ToParent: true, Sequence: 1,
				Source:  recoveryAddress{Namespace: info.Namespace, WorkflowID: leafID, RunID: leafRunID},
				Request: recoveryRequestID{WorkflowID: leafID, RunID: leafRunID, Publication: "leaf-publication"},
				Open:    failure,
			}
			if err := control.receiveFrame(current, frame); err != nil {
				complete.SetError(err)
				return
			}
			assert.NotEmpty(t, control.obligations, "the callback records onward work before acknowledging its child")
			frame.Sequence, frame.Open = 2, nil
			frame.Request = recoveryRequestID{}
			frame.Pause = &recoveryPauseInterval{
				StartedAt: encodeRecoveryTime(view.Now().Add(-time.Second)), Through: encodeRecoveryTime(view.Now()),
			}
			complete.Set(nil, control.receiveFrame(current, frame))
		})
		if err := done.Get(ctx, nil); err != nil {
			return err
		}
		returned = true
		return nil
	})
	require.NoError(t, env.GetWorkflowError())
	assert.True(t, returned)
	assert.True(t, accepted, "ordinary return must drain the wrapper's onward obligation")
	require.Len(t, control.pauses, 1)
	assert.Equal(t, leafID, control.pauses[0].child)
	assert.Len(t, capture.frames, 3, "one onward request and two child acknowledgments; no substituted pause")
}

// runNativeContextOperation starts a blocking engine call on the workflow.Go
// context supplied to the adapter. Success means the SDK resumed that coroutine
// rather than trying to block the root coroutine that waits for its result.
func runNativeContextOperation(
	t *testing.T, operation string, w engine.WorkflowContext, native workflow.Context, capture *recoverySignalCapture,
) error {
	t.Helper()
	switch operation {
	case "timer":
		timer, err := w.NewTimer(w.Context(), time.Millisecond)
		if err != nil {
			return err
		}
		_, err = timer.Get(w.Context())
		return err
	case "await":
		var ready bool
		var waitErr error
		workflow.Go(native, func(ctx workflow.Context) {
			waitErr = workflow.Sleep(ctx, time.Millisecond)
			ready = true
		})
		if err := w.Await(func() bool { return ready }); err != nil {
			return err
		}
		return waitErr
	case "storage":
		_, err := w.ExecuteStorageActivity(engine.StorageActivityCall{
			Name: "native.storage", Command: &api.StorageActivityCommand{},
		})
		return err
	case "planner":
		out, err := w.ExecutePlannerActivity(engine.PlannerActivityCall{
			Name: "native.planner", Input: &api.PlanActivityInput{},
		})
		if err == nil {
			assert.Equal(t, "native-publication", out.PublicationBatchID)
		}
		return err
	case "tool":
		future, err := w.ExecuteToolActivityAsync(engine.ToolActivityCall{
			Name: "native.tool", Input: &api.ToolInput{},
		})
		if err != nil {
			return err
		}
		_, err = future.Get(w.Context())
		return err
	case "child_workflow":
		child, err := w.StartChildWorkflow(w.Context(), engine.ChildWorkflowRequest{
			ID: "native-child", Workflow: "native.child", TaskQueue: "native-context",
			Input: &api.RunInput{RunID: "native-child"},
		})
		if err != nil {
			return err
		}
		out, err := child.Get(w.Context())
		if err == nil {
			assert.Equal(t, "native-child", out.RunID)
		}
		return err
	case "recovery":
		control := w.(*temporalWorkflowContext).control
		parent := recoveryAddress{Namespace: "test", WorkflowID: "parent", RunID: "parent-run"}
		control.parent = &recoveryBinding{Capability: "issued", Parent: parent, Inherited: true}
		workflow.Go(native, func(ctx workflow.Context) {
			if err := workflow.Await(ctx, func() bool { return len(capture.frames) == 1 }); err != nil {
				return
			}
			frame := capture.frames[0]
			require.NoError(t, control.receiveFrame(ctx, recoveryFrame{
				Capability: frame.Capability, Source: parent, Sequence: frame.Sequence, Ack: true, ToParent: true,
			}))
		})
		report, err := w.ProviderRecovery().ReportPause(w, engine.ProviderRecoveryPaused{
			StartedAt: w.Now(), Through: w.Now(),
		})
		if err != nil {
			return err
		}
		_, err = report.Get(w.Context())
		return err
	default:
		t.Fatalf("unknown operation %q", operation)
		return nil
	}
}
