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
		if candidate == nil || !errors.As(candidate.err, &outputErr) {
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
