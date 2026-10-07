package temporal

// These tests control signal delivery separately from receiver acceptance.
// Native server tests supply real child identities; these workflow tests exercise
// early messages, gaps, duplicates, and independent streams without wall-clock waits.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	recoverySignalCapture struct {
		interceptor.WorkerInterceptorBase
		frames []recoveryFrame
	}
	recoverySignalCaptureInbound struct {
		interceptor.WorkflowInboundInterceptorBase
		capture *recoverySignalCapture
	}
	recoverySignalCaptureOutbound struct {
		interceptor.WorkflowOutboundInterceptorBase
		capture *recoverySignalCapture
	}
)

func (c *recoverySignalCapture) InterceptWorkflow(
	_ workflow.Context, next interceptor.WorkflowInboundInterceptor,
) interceptor.WorkflowInboundInterceptor {
	return &recoverySignalCaptureInbound{
		WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next},
		capture:                        c,
	}
}

func (i *recoverySignalCaptureInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&recoverySignalCaptureOutbound{
		WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next},
		capture:                         i.capture,
	})
}

func (o *recoverySignalCaptureOutbound) SignalExternalWorkflow(
	ctx workflow.Context, id, run, name string, value any,
) workflow.Future {
	if name != recoverySignalName {
		return o.Next.SignalExternalWorkflow(ctx, id, run, name, value)
	}
	o.capture.frames = append(o.capture.frames, value.(recoveryFrame))
	done, set := workflow.NewFuture(ctx)
	set.SetValue(struct{}{})
	return done
}

func TestProviderRecoveryDeliveryBuffersOrdersAndRetainsAcceptance(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	eng := &Engine{}
	capture := &recoverySignalCapture{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&workflowControlInterceptor{engine: eng}, capture,
	}})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		w, err := NewWorkflowContext(eng, ctx)
		if err != nil {
			return err
		}
		control := w.(*temporalWorkflowContext).control
		parent := recoveryAddress{
			Namespace:  workflow.GetInfo(ctx).Namespace,
			WorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID, RunID: workflow.GetInfo(ctx).WorkflowExecution.RunID,
		}
		child := &recoveryChild{
			id: "issued-child", binding: recoveryBinding{Capability: "issued", Parent: parent, Inherited: true},
			execution: workflow.Execution{ID: "issued-child", RunID: "attempt-1"},
		}
		control.children["issued"] = child
		var callbacks []string
		require.NoError(t, w.ProviderRecovery().Register(func(
			_ engine.WorkflowContext, id string, failure engine.ProviderRecoveryFailure, _ engine.ProviderRecovery,
		) (engine.ProviderRecoveryReceive, error) {
			assert.Equal(t, child.id, id)
			callbacks = append(callbacks, failure.PublicationBatchID)
			return func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error { return nil }, nil
		}))
		require.NoError(t, w.ProviderRecovery().RegisterPauseHandler(func(
			_ engine.WorkflowContext, id string, pause engine.ProviderRecoveryPaused,
		) error {
			assert.Equal(t, child.id, id)
			assert.Equal(t, w.Now().Add(-time.Second), pause.Through)
			callbacks = append(callbacks, "pause")
			return nil
		}))
		source := recoveryAddress{Namespace: parent.Namespace, WorkflowID: child.id, RunID: "attempt-1"}
		firstFailure, err := encodeRecoveryFailure(engine.ProviderRecoveryFailure{
			PublicationBatchID: "first", StartedAt: w.Now(), EndedAt: w.Now(),
			Err: model.NewProviderError("synthetic", "complete", 503,
				model.ProviderErrorKindUnavailable, "capacity", "wait", "request", true, nil),
		})
		require.NoError(t, err)
		first := recoveryFrame{
			Capability: "issued", Source: source, FirstRunID: "attempt-1", ToParent: true, Sequence: 1,
			Request: recoveryRequestID{WorkflowID: child.id, RunID: source.RunID, Publication: "first"},
			Open:    firstFailure,
		}
		second := first
		second.Sequence, second.Request.Publication = 2, "second"
		secondFailure := *firstFailure
		secondFailure.PublicationBatchID = "second"
		second.Open = &secondFailure
		pause := recoveryFrame{
			Capability: "issued", Source: source, FirstRunID: "attempt-1", ToParent: true, Sequence: 3,
			Pause: &recoveryPauseInterval{
				StartedAt: encodeRecoveryTime(w.Now().Add(-2 * time.Second)),
				Through:   encodeRecoveryTime(w.Now().Add(-time.Second)),
			},
		}
		for _, frame := range []recoveryFrame{pause, first, first, second} {
			require.NoError(t, control.receiveFrame(ctx, frame))
		}
		assert.Empty(t, callbacks, "child execution has not yet been accepted")
		assert.Empty(t, capture.frames)
		child.bound = true
		control.processRecovery(ctx)
		assert.Equal(t, []string{"first", "second", "pause"}, callbacks)
		require.Len(t, capture.frames, 3)
		for index, ack := range capture.frames {
			assert.True(t, ack.Ack)
			assert.EqualValues(t, index+1, ack.Sequence)
			assert.Nil(t, ack.Error)
		}
		// The original acknowledgment can be lost. A later exact repeat returns
		// the saved outcome, while changing that delivery cannot invoke a receiver.
		require.NoError(t, control.receiveFrame(ctx, first))
		require.Equal(t, capture.frames[0], capture.frames[3])
		conflict := first
		conflict.Request.Publication = "changed"
		require.NoError(t, control.receiveFrame(ctx, conflict))
		require.NotNil(t, capture.frames[4].Error)
		assert.Contains(t, capture.frames[4].Error.Text, "conflicting")
		assert.Len(t, callbacks, 3)

		later := first
		later.Source.RunID, later.Request.RunID = "attempt-3", "attempt-3"
		require.NoError(t, control.receiveFrame(ctx, later))
		delayed := pause
		delayed.Sequence = 4
		require.NoError(t, control.receiveFrame(ctx, delayed))
		assert.Equal(t, []string{"first", "second", "pause", "first", "pause"}, callbacks)
		assert.Len(t, control.endpoints, 3)
		assert.Equal(t, "attempt-1", child.execution.RunID)
		for _, run := range []string{"attempt-1", "attempt-3"} {
			stream := control.incoming[recoveryStreamKey{Capability: "issued", PeerRunID: run, ToParent: true}]
			require.NotNil(t, stream)
			assert.Equal(t, run, stream.accepted[1].frame.Request.RunID)
		}
		return nil
	})
	require.NoError(t, env.GetWorkflowError())
}

func TestProviderRecoveryDeliveryWaitsForAcceptanceAcrossRequestsAndPause(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	eng := &Engine{}
	capture := &recoverySignalCapture{}
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
		&workflowControlInterceptor{engine: eng}, capture,
	}})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		w, err := NewWorkflowContext(eng, ctx)
		if err != nil {
			return err
		}
		native := w.(*temporalWorkflowContext)
		control := native.control
		parent := recoveryAddress{Namespace: "test", WorkflowID: "parent", RunID: "parent-run"}
		control.parent = &recoveryBinding{Capability: "issued", Parent: parent, Inherited: true}
		phase, cancel := native.WithCancel()
		first, err := control.send(phase.(*temporalWorkflowContext), parent, recoveryFrame{
			Capability: "issued", ToParent: true, Request: recoveryRequestID{Publication: "one"},
			Message: &recoveryMessage{Kind: "succeeded"},
		}, nil)
		require.NoError(t, err)
		if err := w.Await(func() bool { return len(capture.frames) == 1 }); err != nil {
			return err
		}
		cancel()
		_, err = first.Get(phase.Context())
		require.ErrorIs(t, err, context.Canceled)
		require.NoError(t, native.Context().Err())
		cleanup := phase.Detached().(*temporalWorkflowContext)
		second, err := control.send(cleanup, parent, recoveryFrame{
			Capability: "issued", ToParent: true, Request: recoveryRequestID{Publication: "two"},
			Message: &recoveryMessage{Kind: "succeeded"},
		}, nil)
		require.NoError(t, err)
		pause, err := w.ProviderRecovery().ReportPause(w, engine.ProviderRecoveryPaused{
			StartedAt: w.Now(), Through: w.Now(),
		})
		require.NoError(t, err)
		for index, future := range []engine.Future[struct{}]{first, second, pause} {
			if err := w.Await(func() bool { return len(capture.frames) == index+1 }); err != nil {
				return err
			}
			assert.False(t, future.IsReady(), "native delivery is not receiver acceptance")
			ack := recoveryFrame{Capability: "issued", Source: parent, Sequence: uint64(index + 1), Ack: true, ToParent: true}
			require.NoError(t, control.receiveFrame(ctx, ack))
			if index == 0 {
				// The first caller is canceled, but the engine still records
				// its acceptance before delivering the detached cleanup.
				require.NoError(t, w.Await(first.IsReady))
			} else if _, err := future.Get(w.Context()); err != nil {
				return fmt.Errorf("accepted send %d: %w", index, err)
			}
			require.NoError(t, control.receiveFrame(ctx, ack), "later exact acknowledgment keeps its result")
		}
		return nil
	})
	require.NoError(t, env.GetWorkflowError())
}

func TestProviderRecoverySignalNamespaceIgnoresFutureChildOptions(t *testing.T) {
	for _, test := range []struct {
		name           string
		acknowledgment bool
		reject         bool
	}{
		{name: "delivery"},
		{name: "accepted_acknowledgment", acknowledgment: true},
		{name: "rejected_acknowledgment", acknowledgment: true, reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			eng := &Engine{}
			env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{
				&workflowControlInterceptor{engine: eng},
			}})
			var executionNamespace string
			env.OnSignalExternalWorkflow(mock.MatchedBy(func(namespace string) bool {
				return namespace == executionNamespace
			}), "issued-peer", "peer-run", recoverySignalName, mock.MatchedBy(func(frame recoveryFrame) bool {
				return frame.Ack == test.acknowledgment &&
					(frame.Error != nil) == test.reject
			})).Return(nil).Once()
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				info := workflow.GetInfo(ctx)
				executionNamespace = info.Namespace
				peer := recoveryAddress{
					Namespace: info.Namespace, WorkflowID: "issued-peer", RunID: "peer-run",
				}
				const futureChildNamespace = "future-child-namespace"
				require.NotEqual(t, futureChildNamespace, info.Namespace)
				// A native caller keeps its current execution but selects another
				// namespace for a future child. Recovery must still reach its
				// retained parent or acknowledgment recipient.
				ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
					//nolint:staticcheck // SA1019: set a different future-child target so the test can check that recovery still signals the retained peer.
					Namespace: futureChildNamespace,
				})
				w, err := NewWorkflowContext(eng, ctx)
				if err != nil {
					return err
				}
				control := w.(*temporalWorkflowContext).control
				assert.Equal(t, peer.Namespace, workflow.GetInfo(ctx).Namespace)
				if !test.acknowledgment {
					control.parent = &recoveryBinding{Capability: "issued", Parent: peer, Inherited: true}
					future, err := w.ProviderRecovery().ReportPause(w, engine.ProviderRecoveryPaused{
						StartedAt: w.Now(), Through: w.Now(),
					})
					if err != nil {
						return err
					}
					require.NoError(t, control.receiveFrame(ctx, recoveryFrame{
						Capability: "issued", Source: peer, Sequence: 1, Ack: true, ToParent: true,
					}))
					_, err = future.Get(w.Context())
					return err
				}
				control.children["issued"] = &recoveryChild{
					id: peer.WorkflowID, bound: true,
					binding: recoveryBinding{
						Capability: "issued", Parent: recoveryAddress{
							Namespace: info.Namespace, WorkflowID: info.WorkflowExecution.ID,
							RunID: info.WorkflowExecution.RunID,
						},
					},
					execution: workflow.Execution{ID: peer.WorkflowID, RunID: peer.RunID},
				}
				frame := recoveryFrame{
					Capability: "issued", Source: peer, FirstRunID: peer.RunID, Sequence: 1, ToParent: true,
					Pause: &recoveryPauseInterval{
						StartedAt: encodeRecoveryTime(w.Now()), Through: encodeRecoveryTime(w.Now()),
					},
				}
				if test.reject {
					frame.Capability = "not-issued"
				}
				return control.receiveFrame(ctx, frame)
			})
			require.NoError(t, env.GetWorkflowError())
			env.AssertExpectations(t)
		})
	}
}
