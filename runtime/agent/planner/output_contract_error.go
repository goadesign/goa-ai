// Package planner defines the values an agent planner sends to and receives
// from the runtime.
package planner

import (
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// OutputContractOrigin is retained as the planner-facing name for the
	// neutral output-contract origin vocabulary.
	OutputContractOrigin = outputcontract.Origin

	// OutputContractError is retained as the planner-facing name for a neutral
	// output-contract failure.
	OutputContractError = outputcontract.Error

	// ModelOutputRecoveryKind identifies the planner response the model must
	// replace after a recoverable output rejection.
	ModelOutputRecoveryKind = outputcontract.RecoveryKind
)

const (
	// OutputContractOriginModel identifies rejected model output.
	OutputContractOriginModel = outputcontract.OriginModel

	// OutputContractOriginPlanner identifies a rejected planner result.
	OutputContractOriginPlanner = outputcontract.OriginPlanner

	// OutputContractOriginTool identifies a rejected tool execution result.
	OutputContractOriginTool = outputcontract.OriginTool

	// ModelOutputRecoveryAnswer replaces a rejected final answer without
	// ordinary tools.
	ModelOutputRecoveryAnswer = outputcontract.RecoveryAnswer

	// ModelOutputRecoveryToolCalls replaces rejected planned tool calls with
	// the current executable tool catalog.
	ModelOutputRecoveryToolCalls = outputcontract.RecoveryToolCalls
)

// NewOutputContractError records why a completed planner result was rejected.
func NewOutputContractError(cause error) *OutputContractError {
	return outputcontract.NewWithOrigin(cause, outputcontract.OriginPlanner)
}

// NewRecoverableModelAnswerError records why a completed model answer was
// rejected. answer must be the exact final response returned by the planner
// model client. The workflow schedules a replacement answer using correction
// as guidance.
func NewRecoverableModelAnswerError(
	cause error,
	answer *FinalResponse,
	correction string,
) *OutputContractError {
	if answer == nil {
		panic("planner: recoverable model output requires the rejected final response")
	}
	return outputcontract.NewRecoverableModelOutput(
		cause,
		answer.Message,
		correction,
		outputcontract.RecoveryAnswer,
	)
}

// NewRecoverableModelToolCallsError records why completed model-authored tool
// calls were rejected. response must be the exact response returned by the
// planner model client. The workflow schedules replacement tool calls using
// correction as guidance and retains the current executable tool catalog.
func NewRecoverableModelToolCallsError(
	cause error,
	response *model.Message,
	correction string,
) *OutputContractError {
	return outputcontract.NewRecoverableModelOutput(
		cause,
		response,
		correction,
		outputcontract.RecoveryToolCalls,
	)
}

// newOutputContractErrorWithOrigin lets framework-owned planner helpers retain
// the model origin when converting validated model-stream failures.
func newOutputContractErrorWithOrigin(cause error, origin OutputContractOrigin) *OutputContractError {
	return outputcontract.NewWithOrigin(cause, origin)
}
