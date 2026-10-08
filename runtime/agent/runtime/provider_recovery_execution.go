package runtime

// One recovery execution keeps the exact failed planner request at its caller.
// It sends only certificates and phase requests to the owner. Permission,
// actual entry, observed time, and owner settlement remain separate transitions.

import (
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	providerRecoveryExecution struct {
		actor   *providerRecoveryActor
		state   *providerRecoveryRequester
		control engine.ProviderRecovery
		owned   *providerRecoveryRequest
		cancel  func()
		queued  []engine.ProviderRecoveryMessage
		opened  bool
	}
)

func (a *providerRecoveryActor) openLocal(wf engine.WorkflowContext, failure engine.ProviderRecoveryFailure) (*providerRecoveryExecution, error) {
	e := &providerRecoveryExecution{actor: a, state: newProviderRecoveryRequester(failure)}
	a.current = e
	if err := a.recordOwn(wf, failure.StartedAt, failure.EndedAt); err != nil {
		return nil, err
	}
	// A completed certificate may arrive after cancellation or a deadline won.
	// Record that winner before debiting the late certificate, so an overrun
	// cannot replace cancellation with a new budget-exhaustion cause.
	if a.clock.closed {
		cause := a.workflowContext.Err()
		if cause == nil {
			cause = engine.ErrPlannerActivityDeadlineExceeded
		}
		if a.owner != nil {
			if err := a.apply(wf, a.owner.stop(cause)); err != nil {
				return nil, err
			}
		} else if err := e.stop(wf, cause); err != nil {
			return nil, err
		}
	}
	if a.owner != nil {
		request, decisions, err := a.owner.open(wf.Now(), failure)
		if err != nil {
			return nil, err
		}
		e.owned = request
		e.opened = true
		a.recipients[request] = providerRecoveryRecipient{local: e}
		if err := a.apply(wf, decisions); err != nil {
			return nil, err
		}
	} else {
		control, err := a.port.Open(wf, failure, e.receive)
		if err != nil {
			return nil, a.fail(err)
		}
		e.control = control
		e.opened = true
		for _, message := range e.queued {
			if err := e.send(wf, message); err != nil {
				return nil, err
			}
		}
		e.queued = nil
	}
	if !a.clock.closed && a.local != nil && a.local.Remaining == 0 {
		if err := e.stop(wf, providerRecoveryExhausted(failure.Err)); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *providerRecoveryExecution) receive(wf engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
	answer, err := e.state.receive(wf.Now(), message)
	if err != nil {
		return e.actor.fail(err)
	}
	e.actor.version++
	if e.state.stopped != nil && e.cancel != nil {
		e.cancel()
	}
	if observed, ok := answer.(engine.ProviderWaitObserved); ok {
		if err := e.actor.recordOwn(wf, observed.StartedAt, observed.Through); err != nil {
			return err
		}
	}
	if answer != nil {
		return e.send(wf, answer)
	}
	return nil
}

func (e *providerRecoveryExecution) send(wf engine.WorkflowContext, message engine.ProviderRecoveryMessage) error {
	if !e.opened {
		e.queued = append(e.queued, message)
		return nil
	}
	if e.owned != nil {
		return e.actor.receiveOwned(wf, e.owned, message)
	}
	return e.actor.send(wf, e.control, message)
}

func (e *providerRecoveryExecution) stop(wf engine.WorkflowContext, cause error) error {
	for _, message := range e.state.stop(cause) {
		if err := e.send(wf, message); err != nil {
			return err
		}
	}
	return nil
}

// await waits for a root decision, never just immediate-parent delivery. The
// requester's existing cancellation and active deadline also interrupt this wait.
func (e *providerRecoveryExecution) await(wf engine.WorkflowContext, phase providerRecoveryRequesterPhase) error {
	err := wf.Await(func() bool { return e.state.phase == phase || e.state.stopped != nil })
	if err != nil {
		if stopErr := e.stop(wf, err); stopErr != nil {
			return errors.Join(err, stopErr)
		}
		return err
	}
	return e.state.stopped
}

// observeWait samples an entered timer when the workflow wakes. Local clock
// credit and the owner's report use the same endpoint, so both count the same
// elapsed interval. Permission alone supplies no elapsed time.
func (e *providerRecoveryExecution) observeWait(wf engine.WorkflowContext) error {
	if !e.state.waitActive {
		return nil
	}
	through := wf.Now()
	if err := e.actor.recordOwn(wf, e.state.started, through); err != nil {
		return err
	}
	if err := e.send(wf, engine.ProviderWaitObserved{
		StartedAt: e.state.started, Through: through,
	}); err != nil {
		return err
	}
	if e.actor.local != nil && e.actor.local.Remaining == 0 && e.state.stopped == nil {
		return e.stop(wf, providerRecoveryExhausted(e.state.failure.Err))
	}
	return nil
}

// expiry may shorten a root permission to the explicit local remainder.
// It can never extend the original permission, including after late evidence.
func (e *providerRecoveryExecution) expiry(now time.Time) time.Time {
	expiry := e.state.expiry
	if e.actor.local != nil {
		localEnd := now.Add(e.actor.local.Remaining)
		if localEnd.Before(expiry) {
			expiry = localEnd
		}
	}
	return expiry
}

func (e *providerRecoveryExecution) wait(wf engine.WorkflowContext, delay time.Duration) error {
	message, err := e.state.requestWait(delay)
	if err != nil {
		return err
	}
	if err := e.send(wf, message); err != nil {
		return err
	}
	if err := e.await(wf, providerRecoveryWaitAllowed); err != nil {
		return err
	}
	message, err = e.state.enter(wf.Now())
	if message != nil {
		if sendErr := e.send(wf, message); sendErr != nil {
			return errors.Join(err, sendErr)
		}
	}
	if err != nil {
		return err
	}
	phase, cancel := wf.WithCancel()
	e.cancel = cancel
	defer func() { cancel(); e.cancel = nil }()
	timer, err := phase.NewTimer(phase.Context(), e.expiry(wf.Now()).Sub(wf.Now()))
	if err != nil {
		message, stateErr := e.state.failUnproven(err)
		if stateErr != nil {
			return errors.Join(err, stateErr)
		}
		return errors.Join(err, e.send(wf, message))
	}
	message, err = e.state.waitEntered()
	if err != nil {
		return err
	}
	if err := e.send(wf, message); err != nil {
		return err
	}
	waitErr := phase.Await(timer.IsReady)
	if waitErr == nil {
		_, waitErr = timer.Get(phase.Context())
	}
	if e.state.stopped != nil {
		waitErr = e.state.stopped
	}
	if waitErr != nil {
		if err := e.stop(wf, waitErr); err != nil {
			return errors.Join(waitErr, err)
		}
	}
	// A completed timer supplies one end time for both local credit and root
	// spending. Sampling again after publication would charge a longer wait.
	ended := wf.Now()
	if err := e.actor.recordOwn(wf, e.state.started, ended); err != nil {
		return err
	}
	message, err = e.state.finishWait(ended)
	if err != nil {
		return err
	}
	if err := e.send(wf, message); err != nil {
		return err
	}
	if waitErr != nil {
		return waitErr
	}
	if e.actor.local != nil && e.actor.local.Remaining == 0 {
		if err := e.stop(wf, providerRecoveryExhausted(e.state.failure.Err)); err != nil {
			return err
		}
	}
	return e.await(wf, providerRecoveryRequestAttempt)
}

func (e *providerRecoveryExecution) attempt(wf engine.WorkflowContext, deadline time.Time) (time.Time, error) {
	message, err := e.state.requestAttempt(deadline)
	if err != nil {
		return time.Time{}, err
	}
	if err := e.send(wf, message); err != nil {
		return time.Time{}, err
	}
	if err := e.await(wf, providerRecoveryAttemptAllowed); err != nil {
		return time.Time{}, err
	}
	message, err = e.state.enter(wf.Now())
	if message != nil {
		if sendErr := e.send(wf, message); sendErr != nil {
			return time.Time{}, errors.Join(err, sendErr)
		}
	}
	if err != nil {
		return time.Time{}, err
	}
	return e.expiry(wf.Now()), nil
}
