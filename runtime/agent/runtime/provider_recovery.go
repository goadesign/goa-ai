package runtime

// Provider recovery retains one failed planning request in workflow history.
// Only the activity's complete model-call journal may certify safe recovery.
// The workflow waits through durable timers, then schedules that same request;
// accepted tools, transcripts, and planner limits are never replayed or reset.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
)

// retryableProviderFailure accepts one failed model call only after all phases
// finished. Errors from cleanup, observers, validation, or application code do
// not become retry permission just because they wrap a provider error.
func (j *modelInvocationJournal) retryableProviderFailure(err error) *model.ProviderError {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.sealed || j.sealedErr != nil || j.outputObserved ||
		j.outputErr != nil || len(j.order) != 1 || !j.selected.IsZero() {
		return nil
	}
	candidate := j.invocations[j.order[0]]
	if !candidate.finished || candidate.outcome == nil || candidate.response != nil ||
		candidate.rejectedOutputErr != nil {
		return nil
	}
	failure, ok := model.AsProviderError(err)
	if !ok || !failure.Retryable() ||
		(failure.Kind() != model.ProviderErrorKindRateLimited && failure.Kind() != model.ProviderErrorKindUnavailable) {
		return nil
	}
	if !onlyProviderFailure(err, failure) {
		return nil
	}
	outcome := candidate.outcome
	if outcome.ValidateFinalized() != nil || !outcome.HasFinalizer ||
		outcome.Incomplete || outcome.ProviderClose.Err != nil ||
		outcome.Context.Err != nil || outcome.Framework.Err != nil ||
		!cleanResults(outcome.CompletionObservers) ||
		!cleanResults(outcome.StreamSetupObservers) || !cleanResults(outcome.CloseObservers) ||
		!cleanResults(outcome.Finishers) || !cleanResults(outcome.Aborts) ||
		!cleanResults(outcome.Usage) || !cleanResults(outcome.Staging) {
		return nil
	}
	for _, result := range outcome.Validations {
		// A rejected unary provider call has no response to validate. Stream
		// receives and successful unary calls require their validation phase.
		if result.Err != nil || (!result.Called && outcome.ProviderCall.Err == nil) {
			return nil
		}
	}
	for _, results := range outcome.ReceiveObservers {
		if !cleanResults(results) {
			return nil
		}
	}
	matched := false
	if outcome.ProviderCall.Err != nil {
		if !onlyProviderFailure(outcome.ProviderCall.Err, failure) {
			return nil
		}
		matched = true
	}
	for _, result := range outcome.ProviderReceives {
		if !result.Called {
			return nil
		}
		if result.Err != nil {
			if !onlyProviderFailure(result.Err, failure) {
				return nil
			}
			matched = true
		}
	}
	if !matched || (outcome.ProviderCall.Err == nil && !outcome.ProviderClose.Called) {
		return nil
	}
	return failure
}

// onlyProviderFailure rejects additional error branches. The rate-limit
// sentinel is an adapter-owned classification of the same provider failure.
// The classified ProviderError owns its provider-specific cause as one error.
//
//nolint:errorlint // Inspect each exact branch: errors.Is/As would accept a join containing an unrelated failure.
func onlyProviderFailure(err error, expected *model.ProviderError) bool {
	if err == expected {
		return true
	}
	if err == model.ErrRateLimited {
		return expected.Kind() == model.ProviderErrorKindRateLimited
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		branches := wrapped.Unwrap()
		if len(branches) == 0 {
			return false
		}
		for _, branch := range branches {
			if !onlyProviderFailure(branch, expected) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return onlyProviderFailure(wrapped.Unwrap(), expected)
	default:
		return false
	}
}

// providerFailureOutput receives a provider failure certified by the finished
// invocation journal and returns it with failed-attempt usage as a successful
// activity result. The workflow uses this safe failure evidence to decide
// whether its recovery allowance permits another attempt. Lost replies and
// activity timeouts remain ordinary errors.
func (a *plannerActivityInvocation) providerFailureOutput(ctx context.Context, failure *model.ProviderError) (*PlanActivityOutput, error) {
	events := newPlannerEvents(a.events.agentID, a.events.runID, a.events.sessionID)
	a.invocations.publishUsage(ctx, events)
	output := &PlanActivityOutput{
		PublicationBatchID: a.publicationBatchID,
		ProviderFailure:    failure,
		Usage:              a.invocations.exportUsage(),
	}
	budget := &planActivityOutputBudget{}
	if err := budget.add(output); err != nil {
		return nil, err
	}
	records, err := events.acceptedRecords(budget)
	if err != nil {
		return nil, err
	}
	output.PlannerEvents = records
	if err := checkPlanActivityOutputBudget(output); err != nil {
		return nil, err
	}
	return output, nil
}

// validateProviderFailureOutput enforces the activity result union at the
// engine boundary. A retry marker cannot accompany an accepted planner result,
// selected history, published text, or another non-success outcome.
func validateProviderFailureOutput(out *PlanActivityOutput) error {
	return new(workflowcodec.Budget).AddSource(out)
}

// runPlanActivity recovers only a successful activity value proving a safe
// provider failure. Every activity retains its single-attempt engine policy.
// Timers survive worker replacement and cancellation interrupts every wait.
func (r *Runtime) runPlanActivity(wfCtx engine.WorkflowContext, activityName string, options engine.ActivityOptions, input PlanActivityInput, base *workflowConversation, deadline time.Time) (*PlanActivityOutput, error) {
	if base.providerControl == nil {
		wrapped, err := installProviderRecovery(wfCtx, base.providerRecovery)
		if err != nil {
			return nil, err
		}
		wfCtx = wrapped
		base.providerControl = wrapped.actor
	}
	a := base.providerControl
	before := a.elapsed
	originalDeadline := deadline
	var usage model.TokenUsage
	var recovery *providerRecoveryExecution
	defer func() { a.current = nil }()
	for attempt := 1; ; attempt++ {
		if err := wfCtx.Context().Err(); err != nil {
			return nil, err
		}
		if a.failure != nil {
			return nil, a.failure
		}
		if !originalDeadline.IsZero() {
			deadline = originalDeadline.Add(a.elapsed - before)
		}
		attemptOptions := options
		attemptDeadline := deadline
		attemptCtx := wfCtx
		var cancel func()
		if recovery != nil {
			expiry, err := recovery.attempt(wfCtx, deadline)
			if err != nil {
				return nil, err
			}
			if attemptDeadline.IsZero() || expiry.Before(attemptDeadline) {
				attemptDeadline = expiry
			}
			remaining := expiry.Sub(wfCtx.Now())
			if attemptOptions.StartToCloseTimeout == 0 || remaining < attemptOptions.StartToCloseTimeout {
				attemptOptions.StartToCloseTimeout = remaining
			}
			attemptCtx, cancel = wfCtx.WithCancel()
			recovery.cancel = cancel
		}
		started := wfCtx.Now()
		if recovery != nil {
			// Entry time belongs to the issued permission, immediately before
			// scheduling; retain it through result validation and publication.
			started = recovery.state.started
		}
		out, err := r.runPlanActivityOnce(attemptCtx, activityName, attemptOptions, input, base, attemptDeadline)
		if cancel != nil {
			cancel()
			recovery.cancel = nil
		}
		if err != nil {
			if recovery != nil {
				message, stateErr := recovery.state.failUnproven(err)
				if stateErr != nil {
					return out, errors.Join(err, stateErr)
				}
				if sendErr := recovery.send(wfCtx, message); sendErr != nil {
					return out, errors.Join(err, sendErr)
				}
				if recovery.state.stopped != nil {
					return out, recovery.state.stopped
				}
			}
			return out, err
		}
		usage, err = model.AddTokenUsage(usage, out.Usage)
		if err != nil {
			return nil, fmt.Errorf("aggregate provider recovery usage: %w", err)
		}
		if out.ProviderFailure == nil {
			if recovery != nil {
				message, err := recovery.state.succeed()
				if err != nil {
					return nil, err
				}
				if err := recovery.send(wfCtx, message); err != nil {
					return nil, err
				}
				if err := recovery.await(wfCtx, providerRecoveryRequestComplete); err != nil {
					return nil, err
				}
			}
			out.Usage = usage
			return out, nil
		}
		if a.owner == nil && !a.port.HasParent() {
			return out, out.ProviderFailure
		}
		failure := engine.ProviderRecoveryFailure{
			PublicationBatchID: out.PublicationBatchID,
			StartedAt:          started, EndedAt: wfCtx.Now(), Err: out.ProviderFailure,
		}
		if recovery == nil {
			recovery, err = a.openLocal(wfCtx, failure)
		} else {
			err = a.recordOwn(wfCtx, started, failure.EndedAt)
			if err == nil {
				var message engine.ProviderRecoveryMessage
				message, err = recovery.state.fail(failure)
				if err == nil {
					err = recovery.send(wfCtx, message)
				}
			}
			if err == nil {
				err = recovery.await(wfCtx, providerRecoveryRequestWait)
			}
		}
		if err != nil {
			return out, err
		}
		if a.local != nil && a.local.Remaining == 0 {
			if err := recovery.stop(wfCtx, providerRecoveryExhausted(failure.Err)); err != nil {
				return out, err
			}
		}
		if err := recovery.wait(wfCtx, providerRecoveryDelay(input.RunID, attempt)); err != nil {
			return out, err
		}
	}
}

// providerRecoveryDelay distributes independent runs deterministically so a
// provider recovery does not release every waiting run at once. Workflow replay
// computes the same 30-second exponential backoff, capped at five minutes.
func providerRecoveryDelay(runID string, attempt int) time.Duration {
	base := min(30*time.Second<<min(attempt-1, 4), 5*time.Minute)
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", runID, attempt)))
	return base * time.Duration(80+int(digest[0])%21) / 100
}
