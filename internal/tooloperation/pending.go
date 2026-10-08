// Package tooloperation retains exactly one next action for an unfinished tool
// invocation. Goa codecs validate its stored shape; MCP checks opaque interaction
// parameters. Construction and accessors copy mutable data, so workflow history
// and registry messages retain the same accepted value and JSON byte size.
package tooloperation

import (
	"bytes"
	"encoding/json"
	"errors"

	gentooloperations "goa.design/goa-ai/registry/gen/tooloperations"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	// Pending retains ordinary input, Task waiting or Task input. Its selected
	// branch cannot carry completed tool data or another branch's fields.
	Pending struct {
		value   gentooloperations.Outcome
		encoded []byte
	}
)

// NewPendingInput copies the original tool's host questions and service state.
// Missing input or an unsupported interaction fails before the value is saved.
func NewPendingInput(input *mcp.InputRequired) (*Pending, error) {
	if input == nil {
		return nil, errors.New("pending input is required")
	}
	return newPending(gentooloperations.NewOutcomeInput(&gentooloperations.PendingInput{
		State: cloneState(input.RequestState), Requests: storeRequests(input.Requests),
	}))
}

// NewPendingTaskWait saves the exact Task to read next. Polling guidance belongs
// to this next observation and imposes no retention or complete-run time limit.
func NewPendingTaskWait(taskID string, pollIntervalMs *int64) (*Pending, error) {
	return newPending(gentooloperations.NewOutcomeTaskWait(&gentooloperations.TaskWait{
		TaskID: taskID, PollIntervalMs: clonePollInterval(pollIntervalMs),
	}))
}

// NewPendingTaskInput copies the outstanding Task questions. Task input cannot
// contain ordinary request state; answers will be sent to tasks/update.
func NewPendingTaskInput(taskID string, pollIntervalMs *int64, input *mcp.InputRequired) (*Pending, error) {
	if input == nil || input.RequestState != nil {
		return nil, errors.New("task input requires questions without request state")
	}
	return newPending(gentooloperations.NewOutcomeTaskInput(&gentooloperations.TaskInput{
		TaskID: taskID, PollIntervalMs: clonePollInterval(pollIntervalMs), Requests: storeRequests(input.Requests),
	}))
}

// AsInput returns copied questions and state only for ordinary tool continuation.
func (p Pending) AsInput() (*mcp.InputRequired, bool) {
	input, ok := p.value.AsInput()
	if !ok {
		return nil, false
	}
	return &mcp.InputRequired{RequestState: cloneState(input.State), Requests: loadRequests(input.Requests)}, true
}

// AsTaskWait returns the exact Task identifier and independent polling guidance
// only when the next action is to read the existing Task.
func (p Pending) AsTaskWait() (string, *int64, bool) { //nolint:unparam // The workflow collector consumes the hint through api.PendingExecution.
	task, ok := p.value.AsTaskWait()
	if !ok {
		return "", nil, false
	}
	return task.TaskID, clonePollInterval(task.PollIntervalMs), true
}

// AsTaskInput returns the exact Task, independent polling guidance and copied
// host questions only when the next action is to answer Task input.
func (p Pending) AsTaskInput() (string, *int64, *mcp.InputRequired, bool) {
	task, ok := p.value.AsTaskInput()
	if !ok {
		return "", nil, nil, false
	}
	return task.TaskID, clonePollInterval(task.PollIntervalMs), &mcp.InputRequired{Requests: loadRequests(task.Requests)}, true
}

// Validate rejects an unconstructed pending value. Construction and decoding
// already checked its selected branch and saved the validated JSON bytes.
func (p Pending) Validate() error {
	if len(p.encoded) == 0 {
		return errors.New("pending execution has no selected outcome")
	}
	return nil
}

// MarshalJSON copies the generated JSON so callers cannot change saved history.
func (p Pending) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return bytes.Clone(p.encoded), nil
}

// UnmarshalJSON validates the generated shape and opaque interaction parameters
// before replacing the receiver. A failed decode leaves its old value unchanged.
func (p *Pending) UnmarshalJSON(data []byte) error {
	value, err := gentooloperations.DecodePendingExecution(data)
	if err != nil {
		return err
	}
	decoded, err := newPending(value.Outcome)
	if err != nil {
		return err
	}
	*p = *decoded
	return nil
}

// newPending encodes the selected native branch and checks host interactions.
// The retained bytes let workflow guards measure this value without encoding it.
func newPending(value gentooloperations.Outcome) (*Pending, error) {
	encoded, err := gentooloperations.EncodePendingExecution(&gentooloperations.PendingExecution{Outcome: value})
	if err != nil {
		return nil, err
	}
	pending := &Pending{value: value, encoded: encoded}
	var input *mcp.InputRequired
	if ordinary, ok := pending.AsInput(); ok {
		input = ordinary
	}
	if _, _, taskInput, ok := pending.AsTaskInput(); ok {
		input = taskInput
	}
	if input != nil {
		if err := input.Validate(mcp.InputSupport{Form: true, URL: true}); err != nil {
			return nil, err
		}
	}
	return pending, nil
}

// storeRequests copies exact request identifiers and parameter bytes into the
// generated saved value. An absent object remains absent; an empty one is kept.
func storeRequests(requests map[string]mcp.InputRequest) map[string]*gentooloperations.HostRequest {
	if requests == nil {
		return nil
	}
	stored := make(map[string]*gentooloperations.HostRequest, len(requests))
	for id, request := range requests {
		stored[id] = &gentooloperations.HostRequest{Method: request.Method, Params: bytes.Clone(request.Params)}
	}
	return stored
}

// loadRequests returns independently mutable requests for host presentation.
func loadRequests(requests map[string]*gentooloperations.HostRequest) map[string]mcp.InputRequest {
	if requests == nil {
		return nil
	}
	loaded := make(map[string]mcp.InputRequest, len(requests))
	for id, request := range requests {
		loaded[id] = mcp.InputRequest{Method: request.Method, Params: bytes.Clone(json.RawMessage(request.Params))}
	}
	return loaded
}

// clonePollInterval keeps accessor changes from altering the saved observation.
func clonePollInterval(interval *int64) *int64 {
	if interval == nil {
		return nil
	}
	copy := *interval
	return &copy
}
