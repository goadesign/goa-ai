// Package tooloperation retains the workflow's next operation on one unfinished
// tool invocation. Constructors copy state and answers; callers receive copies
// through accessors. Construction retains validated JSON bytes for safe size
// checks without serialization. Generated Goa codecs own the JSON shape and branch
// validation. This package checks that opaque answer bytes are JSON objects.
package tooloperation

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	gentooloperations "goa.design/goa-ai/registry/gen/tooloperations"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	// Continuation selects exactly one later operation. Its value stays private
	// so callers cannot combine Task queries, answers and input continuation.
	Continuation struct {
		value   gentooloperations.Operation
		encoded []byte
	}
)

// NewInput copies the accepted host answers and saved state for the original
// tool's next input round. The supplied continuation must be non-nil.
func NewInput(input *mcp.CallContinuation) (*Continuation, error) {
	state := cloneState(input.RequestState)
	return newContinuation(gentooloperations.NewOperationInput(&gentooloperations.InputContinuation{
		State:     state,
		Responses: copyAnswers(input.InputResponses),
	}))
}

// NewTaskGet selects a read of the exact existing Task, including an empty ID.
func NewTaskGet(taskID string) (*Continuation, error) {
	return newContinuation(gentooloperations.NewOperationTaskGet(gentooloperations.OperationBranchTaskGet(taskID)))
}

// NewTaskUpdate copies host answers for the exact existing Task. An empty object
// is valid; construction rejects a missing object before the operation is saved.
func NewTaskUpdate(taskID string, answers map[string]json.RawMessage) (*Continuation, error) {
	return newContinuation(gentooloperations.NewOperationTaskUpdate(&gentooloperations.TaskAnswers{
		TaskID:    taskID,
		Responses: copyAnswers(answers),
	}))
}

// NewTaskCancel selects cancellation of the existing Task. Its acknowledgement
// does not prove that the server stopped work or selected a cancelled state.
func NewTaskCancel(taskID string) (*Continuation, error) {
	return newContinuation(gentooloperations.NewOperationTaskCancel(gentooloperations.OperationBranchTaskCancel(taskID)))
}

// FromValue validates a generated registry value and copies its mutable fields.
// A missing optional value remains absent; malformed selected operations fail.
func FromValue(value *gentooloperations.ExecutionContinuation) (*Continuation, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := gentooloperations.EncodeExecutionContinuation(value)
	if err != nil {
		return nil, err
	}
	operation := &Continuation{value: cloneOperation(value.Operation), encoded: encoded}
	if err := operation.validateAnswers(); err != nil {
		return nil, err
	}
	return operation, nil
}

// Value returns a generated registry record with independent state and answers.
// A missing continuation stays absent on an original tool call.
func Value(operation *Continuation) *gentooloperations.ExecutionContinuation {
	if operation == nil {
		return nil
	}
	return &gentooloperations.ExecutionContinuation{Operation: cloneOperation(operation.value)}
}

// EncodedJSONSize lets workflow guards charge only this framework-owned value.
// The generated JSON was validated at construction, so checking its length does
// not serialize, copy or traverse caller data. Other types remain unsupported.
func EncodedJSONSize(value any) (size int, recognized bool, err error) {
	var operation *Continuation
	switch value := value.(type) {
	case Continuation:
		operation = &value
	case *Continuation:
		operation = value
	default:
		return 0, false, nil
	}
	if operation == nil {
		return len("null"), true, nil
	}
	if err := operation.Validate(); err != nil {
		return 0, true, err
	}
	return len(operation.encoded), true, nil
}

// AsInput returns copied answers and state only for an input continuation.
func (c Continuation) AsInput() (*mcp.CallContinuation, bool) {
	input, ok := c.value.AsInput()
	if !ok {
		return nil, false
	}
	return &mcp.CallContinuation{RequestState: cloneState(input.State), InputResponses: readAnswers(input.Responses)}, true
}

// AsTaskGet returns the exact Task ID only for a Task query.
func (c Continuation) AsTaskGet() (string, bool) {
	id, ok := c.value.AsTaskGet()
	return string(id), ok
}

// AsTaskUpdate returns the exact Task ID and copied host answers only for an update.
func (c Continuation) AsTaskUpdate() (string, map[string]json.RawMessage, bool) {
	input, ok := c.value.AsTaskUpdate()
	if !ok {
		return "", nil, false
	}
	return input.TaskID, readAnswers(input.Responses), true
}

// AsTaskCancel returns the exact Task ID only for a cancellation request.
func (c Continuation) AsTaskCancel() (string, bool) {
	id, ok := c.value.AsTaskCancel()
	return string(id), ok
}

// Validate rejects an unconstructed value before activity or registry admission.
// Constructors and decoders already checked and saved its immutable JSON bytes.
func (c Continuation) Validate() error {
	if len(c.encoded) == 0 {
		return errors.New("execution continuation has no selected operation")
	}
	return nil
}

// MarshalJSON returns a copy of the JSON produced by the generated codec.
// Callers cannot change the saved operation by modifying these bytes.
func (c Continuation) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return bytes.Clone(c.encoded), nil
}

// UnmarshalJSON uses the generated codec before accepting a saved operation.
// A failed decode leaves the receiver unchanged.
func (c *Continuation) UnmarshalJSON(data []byte) error {
	value, err := gentooloperations.DecodeExecutionContinuation(data)
	if err != nil {
		return err
	}
	decoded, err := newContinuation(value.Operation)
	if err != nil {
		return err
	}
	*c = *decoded
	return nil
}

// newContinuation checks opaque answers and records the generated JSON once.
// Later size checks and serialization use the same immutable bytes.
func newContinuation(value gentooloperations.Operation) (*Continuation, error) {
	operation := &Continuation{value: value}
	if err := operation.validateAnswers(); err != nil {
		return nil, err
	}
	encoded, err := gentooloperations.EncodeExecutionContinuation(&gentooloperations.ExecutionContinuation{Operation: value})
	if err != nil {
		return nil, err
	}
	operation.encoded = encoded
	return operation, nil
}

// copyAnswers preserves exact identifiers and answer bytes while separating the
// saved operation from a caller's mutable answer map.
func copyAnswers(answers map[string]json.RawMessage) map[string][]byte {
	if answers == nil {
		return nil
	}
	copy := make(map[string][]byte, len(answers))
	for id, value := range answers {
		copy[id] = append([]byte(nil), value...)
	}
	return copy
}

// readAnswers returns independently mutable JSON values for a typed tool caller.
func readAnswers(answers map[string][]byte) map[string]json.RawMessage {
	if answers == nil {
		return nil
	}
	copy := make(map[string]json.RawMessage, len(answers))
	for id, value := range answers {
		copy[id] = append(json.RawMessage(nil), value...)
	}
	return copy
}

// cloneOperation copies the mutable fields of a selected operation. Scalar Task
// identifiers already have value ownership and keep their exact string bytes.
func cloneOperation(operation gentooloperations.Operation) gentooloperations.Operation {
	if input, ok := operation.AsInput(); ok {
		return gentooloperations.NewOperationInput(&gentooloperations.InputContinuation{
			State:     cloneState(input.State),
			Responses: copyAnswers(readAnswers(input.Responses)),
		})
	}
	if update, ok := operation.AsTaskUpdate(); ok {
		return gentooloperations.NewOperationTaskUpdate(&gentooloperations.TaskAnswers{
			TaskID:    update.TaskID,
			Responses: copyAnswers(readAnswers(update.Responses)),
		})
	}
	return operation
}

// cloneState retains explicit empty state and keeps accessor mutations private.
func cloneState(state *string) *string {
	if state == nil {
		return nil
	}
	value := *state
	return &value
}

// validateAnswers checks the content of opaque answer bytes. Goa validates their
// surrounding fields, but a Bytes attribute cannot express JSON object syntax.
func (c Continuation) validateAnswers() error {
	var answers map[string][]byte
	if input, ok := c.value.AsInput(); ok && input != nil {
		answers = input.Responses
	}
	if update, ok := c.value.AsTaskUpdate(); ok && update != nil {
		answers = update.Responses
	}
	for id, answer := range answers {
		var object map[string]json.RawMessage
		if err := jsonv2.Unmarshal(answer, &object); err != nil {
			return fmt.Errorf("input response %q: %w", id, err)
		}
		if object == nil {
			return fmt.Errorf("input response %q must be one JSON object", id)
		}
	}
	return nil
}
