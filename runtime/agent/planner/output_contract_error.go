// Package planner defines the values an agent planner sends to and receives
// from the runtime.
package planner

import "goa.design/goa-ai/runtime/agent/internal/outputcontract"

type (
	// OutputContractOrigin is retained as the planner-facing name for the
	// neutral output-contract origin vocabulary.
	OutputContractOrigin = outputcontract.Origin

	// OutputContractError is retained as the planner-facing name for a neutral
	// terminal output-contract failure.
	OutputContractError = outputcontract.Error
)

const (
	// OutputContractOriginModel identifies rejected model output.
	OutputContractOriginModel = outputcontract.OriginModel

	// OutputContractOriginPlanner identifies a rejected planner result.
	OutputContractOriginPlanner = outputcontract.OriginPlanner

	// OutputContractOriginTool identifies a rejected tool execution result.
	OutputContractOriginTool = outputcontract.OriginTool
)

// NewOutputContractError records why a completed planner result was rejected.
func NewOutputContractError(cause error) *OutputContractError {
	return outputcontract.NewWithOrigin(cause, outputcontract.OriginPlanner)
}

// newOutputContractErrorWithOrigin lets framework-owned planner helpers retain
// the model origin when converting validated model-stream failures.
func newOutputContractErrorWithOrigin(cause error, origin OutputContractOrigin) *OutputContractError {
	return outputcontract.NewWithOrigin(cause, origin)
}
