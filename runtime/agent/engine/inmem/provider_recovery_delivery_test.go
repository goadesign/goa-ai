package inmem

// These tests exercise private delivery identities separately from runtime phase
// policy. A delivery repeats by its sequence; equal messages on new sequences
// are legitimate new transitions. The issued handle proves the execution.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	recoveryContextWrapper struct {
		engine.WorkflowContext
	}
)

func (w *recoveryContextWrapper) Context() context.Context {
	return engine.WithWorkflowContext(w.WorkflowContext.Context(), w)
}

func TestProviderRecoveryEarlyBindingOrderedDuplicates(t *testing.T) {
	parent, child, binding := recoveryBoundContexts(t)
	var order []string
	require.NoError(t, parent.ProviderRecovery().Register(func(_ engine.WorkflowContext, id string, failure engine.ProviderRecoveryFailure, _ engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
		assert.Equal(t, binding.id, id)
		order = append(order, failure.PublicationBatchID)
		return func(_ engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
			assert.IsType(t, engine.ProviderWaitRequest{}, message)
			order = append(order, "wait")
			return nil
		}, nil
	}))
	require.NoError(t, parent.ProviderRecovery().RegisterPauseHandler(func(engine.WorkflowContext, string, engine.ProviderRecoveryPaused) error {
		order = append(order, "pause")
		return nil
	}))
	failure := recoveryFailure("first")
	first, err := child.ProviderRecovery().Open(child, failure, func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error { return nil })
	require.NoError(t, err)
	repeat, err := child.ProviderRecovery().Open(child, failure, func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error {
		return errors.New("duplicate Open must not replace the receiver")
	})
	require.NoError(t, err)
	assert.Same(t, first, repeat)
	conflicting := failure
	conflicting.EndedAt = conflicting.EndedAt.Add(time.Second)
	_, err = child.ProviderRecovery().Open(child, conflicting, func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error { return nil })
	require.ErrorContains(t, err, "conflicting")
	pause, err := child.ProviderRecovery().ReportPause(child, engine.ProviderRecoveryPaused{StartedAt: failure.StartedAt, Through: failure.EndedAt})
	require.NoError(t, err)
	_, err = child.ProviderRecovery().Open(child, recoveryFailure("second"), func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error { return nil })
	require.NoError(t, err)
	wait, err := first.Send(child, engine.ProviderWaitRequest{Delay: time.Second})
	require.NoError(t, err)
	stream := child.control.up
	saved := stream.deliveries[stream.next]
	duplicate := stream.deliver(stream.next, saved.value, func() error {
		return errors.New("duplicate delivery must not run")
	}, nil)
	assert.Same(t, wait, duplicate)
	conflict := stream.deliver(stream.next, engine.ProviderWaitRequest{Delay: 2 * time.Second}, func() error {
		return errors.New("conflict must not run")
	}, nil)
	_, err = conflict.Get(t.Context())
	require.ErrorContains(t, err, "conflicting")
	// Deliveries may arrive before StartChildWorkflow finishes binding its
	// issued handle. They stay pending, including every later pause or request.
	parent.control.tasks.dispatch()
	assert.Empty(t, order)
	assert.False(t, pause.IsReady())
	assert.False(t, wait.IsReady())
	parent.control.children[binding.id] = binding
	close(binding.accepted)
	parent.control.tasks.dispatch()
	assert.Equal(t, []string{"first", "pause", "second", "wait"}, order)
	assert.True(t, pause.IsReady())
	assert.True(t, wait.IsReady())
	_, err = duplicate.Get(t.Context())
	require.NoError(t, err)
	// An equal request with a fresh sequence still reaches the runtime.
	another, err := first.Send(child, engine.ProviderWaitRequest{Delay: time.Second})
	require.NoError(t, err)
	parent.control.tasks.dispatch()
	_, err = another.Get(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "pause", "second", "wait", "wait"}, order)
}

func TestProviderRecoveryRejectsWrongIssuedExecution(t *testing.T) {
	for _, wrong := range []string{"accepted_binding", "wrong_handle", "unissued_control"} {
		t.Run(wrong, func(t *testing.T) {
			parent, child, binding := recoveryBoundContexts(t)
			parent.control.children[binding.id] = binding
			close(binding.accepted)
			switch wrong {
			case "accepted_binding":
				parent.control.children[binding.id] = &recoveryChild{id: binding.id}
			case "wrong_handle":
				_, _, otherBinding := recoveryBoundContexts(t)
				binding.handle = otherBinding.handle
			case "unissued_control":
				child = &wfCtx{ctx: t.Context(), id: child.id}
				child.executionControl()
				child.control.parent = binding
				child.control.inherited = true
			}
			var stopped error
			var called bool
			require.NoError(t, parent.ProviderRecovery().Register(func(engine.WorkflowContext, string, engine.ProviderRecoveryFailure, engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
				called = true
				return nil, errors.New("invalid execution must not reach runtime")
			}))
			_, err := child.ProviderRecovery().Open(child, recoveryFailure("first"), func(_ engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
				stopped = message.(engine.ProviderRecoveryStopped).Err
				return nil
			})
			require.NoError(t, err)
			parent.control.tasks.dispatch()
			child.control.tasks.dispatch()
			require.Error(t, stopped)
			assert.False(t, called)
		})
	}
}

func TestProviderRecoveryIssuedAttemptsKeepOriginalRequests(t *testing.T) {
	parent, first, binding := recoveryBoundContexts(t)
	parent.control.children[binding.id] = binding
	close(binding.accepted)
	firstFailure := recoveryFailure("same-publication")
	nextFailure := firstFailure
	nextFailure.Err = model.NewProviderError("synthetic", "request", 503,
		model.ProviderErrorKindUnavailable, "capacity", "next attempt", "next-provider-id", true, nil)
	originalPause := engine.ProviderRecoveryPaused{
		StartedAt: firstFailure.StartedAt,
		Through:   firstFailure.EndedAt,
	}
	var failures []engine.ProviderRecoveryFailure
	var requests []engine.ProviderRecovery
	var pauses []engine.ProviderRecoveryPaused
	require.NoError(t, parent.ProviderRecovery().Register(func(_ engine.WorkflowContext, id string, failure engine.ProviderRecoveryFailure, request engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
		assert.Equal(t, binding.id, id)
		failures = append(failures, failure)
		requests = append(requests, request)
		return func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error { return nil }, nil
	}))
	require.NoError(t, parent.ProviderRecovery().RegisterPauseHandler(func(_ engine.WorkflowContext, id string, pause engine.ProviderRecoveryPaused) error {
		assert.Equal(t, binding.id, id)
		pauses = append(pauses, pause)
		return nil
	}))
	_, err := first.ProviderRecovery().Open(first, firstFailure, func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error {
		return errors.New("a completed attempt cannot receive a later reply")
	})
	require.NoError(t, err)
	report, err := first.ProviderRecovery().ReportPause(first, originalPause)
	require.NoError(t, err)
	first.control.tasks.close(context.DeadlineExceeded)

	// A retry gets another issued execution for the same handle. Its request
	// and reply receiver remain separate from the first attempt's queued work.
	next := &wfCtx{ctx: t.Context(), id: first.id}
	next.executionControl()
	next.control.parent = binding
	next.control.inherited = true
	binding.handle.issuedControls[next.control] = struct{}{}
	var replies int
	_, err = next.ProviderRecovery().Open(next, nextFailure, func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error {
		replies++
		return nil
	})
	require.NoError(t, err)
	parent.control.tasks.dispatch()
	_, err = report.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, []engine.ProviderRecoveryFailure{firstFailure, nextFailure}, failures)
	assert.Equal(t, []engine.ProviderRecoveryPaused{originalPause}, pauses)
	assert.Same(t, first.control, requests[0].(*recoveryEndpoint).stream.target)
	assert.Same(t, next.control, requests[1].(*recoveryEndpoint).stream.target)
	assert.NotSame(t, requests[0].(*recoveryEndpoint).request, requests[1].(*recoveryEndpoint).request)

	oldReply, err := requests[0].Send(parent, engine.ProviderRecoverySettled{})
	require.NoError(t, err)
	_, err = oldReply.Get(t.Context())
	require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
	next.control.tasks.dispatch()
	assert.Zero(t, replies)
	newReply, err := requests[1].Send(parent, engine.ProviderRecoverySettled{})
	require.NoError(t, err)
	next.control.tasks.dispatch()
	_, err = newReply.Get(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, replies)
}

func TestProviderRecoveryDerivedScopesAndExactErrors(t *testing.T) {
	parent, child, binding := recoveryBoundContexts(t)
	parent.control.children[binding.id] = binding
	close(binding.accepted)
	sub, cancel := child.WithCancel()
	detached := sub.Detached()
	assert.Same(t, child.control, sub.(*wfCtx).control)
	assert.Same(t, child.control, detached.(*wfCtx).control)
	assert.True(t, detached.ProviderRecovery().HasParent())
	require.NoError(t, detached.ProviderRecovery().Register(func(engine.WorkflowContext, string, engine.ProviderRecoveryFailure, engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
		return nil, nil
	}))
	require.ErrorContains(t, sub.ProviderRecovery().Register(func(engine.WorkflowContext, string, engine.ProviderRecoveryFailure, engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
		return nil, nil
	}), "already registered")
	require.NoError(t, detached.ProviderRecovery().RegisterPauseHandler(func(engine.WorkflowContext, string, engine.ProviderRecoveryPaused) error { return nil }))
	require.ErrorContains(t, child.ProviderRecovery().RegisterPauseHandler(func(engine.WorkflowContext, string, engine.ProviderRecoveryPaused) error { return nil }), "already registered")
	cancel()
	require.ErrorIs(t, sub.Context().Err(), context.Canceled)
	require.NoError(t, child.Context().Err())
	require.NoError(t, detached.Context().Err())
	var incoming engine.ProviderRecovery
	failure := recoveryFailure("original")
	failure.Err = model.NewProviderError("synthetic", "request", 429, model.ProviderErrorKindRateLimited,
		"capacity", "exact diagnostic", "provider-id", true, errors.New(""))
	require.NoError(t, parent.ProviderRecovery().Register(func(_ engine.WorkflowContext, id string, received engine.ProviderRecoveryFailure, control engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
		assert.Equal(t, binding.id, id)
		assert.Equal(t, failure, received)
		assert.Error(t, received.Err.Unwrap())
		assert.Empty(t, received.Err.Unwrap().Error())
		incoming = control
		return func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error { return nil }, nil
	}))
	var received []error
	wrapped := &recoveryContextWrapper{WorkflowContext: detached}
	outgoing, err := sub.ProviderRecovery().Open(wrapped, failure, func(w engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
		require.NoError(t, w.Context().Err(), "Open retains the detached reply scope")
		received = append(received, message.(engine.ProviderRecoveryStopped).Err)
		return nil
	})
	require.NoError(t, err)
	_, err = outgoing.Send(wrapped, engine.ProviderPhaseUnused{})
	require.NoError(t, err)
	parent.control.tasks.dispatch()
	_, err = outgoing.Forward(child, func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error { return nil })
	require.ErrorContains(t, err, "incoming")
	other := &wfCtx{ctx: t.Context()}
	other.executionControl()
	_, err = outgoing.Send(other, engine.ProviderWaitRequest{Delay: time.Second})
	require.ErrorContains(t, err, "another execution")
	causes := []error{
		fmt.Errorf("canceled exactly: %w", context.Canceled),
		fmt.Errorf("deadline exactly: %w", context.DeadlineExceeded),
		fmt.Errorf("planner exactly: %w: %w", engine.ErrPlannerActivityDeadlineExceeded, context.DeadlineExceeded),
		failure.Err,
	}
	for _, cause := range causes {
		future, err := incoming.Send(parent, engine.ProviderRecoveryStopped{Err: cause})
		require.NoError(t, err)
		assert.False(t, future.IsReady())
		child.control.tasks.dispatch()
		assert.True(t, future.IsReady())
	}
	require.Len(t, received, len(causes))
	for index, cause := range causes {
		assert.Equal(t, cause.Error(), received[index].Error())
		for _, classification := range []error{context.Canceled, context.DeadlineExceeded, engine.ErrPlannerActivityDeadlineExceeded} {
			assert.Equal(t, errors.Is(cause, classification), errors.Is(received[index], classification))
		}
	}
}

func TestProviderRecoveryMissingAndClosedParent(t *testing.T) {
	parent, child, binding := recoveryBoundContexts(t)
	parent.control.children[binding.id] = binding
	close(binding.accepted)
	root := &wfCtx{ctx: t.Context()}
	root.executionControl()
	assert.False(t, root.ProviderRecovery().HasParent())
	_, err := root.ProviderRecovery().Open(root, recoveryFailure("root"), func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error { return nil })
	require.ErrorContains(t, err, "no inherited parent")
	pause, err := root.ProviderRecovery().ReportPause(root, engine.ProviderRecoveryPaused{})
	require.NoError(t, err)
	assert.True(t, pause.IsReady())
	_, err = pause.Get(t.Context())
	require.NoError(t, err)
	parent.control.tasks.close(engine.ErrWorkflowCompleted)
	var stopped error
	_, err = child.ProviderRecovery().Open(child, recoveryFailure("closed"), func(_ engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
		stopped = message.(engine.ProviderRecoveryStopped).Err
		return nil
	})
	require.NoError(t, err)
	child.control.tasks.dispatch()
	require.ErrorIs(t, stopped, engine.ErrWorkflowCompleted)
	pause, err = child.ProviderRecovery().ReportPause(child, engine.ProviderRecoveryPaused{})
	require.NoError(t, err)
	_, err = pause.Get(t.Context())
	require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
}

func TestProviderRecoveryAcceptanceRecordsOnwardBeforeReady(t *testing.T) {
	parent, child, binding := recoveryBoundContexts(t)
	parent.control.children[binding.id] = binding
	close(binding.accepted)
	var incoming engine.ProviderRecovery
	var reply engine.Future[struct{}]
	require.NoError(t, parent.ProviderRecovery().Register(func(_ engine.WorkflowContext, _ string, _ engine.ProviderRecoveryFailure, control engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
		incoming = control
		return func(w engine.WorkflowContext, _ engine.ProviderRecoveryMessage) error {
			var err error
			reply, err = incoming.Send(w, engine.ProviderRecoverySettled{})
			return err
		}, nil
	}))
	var replied bool
	control, err := child.ProviderRecovery().Open(child, recoveryFailure("first"), func(engine.WorkflowContext, engine.ProviderRecoveryMessage) error {
		replied = true
		return nil
	})
	require.NoError(t, err)
	sent, err := control.Send(child, engine.ProviderAttemptSucceeded{})
	require.NoError(t, err)
	assert.False(t, sent.IsReady())
	parent.control.tasks.dispatch()
	assert.True(t, sent.IsReady())
	require.NotNil(t, reply)
	assert.False(t, reply.IsReady())
	assert.False(t, replied)
	assert.Len(t, parent.control.pending, 1)
	child.control.tasks.dispatch()
	assert.True(t, replied)
	assert.True(t, reply.IsReady())
}

func TestProviderRecoveryRejectedAcceptanceStaysRejected(t *testing.T) {
	parent, child, binding := recoveryBoundContexts(t)
	parent.control.children[binding.id] = binding
	close(binding.accepted)
	cause := fmt.Errorf("receiver stopped: %w", context.Canceled)
	require.NoError(t, parent.ProviderRecovery().Register(func(engine.WorkflowContext, string, engine.ProviderRecoveryFailure, engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
		return nil, cause
	}))
	var stopped error
	request, err := child.ProviderRecovery().Open(child, recoveryFailure("first"), func(_ engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
		stopped = message.(engine.ProviderRecoveryStopped).Err
		return nil
	})
	require.NoError(t, err)
	send, err := request.Send(child, engine.ProviderWaitRequest{Delay: time.Second})
	require.NoError(t, err)
	parent.control.tasks.dispatch()
	_, err = send.Get(t.Context())
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, stopped, context.Canceled)
	stream := child.control.up
	original := stream.deliveries[stream.next]
	repeated := stream.deliver(stream.next, original.value, func() error { return nil }, nil)
	assert.Same(t, send, repeated)
	_, err = repeated.Get(t.Context())
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, cause.Error(), stopped.Error())
}

// recoveryBoundContexts stops immediately before StartChildWorkflow publishes
// the issued handle to its parent. Tests decide when that final binding happens.
func recoveryBoundContexts(t *testing.T) (*wfCtx, *wfCtx, *recoveryChild) {
	t.Helper()
	implementation := New().(*eng)
	parent := &wfCtx{ctx: t.Context(), id: "parent", eng: implementation}
	parent.executionControl()
	implementation.mu.Lock()
	h, ctx := implementation.reserveWorkflowLocked(t.Context(), "child")
	implementation.mu.Unlock()
	t.Cleanup(func() { h.cancel() })
	child := &wfCtx{ctx: ctx, id: "child", eng: implementation}
	child.executionControl()
	binding := &recoveryChild{
		parent: parent.control, handle: h, id: "child", inherited: true,
		accepted: make(chan struct{}),
	}
	child.control.parent = binding
	child.control.inherited = true
	h.issuedControls[child.control] = struct{}{}
	h.parent = binding
	return parent, child, binding
}
