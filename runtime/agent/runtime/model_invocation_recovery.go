// Package runtime ties rejected model output to the exact model call started in
// one planner activity. Completed-answer recovery verifies a response
// fingerprint; pre-canonical tool-call recovery selects the invocation by start
// order and carries only bounded generated guidance.
package runtime

import (
	"bytes"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/modelcall"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

// commitModelInvocationRecovery selects the earliest finalized validation when
// every provider and callback phase is clean and the planner added no error.
func (j *modelInvocationJournal) commitModelInvocationRecovery(plannerErr error) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	var staged []*modelInvocationCandidate
	var eligible []*modelInvocationCandidate
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate == nil || !candidate.finished || candidate.outcome == nil {
			return false
		}
		if candidate.rejectedValidationErr == nil {
			if candidate.outcome.ValidateFinalized() != nil || candidate.outcome.Error() != nil {
				return false
			}
			continue
		}
		if !recoverableModelCall(candidate) {
			return false
		}
		staged = append(staged, candidate)
		if candidateRecoveryEligible(candidate) {
			eligible = append(eligible, candidate)
		}
	}
	if len(eligible) == 0 || !onlyPropagatesModelValidation(plannerErr, staged) {
		return false
	}
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate != nil && candidate == eligible[0] {
			j.recovery = id
			return true
		}
	}
	return false
}

// stagedModelCallsClean reports whether provider and callback phases contain
// no failure beyond each exact staged validation.
func (j *modelInvocationJournal) stagedModelCallsClean() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate == nil || !candidate.finished || candidate.outcome == nil {
			return false
		}
		if candidate.rejectedValidationErr != nil {
			if !recoverableModelCall(candidate) {
				return false
			}
			continue
		}
		if candidate.outcome.ValidateFinalized() != nil || candidate.outcome.Error() != nil {
			return false
		}
	}
	return true
}

// hasRecoverableModelOutput reports whether a staged validation has the exact
// bounded guidance and terminal usage required by ModelInvocationRecovery.
func (j *modelInvocationJournal) hasRecoverableModelOutput() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate != nil && candidateRecoveryEligible(candidate) {
			return true
		}
	}
	return false
}

// hasStagedModelOutput reports whether a model-boundary validation candidate
// requires the activity's one terminal recovery decision.
func (j *modelInvocationJournal) hasStagedModelOutput() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate != nil && candidate.rejectedValidationErr != nil {
			return true
		}
	}
	return false
}

// outcomeErrors joins frozen call failures for outward activity diagnostics.
// Recovery decisions never inspect this derived error.
func (j *modelInvocationJournal) outcomeErrors() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var err error
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate != nil && candidate.outcome != nil {
			err = errors.Join(err, candidate.outcome.Error())
		}
	}
	return err
}

// recoverableModelCall verifies the complete structured result for one staged
// validation. Only the runtime observer may return that exact rejection. A
// provider close failure is cleanup evidence already reported to stream
// observers; it does not replace a completed validation result.
func recoverableModelCall(candidate *modelInvocationCandidate) bool {
	if !candidate.finished || candidate.outcome == nil || candidate.rejectedOutputErr == nil {
		return false
	}
	outcome := candidate.outcome
	if outcome.ValidateFinalized() != nil ||
		!outcome.ProviderCall.Called ||
		outcome.ProviderCall.Err != nil ||
		outcome.Context.Err != nil ||
		outcome.Framework.Err != nil ||
		!outcome.HasFinalizer {
		return false
	}
	if !cleanResults(outcome.ProviderReceives) ||
		!cleanResults(outcome.Finishers) ||
		!cleanResults(outcome.Usage) ||
		!cleanResults(outcome.Staging) ||
		len(outcome.Staging) == 0 {
		return false
	}
	if !validationMatches(outcome.Validations, candidate.rejectedValidationErr) {
		return false
	}
	if !cleanResults(outcome.CloseObservers) {
		return false
	}
	if len(outcome.StreamSetupObservers) > 0 {
		if !outcome.ProviderClose.Called ||
			!outcome.Completed ||
			outcome.Incomplete {
			return false
		}
	}
	if !observerResultsMatch(
		outcome.CompletionObservers,
		outcome.FinalizerIndex,
		candidate,
	) || !observerResultsMatch(
		outcome.StreamSetupObservers,
		outcome.FinalizerIndex,
		candidate,
	) {
		return false
	}
	for _, results := range outcome.ReceiveObservers {
		if !observerResultsMatch(results, outcome.FinalizerIndex, candidate) {
			return false
		}
	}
	return true
}

// candidateRecoveryEligible requires one and only one bounded recovery value
// plus usage attributed to the same rejected invocation.
func candidateRecoveryEligible(candidate *modelInvocationCandidate) bool {
	if candidate == nil || !candidate.usageSeen || candidate.outcome == nil ||
		len(candidate.outcome.Usage) == 0 {
		return false
	}
	correctionPresent := candidate.recoveryCorrection != ""
	namePresent := candidate.unadvertisedToolName != ""
	return correctionPresent != namePresent
}

// cleanResults reports whether every expected callback ran without an error.
func cleanResults(results []modelcall.Result) bool {
	for _, result := range results {
		if !result.Called || result.Err != nil {
			return false
		}
	}
	return true
}

// validationMatches requires one model-boundary validation slot to hold the
// exact staged validation object and rejects every other validation error.
func validationMatches(results []modelcall.Result, expected *model.OutputValidationError) bool {
	matched := false
	for _, result := range results {
		if !result.Called || result.Err == nil {
			continue
		}
		actual, ok := exactModelOutputValidation(result.Err)
		if !ok || !modelcall.Exact(actual, expected) || matched {
			return false
		}
		matched = true
	}
	return matched
}

// observerResultsMatch permits the runtime observer's exact validation return
// and requires every other observer slot to be clean.
func observerResultsMatch(
	results []modelcall.Result,
	runtimeIndex int,
	candidate *modelInvocationCandidate,
) bool {
	for i, result := range results {
		if !result.Called {
			return false
		}
		if i == runtimeIndex {
			if result.Err != nil && !onlyExpectedValidation(result.Err, candidate) {
				return false
			}
			continue
		}
		if result.Err != nil {
			return false
		}
	}
	return true
}

// onlyPropagatesModelValidation accepts joins whose leaves are exact staged
// validation errors. A wrapper or newly created leaf is a planner rewrite.
func onlyPropagatesModelValidation(err error, candidates []*modelInvocationCandidate) bool {
	if err == nil {
		return true
	}
	return onlyExpectedValidation(err, candidates...)
}

func onlyExpectedValidation(err error, candidates ...*modelInvocationCandidate) bool {
	for _, candidate := range candidates {
		if modelcall.Exact(err, candidate.rejectedValidationErr) ||
			modelcall.Exact(err, candidate.rejectedOutputErr) {
			return true
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyExpectedValidation(cause, candidates...) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyExpectedValidation(wrapped.Unwrap(), candidates...)
	}
	return false
}

// exactModelOutputValidation returns the one OutputValidationError shared by
// every error leaf. Runtime staging and recovery use it so wrappers and
// duplicate joins preserve identity while any unrelated leaf rejects the
// classification.
func exactModelOutputValidation(err error) (*model.OutputValidationError, bool) {
	if err == nil {
		return nil, false
	}
	//nolint:errorlint // Exact root identity is required before following wrappers.
	if validationErr, ok := err.(*model.OutputValidationError); ok {
		return validationErr, validationErr != nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return nil, false
		}
		var expected *model.OutputValidationError
		for _, cause := range causes {
			actual, valid := exactModelOutputValidation(cause)
			if !valid {
				return nil, false
			}
			if expected == nil {
				expected = actual
				continue
			}
			if !modelcall.Exact(actual, expected) {
				return nil, false
			}
		}
		return expected, true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return exactModelOutputValidation(wrapped.Unwrap())
	}
	return nil, false
}

// outputContractError returns the earliest-started invocation that rejected
// model output. Completion order may latch the activity sooner, but it cannot
// change the terminal reason after concurrent calls finish.
func (j *modelInvocationJournal) outputContractError() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.outputContractErrorLocked()
}

// outputContractErrorLocked returns the earliest-started rejected invocation.
// The caller must hold j.mu.
func (j *modelInvocationJournal) outputContractErrorLocked() error {
	for _, id := range j.order {
		candidate := j.invocations[id]
		var outputErr *planner.OutputContractError
		if candidate == nil || !candidate.finished || candidate.outcome == nil ||
			!errors.As(candidate.err, &outputErr) {
			continue
		}
		return candidate.err
	}
	return j.outputErr
}

// rejectedModelResponseEvidence returns complete-response evidence from the
// same earliest-started rejected invocation used by outputContractError.
func (j *modelInvocationJournal) rejectedModelResponseEvidence() model.ResponseEvidence {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, id := range j.order {
		candidate := j.invocations[id]
		var outputErr *planner.OutputContractError
		if candidate == nil ||
			!candidate.finished ||
			candidate.outcome == nil ||
			(!j.recovery.IsZero() && j.recovery != id) ||
			!errors.As(candidate.err, &outputErr) {
			continue
		}
		if candidate.rejectedResponseEvidence == nil {
			return model.ResponseEvidence{}
		}
		return *candidate.rejectedResponseEvidence
	}
	return model.ResponseEvidence{}
}

// recoverableModelInvocationRecovery returns the one bounded recovery fact
// from the exact earliest-started invocation selected as this activity's
// rejection. A later completion cannot supply a name or correction for an
// earlier failure. Missing or contradictory facts remain terminal.
func (j *modelInvocationJournal) recoverableModelInvocationRecovery() *api.ModelInvocationRecovery {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, id := range j.order {
		candidate := j.invocations[id]
		var outputErr *planner.OutputContractError
		if candidate == nil ||
			j.recovery != id ||
			!errors.As(candidate.err, &outputErr) {
			continue
		}
		correctionPresent := candidate.recoveryCorrection != ""
		namePresent := candidate.unadvertisedToolName != ""
		if correctionPresent == namePresent {
			return nil
		}
		return &api.ModelInvocationRecovery{
			Correction:           candidate.recoveryCorrection,
			UnadvertisedToolName: candidate.unadvertisedToolName,
		}
	}
	return nil
}

// recoverableModelResponseEvidence verifies that the planner rejected one exact
// completed answer from this activity and returns its response fingerprint.
func (j *modelInvocationJournal) recoverableModelResponseEvidence(message *model.Message) (model.ResponseEvidence, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if message == nil {
		return model.ResponseEvidence{}, errors.New("recoverable model output is missing its rejected answer")
	}
	var matched *modelInvocationCandidate
	for _, id := range j.order {
		candidate := j.invocations[id]
		if candidate == nil {
			continue
		}
		contains, err := responseContainsMessage(candidate.response, message)
		if err != nil {
			return model.ResponseEvidence{}, err
		}
		if !contains {
			continue
		}
		if matched != nil {
			return model.ResponseEvidence{}, errors.New("recoverable model output matches multiple model invocations")
		}
		matched = candidate
	}
	if matched == nil {
		return model.ResponseEvidence{}, errors.New("recoverable model output references a response from another planner activity")
	}
	if !matched.finished || matched.err != nil {
		return model.ResponseEvidence{}, errors.New("recoverable model output references an incomplete model invocation")
	}
	if !matched.responseEvidence.Present || matched.responseEvidence.SHA256 == "" {
		return model.ResponseEvidence{}, errors.New("recoverable model output response has no stable fingerprint")
	}
	return matched.responseEvidence, nil
}

// responseContainsMessage reports whether one unchanged framework-owned message
// is part of a completed response. Origin proves where the message came from;
// canonical JSON proves the planner did not alter its content afterward.
func responseContainsMessage(response *model.Response, message *model.Message) (bool, error) {
	if response == nil || message == nil {
		return false, nil
	}
	messageJSON, err := message.MarshalJSON()
	if err != nil {
		return false, fmt.Errorf("encode recoverable model answer: %w", err)
	}
	for i := range response.Content {
		candidate := &response.Content[i]
		if !model.SameMessageOrigin(candidate, message) {
			continue
		}
		candidateJSON, err := candidate.MarshalJSON()
		if err != nil {
			return false, fmt.Errorf("encode recorded model answer: %w", err)
		}
		return bytes.Equal(candidateJSON, messageJSON), nil
	}
	return false, nil
}
