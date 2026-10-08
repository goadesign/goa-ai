package runtime

// The workflow recovery actor connects the allowance and requester state
// machines to the engine's accepted child controls. Engine callbacks and the
// workflow main function run serially. Callbacks record outgoing obligations
// without waiting; workflow waits collect their acceptance and propagate errors.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	providerRecoveryActor struct {
		// workflowContext belongs to the installed workflow, not a canceled
		// phase or detached cleanup scope that shares this actor.
		workflowContext context.Context
		finalization    *workflowFinalizationState
		port            engine.ProviderRecoveryPort
		owner           *providerRecoveryOwner
		local           *providerRecoveryBudget
		clock           *providerRecoveryClock
		elapsed         time.Duration
		reported        providerRecoveryIntervals
		children        map[string]*providerRecoveryBranch
		work            []providerRecoveryWork
		recipients      map[*providerRecoveryRequest]providerRecoveryRecipient
		deliveries      []engine.Future[struct{}]
		relays          []*providerRecoveryRelay
		current         *providerRecoveryExecution
		failure         error
		version         uint64
		budget          *time.Time
		hard            *time.Time
	}

	providerRecoveryWork struct {
		branch *providerRecoveryBranch
		ready  func() bool
	}

	providerRecoveryRecipient struct {
		control engine.ProviderRecovery
		local   *providerRecoveryExecution
	}

	providerRecoveryRelay struct {
		actor *providerRecoveryActor
		down  engine.ProviderRecovery
		up    engine.ProviderRecovery
	}
)

// installProviderRecovery uses the restored local remainder when present.
// Inherited recovery forwards to the actual parent; a positive local policy
// then limits only this workflow's own failed planning and backoff time.
func installProviderRecovery(wf engine.WorkflowContext, local *providerRecoveryBudget) (*providerRecoveryWorkflowContext, error) {
	a := &providerRecoveryActor{
		workflowContext: wf.Context(),
		port:            wf.ProviderRecovery(),
		local:           local,
		clock:           new(providerRecoveryClock),
		children:        make(map[string]*providerRecoveryBranch),
		recipients:      make(map[*providerRecoveryRequest]providerRecoveryRecipient),
	}
	if local != nil && !a.port.HasParent() {
		a.owner = newProviderRecoveryOwner(local)
	}
	wrapped := &providerRecoveryWorkflowContext{WorkflowContext: wf, actor: a}
	if err := a.port.RegisterPauseHandler(a.acceptPause); err != nil {
		return nil, fmt.Errorf("register child recovery clock: %w", err)
	}
	if a.owner != nil || a.port.HasParent() {
		if err := a.port.Register(a.accept); err != nil {
			return nil, fmt.Errorf("register provider recovery: %w", err)
		}
	}
	return wrapped, nil
}

// accept receives an already bound child request. An intermediate workflow
// forwards it without charging its local policy or claiming the leaf's pause.
func (a *providerRecoveryActor) accept(wf engine.WorkflowContext, childID string, failure engine.ProviderRecoveryFailure, control engine.ProviderRecovery) (engine.ProviderRecoveryReceive, error) {
	if _, ok := a.children[childID]; !ok {
		return nil, fmt.Errorf("provider recovery from untracked child %q", childID)
	}
	if a.owner == nil {
		relay := &providerRecoveryRelay{actor: a, down: control}
		up, err := control.Forward(wf, relay.receiveDown)
		if err != nil {
			return nil, a.fail(err)
		}
		relay.up = up
		a.relays = append(a.relays, relay)
		return relay.receiveUp, nil
	}
	// A child's first failure can arrive through detached cleanup after the
	// owner was canceled. Keep that cancellation before charging the whole
	// failure, so the child receives the winning cause even if allowance runs out.
	if cause := a.workflowContext.Err(); cause != nil && a.owner.stopped == nil {
		if err := a.apply(wf, a.owner.stop(cause)); err != nil {
			return nil, err
		}
	}
	request, decisions, err := a.owner.open(wf.Now(), failure)
	if err != nil {
		return nil, a.fail(err)
	}
	a.recipients[request] = providerRecoveryRecipient{control: control}
	if err := a.apply(wf, decisions); err != nil {
		return nil, err
	}
	return func(ctx engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
		return a.receiveOwned(ctx, request, message)
	}, nil
}

// receiveOwned applies local and child evidence through the same owner. The
// first actual timer entry asks for a measured prefix; repeated observations
// cannot create a request-and-reply loop when workflow time has not advanced.
func (a *providerRecoveryActor) receiveOwned(wf engine.WorkflowContext, request *providerRecoveryRequest, message engine.ProviderRecoveryMessage) error {
	if cause := a.workflowContext.Err(); cause != nil && a.owner.stopped == nil {
		if err := a.apply(wf, a.owner.stop(cause)); err != nil {
			return err
		}
	}
	entered := request.waitEntered
	decisions, err := a.owner.receive(wf.Now(), request, message)
	if err != nil {
		return a.fail(err)
	}
	if !entered && request.waitEntered && request.stopped == nil {
		decisions = append(decisions, a.owner.observations()...)
	}
	return a.apply(wf, decisions)
}

// acceptPause changes only the immediate child's clock evidence. It computes
// this workflow's measured pause before recording an independent onward report.
func (a *providerRecoveryActor) acceptPause(wf engine.WorkflowContext, childID string, pause engine.ProviderRecoveryPaused) error {
	branch, ok := a.children[childID]
	if !ok {
		return a.fail(fmt.Errorf("provider pause from untracked child %q", childID))
	}
	if err := validateRecoveryInterval(wf.Now(), pause.StartedAt, pause.Through); err != nil {
		return a.fail(err)
	}
	if pause.StartedAt.Before(branch.start) || !branch.end.IsZero() && pause.Through.After(branch.end) {
		return a.fail(errors.New("provider pause lies outside its child's observed lifetime"))
	}
	branch.paused = branch.paused.add(providerRecoveryInterval{
		start: pause.StartedAt, end: pause.Through,
	})
	return a.observe(wf)
}

func (a *providerRecoveryActor) apply(wf engine.WorkflowContext, decisions []providerRecoveryDecision) error {
	for _, decision := range decisions {
		target, ok := a.recipients[decision.request]
		if !ok {
			panic("provider recovery decision has no accepted recipient")
		}
		if target.local != nil {
			if err := target.local.receive(wf, decision.message); err != nil {
				return a.fail(err)
			}
		} else if err := a.send(wf, target.control, decision.message); err != nil {
			return err
		}
	}
	if a.owner != nil && a.owner.stopped != nil &&
		!errors.Is(a.owner.stopped, engine.ErrPlannerActivityDeadlineExceeded) {
		// The owner accepted the evidence and issued Stop. Record the workflow
		// failure without returning it as a rejection of that accepted message.
		a.recordFailure(a.owner.stopped)
	}
	a.version++
	return nil
}

func (a *providerRecoveryActor) send(wf engine.WorkflowContext, control engine.ProviderRecovery, message engine.ProviderRecoveryMessage) error {
	future, err := control.Send(wf, message)
	if err != nil {
		return a.fail(err)
	}
	a.deliveries = append(a.deliveries, future)
	a.version++
	return nil
}

func (r *providerRecoveryRelay) receiveUp(wf engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
	return r.actor.send(wf, r.up, message)
}

func (r *providerRecoveryRelay) receiveDown(wf engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
	return r.actor.send(wf, r.down, message)
}

// observe samples ordinary completion without requiring healthy work to send
// recovery reports. Newly proven pause extends active deadlines once and is
// retained as an outgoing obligation before this callback returns.
func (a *providerRecoveryActor) observe(wf engine.WorkflowContext) error {
	now := wf.Now()
	if a.workflowContext.Err() != nil {
		a.clock.closed = true
	}
	if a.finalization != nil {
		switch a.finalization.phase.Load() {
		case workflowCancellationAccepted, workflowFinalizationInProgress, workflowFinalizationFinished:
			a.clock.closed = true
		}
	}
	for _, work := range a.work {
		if work.branch.end.IsZero() && work.ready() {
			work.branch.end = now
		}
	}
	credit := a.clock.observe(now)
	a.elapsed += credit
	if credit > 0 {
		if a.budget != nil && !a.budget.IsZero() {
			*a.budget = a.budget.Add(credit)
		}
		if a.hard != nil && !a.hard.IsZero() {
			*a.hard = a.hard.Add(credit)
		}
		a.version++
	}
	added := a.clock.credited.subtract(a.reported)
	for _, interval := range added {
		future, err := a.port.ReportPause(wf, engine.ProviderRecoveryPaused{
			StartedAt: interval.start, Through: interval.end,
		})
		if err != nil {
			return a.fail(err)
		}
		a.deliveries = append(a.deliveries, future)
		a.reported = a.reported.add(interval)
	}
	return nil
}

// recordOwn retains the whole certified failure or entered timer interval.
// Descendant evidence never calls this method and therefore cannot spend a
// child's explicit local policy.
func (a *providerRecoveryActor) recordOwn(wf engine.WorkflowContext, start, end time.Time) error {
	interval := providerRecoveryInterval{start: start, end: end}
	a.clock.own = a.clock.own.add(interval)
	if a.local != nil && a.owner == nil {
		a.local.activate()
		a.local.allowance.confirm(interval)
		a.local.Remaining = a.local.allowance.remaining()
	}
	return a.observe(wf)
}

// collect checks completed acceptance futures outside engine callbacks. Failed
// delivery stops recovery instead of being interpreted as phase settlement.
func (a *providerRecoveryActor) collect(wf engine.WorkflowContext) error {
	pending := a.deliveries
	a.deliveries = nil
	for _, future := range pending {
		if !future.IsReady() {
			a.deliveries = append(a.deliveries, future)
			continue
		}
		if _, err := future.Get(wf.Context()); err != nil {
			a.recordFailure(err)
		}
	}
	return a.failure
}

func (a *providerRecoveryActor) fail(err error) error {
	a.recordFailure(err)
	return a.failure
}

// recordFailure keeps the first failure and cancels the current recovery phase.
// Later workflow waits return that stored cause; later failures cannot replace it.
func (a *providerRecoveryActor) recordFailure(err error) {
	if a.failure == nil {
		a.failure = err
		a.version++
		if a.current != nil && a.current.cancel != nil {
			a.current.cancel()
		}
	}
}

// close ends recovery admission before ordinary workflow completion. Successful
// completion drains accepted reports; cancellation keeps its original error
// without waiting forever for an unreachable recipient.
func (a *providerRecoveryActor) close(wf engine.WorkflowContext, cause error) error {
	if cause != nil {
		a.clock.closed = true
		for _, relay := range a.relays {
			if err := a.send(wf, relay.up, engine.ProviderRecoveryStopped{Err: cause}); err != nil {
				return err
			}
			if err := a.send(wf, relay.down, engine.ProviderRecoveryStopped{Err: cause}); err != nil {
				return err
			}
		}
		if a.owner != nil {
			if err := a.apply(wf, a.owner.stop(cause)); err != nil {
				return err
			}
		}
		return nil
	}
	for len(a.deliveries) > 0 {
		if err := a.collect(wf); err != nil {
			return err
		}
		if len(a.deliveries) > 0 {
			if err := wf.Await(func() bool { return len(a.deliveries) == 0 || a.deliveryReady() }); err != nil {
				return err
			}
		}
	}
	a.clock.closed = true
	return a.failure
}

func (a *providerRecoveryActor) deliveryReady() bool {
	for _, future := range a.deliveries {
		if future.IsReady() {
			return true
		}
	}
	return false
}
