package runtime

// These tests exercise registered pause callbacks through an issued-child
// fixture. They distinguish root spending from each ancestor's measured pause;
// real engine tests separately prove transport identity and callback scheduling.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	recoveryActorWorkflow struct {
		*routeWorkflowContext
		children map[string]*controlledChildHandle
	}
	recoveryActorAcceptance struct {
		ready bool
		err   error
	}
	recoveryActorControl struct {
		engine.ProviderRecovery
		messages []engine.ProviderRecoveryMessage
	}
	recoveryJoinChild struct {
		canceled bool
		joined   bool
	}
)

func (w *recoveryActorWorkflow) StartChildWorkflow(_ context.Context, request engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	child := &controlledChildHandle{ready: make(chan struct{})}
	w.children[request.ID] = child
	return child, nil
}

func (f *recoveryActorAcceptance) IsReady() bool {
	return f.ready
}

func (f *recoveryActorAcceptance) Get(context.Context) (struct{}, error) {
	return struct{}{}, f.err
}

func (c *recoveryActorControl) Send(_ engine.WorkflowContext, message engine.ProviderRecoveryMessage) (engine.Future[struct{}], error) {
	c.messages = append(c.messages, message)
	return testProviderRecoveryAcceptance{}, nil
}

func (h *recoveryJoinChild) IsReady() bool {
	return false
}

func (h *recoveryJoinChild) Cancel(context.Context) error {
	h.canceled = true
	return nil
}

func (h *recoveryJoinChild) Get(ctx context.Context) (*RunOutput, error) {
	if !h.canceled || ctx.Err() != nil {
		return nil, errors.New("child must be canceled and joined through a detached context")
	}
	h.joined = true
	return nil, context.Canceled
}

func TestProviderRecoveryActorPropagatesMeasuredSubtreePause(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	rootNative := &recoveryActorWorkflow{
		routeWorkflowContext: &routeWorkflowContext{ctx: t.Context(), now: func() time.Time { return now }},
		children:             make(map[string]*controlledChildHandle),
	}
	rootAllowance := &providerRecoveryBudget{Remaining: time.Minute}
	root, err := installProviderRecovery(rootNative, rootAllowance)
	require.NoError(t, err)
	_, err = root.StartChildWorkflow(t.Context(), engine.ChildWorkflowRequest{ID: "mid"})
	require.NoError(t, err)
	rootDeadline := start.Add(15 * time.Second)
	root.actor.budget = &rootDeadline
	root.actor.clock.beginWait(start)

	midPort := &testProviderRecoveryPort{parent: true}
	midNative := &recoveryActorWorkflow{
		routeWorkflowContext: &routeWorkflowContext{
			ctx: t.Context(), now: func() time.Time { return now }, recoveryPort: midPort,
		},
		children: make(map[string]*controlledChildHandle),
	}
	midAllowance := &providerRecoveryBudget{Remaining: 10 * time.Second}
	mid, err := installProviderRecovery(midNative, midAllowance)
	require.NoError(t, err)
	for _, id := range []string{"leaf", "healthy"} {
		_, err := mid.StartChildWorkflow(t.Context(), engine.ChildWorkflowRequest{ID: id})
		require.NoError(t, err)
	}
	var reports []engine.ProviderRecoveryPaused
	midPort.report = func(pause engine.ProviderRecoveryPaused) (engine.Future[struct{}], error) {
		reports = append(reports, pause)
		err := rootNative.recoveryPort.pause(rootNative, "mid", pause)
		return testProviderRecoveryAcceptance{}, err
	}
	mid.actor.clock.beginWait(start)
	now = start.Add(5 * time.Second)
	require.NoError(t, midPort.pause(midNative, "leaf", engine.ProviderRecoveryPaused{StartedAt: start, Through: now}))
	assert.Empty(t, reports, "one paused leaf cannot hide its healthy sibling")
	assert.Equal(t, start.Add(15*time.Second), rootDeadline)

	close(midNative.children["healthy"].ready)
	require.NoError(t, mid.actor.observe(midNative))
	now = start.Add(10 * time.Second)
	pause := engine.ProviderRecoveryPaused{StartedAt: start, Through: now}
	require.NoError(t, midPort.pause(midNative, "leaf", pause))
	require.Len(t, reports, 1)
	assert.Equal(t, start.Add(5*time.Second), reports[0].StartedAt)
	assert.Equal(t, now, reports[0].Through)
	assert.Equal(t, 5*time.Second, mid.actor.elapsed)
	assert.Equal(t, 5*time.Second, root.actor.elapsed)
	assert.Equal(t, start.Add(20*time.Second), rootDeadline)
	assert.Equal(t, time.Minute, rootAllowance.Remaining, "pause evidence does not spend allowance")
	assert.Equal(t, 10*time.Second, midAllowance.Remaining, "descendants do not spend the explicit local cap")

	require.NoError(t, midPort.pause(midNative, "leaf", pause))
	assert.Len(t, reports, 1, "overlapping reports must earn credit once")

	// Mid resumes local publication. A later leaf pause cannot credit that
	// active work even though the root continues to wait for Mid.
	mid.actor.clock.endWait(now)
	now = start.Add(12 * time.Second)
	require.NoError(t, midPort.pause(midNative, "leaf", engine.ProviderRecoveryPaused{StartedAt: start, Through: now}))
	assert.Len(t, reports, 1)
	assert.Equal(t, 5*time.Second, root.actor.elapsed)

	root.actor.finalization = new(workflowFinalizationState)
	root.actor.finalization.phase.Store(workflowCancellationAccepted)
	mid.actor.clock.beginWait(now)
	now = start.Add(14 * time.Second)
	require.NoError(t, midPort.pause(midNative, "leaf", engine.ProviderRecoveryPaused{StartedAt: start, Through: now}))
	assert.Equal(t, 7*time.Second, mid.actor.elapsed)
	assert.Equal(t, 5*time.Second, root.actor.elapsed, "a terminal root cannot be revived by a late report")
}

func TestProviderRecoveryActorDrainsAcceptedReports(t *testing.T) {
	native := &routeWorkflowContext{ctx: t.Context()}
	wf, err := installProviderRecovery(native, nil)
	require.NoError(t, err)
	wf.actor.deliveries = append(wf.actor.deliveries, &recoveryActorAcceptance{ready: true})
	require.NoError(t, wf.actor.close(wf, nil))
	assert.Empty(t, wf.actor.deliveries)
	assert.True(t, wf.actor.clock.closed)
}

func TestProviderRecoveryActorAccountsWholeIntervalsFromSavedRemainder(t *testing.T) {
	start := time.Unix(100, 0)
	now := start.Add(5 * time.Second)
	native := &routeWorkflowContext{
		ctx: t.Context(), now: func() time.Time { return now },
		recoveryPort: &testProviderRecoveryPort{parent: true},
	}
	budget := &providerRecoveryBudget{Remaining: 7 * time.Second}
	wf, err := installProviderRecovery(native, budget)
	require.NoError(t, err)
	require.NoError(t, wf.actor.recordOwn(wf, start, now))
	assert.Equal(t, 2*time.Second, budget.Remaining)
	assert.Equal(t, 5*time.Second, wf.actor.elapsed)

	now = start.Add(8 * time.Second)
	require.NoError(t, wf.actor.recordOwn(wf, start.Add(4*time.Second), now))
	assert.Zero(t, budget.Remaining)
	assert.Equal(t, 8*time.Second, wf.actor.elapsed)
	assert.Equal(t, 8*time.Second, budget.allowance.confirmed.duration())
	assert.True(t, budget.allowance.exhausted)
}

func TestProviderRecoveryActorDeliveryFailureStopsRecovery(t *testing.T) {
	native := &routeWorkflowContext{ctx: t.Context()}
	wf, err := installProviderRecovery(native, &providerRecoveryBudget{Remaining: time.Minute})
	require.NoError(t, err)
	cause := errors.New("parent acceptance was lost")
	wf.actor.deliveries = append(wf.actor.deliveries, &recoveryActorAcceptance{ready: true, err: cause})
	require.ErrorIs(t, wf.Await(func() bool { return true }), cause)
	assert.Equal(t, time.Minute, wf.actor.local.Remaining)
}

func TestProviderRecoveryActorCancellationWinsLateInitialDebit(t *testing.T) {
	now := time.Unix(100, 0)
	ctx, cancel := context.WithCancel(t.Context())
	native := &routeWorkflowContext{ctx: ctx, now: func() time.Time { return now }}
	wf, err := installProviderRecovery(native, &providerRecoveryBudget{Remaining: time.Second})
	require.NoError(t, err)
	cancel()
	output := providerRecoveryFailureOutput()
	request, err := wf.actor.openLocal(wf, engine.ProviderRecoveryFailure{
		PublicationBatchID: output.PublicationBatchID,
		StartedAt:          now.Add(-5 * time.Second),
		EndedAt:            now,
		Err:                output.ProviderFailure,
	})
	require.NoError(t, err)
	require.ErrorIs(t, request.state.stopped, context.Canceled)
	require.ErrorIs(t, wf.actor.owner.stopped, context.Canceled)
	assert.Zero(t, wf.actor.local.Remaining)
	assert.Equal(t, 5*time.Second, wf.actor.local.allowance.confirmed.duration())
	assert.Zero(t, wf.actor.elapsed)
	assert.Empty(t, wf.actor.local.allowance.holds)
}

func TestProviderRecoveryActorFirstChildFailurePreservesOwnerStop(t *testing.T) {
	tests := []struct {
		name     string
		previous error
		want     error
	}{
		{name: "owner canceled", want: context.Canceled},
		{
			name:     "earlier deadline remains first",
			previous: engine.ErrPlannerActivityDeadlineExceeded,
			want:     engine.ErrPlannerActivityDeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start := time.Unix(100, 0)
			now := start
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			native := &recoveryActorWorkflow{
				routeWorkflowContext: &routeWorkflowContext{
					ctx: ctx, now: func() time.Time { return now },
				},
				children: make(map[string]*controlledChildHandle),
			}
			wf, err := installProviderRecovery(native, &providerRecoveryBudget{Remaining: time.Second})
			require.NoError(t, err)
			_, err = wf.StartChildWorkflow(wf.Context(), engine.ChildWorkflowRequest{ID: "child"})
			require.NoError(t, err)
			if test.previous != nil {
				require.NoError(t, wf.actor.apply(wf, wf.actor.owner.stop(test.previous)))
			}
			now = start.Add(5 * time.Second)
			cancel()
			callback := wf.Detached()
			require.NoError(t, callback.Context().Err())
			control := new(recoveryActorControl)
			receive, err := native.recoveryPort.accept(
				callback, "child", recoveryOwnerFailure("late-child", start, now), control,
			)
			require.NoError(t, err)
			require.NotNil(t, receive)
			assert.Zero(t, wf.actor.local.Remaining)
			assert.Equal(t, 5*time.Second, wf.actor.local.allowance.confirmed.duration())
			require.ErrorIs(t, wf.actor.owner.stopped, test.want)
			assert.Zero(t, wf.actor.elapsed)
			assert.Empty(t, wf.actor.local.allowance.holds)
			require.Len(t, control.messages, 1)
			stopped, ok := control.messages[0].(engine.ProviderRecoveryStopped)
			require.True(t, ok)
			require.ErrorIs(t, stopped.Err, test.want)

			require.NoError(t, receive(callback, engine.ProviderWaitRequest{Delay: time.Second}))
			assert.Len(t, control.messages, 1, "late phase requests cannot earn a new permission")
			assert.Zero(t, wf.actor.local.Remaining)
			assert.Empty(t, wf.actor.local.allowance.holds)
		})
	}
}

func TestProviderRecoveryActorSeparatesDerivedAndOwnerCancellation(t *testing.T) {
	now := time.Unix(100, 0)
	ctx, cancelOwner := context.WithCancel(t.Context())
	defer cancelOwner()
	native := &routeWorkflowContext{ctx: ctx, now: func() time.Time { return now }}
	wf, err := installProviderRecovery(native, &providerRecoveryBudget{Remaining: time.Minute})
	require.NoError(t, err)
	recovery, err := wf.actor.openLocal(wf, recoveryOwnerFailure("failed-plan", now, now))
	require.NoError(t, err)

	phase, cancelPhase := wf.WithCancel()
	cancelPhase()
	require.ErrorIs(t, recovery.wait(phase, time.Second), context.Canceled)
	require.NoError(t, wf.actor.owner.stopped, "one canceled phase must not stop the workflow owner")
	assert.False(t, wf.actor.clock.closed)
	assert.Empty(t, wf.actor.local.allowance.holds)
	require.NoError(t, wf.actor.recordOwn(wf, now.Add(-time.Second), now))
	assert.Equal(t, time.Second, wf.actor.elapsed)

	cancelOwner()
	now = now.Add(time.Second)
	require.NoError(t, wf.actor.recordOwn(wf.Detached(), now.Add(-time.Second), now))
	assert.True(t, wf.actor.clock.closed, "detached cleanup cannot hide owner cancellation")
	assert.Equal(t, time.Second, wf.actor.elapsed)
}

func TestAwaitAgentChildCancellationJoinsBeforeReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	wf := &routeWorkflowContext{ctx: ctx}
	child := new(recoveryJoinChild)
	_, err := awaitAgentChild(wf, child, ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, child.canceled)
	assert.True(t, child.joined)
}

func TestProviderRecoveryActorTimerEntryRequestsOneMeasuredObservation(t *testing.T) {
	now := time.Unix(100, 0)
	native := &routeWorkflowContext{ctx: t.Context(), now: func() time.Time { return now }}
	wf, err := installProviderRecovery(native, &providerRecoveryBudget{Remaining: time.Minute})
	require.NoError(t, err)
	output := providerRecoveryFailureOutput()
	execution, err := wf.actor.openLocal(wf, engine.ProviderRecoveryFailure{
		PublicationBatchID: output.PublicationBatchID,
		StartedAt:          now, EndedAt: now, Err: output.ProviderFailure,
	})
	require.NoError(t, err)
	message, err := execution.state.requestWait(time.Second)
	require.NoError(t, err)
	require.NoError(t, execution.send(wf, message))
	message, err = execution.state.enter(now)
	require.NoError(t, err)
	require.Nil(t, message)
	message, err = execution.state.waitEntered()
	require.NoError(t, err)
	require.NoError(t, execution.send(wf, message))
	assert.True(t, execution.owned.waitEntered)
	assert.Equal(t, providerRecoveryWaitAllowed, execution.state.phase)
	assert.Equal(t, time.Minute, wf.actor.local.Remaining)
	assert.Zero(t, wf.actor.elapsed, "entry and its zero-time reply prove no elapsed time")

	now = now.Add(time.Second)
	require.NoError(t, execution.observeWait(wf))
	message, err = execution.state.finishWait(now)
	require.NoError(t, err)
	require.NoError(t, execution.send(wf, message))
	assert.Equal(t, providerRecoveryRequestAttempt, execution.state.phase)
	assert.Equal(t, 59*time.Second, wf.actor.local.Remaining)
	assert.Equal(t, time.Second, wf.actor.elapsed)
}
