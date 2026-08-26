// Package runtime ties rejected model answers to the exact model call completed
// in one planner activity. The activity records only bounded response evidence
// after this check succeeds.
package runtime

import (
	"errors"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

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
		if candidate != nil && errors.As(candidate.err, &outputErr) {
			return candidate.err
		}
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
		if candidate == nil || !errors.As(candidate.err, &outputErr) {
			continue
		}
		if candidate.rejectedResponseEvidence == nil {
			return model.ResponseEvidence{}
		}
		return *candidate.rejectedResponseEvidence
	}
	return model.ResponseEvidence{}
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
		if candidate == nil || !responseContainsMessage(candidate.response, message) {
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

// responseContainsMessage reports whether one exact framework-owned message is
// part of a completed response.
func responseContainsMessage(response *model.Response, message *model.Message) bool {
	if response == nil || message == nil {
		return false
	}
	for i := range response.Content {
		if model.SameMessageOrigin(&response.Content[i], message) {
			return true
		}
	}
	return false
}
