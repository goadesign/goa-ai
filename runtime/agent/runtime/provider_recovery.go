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
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// providerRecoveryBudget is private workflow state. Remaining is retained
	// in external-input checkpoints; elapsed credits only this workflow's
	// provider failure time back to its active-work deadlines.
	providerRecoveryBudget struct {
		Remaining time.Duration
		elapsed   time.Duration
	}
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

// providerFailureOutput returns failed usage and typed retry permission as a
// successful activity value. Lost replies and activity timeouts remain ordinary
// errors and cannot cause the workflow to replay possibly published output.
func (a *plannerActivityInvocation) providerFailureOutput(ctx context.Context, err error) (*PlanActivityOutput, error) {
	failure := hooks.RunFailureFromError(err)
	failure.DebugMessage = errorevidence.DiagnosticMessage(failure.DebugMessage)
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
	if out.Result != nil || out.OutputContractFailure != nil || out.ModelInvocationRecovery != nil ||
		out.PlanningFailure != nil || len(out.Transcript) != 0 || out.HistoryContext != nil ||
		out.PublishedAssistantText != "" || out.RecoveryCatalog != nil {
		return errors.New("provider failure cannot accompany planner output or another result variant")
	}
	failure := out.ProviderFailure
	if failure.Provider == "" || !failure.Retryable ||
		(failure.Kind != string(model.ProviderErrorKindRateLimited) && failure.Kind != string(model.ProviderErrorKindUnavailable)) {
		return errors.New("provider recovery requires a typed temporary provider failure")
	}
	return nil
}

// runPlanActivity recovers only a successful activity value proving a safe
// provider failure. Every activity retains its single-attempt engine policy.
// Timers survive worker replacement and cancellation interrupts every wait.
func (r *Runtime) runPlanActivity(wfCtx engine.WorkflowContext, activityName string, options engine.ActivityOptions, input PlanActivityInput, base *workflowConversation, deadline time.Time) (*PlanActivityOutput, error) {
	recovery := base.providerRecovery
	if recovery == nil {
		return r.runPlanActivityOnce(wfCtx, activityName, options, input, base, deadline)
	}
	var usage model.TokenUsage
	for attempt := 1; ; attempt++ {
		if err := wfCtx.Context().Err(); err != nil {
			return nil, err
		}
		attemptOptions := options
		attemptDeadline := deadline
		if attempt > 1 && (attemptOptions.StartToCloseTimeout == 0 || attemptOptions.StartToCloseTimeout > recovery.Remaining) {
			attemptOptions.StartToCloseTimeout = recovery.Remaining
		}
		started := wfCtx.Now()
		if attempt > 1 {
			// The retry's queue wait and execution must both fit the remaining
			// recovery allowance. A timeout remains an ambiguous terminal error.
			recoveryDeadline := started.Add(recovery.Remaining)
			if attemptDeadline.IsZero() || recoveryDeadline.Before(attemptDeadline) {
				attemptDeadline = recoveryDeadline
			}
		}
		out, err := r.runPlanActivityOnce(wfCtx, activityName, attemptOptions, input, base, attemptDeadline)
		if err != nil {
			return out, err
		}
		usage, err = model.AddTokenUsage(usage, out.Usage)
		if err != nil {
			return nil, fmt.Errorf("aggregate provider recovery usage: %w", err)
		}
		if out.ProviderFailure == nil {
			out.Usage = usage
			return out, nil
		}
		failure := &planningFailureError{failure: *out.ProviderFailure}
		elapsed := wfCtx.Now().Sub(started)
		recovery.Remaining = max(0, recovery.Remaining-elapsed)
		recovery.elapsed += elapsed
		if !deadline.IsZero() {
			deadline = deadline.Add(elapsed)
		}
		if recovery.Remaining == 0 {
			return out, fmt.Errorf("provider recovery budget exhausted: %w", failure)
		}
		delay := min(providerRecoveryDelay(input.RunID, attempt), recovery.Remaining)
		started = wfCtx.Now()
		timer, err := wfCtx.NewTimer(wfCtx.Context(), delay)
		if err != nil {
			return nil, fmt.Errorf("schedule provider recovery: %w", err)
		}
		_, waitErr := timer.Get(wfCtx.Context())
		elapsed = wfCtx.Now().Sub(started)
		recovery.Remaining = max(0, recovery.Remaining-elapsed)
		recovery.elapsed += elapsed
		if !deadline.IsZero() {
			deadline = deadline.Add(elapsed)
		}
		if waitErr != nil {
			return nil, waitErr
		}
		if recovery.Remaining == 0 {
			return out, fmt.Errorf("provider recovery budget exhausted: %w", failure)
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

// preserveProviderRecoveryDeadlines removes failed provider work and its waits
// from active time while preserving the separate finite recovery allowance.
func preserveProviderRecoveryDeadlines(base *workflowConversation, before time.Duration, budget, hard *time.Time) {
	if base.providerRecovery == nil {
		return
	}
	elapsed := base.providerRecovery.elapsed - before
	if !budget.IsZero() {
		*budget = budget.Add(elapsed)
	}
	if !hard.IsZero() {
		*hard = hard.Add(elapsed)
	}
}
