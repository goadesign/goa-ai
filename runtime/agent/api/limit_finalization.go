// Package api defines the values that Goa-AI workflows send to activities and
// receive back.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"goa.design/goa-ai/runtime/agent/planner"
)

type (
	// LimitFinalizationActivityOutput tells a workflow to execute the returned
	// final result, load saved messages, or stop because the session ended.
	// Construct it with one of the functions below so exactly one choice is set.
	LimitFinalizationActivityOutput struct {
		disposition LimitFinalizationDisposition
		result      *planner.PlanResult
	}

	limitFinalizationActivityOutputWire struct {
		Disposition LimitFinalizationDisposition `json:"disposition"`
		Result      *planner.PlanResult          `json:"result,omitempty"`
	}
)

// TerminalLimitFinalizationActivityOutput returns the final response or final
// tool calls selected by the planner.
func TerminalLimitFinalizationActivityOutput(result planner.PlanResult) *LimitFinalizationActivityOutput {
	return &LimitFinalizationActivityOutput{
		disposition: LimitFinalizationDispositionTerminalPlan,
		result:      &result,
	}
}

// HistoryRequiredLimitFinalizationActivityOutput asks the workflow to load
// saved messages and call PlanResume.
func HistoryRequiredLimitFinalizationActivityOutput() *LimitFinalizationActivityOutput {
	return &LimitFinalizationActivityOutput{
		disposition: LimitFinalizationDispositionHistoryRequired,
	}
}

// SessionEndedLimitFinalizationActivityOutput reports that the saved session
// was already ended before the planner ran.
func SessionEndedLimitFinalizationActivityOutput() *LimitFinalizationActivityOutput {
	return &LimitFinalizationActivityOutput{
		disposition: LimitFinalizationDispositionSessionEnded,
	}
}

// Disposition returns what the workflow must do next.
func (o *LimitFinalizationActivityOutput) Disposition() LimitFinalizationDisposition {
	return o.disposition
}

// TerminalPlan returns the final response or final tool calls. It returns nil
// when the workflow must load messages or stop for an ended session.
func (o *LimitFinalizationActivityOutput) TerminalPlan() *planner.PlanResult {
	return o.result
}

// MarshalJSON encodes the one selected action for an activity result.
func (o *LimitFinalizationActivityOutput) MarshalJSON() ([]byte, error) {
	if o == nil {
		return nil, errors.New("limit finalization output is required")
	}
	if err := validateLimitFinalizationActivityOutput(o.disposition, o.result); err != nil {
		return nil, err
	}
	return json.Marshal(limitFinalizationActivityOutputWire{
		Disposition: o.disposition,
		Result:      o.result,
	})
}

// UnmarshalJSON decodes an activity result and rejects unknown fields, extra
// JSON values, and combinations that select more than one next action.
func (o *LimitFinalizationActivityOutput) UnmarshalJSON(data []byte) error {
	var wire limitFinalizationActivityOutputWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("decode limit finalization output: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return errors.New("decode limit finalization output: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode limit finalization output: %w", err)
	}
	if err := validateLimitFinalizationActivityOutput(wire.Disposition, wire.Result); err != nil {
		return err
	}
	o.disposition = wire.Disposition
	o.result = wire.Result
	return nil
}

// validateLimitFinalizationActivityOutput rejects a missing action, a final
// result without the terminal-plan action, and a terminal-plan action without
// a final result.
func validateLimitFinalizationActivityOutput(
	disposition LimitFinalizationDisposition,
	result *planner.PlanResult,
) error {
	switch disposition {
	case LimitFinalizationDispositionTerminalPlan:
		if result == nil {
			return errors.New("terminal limit finalization is missing its plan")
		}
	case LimitFinalizationDispositionHistoryRequired,
		LimitFinalizationDispositionSessionEnded:
		if result != nil {
			return fmt.Errorf("%s limit finalization contains a terminal plan", disposition)
		}
	default:
		return fmt.Errorf("unknown limit finalization disposition %q", disposition)
	}
	return nil
}
