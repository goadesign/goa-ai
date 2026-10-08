package runtime

// The provider recovery owner serializes decisions for requests already bound
// to real workflow children by the engine. It charges certified elapsed time,
// reserves one next phase, and returns messages for the runtime to deliver.
// It performs no I/O and never retries delivery, tools, or whole child runs.

import (
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// providerRecoveryOwner holds the root allowance and accepted requests.
	// pending preserves the arrival order of individual phase requests. stopped
	// records the first terminal cause; later evidence can change accounting
	// but cannot change that cause or issue another permission.
	providerRecoveryOwner struct {
		budget   *providerRecoveryBudget
		requests []*providerRecoveryRequest
		pending  []*providerRecoveryRequest
		stopped  error
		// lastFailure identifies the request whose measured evidence most
		// recently changed spending, so exhaustion reports that actual cause.
		lastFailure *model.ProviderError
	}

	// providerRecoveryRequest is one retained planner input's recovery state.
	// The engine owns its execution address and delivery sequence separately.
	providerRecoveryRequest struct {
		failure      engine.ProviderRecoveryFailure
		publications map[string]struct{}
		phase        providerRecoveryPhase
		delay        time.Duration
		deadline     time.Time
		hold         *providerRecoveryReservation
		wait         providerRecoveryInterval
		waitEntered  bool
		lastWait     providerRecoveryInterval
		hasLastWait  bool
		stopped      error
		stopSent     bool
	}

	// providerRecoveryDecision targets an already accepted request. The runtime
	// sends these messages in order and retains their acceptance futures before
	// returning from its engine callback.
	providerRecoveryDecision struct {
		request *providerRecoveryRequest
		message engine.ProviderRecoveryMessage
	}

	providerRecoveryPhase uint8
)

const (
	providerRecoveryNeedsWait providerRecoveryPhase = iota
	providerRecoveryPendingWait
	providerRecoveryWaiting
	providerRecoveryNeedsAttempt
	providerRecoveryPendingAttempt
	providerRecoveryAttempting
	providerRecoveryFinished
)

// newProviderRecoveryOwner starts live accounting from the checkpoint remainder.
// No child receives a copy of this owner or of its original policy maximum.
func newProviderRecoveryOwner(budget *providerRecoveryBudget) *providerRecoveryOwner {
	budget.activate()
	return &providerRecoveryOwner{budget: budget}
}

// open records the whole first failed activity before deciding whether recovery
// can continue. Even a late initial failure after Stop is retained truthfully.
func (o *providerRecoveryOwner) open(now time.Time, failure engine.ProviderRecoveryFailure) (*providerRecoveryRequest, []providerRecoveryDecision, error) {
	if err := validateRecoveryFailure(now, failure); err != nil {
		return nil, nil, err
	}
	request := &providerRecoveryRequest{
		failure:      failure,
		publications: map[string]struct{}{failure.PublicationBatchID: {}},
		phase:        providerRecoveryNeedsWait,
	}
	o.requests = append(o.requests, request)
	o.record(providerRecoveryInterval{start: failure.StartedAt, end: failure.EndedAt}, failure.Err)
	var decisions []providerRecoveryDecision
	switch {
	case o.stopped != nil:
		decisions = request.stop(o.stopped)
	case o.budget.allowance.exhausted:
		decisions = o.stop(providerRecoveryExhausted(failure.Err))
	}
	return request, decisions, nil
}

// receive applies one accepted transition. Phase results remain acceptable
// after Stop so the owner can account for actual work. New phase requests then
// receive Stop instead of permission, regardless of any later released hold.
func (o *providerRecoveryOwner) receive(now time.Time, request *providerRecoveryRequest, message engine.ProviderRecoveryMessage) ([]providerRecoveryDecision, error) {
	switch message := message.(type) {
	case engine.ProviderWaitRequest:
		if message.Delay <= 0 {
			return nil, errors.New("provider recovery wait must have a positive delay")
		}
		if request.stopped == nil {
			if request.phase != providerRecoveryNeedsWait {
				return nil, errors.New("provider recovery wait requested out of order")
			}
			request.delay = message.Delay
			request.phase = providerRecoveryPendingWait
			o.pending = append(o.pending, request)
			break
		}
		return request.stop(request.stopped), nil
	case engine.ProviderAttemptRequest:
		if request.stopped == nil {
			if request.phase != providerRecoveryNeedsAttempt {
				return nil, errors.New("provider recovery attempt requested before wait settlement")
			}
			request.deadline = message.ActiveDeadline
			request.phase = providerRecoveryPendingAttempt
			o.pending = append(o.pending, request)
			break
		}
		return request.stop(request.stopped), nil
	case engine.ProviderWaitObserved:
		if err := validateRecoveryInterval(now, message.StartedAt, message.Through); err != nil {
			return nil, err
		}
		if request.closedWaitContains(message.StartedAt, message.Through) {
			return nil, nil
		}
		if err := request.observeWait(message.StartedAt, message.Through); err != nil {
			return nil, err
		}
		o.record(request.wait, request.failure.Err)
	case engine.ProviderWaitFinished:
		if err := validateRecoveryInterval(now, message.StartedAt, message.EndedAt); err != nil {
			return nil, err
		}
		if request.hasLastWait && request.lastWait.start.Equal(message.StartedAt) {
			if !request.lastWait.end.Equal(message.EndedAt) {
				return nil, errors.New("provider recovery wait closed with conflicting times")
			}
			return nil, nil
		}
		if err := request.observeWait(message.StartedAt, message.EndedAt); err != nil {
			return nil, err
		}
		o.record(request.wait, request.failure.Err)
		o.release(request)
		request.lastWait = request.wait
		request.hasLastWait = true
		request.waitEntered = false
		request.phase = providerRecoveryNeedsAttempt
		return o.settle(now, request), nil
	case engine.ProviderAttemptSucceeded:
		if request.phase != providerRecoveryAttempting {
			return nil, errors.New("provider recovery success without an issued attempt")
		}
		o.release(request)
		request.phase = providerRecoveryFinished
		return o.settle(now, request), nil
	case engine.ProviderAttemptFailed:
		if request.phase != providerRecoveryAttempting {
			return nil, errors.New("provider recovery failure without an issued attempt")
		}
		if err := validateRecoveryFailure(now, message.Failure); err != nil {
			return nil, err
		}
		if !request.permitsEntry(message.Failure.StartedAt) {
			return nil, errors.New("provider recovery attempt entered outside its permission")
		}
		if _, exists := request.publications[message.Failure.PublicationBatchID]; exists {
			return nil, errors.New("provider recovery attempt reused a failed publication")
		}
		request.publications[message.Failure.PublicationBatchID] = struct{}{}
		request.failure = message.Failure
		o.record(providerRecoveryInterval{
			start: message.Failure.StartedAt,
			end:   message.Failure.EndedAt,
		}, message.Failure.Err)
		o.release(request)
		request.phase = providerRecoveryNeedsWait
		return o.settle(now, request), nil
	case engine.ProviderPhaseUnused:
		if request.hold == nil || request.waitEntered {
			return nil, errors.New("provider recovery non-entry conflicts with phase state")
		}
		o.release(request)
		decisions := request.stop(fmt.Errorf("provider recovery phase did not start: %w", request.failure.Err))
		return append(decisions, o.reconsider(now)...), nil
	case engine.ProviderPhaseFailed:
		if message.Err == nil {
			return nil, errors.New("provider recovery phase failure requires its cause")
		}
		if request.hold == nil {
			return nil, errors.New("provider recovery phase failure without an issued phase")
		}
		// A timeout or lost result cannot prove that the phase used no time.
		// Keep its hold and return the actual error without another permission.
		return request.stop(message.Err), nil
	case engine.ProviderRecoveryStopped:
		if message.Err == nil {
			return nil, errors.New("provider recovery Stop requires its cause")
		}
		return request.stop(message.Err), nil
	default:
		return nil, fmt.Errorf("provider recovery owner cannot receive %T", message)
	}
	return o.reconsider(now), nil
}

// reconsider admits pending phases in the recorded request order. It may share
// an existing held interval at no incremental cost. If capacity is merely held,
// the request remains pending; no exhaustion error or artificial timeout occurs.
func (o *providerRecoveryOwner) reconsider(now time.Time) []providerRecoveryDecision {
	if o.stopped != nil || len(o.requests) == 0 {
		return nil
	}
	if o.budget.allowance.exhausted {
		return o.stop(providerRecoveryExhausted(o.lastFailure))
	}
	var decisions []providerRecoveryDecision
	pending := o.pending[:0]
	for _, request := range o.pending {
		if request.stopped != nil {
			continue
		}
		var limit time.Time
		switch request.phase {
		case providerRecoveryPendingWait:
			limit = now.Add(request.delay)
		case providerRecoveryPendingAttempt:
			limit = request.deadline
			if !limit.IsZero() && !now.Before(limit) {
				decisions = append(decisions, request.stop(engine.ErrPlannerActivityDeadlineExceeded)...)
				continue
			}
		case providerRecoveryNeedsWait, providerRecoveryWaiting, providerRecoveryNeedsAttempt,
			providerRecoveryAttempting, providerRecoveryFinished:
			panic("provider recovery pending queue contains an issued phase")
		default:
			panic("provider recovery pending queue contains an issued phase")
		}
		hold := o.budget.allowance.reserve(now, limit)
		if hold == nil {
			pending = append(pending, request)
			continue
		}
		request.hold = hold
		if request.phase == providerRecoveryPendingWait {
			request.phase = providerRecoveryWaiting
			request.wait = providerRecoveryInterval{}
		} else {
			request.phase = providerRecoveryAttempting
		}
		decisions = append(decisions, providerRecoveryDecision{
			request: request,
			message: engine.ProviderRecoveryPermission{ExpiresAt: hold.interval.end},
		})
	}
	o.pending = pending
	return decisions
}

// stop closes owner admission once. Issued phases keep their original expiry
// and are asked to cancel; late phase evidence can still be settled.
func (o *providerRecoveryOwner) stop(cause error) []providerRecoveryDecision {
	if o.stopped == nil {
		o.stopped = cause
	}
	var decisions []providerRecoveryDecision
	for _, request := range o.requests {
		if request.phase != providerRecoveryFinished {
			decisions = append(decisions, request.stop(o.stopped)...)
		}
	}
	return decisions
}

// observations requests measured timer prefixes without making a deadline,
// extending a permission, or prescribing a polling frequency.
func (o *providerRecoveryOwner) observations() []providerRecoveryDecision {
	var decisions []providerRecoveryDecision
	for _, request := range o.requests {
		if request.phase == providerRecoveryWaiting && request.stopped == nil {
			decisions = append(decisions, providerRecoveryDecision{
				request: request,
				message: engine.ProviderWaitObservationRequest{},
			})
		}
	}
	return decisions
}

// canCheckpoint rejects suspension while a request could still consume time or
// has an unproven outcome. Only settled remaining capacity may be saved.
func (o *providerRecoveryOwner) canCheckpoint() bool {
	for _, request := range o.requests {
		if request.hold != nil || request.stopped == nil && request.phase != providerRecoveryFinished {
			return false
		}
	}
	return true
}

// settle records a root acknowledgment before admitting pending phases. It never
// acknowledges a successful continuation after a terminal cause already won.
func (o *providerRecoveryOwner) settle(now time.Time, request *providerRecoveryRequest) []providerRecoveryDecision {
	if o.stopped != nil {
		return request.stop(o.stopped)
	}
	if o.budget.allowance.exhausted {
		return o.stop(providerRecoveryExhausted(request.failure.Err))
	}
	if request.stopped != nil {
		return request.stop(request.stopped)
	}
	decisions := make([]providerRecoveryDecision, 0, len(o.pending)+1)
	decisions = append(decisions, providerRecoveryDecision{
		request: request,
		message: engine.ProviderRecoverySettled{},
	})
	return append(decisions, o.reconsider(now)...)
}

// record debits actual evidence without adding it to this owner's active-clock
// credit. Descendant spending alone does not prove the owner had no healthy work.
func (o *providerRecoveryOwner) record(interval providerRecoveryInterval, failure *model.ProviderError) {
	o.budget.allowance.confirm(interval)
	o.budget.Remaining = o.budget.allowance.remaining()
	o.lastFailure = failure
}

// release couples phase settlement to removal of that exact hold. A later
// transition cannot accidentally release another phase's capacity.
func (o *providerRecoveryOwner) release(request *providerRecoveryRequest) {
	o.budget.allowance.release(request.hold)
	request.hold = nil
}

// stop keeps the first request-level cause and emits its terminal response once.
// It deliberately retains any hold whose actual use remains unknown.
func (r *providerRecoveryRequest) stop(cause error) []providerRecoveryDecision {
	if r.stopped == nil {
		r.stopped = cause
	}
	if r.stopSent {
		return nil
	}
	r.stopSent = true
	return []providerRecoveryDecision{{
		request: r,
		message: engine.ProviderRecoveryStopped{Err: r.stopped},
	}}
}

// observeWait accepts only a timer entered before its original permission
// expired. Actual elapsed time may exceed that expiry and is retained in full.
func (r *providerRecoveryRequest) observeWait(start, through time.Time) error {
	if r.phase != providerRecoveryWaiting || !r.permitsEntry(start) {
		return errors.New("provider recovery wait entered outside its permission")
	}
	if r.waitEntered && (!r.wait.start.Equal(start) || through.Before(r.wait.end)) {
		return errors.New("provider recovery wait observation conflicts with prior progress")
	}
	r.wait = providerRecoveryInterval{start: start, end: through}
	r.waitEntered = true
	return nil
}

// closedWaitContains accepts a response to an observation request that crossed
// an already accepted wait closure. It adds no debit and emits no new settlement.
func (r *providerRecoveryRequest) closedWaitContains(start, through time.Time) bool {
	return r.hasLastWait && r.lastWait.start.Equal(start) && !through.After(r.lastWait.end)
}

// permitsEntry applies the inclusive start and exclusive original expiry.
func (r *providerRecoveryRequest) permitsEntry(start time.Time) bool {
	return r.hold != nil && !start.Before(r.hold.interval.start) && start.Before(r.hold.interval.end)
}

// validateRecoveryFailure checks evidence crossing the engine/runtime contract.
// Native failures without this complete certificate never enter recovery.
func validateRecoveryFailure(now time.Time, failure engine.ProviderRecoveryFailure) error {
	if failure.PublicationBatchID == "" {
		return errors.New("provider recovery requires a completed publication")
	}
	if failure.Err == nil || !failure.Err.Retryable() ||
		(failure.Err.Kind() != model.ProviderErrorKindRateLimited &&
			failure.Err.Kind() != model.ProviderErrorKindUnavailable) {
		return errors.New("provider recovery requires a certified temporary provider failure")
	}
	return validateRecoveryInterval(now, failure.StartedAt, failure.EndedAt)
}

// validateRecoveryInterval rejects missing, reversed, or future evidence at
// the runtime receiver. An equal start and end is a valid zero-time observation.
func validateRecoveryInterval(now, start, end time.Time) error {
	if start.IsZero() || end.IsZero() || end.Before(start) || end.After(now) {
		return errors.New("provider recovery requires a completed workflow-time interval")
	}
	return nil
}

func providerRecoveryExhausted(cause *model.ProviderError) error {
	return fmt.Errorf("provider recovery budget exhausted: %w", cause)
}
