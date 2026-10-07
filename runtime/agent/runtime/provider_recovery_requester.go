package runtime

// The provider recovery requester retains phase permission beside the unfinished
// planner input. Engine acceptance does not advance it: only the root's explicit
// settlement does. The runtime performs timers and activities between entry and
// completion calls, and carries returned messages over the bound engine control.

import (
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	providerRecoveryRequester struct {
		failure    engine.ProviderRecoveryFailure
		phase      providerRecoveryRequesterPhase
		expiry     time.Time
		started    time.Time
		entered    bool
		waitActive bool
		waitClosed providerRecoveryInterval
		hasWait    bool
		stopped    error
	}

	providerRecoveryRequesterPhase uint8
)

const (
	providerRecoveryRequestWait providerRecoveryRequesterPhase = iota
	providerRecoveryWaitPermission
	providerRecoveryWaitAllowed
	providerRecoveryWaitSettlement
	providerRecoveryRequestAttempt
	providerRecoveryAttemptPermission
	providerRecoveryAttemptAllowed
	providerRecoveryFailureSettlement
	providerRecoverySuccessSettlement
	providerRecoveryRequestComplete
)

// newProviderRecoveryRequester starts only after the runtime has certified and
// published a failed planner result. Healthy initial activities create no state.
func newProviderRecoveryRequester(failure engine.ProviderRecoveryFailure) *providerRecoveryRequester {
	return &providerRecoveryRequester{failure: failure}
}

// requestWait asks for exactly one positive backoff. A caller cannot skip the
// previous phase's settlement by sending another request.
func (r *providerRecoveryRequester) requestWait(delay time.Duration) (engine.ProviderRecoveryMessage, error) {
	if r.stopped != nil {
		return nil, r.stopped
	}
	if r.phase != providerRecoveryRequestWait || delay <= 0 {
		return nil, errors.New("provider recovery wait requested before failure settlement")
	}
	r.phase = providerRecoveryWaitPermission
	return engine.ProviderWaitRequest{Delay: delay}, nil
}

func (r *providerRecoveryRequester) requestAttempt(deadline time.Time) (engine.ProviderRecoveryMessage, error) {
	if r.stopped != nil {
		return nil, r.stopped
	}
	if r.phase != providerRecoveryRequestAttempt {
		return nil, errors.New("provider recovery attempt requested before wait settlement")
	}
	r.phase = providerRecoveryAttemptPermission
	return engine.ProviderAttemptRequest{ActiveDeadline: deadline}, nil
}

// receive applies root decisions and answers observations with measured time.
// The caller cancels an entered phase when stopped becomes non-nil. Stop is
// permanent even if an older permission or settlement arrives afterward.
func (r *providerRecoveryRequester) receive(now time.Time, message engine.ProviderRecoveryMessage) (engine.ProviderRecoveryMessage, error) {
	switch message := message.(type) {
	case engine.ProviderRecoveryPermission:
		if message.ExpiresAt.IsZero() {
			return nil, errors.New("provider recovery permission requires a finite expiry")
		}
		switch r.phase {
		case providerRecoveryWaitPermission:
			r.phase = providerRecoveryWaitAllowed
		case providerRecoveryAttemptPermission:
			r.phase = providerRecoveryAttemptAllowed
		case providerRecoveryRequestWait, providerRecoveryWaitAllowed, providerRecoveryWaitSettlement,
			providerRecoveryRequestAttempt, providerRecoveryAttemptAllowed, providerRecoveryFailureSettlement,
			providerRecoverySuccessSettlement, providerRecoveryRequestComplete:
			return nil, errors.New("provider recovery permission without an outstanding request")
		default:
			return nil, errors.New("provider recovery permission without an outstanding request")
		}
		r.expiry = message.ExpiresAt
		r.entered = false
		r.waitActive = false
		// A delayed permission still belongs to its original phase. If Stop
		// already won, report non-entry and release the owner's proven hold.
		if r.stopped == nil {
			return nil, nil
		}
		return engine.ProviderPhaseUnused{}, nil
	case engine.ProviderRecoverySettled:
		if r.stopped == nil {
			switch r.phase {
			case providerRecoveryWaitSettlement:
				r.phase = providerRecoveryRequestAttempt
			case providerRecoveryFailureSettlement:
				r.phase = providerRecoveryRequestWait
			case providerRecoverySuccessSettlement:
				r.phase = providerRecoveryRequestComplete
			case providerRecoveryRequestWait, providerRecoveryWaitPermission, providerRecoveryWaitAllowed,
				providerRecoveryRequestAttempt, providerRecoveryAttemptPermission, providerRecoveryAttemptAllowed,
				providerRecoveryRequestComplete:
				return nil, errors.New("provider recovery settled without a reported outcome")
			default:
				return nil, errors.New("provider recovery settled without a reported outcome")
			}
		}
	case engine.ProviderRecoveryStopped:
		if message.Err == nil {
			return nil, errors.New("provider recovery Stop requires its cause")
		}
		if r.stopped == nil {
			r.stopped = message.Err
		}
	case engine.ProviderWaitObservationRequest:
		if r.phase == providerRecoveryWaitAllowed && r.waitActive {
			return engine.ProviderWaitObserved{StartedAt: r.started, Through: now}, nil
		}
		if r.hasWait {
			return engine.ProviderWaitFinished{
				StartedAt: r.waitClosed.start,
				EndedAt:   r.waitClosed.end,
			}, nil
		}
	default:
		return nil, fmt.Errorf("provider recovery requester cannot receive %T", message)
	}
	return nil, nil
}

// enter checks the original expiry immediately before scheduling. It returns a
// non-entry proof instead of renewing a delayed permission. The caller also
// checks its cancellation scope immediately before invoking this method.
func (r *providerRecoveryRequester) enter(now time.Time) (engine.ProviderRecoveryMessage, error) {
	if r.stopped != nil {
		return nil, r.stopped
	}
	if r.entered || r.phase != providerRecoveryWaitAllowed && r.phase != providerRecoveryAttemptAllowed {
		return nil, errors.New("provider recovery phase entered without permission")
	}
	if !now.Before(r.expiry) {
		r.stopped = fmt.Errorf("provider recovery permission expired before phase entry: %w", r.failure.Err)
		return engine.ProviderPhaseUnused{}, r.stopped
	}
	r.started = now
	r.entered = true
	return nil, nil
}

// waitEntered is called after the timer was actually scheduled. Merely receiving
// permission cannot produce a timer observation or active-time credit.
func (r *providerRecoveryRequester) waitEntered() (engine.ProviderRecoveryMessage, error) {
	if r.phase != providerRecoveryWaitAllowed || !r.entered {
		return nil, errors.New("provider recovery timer observation before entry")
	}
	r.waitActive = true
	return engine.ProviderWaitObserved{StartedAt: r.started, Through: r.started}, nil
}

// finishWait records the actual timer interval, even when cancellation or an
// expired owner allowance has already stopped recovery. The caller reports
// any winning cancellation before considering another phase.
func (r *providerRecoveryRequester) finishWait(now time.Time) (engine.ProviderRecoveryMessage, error) {
	if r.phase != providerRecoveryWaitAllowed || !r.waitActive || now.Before(r.started) {
		return nil, errors.New("provider recovery wait completion without timer entry")
	}
	r.waitClosed = providerRecoveryInterval{start: r.started, end: now}
	r.hasWait = true
	r.entered = false
	r.waitActive = false
	r.phase = providerRecoveryWaitSettlement
	return engine.ProviderWaitFinished{StartedAt: r.started, EndedAt: now}, nil
}

// succeed reports a completed retry but does not finish recovery. The root must
// settle the successful phase before the caller returns its planner output.
func (r *providerRecoveryRequester) succeed() (engine.ProviderRecoveryMessage, error) {
	if r.phase != providerRecoveryAttemptAllowed || !r.entered {
		return nil, errors.New("provider recovery success without attempt entry")
	}
	r.entered = false
	r.phase = providerRecoverySuccessSettlement
	return engine.ProviderAttemptSucceeded{}, nil
}

// fail reports a fresh certified failure from the same retained planner input.
// Publication can finish after expiry; the completed interval is kept in full.
func (r *providerRecoveryRequester) fail(failure engine.ProviderRecoveryFailure) (engine.ProviderRecoveryMessage, error) {
	if r.phase != providerRecoveryAttemptAllowed || !r.entered || !failure.StartedAt.Equal(r.started) {
		return nil, errors.New("provider recovery failure does not match the entered attempt")
	}
	r.failure = failure
	r.entered = false
	r.phase = providerRecoveryFailureSettlement
	return engine.ProviderAttemptFailed{Failure: failure}, nil
}

// failUnproven reports a native failure without turning it into a certificate.
// The owner retains the hold because this outcome does not prove unused time.
func (r *providerRecoveryRequester) failUnproven(cause error) (engine.ProviderRecoveryMessage, error) {
	if cause == nil || !r.entered {
		return nil, errors.New("provider recovery native failure requires an entered phase and cause")
	}
	if r.stopped == nil {
		r.stopped = cause
	}
	return engine.ProviderPhaseFailed{Err: cause}, nil
}

// stop reports a local terminal decision before any proof of non-entry. This
// preserves cancellation as the cause while releasing a known unused permission.
// An entered phase keeps its hold until an actual outcome is available.
func (r *providerRecoveryRequester) stop(cause error) []engine.ProviderRecoveryMessage {
	if r.stopped == nil {
		r.stopped = cause
	}
	messages := []engine.ProviderRecoveryMessage{engine.ProviderRecoveryStopped{Err: r.stopped}}
	if !r.entered && (r.phase == providerRecoveryWaitAllowed || r.phase == providerRecoveryAttemptAllowed) {
		messages = append(messages, engine.ProviderPhaseUnused{})
	}
	return messages
}
